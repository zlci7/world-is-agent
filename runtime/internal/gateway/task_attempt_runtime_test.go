package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

func beginWireExecution(t *testing.T, f *taskWireFixture) (*streamEnvironment, task.ExecutionContext, task.Record, task.Operation) {
	t.Helper()
	f.server.dispatcher.Stop()
	r, op := seedWireOperation(t, f)
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	env := entry.Environment.(*streamEnvironment)
	if err := f.server.worlds.UpdateClock(f.ctx, env, &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 50, Sequence: 2}}); err != nil {
		t.Fatal(err)
	}
	f.head.Clock = task.Clock{ID: "game", Tick: 50, Sequence: 2}
	wakes, err := f.service.ClaimDue(f.ctx, f.head.Binding, f.head.Clock, 1)
	if err != nil || len(wakes) != 1 {
		t.Fatal(wakes, err)
	}
	w := wakes[0]
	if err := f.service.MarkEnqueued(f.ctx, f.head.Binding, w.ID, w.ClaimID); err != nil {
		t.Fatal(err)
	}
	exec, r, err := f.service.BeginWake(f.ctx, f.head.Binding, w.ID, w.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	return env, exec, r, op
}

func TestTaskAttemptTechnicalFailuresHaveSeparateBoundedCounter(t *testing.T) {
	for _, mode := range []string{"model_error", "timeout", "budget", "cancelled", "no_progress", "observe"} {
		t.Run(mode, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			env, exec, r, _ := beginWireExecution(t, f)
			var failure error
			switch mode {
			case "model_error":
				failure = errors.New("provider failed")
			case "timeout":
				failure = context.DeadlineExceeded
			case "budget":
				failure = errors.New("max steps exceeded")
			case "cancelled":
				failure = context.Canceled
			case "observe":
				failure = taskObservationFailure{errors.New("observation unavailable")}
			}
			done := make(chan error, 1)
			go func() { done <- env.finishTaskExecution(context.Background(), exec, failure) }()
			queries := 2
			if mode != "observe" {
				queries = 3
			}
			for i := 0; i < queries; i++ {
				f.observe(f.next())
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			current := f.awaitRecord(r.ID, func(r task.Record) bool { return r.State == task.StatePaused })
			wantNo := 1
			if mode == "observe" {
				wantNo = 0
			}
			if current.Result != nil || current.ReconcileAttempts != 3 || current.NoProgressAttempts != wantNo || current.PauseReason != "evidence_unconfirmed" || f.model.calls.Load() != 0 {
				t.Fatalf("unbounded/mixed cleanup: %+v calls=%d", current, f.model.calls.Load())
			}
		})
	}
}

