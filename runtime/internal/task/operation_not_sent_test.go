package task

import (
	"context"
	"errors"
	"testing"
)

func TestOperationNotSentIsDurableAndDoesNotChangeRevision(t *testing.T) {
	f, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(f, created.Task, "not-sent")
	op := registeredOperation(exec, "not-sent")
	if _, err := f.svc.RegisterOperation(context.Background(), exec, op); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.svc.MarkOperationNotSent(context.Background(), exec.Binding, exec.Owner, exec.TaskID, op.ID, op.ActionID); err != nil {
			t.Fatal(err)
		}
	}
	r, err := f.svc.Read(context.Background(), exec.Owner, exec.TaskID)
	if err != nil || r.Revision != created.Task.Revision || r.Operations[0].Status != "not_sent" || r.NeedsReconcile {
		t.Fatalf("not-sent record: %+v %v", r, err)
	}
	if _, err := f.svc.RegisterOperation(context.Background(), exec, op); err == nil {
		t.Fatal("not-sent operation was re-registered")
	}
	evidence := taskEvidence(exec.Binding, r, "impossible", EvidenceKindProgress)
	evidence.OperationID, evidence.StartRevision = op.ID, op.StartRevision
	evidence.OccurredAt = f.clock.Tick
	if _, err := f.svc.AdmitEvidence(context.Background(), exec.Binding, evidence); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf("impossible evidence: %v", err)
	}
	evidence.Binding.Generation++
	evidence.RevalidatedIn = &evidence.Binding
	if _, err := f.svc.AdmitEvidence(context.Background(), exec.Binding, evidence); err == nil {
		t.Fatal("revalidated evidence accepted for not-sent operation")
	}
	r.State, r.NextWakeAt = StateRunning, nil
	recovered, err := recoveredRunningRecord(r, f.clock.Tick)
	if err != nil || recovered.Operations[0].Status != OperationStatusNotSent {
		t.Fatalf("recovery changed not-sent operation: %+v %v", recovered, err)
	}
	path := f.store.path
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	store := openTaskTestStore(t, StoreOptions{Path: path})
	r, err = NewService(store).Read(context.Background(), exec.Owner, exec.TaskID)
	if err != nil || r.Operations[0].Status != "not_sent" {
		t.Fatalf("reopened: %+v %v", r, err)
	}
}

func TestOperationNotSentRejectsMismatchEvidenceAndUncertain(t *testing.T) {
	for _, change := range []string{"action", "operation", "owner", "binding", "evidence", "uncertain"} {
		t.Run(change, func(t *testing.T) {
			f, created, _ := newIntentFixture(t, StoreOptions{})
			exec := operationExecution(f, created.Task, "not-sent")
			op := registeredOperation(exec, "not-sent")
			if _, err := f.svc.RegisterOperation(context.Background(), exec, op); err != nil {
				t.Fatal(err)
			}
			binding, owner, operationID, actionID := exec.Binding, exec.Owner, op.ID, op.ActionID
			switch change {
			case "action":
				actionID = "other"
			case "operation":
				operationID = "other"
			case "owner":
				owner.EntityID = "other"
			case "binding":
				binding.Generation++
			case "evidence":
				e := taskEvidence(exec.Binding, created.Task, "received", EvidenceKindProgress)
				e.OperationID, e.StartRevision, e.OccurredAt = op.ID, op.StartRevision, f.clock.Tick
				if _, err := f.svc.AdmitEvidence(context.Background(), exec.Binding, e); err != nil {
					t.Fatal(err)
				}
			case "uncertain":
				r, _ := f.svc.Read(context.Background(), exec.Owner, exec.TaskID)
				r.Operations[0].Status = OperationStatusUncertain
				setRecordForIntentTest(t, f.store, r)
			}
			if err := f.svc.MarkOperationNotSent(context.Background(), binding, owner, exec.TaskID, operationID, actionID); err == nil {
				t.Fatal("invalid transition accepted")
			}
			r, err := f.svc.Read(context.Background(), exec.Owner, exec.TaskID)
			if err != nil || r.Operations[0].Status == "not_sent" {
				t.Fatalf("invalid transition mutated record: %+v %v", r, err)
			}
		})
	}
}

func TestOperationNotSentStorageFailureRollsBack(t *testing.T) {
	f, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(f, created.Task, "not-sent")
	op := registeredOperation(exec, "not-sent")
	if _, err := f.svc.RegisterOperation(context.Background(), exec, op); err != nil {
		t.Fatal(err)
	}
	f.store.testAfterNoRevisionStage = func(context.Context, string) error { return errors.New("injected storage failure") }
	if err := f.svc.MarkOperationNotSent(context.Background(), exec.Binding, exec.Owner, exec.TaskID, op.ID, op.ActionID); err == nil {
		t.Fatal("storage failure ignored")
	}
	f.store.testAfterNoRevisionStage = nil
	r, err := f.svc.Read(context.Background(), exec.Owner, exec.TaskID)
	if err != nil || r.Operations[0].Status != OperationStatusRegistered {
		t.Fatalf("rollback failed: %+v %v", r, err)
	}
}
