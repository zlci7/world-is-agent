package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestFinishAttemptTransitionTable(t *testing.T) {
	tests := []struct {
		name            string
		beforeNo        int
		beforeReconcile int
		beforeNeeds     bool
		outcome         AttemptOutcome
		wantNo          int
		wantReconcile   int
		wantNeeds       bool
	}{
		{name: "cognitive no progress", beforeNo: 1, beforeReconcile: 2, outcome: AttemptOutcome{Kind: AttemptOutcomeKindNoProgress, Reason: "model made no durable progress"}, wantNo: 2, wantNeeds: true},
		{name: "technical reconcile failure", beforeNo: 2, beforeReconcile: 1, beforeNeeds: true, outcome: AttemptOutcome{Kind: AttemptOutcomeKindReconcileFailed}, wantNo: 2, wantReconcile: 2, wantNeeds: true},
		{name: "successful reconciliation preserves cognitive streak", beforeNo: 2, beforeReconcile: 2, beforeNeeds: true, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, wantNo: 2},
		{name: "domain progress clears both streaks", beforeNo: 2, beforeReconcile: 2, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, wantNo: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRunningAttemptFixture(t)
			before := fixture.running
			before.NoProgressAttempts = tt.beforeNo
			before.ReconcileAttempts = tt.beforeReconcile
			before.NeedsReconcile = tt.beforeNeeds
			before.Progress = jsonLiteralForAttempt(t, `{"step":"unchanged"}`)
			storeD2TaskForTest(t, fixture.store, before)
			wakeBefore := rawOnlyWakeJSON(t, fixture.store, before)

			got, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, tt.outcome)
			if err != nil {
				t.Fatalf("FinishAttempt error = %v", err)
			}
			if got.Revision != before.Revision+1 || got.State != StateRunning || got.NextWakeAt != nil || got.Result != nil ||
				got.NoProgressAttempts != tt.wantNo || got.ReconcileAttempts != tt.wantReconcile || got.NeedsReconcile != tt.wantNeeds ||
				got.PauseReason != "" || !bytes.Equal(got.Progress, before.Progress) || !reflect.DeepEqual(got.Operations, before.Operations) ||
				!reflect.DeepEqual(got.Evidence, before.Evidence) || !reflect.DeepEqual(got.Cleanup, before.Cleanup) {
				t.Fatalf("FinishAttempt result = %+v", got)
			}
			if after := rawOnlyWakeJSON(t, fixture.store, before); !bytes.Equal(after, wakeBefore) {
				t.Fatal("non-pausing outcome changed running Wake")
			}
			stored, err := fixture.svc.Read(context.Background(), before.Owner, before.ID)
			if err != nil || !reflect.DeepEqual(stored, got) {
				t.Fatalf("stored Task = (%+v, %v), want returned Task", stored, err)
			}
		})
	}
}

func TestFinishAttemptPropagatesRevisionAndAcceptsBothWakeSources(t *testing.T) {
	fixture := newRunningAttemptFixture(t)
	exec := fixture.exec

	first, err := fixture.svc.FinishAttempt(context.Background(), exec, AttemptOutcome{Kind: AttemptOutcomeKindNoProgress})
	if err != nil || first.Revision != 3 || first.NoProgressAttempts != 1 || !first.NeedsReconcile {
		t.Fatalf("first no-progress = (%+v, %v)", first, err)
	}
	if _, err := fixture.svc.FinishAttempt(context.Background(), exec, AttemptOutcome{Kind: AttemptOutcomeKindNoProgress}); !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("response-loss retry error = %v", err)
	}
	exec.ExpectedRevision = first.Revision
	exec.Source = SourceRef{Kind: SourceKindTaskWake, EventID: "attempt-event", TurnID: "attempt-turn", CallID: "attempt-call"}
	second, err := fixture.svc.FinishAttempt(context.Background(), exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress})
	if err != nil || second.Revision != 4 || second.NoProgressAttempts != 1 || second.ReconcileAttempts != 0 || second.NeedsReconcile {
		t.Fatalf("reconciliation progress = (%+v, %v)", second, err)
	}
	exec.ExpectedRevision = second.Revision
	third, err := fixture.svc.FinishAttempt(context.Background(), exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress})
	if err != nil || third.Revision != 5 || third.NoProgressAttempts != 0 || third.ReconcileAttempts != 0 || third.NeedsReconcile {
		t.Fatalf("domain progress = (%+v, %v)", third, err)
	}
}

