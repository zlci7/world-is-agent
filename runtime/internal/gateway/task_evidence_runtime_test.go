package gateway

import (
	"context"
	"database/sql"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"google.golang.org/protobuf/proto"
)

func seedWireOperation(t *testing.T, f *taskWireFixture) (task.Record, task.Operation) {
	t.Helper()
	source := task.SourceRef{Kind: task.SourceKindInternal, EventID: "seed", TurnID: "seed", CallID: "seed"}
	exec := task.ExecutionContext{Owner: f.key, Binding: f.head.Binding, Clock: f.head.Clock, Source: source}
	created, err := f.service.Create(f.ctx, exec, task.TaskSpec{Instruction: "Complete the agreed generic contract", ClockID: "game", WakeAt: 50, DeadlineAt: 100, ResultContract: task.ResultContractAuthoritativeEvidence, Source: source}, task.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	exec.TaskID, exec.ExpectedRevision = created.Task.ID, created.Task.Revision
	op, err := f.service.RegisterOperation(f.ctx, exec, task.Operation{ID: "operation", ActionID: "action", CommandFingerprint: "exact-intent", Binding: exec.Binding, StartRevision: exec.ExpectedRevision, Status: task.OperationStatusRegistered})
	if err != nil {
		t.Fatal(err)
	}
	return created.Task, op
}

func wireReceipt(f *taskWireFixture, r task.Record, op task.Operation, outcome string) *protocol.TaskEvidence {
	return &protocol.TaskEvidence{FactId: "receipt", TaskId: r.ID, OperationId: op.ID, Scope: taskScopeToProtocol(f.head.Binding), StartRevision: op.StartRevision, OccurredAt: 10, Outcome: outcome}
}

func TestEvidenceDoesNotRequireModel(t *testing.T) {
	for _, outcome := range []string{"satisfied", "unsatisfied"} {
		for _, interaction := range []bool{false, true} {
			name := outcome
			if interaction {
				name += "/interaction"
			}
			t.Run(name, func(t *testing.T) {
				f := newTaskWireFixture(t, false)
				r, op := seedWireOperation(t, f)
				var source *protocol.InteractionSource
				if interaction {
					source = &protocol.InteractionSource{SourceId: "arrival-source", Kind: "arrival", PlayerEntityId: "player", TaskId: r.ID, OperationId: op.ID, Scope: taskScopeToProtocol(f.head.Binding)}
				}
				evidence := wireReceipt(f, r, op, outcome)
				f.event("receipt-event", []*protocol.TaskEvidence{evidence}, source)
				if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
					t.Fatal("evidence rejected")
				}
				control := f.next().GetTaskControl()
				if control == nil {
					t.Fatal("deterministic terminal did not release control")
				}
				f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{RequestId: control.RequestId, TaskId: r.ID, OperationId: op.ID, Scope: control.Scope, Status: "handed_off"}}})
				confirmed := f.awaitRecord(r.ID, func(r task.Record) bool { return r.Result != nil && len(r.Cleanup) == 1 })
				if interaction {
					f.observe(f.next())
					completion := f.next().GetTurnCompletion()
					if completion.GetEventId() != "receipt-event" || completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED {
						t.Fatalf("ordinary interaction: %v", completion)
					}
				}
				wantCalls := int32(0)
				if interaction {
					wantCalls = 1
				}
				if f.model.calls.Load() != wantCalls {
					t.Fatalf("model calls=%d", f.model.calls.Load())
				}
				f.event("receipt-retry", []*protocol.TaskEvidence{evidence}, source)
				if f.next().GetEventAck() == nil {
					t.Fatal("duplicate evidence started another turn")
				}
				current := f.awaitRecord(r.ID, func(r task.Record) bool { return r.Result != nil })
				if current.Result.ID != confirmed.Result.ID || len(current.Evidence) != 1 || len(current.Cleanup) != 1 || f.model.calls.Load() != wantCalls {
					t.Fatalf("duplicate or dialogue failure changed terminal: %+v calls=%d", current, f.model.calls.Load())
				}
				if interaction {
					source.SourceId = "another-correlation-for-same-fact"
					f.event("same-fact", []*protocol.TaskEvidence{evidence}, source)
					if f.next().GetEventAck() == nil {
						t.Fatal("missing duplicate fact ack")
					}
					entry, _ := f.server.worlds.Current(f.head.Binding.World)
					lane, _ := entry.Lanes.GetOrCreate(f.key)
					idle := make(chan struct{})
					if err := lane.EnqueueMaintenance(session.Task{ID: "idle-probe", Run: func(context.Context) { close(idle) }}); err != nil {
						t.Fatal(err)
					}
					select {
					case <-idle:
					case message := <-f.messages:
						t.Fatalf("same fact started another interaction: %v", message)
					case <-time.After(time.Second):
						t.Fatal("evidence lane did not finish")
					}
					if f.model.calls.Load() != wantCalls {
						t.Fatal("same fact repeated model")
					}
				}
			})
		}
	}
}

