package task

import (
	"context"
	"errors"
	"testing"
)

func TestTaskObservationFailureUsesOnlyTechnicalCounter(t *testing.T) {
	f := newRunningAttemptFixture(t)
	if _, err := f.svc.FinishAttempt(context.Background(), f.exec, AttemptOutcome{Kind: AttemptOutcomeKindReconcileFailed}); !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("reconciliation accepted without marker: %v", err)
	}
	exec := f.exec
	for i := 1; i <= 3; i++ {
		kind := AttemptOutcomeKindReconcileFailed
		if i == 1 {
			kind = "observation_failed"
		}
		r, err := f.svc.FinishAttempt(context.Background(), exec, AttemptOutcome{Kind: kind})
		if err != nil {
			t.Fatalf("observation failure %d: %v", i, err)
		}
		if r.ReconcileAttempts != i || r.NoProgressAttempts != 0 || r.Result != nil {
			t.Fatalf("mixed attempt counters: %+v", r)
		}
		if i < 3 && (!r.NeedsReconcile || r.State != StateRunning) {
			t.Fatalf("missing technical reconciliation: %+v", r)
		}
		if i == 3 && (r.State != StatePaused || r.PauseReason != "evidence_unconfirmed" || r.NeedsReconcile) {
			t.Fatalf("technical failure unbounded: %+v", r)
		}
		exec.ExpectedRevision = r.Revision
	}
}