func TestFinishAttemptRejectsInvalidTransitionAndFencesWithoutMutation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*runningAttemptFixture)
		outcome AttemptOutcome
		want    error
	}{
		{name: "blank reason", outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress, Reason: "   "}, want: ErrInvalidTaskSpec},
		{name: "unknown outcome", outcome: AttemptOutcome{Kind: "retry"}, want: ErrInvalidTaskSpec},
		{name: "no progress while reconciling", mutate: func(f *runningAttemptFixture) {
			f.running.NeedsReconcile = true
			storeD2TaskForTest(t, f.store, f.running)
		}, outcome: AttemptOutcome{Kind: AttemptOutcomeKindNoProgress}, want: ErrTaskChanged},
		{name: "reconcile failure while deciding", outcome: AttemptOutcome{Kind: AttemptOutcomeKindReconcileFailed}, want: ErrTaskChanged},
		{name: "stale revision", mutate: func(f *runningAttemptFixture) { f.exec.ExpectedRevision-- }, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, want: ErrTaskChanged},
		{name: "mixed short source", mutate: func(f *runningAttemptFixture) {
			f.exec.Source.EventID = "event-only"
		}, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, want: ErrSourceInvalid},
		{name: "non wake source", mutate: func(f *runningAttemptFixture) {
			f.exec.Source = SourceRef{Kind: SourceKindInternal, EventID: "event", TurnID: "turn", CallID: "call"}
		}, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, want: ErrSourceInvalid},
		{name: "wrong wake", mutate: func(f *runningAttemptFixture) {
			f.exec.WakeID = "wake-other"
			f.exec.Source.CallID = f.exec.WakeID
		}, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, want: ErrTaskNotFound},
		{name: "foreign claimant", mutate: func(f *runningAttemptFixture) {
			f.svc = deterministicRestartService(f.store)
		}, outcome: AttemptOutcome{Kind: AttemptOutcomeKindProgress}, want: ErrTaskChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRunningAttemptFixture(t)
			if tt.mutate != nil {
				tt.mutate(&fixture)
			}
			before := snapshotD2Rows(t, fixture.store, fixture.running)
			got, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, tt.outcome)
			if !errors.Is(err, tt.want) || got.ID != "" {
				t.Fatalf("FinishAttempt = (%+v, %v), want zero and %v", got, err, tt.want)
			}
			assertD2Rows(t, fixture.store, fixture.running, before)
		})
	}
}

func TestFinishAttemptThirdFailurePausesAndConsumesWakeAtomically(t *testing.T) {
	tests := []struct {
		name          string
		beforeNo      int
		beforeRecon   int
		needs         bool
		outcome       AttemptOutcome
		wantReason    string
		wantNo        int
		wantReconcile int
	}{
		{name: "no progress", beforeNo: 2, outcome: AttemptOutcome{Kind: AttemptOutcomeKindNoProgress}, wantReason: "no_progress", wantNo: 3},
		{name: "evidence unconfirmed", beforeNo: 2, beforeRecon: 2, needs: true, outcome: AttemptOutcome{Kind: AttemptOutcomeKindReconcileFailed}, wantReason: "evidence_unconfirmed", wantNo: 2, wantReconcile: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRunningAttemptFixture(t)
			fixture.running.NoProgressAttempts = tt.beforeNo
			fixture.running.ReconcileAttempts = tt.beforeRecon
			fixture.running.NeedsReconcile = tt.needs
			storeD2TaskForTest(t, fixture.store, fixture.running)
			got, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, tt.outcome)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != StatePaused || got.Revision != fixture.running.Revision+1 || got.PauseReason != tt.wantReason ||
				got.NeedsReconcile || got.NoProgressAttempts != tt.wantNo || got.ReconcileAttempts != tt.wantReconcile ||
				got.NextWakeAt != nil || got.Result != nil {
				t.Fatalf("paused Task = %+v", got)
			}
			wake, err := fixture.store.loadWake(context.Background(), got.Owner, fixture.wake.ID)
			if err != nil || wake.Status != wakeStatusConsumed || wake.ID != fixture.wake.ID ||
				wake.ClaimID != fixture.wake.ClaimID || wake.ClaimedBy != fixture.wake.ClaimedBy || wake.Attempt != fixture.wake.Attempt {
				t.Fatalf("consumed Wake = (%+v, %v)", wake, err)
			}
			beforeRetry := snapshotD2Rows(t, fixture.store, fixture.running)
			if _, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, tt.outcome); !errors.Is(err, ErrTaskChanged) {
				t.Fatalf("response-loss retry error = %v", err)
			}
			assertD2Rows(t, fixture.store, fixture.running, beforeRetry)
		})
	}
}