func TestTaskControlOutcomesAndCorrelation(t *testing.T) {
	for _, status := range []string{"released", "handed_off", "unconfirmed", "timeout", "mismatch"} {
		t.Run(status, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			r, op := seedWireOperation(t, f)
			f.event("terminal", []*protocol.TaskEvidence{wireReceipt(f, r, op, "satisfied")}, nil)
			if f.next().GetEventAck() == nil {
				t.Fatal("missing ack")
			}
			control := f.next().GetTaskControl()
			if control == nil {
				t.Fatal("missing release")
			}
			result := &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: status}
			if status == "mismatch" {
				result.Status = "released"
				result.OperationId = "wrong"
				f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: result}})
				if f.next().GetError() == nil {
					t.Fatal("mismatched result accepted")
				}
				result.OperationId = op.ID
			}
			if status != "timeout" {
				f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: result}})
			}
			final := f.awaitRecord(r.ID, func(r task.Record) bool { return len(r.Cleanup) == 1 })
			want := status
			if status == "timeout" {
				want = "unconfirmed"
			}
			if status == "mismatch" {
				want = "released"
			}
			if final.Cleanup[0].Status != want {
				t.Fatalf("cleanup=%+v", final.Cleanup)
			}
			result.Status = "released"
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: result}})
			if f.next().GetError() == nil {
				t.Fatal("duplicate release result accepted")
			}
			f.event("duplicate-terminal", []*protocol.TaskEvidence{wireReceipt(f, r, op, "satisfied")}, nil)
			if f.next().GetEventAck() == nil {
				t.Fatal("duplicate sent cleanup again")
			}
		})
	}
}

func TestTaskEvidenceRejectsInvalidStaleAndUnregistered(t *testing.T) {
	for _, mode := range []string{"operation", "revision", "generation", "run", "world"} {
		t.Run(mode, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			r, op := seedWireOperation(t, f)
			e := wireReceipt(f, r, op, "satisfied")
			switch mode {
			case "operation":
				e.OperationId = "unknown"
			case "revision":
				e.StartRevision++
			case "generation":
				e.Scope.ExecutionGeneration++
			case "run":
				e.Scope.WorldRunId = "rolled-back-run"
			case "world":
				e.Scope.WorldId = "other"
			}
			f.event("invalid", []*protocol.TaskEvidence{e}, nil)
			if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_REJECTED {
				t.Fatal("invalid fact accepted")
			}
			current, _ := f.service.Read(f.ctx, f.key, r.ID)
			if current.Result != nil || len(current.Evidence) != 0 || f.model.calls.Load() != 0 {
				t.Fatal("untrusted evidence changed task")
			}
		})
	}
}

func TestTaskEvidenceConcurrentResultEventAndObservation(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	env := entry.Environment.(*streamEnvironment)
	evidence := wireReceipt(f, r, op, "satisfied")
	const count = 8
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			_, err := env.admitTaskEvidence(context.Background(), f.key, []*protocol.TaskEvidence{proto.Clone(evidence).(*protocol.TaskEvidence)}, false, op.ActionID)
			results <- err
		}()
	}
	for i := 0; i < count; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	f.event("terminal", []*protocol.TaskEvidence{evidence}, nil)
	if f.next().GetEventAck() == nil {
		t.Fatal("missing ack")
	}
	control := f.next().GetTaskControl()
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{RequestId: control.RequestId, Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, Status: "released"}}})
	final := f.awaitRecord(r.ID, func(r task.Record) bool { return len(r.Cleanup) == 1 })
	if len(final.Evidence) != 1 || final.Result == nil || f.model.calls.Load() != 0 {
		t.Fatalf("race: %+v", final)
	}
}

func TestTaskControlDisconnectPersistsUnconfirmed(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	f.event("terminal", []*protocol.TaskEvidence{wireReceipt(f, r, op, "satisfied")}, nil)
	f.next()
	if f.next().GetTaskControl() == nil {
		t.Fatal("missing control")
	}
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := f.service.Read(f.ctx, f.key, r.ID)
	if len(current.Cleanup) != 1 || current.Cleanup[0].Status != "unconfirmed" {
		t.Fatalf("disconnect lost cleanup: %+v", current)
	}
	if entry.Environment.(*streamEnvironment).resolveTaskControl(&protocol.TaskControlResult{Scope: taskScopeToProtocol(entry.Head.Binding), RequestId: "old", TaskId: r.ID, OperationId: op.ID, Status: "released"}) {
		t.Fatal("stale stream accepted control")
	}
}