func TestTaskObservationTerminalSkipsCognition(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, r, op := beginWireExecution(t, f)
	slot := f.server.worlds.slot(f.head.Binding.World, false)
	slot.mu.Lock()
	catalog := slot.owner.catalog
	slot.mu.Unlock()
	loop := f.server.agentLoop.(*agent.Loop)
	done := make(chan error, 1)
	go func() {
		done <- loop.HandleTaskWake(f.ctx, env, agent.ConnectionContext{GameID: "sim", SessionID: "wire"}, worldRequest().Entities[0], catalog, exec, r)
	}()
	f.observe(f.next(), wireReceipt(f, r, op, "satisfied"))
	control := f.next().GetTaskControl()
	if control == nil {
		t.Fatal("observed terminal did not release")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
	completion := f.next().GetTurnCompletion()
	if completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("terminal evidence called failing model: %v", completion)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.model.calls.Load() != 0 {
		t.Fatal("evidence closure called model")
	}
}

func TestTaskOperationAmbiguousSendReconcilesOneOperation(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, r, op := beginWireExecution(t, f)
	done := make(chan error, 1)
	go func() {
		done <- env.finishTaskExecution(context.Background(), exec, taskActionFailure{context.DeadlineExceeded})
	}()
	query := f.next()
	before, _ := f.service.Read(f.ctx, f.key, r.ID)
	if before.NoProgressAttempts != 0 {
		t.Fatal("ambiguous send counted as model no progress before reconciliation")
	}
	f.observe(query, wireReceipt(f, r, op, "satisfied"))
	control := f.next().GetTaskControl()
	if control == nil {
		t.Fatal("ambiguous operation not reconciled")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, _ := f.service.Read(f.ctx, f.key, r.ID)
	if len(final.Operations) != 1 || final.Operations[0].ID != op.ID || final.Result == nil || f.model.calls.Load() != 0 {
		t.Fatalf("ambiguous action replayed: %+v", final)
	}
}

func TestTaskOperationStaleSnapshotRegistrationSendsNothing(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, r, _ := beginWireExecution(t, f)
	r.Spec.Contract = []byte(`{"clock":{"id":"game","tick":10,"sequence":1},"wake_at":50,"deadline_at":100}`)
	r.Revision++
	_, epoch, _ := env.taskAuthority.Current()
	rc := tool.RuntimeCallContext{Execution: exec, ObservedTask: &r, AuthorityEpoch: epoch}
	req := &protocol.ActionRequest{ActionId: "never-send", WorldId: "world", EntityId: "actor", Capability: "follow_route"}
	if err := env.RegisterTaskAction(f.ctx, rc, req); !errors.Is(err, task.ErrTaskChanged) {
		t.Fatalf("stale registration error=%v", err)
	}
	if req.TaskSource != nil {
		t.Fatal("registration failure authorized send")
	}
	current, _ := f.service.Read(f.ctx, f.key, r.ID)
	if len(current.Operations) != 1 {
		t.Fatal("registration failed but operation was committed")
	}
}

func TestTaskAsyncActionResultWaitCommitsInCurrentExecution(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, _, r, op := beginWireExecution(t, f)
	req := &protocol.ActionRequest{ActionId: op.ActionID, WorldId: "world", EntityId: "actor", Capability: "follow_route"}
	startDone := make(chan error, 1)
	go func() { _, err := env.StartAction(f.ctx, req); startDone <- err }()
	if f.next().GetAction() == nil {
		t.Fatal("no async action")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_ActionStatus{ActionStatus: &protocol.ActionStatusUpdate{ActionId: op.ActionID, Status: protocol.ActionStatus_ACTION_STATUS_RUNNING}}})
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := env.WaitActionResult(f.ctx, op.ActionID); done <- err }()
	evidence := wireReceipt(f, r, op, "progress")
	wait := int64(70)
	evidence.WaitUntil = &wait
	f.result(req, []*protocol.TaskEvidence{evidence}, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ActionResult waited on its own lane")
	}
	current, _ := f.service.Read(f.ctx, f.key, r.ID)
	if current.State != task.StateWaiting || current.NextWakeAt == nil || *current.NextWakeAt != 70 {
		t.Fatalf("async wait missing: %+v", current)
	}
}

func TestTaskPendingTerminalEvidenceEndsWakeWithoutModel(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, r, op := beginWireExecution(t, f)
	if _, err := env.admitTaskEvidence(f.ctx, f.key, []*protocol.TaskEvidence{wireReceipt(f, r, op, "satisfied")}, false, ""); err != nil {
		t.Fatal(err)
	}
	slot := f.server.worlds.slot(f.head.Binding.World, false)
	slot.mu.Lock()
	catalog := slot.owner.catalog
	slot.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- f.server.agentLoop.(*agent.Loop).HandleTaskWake(f.ctx, env, agent.ConnectionContext{}, worldRequest().Entities[0], catalog, exec, r)
	}()
	f.observe(f.next())
	control := f.next().GetTaskControl()
	if control == nil {
		t.Fatal("pending trusted evidence was not deterministically reconciled")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
	if f.next().GetTurnCompletion().GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatal("wake failed after evidence")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.model.calls.Load() != 0 {
		t.Fatal("pending terminal evidence called model")
	}
}

func TestTaskDispatcherModelFailuresFinishExecution(t *testing.T) {
	for _, mode := range []string{"provider", "timeout", "budget", "cancelled", "no_progress"} {
		t.Run(mode, func(t *testing.T) {
			f := newTaskWireFixture(t, false, func(c *agent.Config) { c.LLMTimeout = 20 * time.Millisecond; c.MaxToolCallsPerStep = 1 })
			f.model.mode = mode
			source := task.SourceRef{Kind: task.SourceKindInternal, EventID: "seed", TurnID: "seed", CallID: "seed"}
			exec := task.ExecutionContext{Owner: f.key, Binding: f.head.Binding, Clock: f.head.Clock, Source: source}
			created, err := f.service.Create(f.ctx, exec, task.TaskSpec{Instruction: "Inspect later", ClockID: "game", WakeAt: 11, DeadlineAt: 100, ResultContract: task.ResultContractAuthoritativeEvidence, Source: source}, task.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 11, Sequence: 2}}}})
			for attempt := 0; attempt < 3; attempt++ {
				f.observe(f.next())
				if completion := f.next().GetTurnCompletion(); completion == nil {
					t.Fatal("cognition omitted TurnCompletion")
				}
				if attempt < 2 {
					f.observe(f.next())
				}
			}
			final := f.awaitRecord(created.Task.ID, func(r task.Record) bool { return r.State == task.StatePaused })
			if final.Result != nil || final.NoProgressAttempts != 3 || final.ReconcileAttempts != 0 || final.PauseReason != "no_progress" || f.model.calls.Load() != 3 {
				t.Fatalf("attempt was not finite: %+v calls=%d", final, f.model.calls.Load())
			}
		})
	}
}

