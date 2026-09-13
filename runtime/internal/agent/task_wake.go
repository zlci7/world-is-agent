package agent

import (
	"context"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/idgen"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

// HandleTaskWake runs an admitted internal trigger through the ordinary bounded
// Turn machinery. Wake correlation grants no player interaction authority.
func (l *Loop) HandleTaskWake(ctx context.Context, env Environment, conn ConnectionContext, target *protocol.EntityRef, catalog *tool.EnvironmentToolCatalog, exec task.ExecutionContext, record task.Record) error {
	if !l.config.Task.Enabled || catalog == nil || target.GetEntityId() != exec.Owner.EntityID || exec.Validate() != nil || exec.Source.Kind != task.SourceKindTaskWake || exec.WakeID == "" || exec.TaskID != record.ID || exec.Owner != record.Owner || exec.ExpectedRevision != record.Revision {
		return task.ErrSourceInvalid
	}
	provider, ok := env.(taskRuntimeEnvironment)
	if !ok {
		return task.ErrWorldNotReady
	}
	service, world := provider.TaskRuntime()
	if service == nil || world == nil {
		return task.ErrWorldNotReady
	}
	head, epoch, ready := world.Current()
	if !ready || head.Binding != exec.Binding {
		return task.ErrGenerationStale
	}
	turnID := idgen.New("turn")
	exec.Source = task.SourceRef{Kind: task.SourceKindTaskWake, EventID: exec.WakeID, TurnID: turnID, CallID: exec.WakeID}
	authority := tool.RuntimeCallContext{Execution: exec, ObservedTask: &record, AuthorityEpoch: epoch, WakeReason: "task_wake"}
	turn := &taskTurnContext{env: env, service: service, world: world, tools: tool.NewTaskTools(service, world, authority), authority: authority, catalog: catalog}
	return l.handleTurn(ctx, env, conn, exec.Owner, target, catalog.BuildTurnToolView(l.toolAdmissionConfig()), nil, turnID, turn)
}

func (t *taskTurnContext) background() bool {
	return t.authority.Execution.Source.Kind == task.SourceKindTaskWake
}

func (t *taskTurnContext) stopped(ctx context.Context) (bool, error) {
	record, err := t.service.Read(ctx, t.authority.Execution.Owner, t.authority.Execution.TaskID)
	if err == nil && record.NeedsReconcile {
		if coordinator, ok := t.env.(interface {
			ReconcileTask(context.Context, session.AgentSessionKey, string) error
		}); ok {
			err = coordinator.ReconcileTask(ctx, record.Owner, record.ID)
			if err == nil {
				record, err = t.service.Read(ctx, record.Owner, record.ID)
			}
		}
	}
	if err != nil {
		return true, err
	}
	var result task.ReconcileResult
	err = t.world.Guard(t.authority.Execution.Binding, t.authority.AuthorityEpoch, func(head task.Head) error {
		exec := t.authority.Execution
		exec.Clock, exec.ExpectedRevision = head.Clock, record.Revision
		var err error
		result, err = t.service.Reconcile(ctx, exec)
		return err
	})
	return result.Next != task.ReconcileNextDecide, err
}

type taskActionRegistrar interface {
	RegisterTaskAction(context.Context, tool.RuntimeCallContext, *protocol.ActionRequest) error
}

func (t *taskTurnContext) beforeAction(env Environment, rc *tool.RuntimeCallContext) func(context.Context, plannedToolCall) error {
	return func(ctx context.Context, call plannedToolCall) error {
		registrar, ok := env.(taskActionRegistrar)
		if !ok || rc == nil {
			return task.ErrWorldNotReady
		}
		return registrar.RegisterTaskAction(ctx, *rc, call.request)
	}
}
