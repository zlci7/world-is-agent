package agent

import (
	"context"
	"strings"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type taskRuntimeEnvironment interface {
	TaskRuntime() (*task.Service, tool.TaskWorld)
}

type taskTurnContext struct {
	service   *task.Service
	world     tool.TaskWorld
	tools     *tool.TaskTools
	authority tool.RuntimeCallContext
	catalog   *tool.EnvironmentToolCatalog
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
		return t.catalog.BuildTurnToolView(config), nil
	}
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
	registry, err := tool.NewRegistry(t.catalog, t.tools.Entries(rc))
	if err != nil {
		return tool.ToolAdmissionResult{}, tool.SanitizeTaskError(err)
	}
	return registry.BuildTurnToolView(config, &rc), nil
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
