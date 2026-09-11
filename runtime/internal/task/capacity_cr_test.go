package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCombinedTerminalCleanupCapacityRejectsBeforeMutation(t *testing.T) {
	t.Run("RecordCleanup large value", func(t *testing.T) {
		fixture, created, operations := newCleanupCapacityCRFixture(t)
		large := Cleanup{
			OperationID: operations[0].ID, Status: CleanupStatusUnconfirmed,
			Reason: string(bytes.Repeat([]byte("r"), intentTerminalStructuralReserve+1024)),
		}
		current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		updated := current
		updated.Cleanup = []Cleanup{large}
		projected := updated
		projected.Cleanup = append(projected.Cleanup, Cleanup{OperationID: operations[1].ID, Status: CleanupStatusReleased})
		limit := capacityBetweenOldAndCombinedFormula(t, fixture.store, current, updated, projected, false)
		path := fixture.store.path
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		store := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: limit})
		svc := NewService(store)
		beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, store, created.Task.Owner, created.Task.ID)
		if err := svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, large); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("RecordCleanup() error = %v, want ErrInvalidTaskSpec", err)
		}
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("rejected cleanup changed task/index/create/history/wake bytes")
		}
	})

	t.Run("nonterminal Reconcile with existing large cleanup", func(t *testing.T) {
		fixture, created, operations := newCleanupCapacityCRFixture(t)
		large := Cleanup{
			OperationID: operations[0].ID, Status: CleanupStatusUnconfirmed,
			Reason: string(bytes.Repeat([]byte("r"), intentTerminalStructuralReserve+1024)),
		}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, large); err != nil {
			t.Fatal(err)
		}
		evidence := operationEvidence(operations[0], created.Task.ID, "fact-capacity-cr-progress", EvidenceKindProgress)
		evidence.Details = json.RawMessage(`{"raw":1.2300}`)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		updated := reconciledCapacityRecord(current, evidence, StateRunning, "")
		projected := updated
		projected.Cleanup = append(projected.Cleanup, Cleanup{OperationID: operations[1].ID, Status: CleanupStatusReleased})
		limit := capacityBetweenOldAndCombinedFormula(t, fixture.store, current, updated, projected, false)
		path := fixture.store.path
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		store := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: limit})
		svc := NewService(store)
		svc.newID = func(prefix string) string { return prefix + "_capacity_cr" }
		beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, store, created.Task.Owner, created.Task.ID)
		if got, err := svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1)); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("Reconcile() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
		}
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("rejected nonterminal reconciliation changed task/index/create/history/wake bytes")
		}
	})

	t.Run("terminal Reconcile with existing large cleanup", func(t *testing.T) {
		fixture, created, operations := newCleanupCapacityCRFixture(t)
		large := Cleanup{
			OperationID: operations[0].ID, Status: CleanupStatusUnconfirmed,
			Reason: string(bytes.Repeat([]byte("r"), intentTerminalStructuralReserve+1024)),
		}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, large); err != nil {
			t.Fatal(err)
		}
		evidence := operationEvidence(operations[0], created.Task.ID, "fact-capacity-cr-terminal", EvidenceKindSatisfied)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		updated := reconciledCapacityRecord(current, evidence, StateSucceeded, "result_capacity_cr")
		projected := updated
		projected.Cleanup = append(projected.Cleanup, Cleanup{OperationID: operations[1].ID, Status: CleanupStatusReleased})
		limit := capacityBetweenOldAndCombinedFormula(t, fixture.store, current, updated, projected, true)
		path := fixture.store.path
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		store := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: limit})
		svc := NewService(store)
		svc.newID = func(prefix string) string { return prefix + "_capacity_cr" }
		beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, store, created.Task.Owner, created.Task.ID)
		if got, err := svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1)); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("Reconcile() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
		}
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("rejected terminal reconciliation changed task/index/create/history/wake bytes")
		}
	})
}

func newCleanupCapacityCRFixture(t *testing.T) (createFixture, CreateResult, []Operation) {
	t.Helper()
	fixture, created, _ := newIntentFixture(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	operations := make([]Operation, 2)
	for index, suffix := range []string{"one", "two"} {
		exec := operationExecution(fixture, created.Task, "capacity-cr-"+suffix)
		operations[index] = registeredOperation(exec, "operation-capacity-cr-"+suffix)
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operations[index]); err != nil {
			t.Fatal(err)
		}
	}
	return fixture, created, operations
}

func reconciledCapacityRecord(current Record, selected Evidence, state State, resultID string) Record {
	updated := current
	updated.Revision++
	updated.State = state
	updated.NextWakeAt = nil
	updated.NeedsReconcile = false
	updated.PauseReason = ""
	updated.NoProgressAttempts = 0
	updated.ReconcileAttempts = 0
	updated.Evidence = cloneEvidenceSlice(current.Evidence)
	for index := range updated.Evidence {
		updated.Evidence[index].Applied = true
	}
	if state == StateRunning {
		updated.Progress = append(json.RawMessage(nil), selected.Details...)
	} else {
		updated.Result = &Result{
			ID: resultID, TaskID: updated.ID, Revision: updated.Revision, State: state,
			Reason: selected.Kind, OccurredAt: selected.OccurredAt,
			EvidenceRefs: []string{selected.FactID}, Source: selected.Source,
		}
	}
	return updated
}

func capacityBetweenOldAndCombinedFormula(t *testing.T, store *SQLiteStore, current, updated, projected Record, terminal bool) int {
	t.Helper()
	fullJSON := mustJSONBytes(t, updated)
	projectedJSON := mustJSONBytes(t, projected)
	base := updated
	base.Cleanup = []Cleanup{}
	baseJSON := mustJSONBytes(t, base)
	createJSON, historyJSON := taskIdempotencyMetadata(t, store, current.Owner, current.ID)
	cleanupFootprint := len(projectedJSON) - len(baseJSON)
	oldReserve := intentTerminalStructuralReserve
	if cleanupFootprint > oldReserve {
		oldReserve = cleanupFootprint
	}
	old := len(baseJSON) + len(createJSON) + len(historyJSON) + len(projectedJSON)
	correct := len(fullJSON) + len(createJSON) + len(historyJSON) + len(projectedJSON)
	if !terminal {
		old = len(baseJSON) + len(createJSON) + len(historyJSON) + len(baseJSON) + oldReserve
		correct += intentTerminalStructuralReserve
	}
	if old >= correct-1 {
		t.Fatalf("capacity formulas do not expose a boundary: old=%d correct=%d", old, correct)
	}
	return old + (correct-old)/2
}