func TestFinishAttemptRollbackAndDetachment(t *testing.T) {
	t.Run("fault", func(t *testing.T) {
		fixture := newRunningAttemptFixture(t)
		before := snapshotD2Rows(t, fixture.store, fixture.running)
		fixture.store.testAfterWakeStage = func(context.Context, string) error { return errors.New("attempt fault") }
		_, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress})
		fixture.store.testAfterWakeStage = nil
		if err == nil {
			t.Fatal("fault returned nil")
		}
		assertD2Rows(t, fixture.store, fixture.running, before)
	})

	t.Run("capacity", func(t *testing.T) {
		fixture := newRunningAttemptFixture(t)
		before := snapshotD2Rows(t, fixture.store, fixture.running)
		fixture.store.options.MaxTaskBytes = 1
		if _, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress}); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("capacity error = %v", err)
		}
		assertD2Rows(t, fixture.store, fixture.running, before)
	})

	t.Run("cancelled context", func(t *testing.T) {
		fixture := newRunningAttemptFixture(t)
		before := snapshotD2Rows(t, fixture.store, fixture.running)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.svc.FinishAttempt(ctx, fixture.exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress}); err == nil {
			t.Fatal("cancelled FinishAttempt returned nil")
		}
		assertD2Rows(t, fixture.store, fixture.running, before)
	})

	t.Run("returned record is detached", func(t *testing.T) {
		fixture := newRunningAttemptFixture(t)
		fixture.running.Progress = jsonLiteralForAttempt(t, `{"step":"stable"}`)
		operation := registeredOperation(fixture.exec, "operation-attempt-detached")
		fixture.running.Operations = []Operation{operation}
		storeD2TaskForTest(t, fixture.store, fixture.running)
		got, err := fixture.svc.FinishAttempt(context.Background(), fixture.exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress})
		if err != nil {
			t.Fatal(err)
		}
		got.Progress[0] = '['
		got.Operations[0].ActionID = "mutated"
		stored, err := fixture.svc.Read(context.Background(), fixture.running.Owner, fixture.running.ID)
		if err != nil || string(stored.Progress) != `{"step":"stable"}` || stored.Operations[0].ActionID != operation.ActionID {
			t.Fatalf("caller mutation reached store: (%+v, %v)", stored, err)
		}
	})
}

type runningAttemptFixture struct {
	createFixture
	created CreateResult
	wake    Wake
	exec    ExecutionContext
	running Record
}

func newRunningAttemptFixture(t *testing.T) runningAttemptFixture {
	t.Helper()
	fixture, created, wake, exec, running, _ := runningWakeForRestart(t, 200)
	return runningAttemptFixture{createFixture: fixture, created: created, wake: wake, exec: exec, running: running}
}

func jsonLiteralForAttempt(t *testing.T, literal string) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(literal), &value); err != nil {
		t.Fatal(err)
	}
	return []byte(literal)
}
