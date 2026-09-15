package agent

import (
	"context"
	"strings"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
	"gameagent/runtime/internal/trace"
)

type taskRuntimeEnvironment interface {
	TaskRuntime() (*task.Service, tool.TaskWorld)
}

type taskTurnContext struct {
	env       Environment
	service   *task.Service
	world     tool.TaskWorld
	tools     *tool.TaskTools
	authority tool.RuntimeCallContext
	catalog   *tool.EnvironmentToolCatalog
	// checkpointID is the authority this snapshot was read under.
	checkpointID string
}

func (l *Loop) beginTaskContext(env Environment, key session.AgentSessionKey, event *protocol.GameEvent, turnID string, catalog *tool.EnvironmentToolCatalog) *taskTurnContext {
	if !l.config.Task.Enabled || catalog == nil {
		return nil
	}
	provider, ok := env.(taskRuntimeEnvironment)
	if !ok {
		return nil
	}
	service, world := provider.TaskRuntime()
	if service == nil || world == nil {
		return nil
	}
	head, epoch, ready := world.Current()
	if !ready {
		return nil
	}
	if err := world.GuardOwner(head.Binding, epoch, key.EntityID, func(task.Head) error { return nil }); err != nil {
		return nil
	}
	source := event.GetInteractionSource()
	scope := source.GetScope()
	if source.GetKind() != "player" || strings.TrimSpace(source.GetSourceId()) == "" || strings.TrimSpace(source.GetPlayerEntityId()) == "" || source.GetTaskId() != "" || source.GetOperationId() != "" || strings.TrimSpace(event.GetEventId()) == "" ||
		scope.GetGameId() != key.GameID || scope.GetWorldId() != key.WorldID || scope.GetWorldRunId() != head.Binding.RunID || scope.GetExecutionGeneration() != head.Binding.Generation || head.Binding.World != (task.WorldKey{GameID: key.GameID, WorldID: key.WorldID}) {
		return nil
	}
	rc := tool.RuntimeCallContext{Execution: task.ExecutionContext{Owner: key, Binding: head.Binding, Clock: head.Clock, Source: task.SourceRef{Kind: task.SourceKindInteraction, EventID: event.GetEventId(), TurnID: turnID}}, InteractionSourceID: source.GetSourceId(), AuthorityEpoch: epoch}
	return &taskTurnContext{service: service, world: world, tools: tool.NewTaskTools(service, world, rc), authority: rc, catalog: catalog}
}

func (t *taskTurnContext) snapshot(ctx context.Context, config tool.ToolAdmissionConfig) (tool.ToolAdmissionResult, error) {
	rc := t.authority
	head, epoch, ready := t.world.Current()
	if !ready || head.Binding != rc.Execution.Binding || epoch != rc.AuthorityEpoch || head.Clock.ID != rc.Execution.Clock.ID {
		t.tools.Close()
		if t.background() {
			return tool.ToolAdmissionResult{}, task.ErrGenerationStale
		}
		return t.catalog.BuildTurnToolView(config), nil
	}
	t.checkpointID = head.CheckpointID
	if rc.Execution.Source.Kind == task.SourceKindTaskWake {
		record, err := t.service.Read(ctx, rc.Execution.Owner, rc.Execution.TaskID)
		if err != nil {
			return tool.ToolAdmissionResult{}, tool.SanitizeTaskError(err)
		}
		rc.ObservedTask = &record
	} else {
		records, err := t.service.ListActive(ctx, rc.Execution.Owner, 1)
		if err != nil {
			return tool.ToolAdmissionResult{}, tool.SanitizeTaskError(err)
		}
		if len(records) > 0 {
			rc.ObservedTask = &records[0]
		}
	}
	// Committed results are facts for this request. They are read in the same turn
	// snapshot as the task, and dropped with it when the binding moved.
	recent, err := t.service.ListRecentResults(ctx, rc.Execution.Owner, 3)
	if err != nil {
		return tool.ToolAdmissionResult{}, tool.SanitizeTaskError(err)
	}
	rc.RecentResults = recent
	if current, currentEpoch, currentReady := t.world.Current(); !currentReady || current.Binding != head.Binding || currentEpoch != epoch {
		t.tools.Close()
		if t.background() {
			return tool.ToolAdmissionResult{}, task.ErrGenerationStale
		}
		return t.catalog.BuildTurnToolView(config), nil
	}
	registry, err := tool.NewRegistry(t.catalog, t.tools.Entries(rc))
	if err != nil {
		return tool.ToolAdmissionResult{}, tool.SanitizeTaskError(err)
	}
	return registry.BuildTurnToolView(config, &rc), nil
}

// emitTrace publishes what this request carries from the task runtime: which task,
// which committed result, and which checkpoint authority the snapshot came from.
func (t *taskTurnContext) emitTrace(tracer trace.TurnTracer, view tool.TurnToolView, step int) {
	rc := view.RuntimeContext()
	if rc == nil || rc.ObservedTask == nil {
		return
	}
	fields := trace.Fields{"step_index": step, "task_id": rc.ObservedTask.ID, "task_state": string(rc.ObservedTask.State)}
	if t.checkpointID != "" {
		fields["checkpoint_id"] = t.checkpointID
	}
	if rc.ObservedTask.Result != nil {
		fields["result_id"] = rc.ObservedTask.Result.ID
		fields["result_state"] = string(rc.ObservedTask.Result.State)
	} else if len(rc.RecentResults) > 0 {
		fields["result_id"] = rc.RecentResults[0].ID
		fields["result_state"] = string(rc.RecentResults[0].State)
	}
	tracer.Emit(trace.EventTaskContextPrepared, trace.EventData{Fields: fields})
}

func (t *taskTurnContext) captureProposals(ctx context.Context, outcome *toolBatchOutcome) error {
	for _, action := range outcome.SuccessfulActions {
		ref, err := t.tools.CaptureProposal(ctx, t.authority, action.ActionResult)
		if err != nil {
			return tool.SanitizeTaskError(err)
		}
		if ref == "" {
			continue
		}
		for i := range outcome.Results {
			result := &outcome.Results[i]
			if result.ToolCallID != action.ToolCall.ID {
				continue
			}
			if result.Output == nil {
				result.Output = map[string]any{}
			}
			result.Output["proposal_ref"] = ref
		}
	}
	return nil
}

type taskControlReleaser interface {
	ReleaseTask(context.Context, task.Record) error
}

func (t *taskTurnContext) releaseCommitted(ctx context.Context, env Environment, view tool.TurnToolView, calls []model.ToolCall) error {
	rc := view.RuntimeContext()
	if rc == nil || rc.ObservedTask == nil {
		return nil
	}
	committed := false
	for _, call := range calls {
		if entry, ok := view.Lookup(call.Name); ok && entry.Executor == t.tools {
			committed = true
		}
	}
	if !committed {
		return nil
	}
	releaser, ok := env.(taskControlReleaser)
	if !ok {
		return nil
	}
	return releaser.ReleaseTask(ctx, *rc.ObservedTask)
}

func redactTaskCalls(calls []model.ToolCall, view tool.TurnToolView) []model.ToolCall {
	result := copyToolCallsForTranscript(calls)
	for i := range result {
		entry, ok := view.Lookup(result[i].Name)
		if ok {
			if _, taskTool := entry.Executor.(*tool.TaskTools); taskTool {
				result[i].Arguments = nil
			}
		}
	}
	return result
}