func TestTaskOperationSaveFencesSyncAndAsyncBeforeSend(t *testing.T) {
	for _, async := range []bool{false, true} {
		name := "sync"
		if async {
			name = "async"
		}
		t.Run(name, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			env, _, _, _ := beginWireExecution(t, f)
			if err := f.server.worlds.SetSaveBarrier(env, f.head.Binding, true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			req := &protocol.ActionRequest{ActionId: "fenced", WorldId: "world", EntityId: "actor", Capability: "follow_route", TaskSource: &protocol.TaskActionSource{Scope: taskScopeToProtocol(f.head.Binding)}}
			var err error
			if async {
				_, err = env.StartAction(ctx, req)
			} else {
				_, err = env.SubmitAction(ctx, req)
			}
			if !errors.Is(err, task.ErrSaveInProgress) {
				t.Fatalf("save fence did not reject emission: %v", err)
			}
			select {
			case message := <-f.messages:
				t.Fatalf("action crossed save fence: %v", message)
			default:
			}
			if err := f.server.worlds.SetSaveBarrier(env, f.head.Binding, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskDeadlineObservationCannotAuthorizeCognition(t *testing.T) {
	f := newTaskWireFixture(t, false)
	r, _ := seedWireOperation(t, f)
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 100, Sequence: 2}}}})
	f.observe(f.next())
	if completion := f.next().GetTurnCompletion(); completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("deadline observation called model: %v", completion)
	}
	for i := 0; i < 2; i++ {
		f.observe(f.next())
	}
	final := f.awaitRecord(r.ID, func(r task.Record) bool { return r.State == task.StatePaused })
	if final.Result != nil || final.NoProgressAttempts != 0 || final.ReconcileAttempts != 3 || f.model.calls.Load() != 0 {
		t.Fatalf("deadline bypassed deterministic reconciliation: %+v", final)
	}
}

func TestTaskFinishAttemptClockAdvanceAfterSnapshot(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, record, _ := beginWireExecution(t, f)
	head, epoch, _ := env.taskAuthority.Current()
	if err := f.server.worlds.UpdateClock(f.ctx, env, &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 51, Sequence: 3}}); err != nil {
		t.Fatal(err)
	}
	finished, err := env.finishTaskAttempt(f.ctx, exec, head, epoch, record, nil)
	if err != nil {
		t.Fatalf("normal Clock advance rejected cleanup: %v", err)
	}
	if finished.NoProgressAttempts != 1 {
		t.Fatalf("cleanup ownership duplicated: %+v", finished)
	}
	if _, _, ready := env.taskAuthority.Current(); !ready {
		t.Fatal("cleanup paused a normally advancing world")
	}
}

