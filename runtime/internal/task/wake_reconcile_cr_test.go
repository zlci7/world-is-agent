package task

import (
	"context"
	"errors"
	"testing"
)

func TestWakeReconcileAcceptsExactShortAndCompleteTaskTurnSources(t *testing.T) {
	t.Run("exact Begin short source", func(t *testing.T) {
		fixture, _, wake, _ := readyEnqueuedWake(t, 200, 300, 200)
		exec, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := fixture.svc.Reconcile(context.Background(), exec)
		if err != nil || result.Next != ReconcileNextDecide {
			t.Fatalf("short-source Reconcile = (%+v, %v)", result, err)
		}
	})

	for _, kind := range []string{EvidenceKindProgress, EvidenceKindSatisfied} {
		t.Run("complete source consumes running wake after "+kind, func(t *testing.T) {
			fixture, _, wake, clock := readyEnqueuedWake(t, 200, 300, 200)
			exec, running, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID)
			if err != nil {
				t.Fatal(err)
			}
			evidence := taskEvidence(fixture.head.Binding, running, "fact-complete-"+kind, kind)
			evidence.OccurredAt = clock.Tick
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence = (%v, %v)", added, err)
			}
			exec.Source = SourceRef{
				Kind: SourceKindTaskWake, EventID: "task-event-" + kind,
				TurnID: "task-turn-" + kind, CallID: "task-call-" + kind,
			}
			if exec.Source.CallID == exec.WakeID {
				t.Fatal("fixture did not exercise CallID distinct from WakeID")
			}
			result, err := fixture.svc.Reconcile(context.Background(), exec)
			if err != nil {
				t.Fatalf("complete-source Reconcile error = %v", err)
			}
			if kind == EvidenceKindProgress && (result.Task.State != StateRunning || result.Next != ReconcileNextDecide) {
				t.Fatalf("progress result = %+v", result)
			}
			if kind == EvidenceKindSatisfied && (result.Task.State != StateSucceeded || result.Next != ReconcileNextSettled) {
				t.Fatalf("terminal result = %+v", result)
			}
			storedWake, err := fixture.store.loadWake(context.Background(), wake.Owner, wake.ID)
			if err != nil || storedWake.Status != wakeStatusConsumed {
				t.Fatalf("running wake after Reconcile = (%+v, %v)", storedWake, err)
			}
		})
	}
}

func TestWakeReconcileRejectsMixedAndIncompleteSources(t *testing.T) {
	base := ExecutionContext{
		Owner: testOwner(), Binding: testBinding(), Clock: testClock(),
		Source: SourceRef{Kind: SourceKindTaskWake, CallID: "wake-a"},
		TaskID: "task-a", WakeID: "wake-a", ExpectedRevision: 2,
	}
	tests := []struct {
		name   string
		mutate func(*ExecutionContext)
	}{
		{name: "short CallID differs", mutate: func(exec *ExecutionContext) { exec.Source.CallID = "other-call" }},
		{name: "event without turn", mutate: func(exec *ExecutionContext) { exec.Source.EventID = "event-a" }},
		{name: "turn without event", mutate: func(exec *ExecutionContext) { exec.Source.TurnID = "turn-a" }},
		{name: "complete task wake missing CallID", mutate: func(exec *ExecutionContext) {
			exec.Source.EventID, exec.Source.TurnID, exec.Source.CallID = "event-a", "turn-a", ""
		}},
		{name: "task wake missing WakeID", mutate: func(exec *ExecutionContext) {
			exec.WakeID = ""
			exec.Source.EventID, exec.Source.TurnID, exec.Source.CallID = "event-a", "turn-a", "call-a"
		}},
		{name: "other source missing EventID", mutate: func(exec *ExecutionContext) {
			exec.Source = SourceRef{Kind: SourceKindInternal, TurnID: "turn-a", CallID: "call-a"}
		}},
		{name: "other source missing TurnID", mutate: func(exec *ExecutionContext) {
			exec.Source = SourceRef{Kind: SourceKindInternal, EventID: "event-a", CallID: "call-a"}
		}},
		{name: "other source missing CallID", mutate: func(exec *ExecutionContext) {
			exec.Source = SourceRef{Kind: SourceKindInternal, EventID: "event-a", TurnID: "turn-a"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := base
			tt.mutate(&exec)
			if err := validateReconcileExecution(exec); !errors.Is(err, ErrSourceInvalid) {
				t.Fatalf("validateReconcileExecution error = %v, want source_invalid", err)
			}
		})
	}
}