func TestTaskEvidenceSameRunActiveQueryRevalidatesOriginalReceipt(t *testing.T) {
	f := newTaskWireFixture(t, false)
	oldEnv, _, r, op := beginWireExecution(t, f)
	original := wireReceipt(f, r, op, "satisfied")
	request := worldRequest()
	request.Clock = &protocol.WorldClock{ClockId: "game", NowTick: 50, Sequence: 2}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: request}})
	ready := f.next().GetWorldBindingReady()
	if ready.GetStatus() != "ready" || ready.Scope.ExecutionGeneration != 2 {
		t.Fatalf("rebind: %v", ready)
	}
	if _, err := oldEnv.admitTaskEvidence(f.ctx, f.key, []*protocol.TaskEvidence{original}, true, ""); err == nil {
		t.Fatal("expired stream receipt accepted")
	}
	f.event("late", []*protocol.TaskEvidence{original}, nil)
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_REJECTED {
		t.Fatal("old receipt accepted outside active query")
	}
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	env := entry.Environment.(*streamEnvironment)
	done := make(chan error, 1)
	go func() {
		_, err := env.Observe(context.WithValue(f.ctx, taskReconcileQueryKey{}, true), "world", "actor")
		done <- err
	}()
	f.observe(f.next(), original)
	control := f.next().GetTaskControl()
	if control.GetScope().GetExecutionGeneration() != 2 {
		t.Fatalf("cleanup did not use current scope: %v", control)
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{RequestId: control.RequestId, Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, Status: "released"}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, _ := f.service.Read(f.ctx, f.key, r.ID)
	if final.Result == nil || len(final.Evidence) != 1 {
		t.Fatalf("revalidation: %+v", final)
	}
	e := final.Evidence[0]
	if e.RevalidatedIn == nil || e.RevalidatedIn.Generation != 2 || e.Binding.Generation != 1 || e.StartRevision != op.StartRevision || e.OccurredAt != 10 {
		t.Fatalf("original receipt identity lost: %+v", e)
	}
}

func TestTaskEvidenceBackgroundProgressHasNoModelTurn(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	evidence := wireReceipt(f, r, op, "progress")
	f.event("progress", []*protocol.TaskEvidence{evidence}, nil)
	if f.next().GetEventAck() == nil {
		t.Fatal("no evidence ack")
	}
	current := f.awaitRecord(r.ID, func(r task.Record) bool { return len(r.Evidence) == 1 && r.Evidence[0].Applied })
	if current.Result != nil || f.model.calls.Load() != 0 {
		t.Fatalf("progress started cognition: %+v", current)
	}
}

func TestTaskControlPersistenceRetryKeepsOneRequest(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	env := entry.Environment.(*streamEnvironment)
	if _, err := env.admitTaskEvidence(f.ctx, f.key, []*protocol.TaskEvidence{wireReceipt(f, r, op, "satisfied")}, false, ""); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER cleanup_fault BEFORE UPDATE ON tasks WHEN json_array_length(NEW.record_json,'$.cleanup') > 0 BEGIN SELECT RAISE(ABORT,'cleanup unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- env.ReconcileTask(f.ctx, f.key, r.ID) }()
	control := f.next().GetTaskControl()
	if control == nil {
		t.Fatal("no cleanup")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{RequestId: control.RequestId, Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, Status: "released"}}})
	if err := <-done; err == nil {
		t.Fatal("test fault did not block persistence")
	}
	if _, err := db.Exec(`DROP TRIGGER cleanup_fault`); err != nil {
		t.Fatal(err)
	}
	if err := env.ReconcileTask(f.ctx, f.key, r.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := f.service.Read(f.ctx, f.key, r.ID)
	if len(current.Cleanup) != 1 || current.Cleanup[0].Status != "released" {
		t.Fatalf("retry lost observed release outcome: %+v", current.Cleanup)
	}
	select {
	case m := <-f.messages:
		t.Fatalf("cleanup retry repeated wire request: %v", m)
	default:
	}
}

