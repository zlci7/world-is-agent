package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type taskContextWorld struct {
	head  task.Head
	epoch uint64
	ready bool
}

func (w *taskContextWorld) Current() (task.Head, uint64, bool) { return w.head, w.epoch, w.ready }
func (w *taskContextWorld) GuardOwner(b task.Binding, e uint64, entityID string, fn func(task.Head) error) error {
	if !w.ready {
		return task.ErrWorldNotReady
	}
	if b != w.head.Binding || e != w.epoch {
		return task.ErrGenerationStale
	}
	return fn(w.head)
}

type taskContextEnvironment struct {
	mixedLoopEnvironment
	svc             *task.Service
	world           *taskContextWorld
	proposal        bool
	submits, starts int
}

func (e *taskContextEnvironment) TaskRuntime() (*task.Service, tool.TaskWorld) { return e.svc, e.world }
func (e *taskContextEnvironment) SubmitAction(ctx context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
	e.submits++
	r, err := e.schedulerTestEnvironment.SubmitAction(ctx, req)
	if e.proposal {
		r.TaskProposal = &protocol.TaskProposal{Clock: &protocol.WorldClock{ClockId: "clock", NowTick: 100, Sequence: 1}, WakeAt: 120, DeadlineAt: 200, ParticipantEntityIds: []string{"actor"}, EquivalenceKey: "agreed"}
	}
	return r, err
}
func (e *taskContextEnvironment) StartAction(ctx context.Context, req *protocol.ActionRequest) (ActionStart, error) {
	e.starts++
	return e.schedulerTestEnvironment.StartAction(ctx, req)
}

type taskContextModel struct {
	run      func(model.Request, int) model.ModelDecision
	requests []model.Request
}

