package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type taskWakeHandler interface {
	HandleTaskWake(context.Context, Environment, ConnectionContext, *protocol.EntityRef, *tool.EnvironmentToolCatalog, task.ExecutionContext, task.Record) error
}

type taskRegistrationFailureEnv struct {
	*taskContextEnvironment
	registrations int
}

func (e *taskRegistrationFailureEnv) RegisterTaskAction(context.Context, tool.RuntimeCallContext, *protocol.ActionRequest) error {
	e.registrations++
	return errors.New("registration storage failed")
}

func TestTaskOperationRegistrationFailurePreventsSyncAndAsyncSend(t *testing.T) {
	for _, async := range []bool{false, true} {
		name := "sync"
		if async {
			name = "async"
		}
		t.Run(name, func(t *testing.T) {
			base, key, _, _ := taskContextFixture(t)
			record := seedContextTask(t, base, key, "task")
			head, err := base.svc.UpdateClock(context.Background(), base.world.head.Binding, task.Clock{ID: "clock", Tick: 120, Sequence: 2})
			if err != nil {
				t.Fatal(err)
			}
			base.world.head = head
			wakes, err := base.svc.ClaimDue(context.Background(), head.Binding, head.Clock, 1)
			if err != nil || len(wakes) != 1 {
				t.Fatal(wakes, err)
			}
			w := wakes[0]
			if err := base.svc.MarkEnqueued(context.Background(), head.Binding, w.ID, w.ClaimID); err != nil {
				t.Fatal(err)
			}
			exec, record, err := base.svc.BeginWake(context.Background(), head.Binding, w.ID, w.ClaimID)
			if err != nil {
				t.Fatal(err)
			}
			capability := schedulerCapability("inspect", tool.ConcurrencySequential)
			if async {
				capability.ExecutionMode = protocol.ExecutionMode_EXECUTION_MODE_ASYNC
			}
			catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{capability}})
			if err != nil {
				t.Fatal(err)
			}
			env := &taskRegistrationFailureEnv{taskContextEnvironment: base}
			provider := &taskContextModel{run: func(model.Request, int) model.ModelDecision {
				return model.ModelDecision{ToolCalls: []model.ToolCall{schedulerCall("inspect", "inspect", "target")}, Control: model.ControlDirective{Kind: model.ControlContinue}}
			}}
			loop := NewLoop(provider, nil, contextConfig(t), WithMemoryStore(memory.NewInMemoryStore()))
			err = loop.HandleTaskWake(context.Background(), env, ConnectionContext{}, &protocol.EntityRef{EntityId: key.EntityID}, catalog, exec, record)
			if err == nil || env.registrations != 1 || env.submits != 0 || env.starts != 0 {
				t.Fatalf("registration failure sent environment action: %v registrations=%d sends=%d/%d", err, env.registrations, env.submits, env.starts)
			}
		})
	}
}

func TestTaskWakeUsesBoundedLoopAndStopsAfterCommittedWait(t *testing.T) {
	env, key, _, catalog := taskContextFixture(t)
	record := seedContextTask(t, env, key, "wake")
	head, err := env.svc.UpdateClock(context.Background(), env.world.head.Binding, task.Clock{ID: "clock", Tick: 120, Sequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	env.world.head = head
	wakes, err := env.svc.ClaimDue(context.Background(), head.Binding, head.Clock, 1)
	if err != nil || len(wakes) != 1 {
		t.Fatal(wakes, err)
	}
	wake := wakes[0]
	if err := env.svc.MarkEnqueued(context.Background(), head.Binding, wake.ID, wake.ClaimID); err != nil {
		t.Fatal(err)
	}
	exec, record, err := env.svc.BeginWake(context.Background(), head.Binding, wake.ID, wake.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	p := &taskContextModel{run: func(req model.Request, n int) model.ModelDecision {
		if n != 1 {
			t.Fatal("committed wait started another model Step")
		}
		if !strings.Contains(req.Messages[0].Content, "task_wake") || !strings.Contains(req.Messages[0].Content, wake.ID) {
			t.Fatalf("missing internal trigger: %s", req.Messages[0].Content)
		}
		for _, def := range req.Tools {
			if def.Name == "create_task" {
				t.Fatal("background wake gained player authority")
			}
		}
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}, ToolCalls: []model.ToolCall{{ID: "wait", Name: "update_task", Arguments: map[string]any{"task_id": record.ID, "intent": "wait", "next_wakeup_at": 150}}}}
	}}
	loop := NewLoop(p, nil, contextConfig(t), WithMemoryStore(memory.NewInMemoryStore()))
	handler, ok := any(loop).(taskWakeHandler)
	if !ok {
		t.Fatal("Loop has no task wake entry into the bounded cognition loop")
	}
	if err := handler.HandleTaskWake(context.Background(), env, ConnectionContext{}, &protocol.EntityRef{EntityId: key.EntityID}, catalog, exec, record); err != nil {
		t.Fatal(err)
	}
	current, err := env.svc.Read(context.Background(), key, record.ID)
	if err != nil || current.State != task.StateWaiting || current.NextWakeAt == nil || *current.NextWakeAt != 150 {
		t.Fatalf("wait not committed: %+v %v", current, err)
	}
	if len(p.requests) != 1 || env.submits != 0 || env.starts != 0 {
		t.Fatalf("requests=%d sends=%d/%d", len(p.requests), env.submits, env.starts)
	}
}