func TestTaskControlPlayerCancellationReleasesOwnedOperation(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	f.model.mode = "player_cancel"
	f.model.taskID = r.ID
	f.event("player-cancel", nil, &protocol.InteractionSource{SourceId: "cancel-input", Kind: "player", PlayerEntityId: "player", Scope: taskScopeToProtocol(f.head.Binding)})
	if f.next().GetEventAck() == nil {
		t.Fatal("missing interaction ack")
	}
	f.observe(f.next())
	control := f.next().GetTaskControl()
	if control == nil {
		t.Fatal("committed player cancellation retained task-owned control")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
	if f.next().GetTurnCompletion().GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatal("player acknowledgment did not finish")
	}
	final, _ := f.service.Read(f.ctx, f.key, r.ID)
	if final.State != task.StateCancelled || len(final.Cleanup) != 1 {
		t.Fatalf("cancel release: %+v", final)
	}
}

func TestTaskControlPersistenceExhaustionClosesAdmission(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	db, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER cleanup_fault BEFORE UPDATE ON tasks WHEN json_array_length(NEW.record_json,'$.cleanup') > 0 BEGIN SELECT RAISE(ABORT,'cleanup unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	f.event("terminal", []*protocol.TaskEvidence{wireReceipt(f, r, op, "satisfied")}, nil)
	f.next()
	control := f.next().GetTaskControl()
	if control == nil {
		t.Fatal("missing release")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
	end := time.Now().Add(time.Second)
	for {
		if _, ready := f.server.worlds.Current(f.head.Binding.World); !ready {
			break
		}
		if time.Now().After(end) {
			t.Fatal("cleanup persistence failure left task admission open")
		}
		time.Sleep(time.Millisecond)
	}
	current, _ := f.service.Read(f.ctx, f.key, r.ID)
	if current.Result == nil || len(current.Cleanup) != 0 || f.model.calls.Load() != 0 {
		t.Fatalf("cleanup failure changed business fact: %+v", current)
	}
	select {
	case m := <-f.messages:
		t.Fatalf("technical cleanup retry repeated wire work: %v", m)
	default:
	}
}

func TestEvidenceInteractionDedupSurvivesSameRunRebind(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, op := seedWireOperation(t, f)
	evidence := wireReceipt(f, r, op, "satisfied")
	source := &protocol.InteractionSource{SourceId: "arrival", Kind: "arrival", PlayerEntityId: "player", TaskId: r.ID, OperationId: op.ID, Scope: taskScopeToProtocol(f.head.Binding)}
	f.event("arrival", []*protocol.TaskEvidence{evidence}, source)
	f.next()
	control := f.next().GetTaskControl()
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
	f.observe(f.next())
	if f.next().GetTurnCompletion() == nil {
		t.Fatal("no interaction completion")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldRequest()}})
	ready := f.next().GetWorldBindingReady()
	if ready.GetStatus() != "ready" || ready.Scope.ExecutionGeneration != 2 {
		t.Fatalf("rebind: %v", ready)
	}
	source.Scope = ready.Scope
	f.event("arrival-replay", []*protocol.TaskEvidence{evidence}, source)
	if ack := f.next().GetEventAck(); ack.GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("known same-run fact could not correlate real interaction: %v", ack)
	}
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	lane, _ := entry.Lanes.GetOrCreate(f.key)
	idle := make(chan struct{})
	if err := lane.EnqueueMaintenance(session.Task{ID: "idle", Run: func(context.Context) { close(idle) }}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-idle:
	case m := <-f.messages:
		t.Fatalf("rebind replayed work: %v", m)
	case <-time.After(time.Second):
		t.Fatal("rebind evidence did not finish")
	}
	if f.model.calls.Load() != 1 {
		t.Fatal("rebind repeated interaction")
	}
}

func TestTaskRuntimeShutdownFencesActiveDispatcherAndReleasesStore(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, _ := seedWireOperation(t, f)
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 50, Sequence: 2}}}})
	if f.next().GetObserve() == nil {
		t.Fatal("dispatcher did not start execution")
	}
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f.server.dispatcher.Notify()
	if len(f.server.worlds.ReadyWorlds()) != 0 || f.model.calls.Load() != 0 {
		t.Fatal("shutdown retained admission/model execution")
	}
	env := entry.Environment.(*streamEnvironment)
	env.pendingMu.Lock()
	pending := len(env.pendingObservations) + len(env.pendingActions)
	env.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("shutdown retained %d pending calls", pending)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: f.dbPath})
	if err != nil {
		t.Fatalf("shutdown retained store owner: %v", err)
	}
	defer reopened.Close()
	current, err := task.NewService(reopened).Read(context.Background(), f.key, r.ID)
	if err != nil || current.Result != nil {
		t.Fatalf("shutdown changed business outcome: %+v %v", current, err)
	}
}