func (p *taskContextModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	p.requests = append(p.requests, r)
	return model.Response{Decision: p.run(r, len(p.requests))}, nil
}
func taskContextFixture(t *testing.T) (*taskContextEnvironment, session.AgentSessionKey, *protocol.GameEvent, *tool.EnvironmentToolCatalog) {
	t.Helper()
	store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := task.NewService(store)
	w := task.WorldKey{GameID: "generic", WorldID: "world"}
	head, err := svc.ActivateWorld(context.Background(), w, "run", task.Clock{ID: "clock", Tick: 100, Sequence: 1}, task.CheckpointRef{Status: "absent", World: w})
	if err != nil {
		t.Fatal(err)
	}
	env := &taskContextEnvironment{svc: svc, world: &taskContextWorld{head: head, epoch: 1, ready: true}}
	key := session.AgentSessionKey{GameID: "generic", WorldID: "world", EntityID: "actor"}
	event := &protocol.GameEvent{EventId: "dialogue", WorldId: key.WorldID, TargetEntityId: key.EntityID, EventType: "generic_input", InteractionSource: &protocol.InteractionSource{SourceId: "source", Kind: "player", PlayerEntityId: "player", Scope: &protocol.TaskScope{GameId: key.GameID, WorldId: key.WorldID, WorldRunId: "run", ExecutionGeneration: 1}}}
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{schedulerCapability("inspect", tool.ConcurrencySequential)}})
	if err != nil {
		t.Fatal(err)
	}
	return env, key, event, catalog
}
func seedContextTask(t *testing.T, env *taskContextEnvironment, key session.AgentSessionKey, callID string) task.Record {
	t.Helper()
	src := task.SourceRef{Kind: task.SourceKindInternal, EventID: "seed", TurnID: "seed", CallID: callID}
	result, err := env.svc.Create(context.Background(), task.ExecutionContext{Owner: key, Binding: env.world.head.Binding, Clock: env.world.head.Clock, Source: src}, task.TaskSpec{Instruction: "Inspect later", ClockID: "clock", WakeAt: 120, DeadlineAt: 200, ResultContract: task.ResultContractAuthoritativeEvidence, Source: src}, task.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	return result.Task
}
func contextConfig(t *testing.T) Config {
	config := DefaultConfig()
	config.Task.Enabled = true
	config.MemoryStore.Root = t.TempDir()
	return config
}
func TestTaskContextFreshDialogueCancel(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	old := seedContextTask(t, env, key, "old")
	exec := task.ExecutionContext{Owner: key, Binding: env.world.head.Binding, Clock: env.world.head.Clock, Source: task.SourceRef{Kind: task.SourceKindInternal, EventID: "seed", TurnID: "seed", CallID: "old-cancel"}, TaskID: old.ID, ExpectedRevision: old.Revision}
	if _, err := env.svc.ApplyIntent(context.Background(), exec, task.Intent{Kind: "cancel", Reason: "finished"}); err != nil {
		t.Fatal(err)
	}
	current := seedContextTask(t, env, key, "current")
	provider := &taskContextModel{run: func(req model.Request, step int) model.ModelDecision {
		if step == 1 {
			if !strings.Contains(req.Messages[0].Content, current.ID) || !strings.Contains(req.Messages[0].Content, `"revision": 1`) {
				t.Fatalf("fresh task absent: %s", req.Messages[0].Content)
			}
			return model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "cancel", Name: "update_task", Arguments: map[string]any{"task_id": current.ID, "intent": "cancel", "reason": "player request"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}
		}
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
	}}
	config := contextConfig(t)
	loop := NewLoop(provider, nil, config, WithHistoryStore(memory.NewInMemoryHistoryStore(config.History)))
	if err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event); err != nil {
		t.Fatal(err)
	}
	record, err := env.svc.Read(context.Background(), key, current.ID)
	if err != nil || record.State != task.StateCancelled || env.submits != 0 || env.starts != 0 {
		t.Fatalf("local cancel: %+v %v sends %d/%d", record, err, env.submits, env.starts)
	}
}
func TestTaskIntentUsesObservedRevision(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	record := seedContextTask(t, env, key, "initial")
	advance := func(call string) {
		exec := task.ExecutionContext{Owner: key, Binding: env.world.head.Binding, Clock: env.world.head.Clock, Source: task.SourceRef{Kind: task.SourceKindInternal, EventID: "advance", TurnID: "advance", CallID: call}, TaskID: record.ID, ExpectedRevision: record.Revision}
		tick := int64(150)
		var err error
		record, err = env.svc.ApplyIntent(context.Background(), exec, task.Intent{Kind: "wait", NextWakeAt: &tick})
		if err != nil {
			t.Fatal(err)
		}
	}
	advance("one")
	advance("two")
	advance("three")
	provider := &taskContextModel{run: func(req model.Request, step int) model.ModelDecision {
		if step == 1 {
			if !strings.Contains(req.Messages[0].Content, `"revision": 4`) {
				t.Fatal("revision 4 not shown")
			}
			advance("four")
			return model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "cancel", Name: "update_task", Arguments: map[string]any{"task_id": record.ID, "intent": "cancel", "reason": "player request"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}
		}
		if step == 2 {
			if !strings.Contains(req.Messages[0].Content, `"revision": 5`) {
				t.Fatal("next step did not refresh revision")
			}
			found := false
			for _, message := range req.Messages {
				for _, result := range message.ToolResults {
					found = found || result.Code == "task_changed"
				}
			}
			if !found {
				t.Fatal("task_changed not returned to model")
			}
		}
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
	}}
	loop := NewLoop(provider, nil, contextConfig(t), WithMemoryStore(memory.NewInMemoryStore()))
	if err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event); err != nil {
		t.Fatal(err)
	}
	current, err := env.svc.Read(context.Background(), key, record.ID)
	if err != nil || current.Revision != 5 || current.State != task.StateWaiting {
		t.Fatalf("call replayed: %+v %v", current, err)
	}
}
func TestMixedToolSQLiteTaskCreate(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	env.proposal = true
	provider := &taskContextModel{run: func(req model.Request, step int) model.ModelDecision {
		switch step {
		case 1:
			return model.ModelDecision{ToolCalls: []model.ToolCall{schedulerCall("inspect", "inspect", "proposal")}, Control: model.ControlDirective{Kind: model.ControlContinue}}
		case 2:
			ref := ""
			for _, message := range req.Messages {
				for _, result := range message.ToolResults {
					if value, ok := result.Output["proposal_ref"].(string); ok {
						ref = value
					}
				}
			}
			if ref == "" {
				t.Fatal("successful proposal has no reference")
			}
			env.submits = 0
			env.starts = 0
			return model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "create", Name: "create_task", Arguments: map[string]any{"proposal_ref": ref, "instruction": "Inspect later"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}
		case 3:
			records, err := env.svc.ListActive(context.Background(), key, 1)
			if err != nil || len(records) != 1 || !strings.Contains(req.Messages[0].Content, records[0].ID) || env.submits != 0 || env.starts != 0 {
				t.Fatalf("persisted local task: %+v %v sends %d/%d", records, err, env.submits, env.starts)
			}
		}
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
	}}
	loop := NewLoop(provider, nil, contextConfig(t), WithMemoryStore(memory.NewInMemoryStore()))
	if err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("model requests=%d", len(provider.requests))
	}
}
func TestRuntimeToolTaskGating(t *testing.T) {
	for _, scenario := range []string{"disabled", "not ready", "invalid source", "foreign scope", "no proposal"} {
		t.Run(scenario, func(t *testing.T) {
			env, key, event, catalog := taskContextFixture(t)
			config := contextConfig(t)
			switch scenario {
			case "disabled":
				config.Task.Enabled = false
			case "not ready":
				env.world.ready = false
			case "invalid source":
				event.InteractionSource = nil
			case "foreign scope":
				event.InteractionSource.Scope.WorldRunId = "other"
			}
			p := &taskContextModel{run: func(req model.Request, _ int) model.ModelDecision {
				if !strings.Contains(req.System, "current View") {
					t.Fatalf("prompt lacks current View constraint: %s", req.System)
				}
				if len(req.Tools) != 1 || req.Tools[0].Name != "inspect" {
					t.Fatalf("gating: %+v", req.Tools)
				}
				return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
			}}
			loop := NewLoop(p, nil, config, WithMemoryStore(memory.NewInMemoryStore()))
			if err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskContextInvalidationClearsSnapshot(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	seedContextTask(t, env, key, "current")
	loop := NewLoop(nil, nil, contextConfig(t))
	turn := loop.beginTaskContext(env, key, event, "turn", catalog)
	env.world.epoch++
	admission, err := turn.snapshot(context.Background(), tool.ToolAdmissionConfig{})
	if err != nil || admission.View.RuntimeContext() != nil || len(admission.View.Available()) != 1 {
		t.Fatalf("invalidated task context retained: %+v %v", admission.View.RuntimeContext(), err)
	}
}