func TestTaskOperationEmptyInternalContractSendsAction(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, record, _ := beginWireExecution(t, f)
	_, epoch, _ := env.taskAuthority.Current()
	req := &protocol.ActionRequest{ActionId: "internal-action", WorldId: "world", EntityId: "actor", Capability: "follow_route"}
	if err := env.RegisterTaskAction(f.ctx, tool.RuntimeCallContext{Execution: exec, ObservedTask: &record, AuthorityEpoch: epoch}, req); err != nil {
		t.Fatalf("valid internal task contract rejected: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := env.SubmitAction(f.ctx, req); done <- err }()
	got := f.next().GetAction()
	if got.GetActionId() != "internal-action" || got.TaskSource == nil || got.TaskSource.TaskContract != nil {
		t.Fatalf("internal action source: %v", got)
	}
	f.result(got, nil, nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTaskOperationLastSendGateRevalidatesEligibility(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, change := range []string{"deadline_during_cognition", "deadline_after_registration", "pending_evidence", "terminal", "revision", "waiting"} {
			t.Run(fmt.Sprintf("async=%v/%s", async, change), func(t *testing.T) {
				f := newTaskWireFixture(t, false)
				env, exec, record, prior := beginWireExecution(t, f)
				_, epoch, _ := env.taskAuthority.Current()
				rc := tool.RuntimeCallContext{Execution: exec, ObservedTask: &record, AuthorityEpoch: epoch}
				req := &protocol.ActionRequest{ActionId: "must-not-send", WorldId: "world", EntityId: "actor", Capability: "follow_route"}
				advance := func() {
					if err := f.server.worlds.UpdateClock(f.ctx, env, &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(exec.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 100, Sequence: 3}}); err != nil {
						t.Fatal(err)
					}
				}
				if change == "deadline_during_cognition" {
					advance()
				}
				registerErr := env.RegisterTaskAction(f.ctx, rc, req)
				if change != "deadline_during_cognition" && registerErr != nil {
					t.Fatal(registerErr)
				}
				if change == "deadline_after_registration" {
					advance()
				}
				if change == "pending_evidence" || change == "terminal" {
					if _, err := env.admitTaskEvidence(f.ctx, f.key, []*protocol.TaskEvidence{wireReceipt(f, record, prior, "satisfied")}, false, ""); err != nil {
						t.Fatal(err)
					}
					if change == "terminal" {
						if _, err := f.service.Reconcile(f.ctx, exec); err != nil {
							t.Fatal(err)
						}
					}
				}
				if change == "revision" {
					if _, err := f.service.FinishAttempt(f.ctx, exec, task.AttemptOutcome{Kind: task.AttemptOutcomeKindNoProgress}); err != nil {
						t.Fatal(err)
					}
				}
				if change == "waiting" {
					wait := int64(70)
					exec.Source = task.SourceRef{Kind: task.SourceKindTaskWake, EventID: "wake", TurnID: "turn", CallID: "wait"}
					if _, err := f.service.ApplyIntent(f.ctx, exec, task.Intent{Kind: "wait", NextWakeAt: &wait}); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
				defer cancel()
				err := registerErr
				if err == nil {
					if async {
						_, err = env.StartAction(ctx, req)
					} else {
						_, err = env.SubmitAction(ctx, req)
					}
				}
				if err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("last send gate did not reject changed task: %v", err)
				}
				sendErr := err
				select {
				case message := <-f.messages:
					t.Fatalf("ineligible action emitted: %v", message)
				default:
				}
				stored, err := f.service.Read(f.ctx, f.key, record.ID)
				if err != nil {
					t.Fatal(err)
				}
				if registerErr == nil && len(stored.Operations) != 2 {
					t.Fatalf("registered operation lost before cleanup: %+v", stored.Operations)
				}
				if change == "pending_evidence" || change == "terminal" {
					done := make(chan error, 1)
					go func() { done <- env.finishTaskExecution(f.ctx, exec, sendErr) }()
					for i := 0; i < 2; i++ {
						control := f.next().GetTaskControl()
						if control == nil {
							t.Fatal("registered rejected operation lost cleanup responsibility")
						}
						f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: control.TaskId, OperationId: control.OperationId, RequestId: control.RequestId, Status: "released"}}})
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					final, err := f.service.Read(f.ctx, f.key, record.ID)
					if err != nil || final.Result == nil || len(final.Cleanup) != 2 || f.model.calls.Load() != 0 {
						t.Fatalf("deterministic terminal cleanup failed: %+v err=%v", final, err)
					}
				}
				if change == "deadline_after_registration" {
					done := make(chan error, 1)
					go func() { done <- env.finishTaskExecution(f.ctx, exec, sendErr) }()
					for i := 0; i < 3; i++ {
						f.observe(f.next())
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					final, err := f.service.Read(f.ctx, f.key, record.ID)
					if err != nil || final.State != task.StatePaused || final.ReconcileAttempts != 3 || len(final.Operations) != 2 || f.model.calls.Load() != 0 {
						t.Fatalf("deadline lost registered operation reconciliation: %+v err=%v", final, err)
					}
				}
			})
		}
	}
}

func TestTaskInitialObservationTransportFailureClassified(t *testing.T) {
	f := newTaskWireFixture(t, false)
	f.server.dispatcher.Stop()
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	env := entry.Environment.(*streamEnvironment)
	env.close()
	_, err := env.Observe(f.ctx, "world", "actor")
	var failure taskObservationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("initial observation transport failure was not classified: %v", err)
	}
}
