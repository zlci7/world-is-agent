package task

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRecordCleanupIdempotencyAndTransitions(t *testing.T) {
	t.Run("new exact reopen and immutable final", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tasks.sqlite")
		fixture, created, _ := newIntentFixture(t, StoreOptions{Path: path})
		exec := operationExecution(fixture, created.Task, "cleanup-final")
		operation := registeredOperation(exec, "operation-cleanup-final")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		before, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		beforeCreate, beforeHistory := taskIdempotencyMetadata(t, fixture.store, created.Task.Owner, created.Task.ID)
		beforeWakes := mustJSONBytes(t, loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID))
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup); err != nil {
			t.Fatalf("RecordCleanup() error = %v", err)
		}
		after, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != before.Revision || after.State != before.State || !reflect.DeepEqual(after.Result, before.Result) ||
			!bytes.Equal(after.Progress, before.Progress) || !reflect.DeepEqual(after.Evidence, before.Evidence) ||
			!reflect.DeepEqual(after.NextWakeAt, before.NextWakeAt) || len(after.Cleanup) != 1 || !reflect.DeepEqual(after.Cleanup[0], cleanup) {
			t.Fatalf("cleanup changed business state: before=%+v after=%+v", before, after)
		}
		afterCreate, afterHistory := taskIdempotencyMetadata(t, fixture.store, created.Task.Owner, created.Task.ID)
		afterWakes := mustJSONBytes(t, loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID))
		if !bytes.Equal(beforeCreate, afterCreate) || !bytes.Equal(beforeHistory, afterHistory) || !bytes.Equal(beforeWakes, afterWakes) {
			t.Fatal("cleanup changed create/intent/wake metadata")
		}
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: 1})
		reopenedService := NewService(reopened)
		if err := reopenedService.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup); err != nil {
			t.Fatalf("exact retry after reopen error = %v", err)
		}
		conflict := cleanup
		conflict.Status = CleanupStatusHandedOff
		if err := reopenedService.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, conflict); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("final cleanup mutation error = %v, want ErrIdempotencyConflict", err)
		}
	})

	for _, finalStatus := range []string{CleanupStatusReleased, CleanupStatusHandedOff} {
		t.Run("unconfirmed upgrades to "+finalStatus, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			exec := operationExecution(fixture, created.Task, "cleanup-upgrade-"+finalStatus)
			operation := registeredOperation(exec, "operation-cleanup-upgrade-"+finalStatus)
			if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
				t.Fatal(err)
			}
			initial := Cleanup{OperationID: operation.ID, Status: CleanupStatusUnconfirmed, Reason: "connection closed"}
			if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, initial); err != nil {
				t.Fatal(err)
			}
			final := Cleanup{OperationID: operation.ID, Status: finalStatus, Reason: "trusted outcome"}
			if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, final); err != nil {
				t.Fatalf("upgrade error = %v", err)
			}
			stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil || len(stored.Cleanup) != 1 || !reflect.DeepEqual(stored.Cleanup[0], final) || stored.Revision != 1 {
				t.Fatalf("upgraded cleanup = (%+v, %v)", stored, err)
			}
		})
	}
}

func TestRecordCleanupAuthorityIdentityAndCapacity(t *testing.T) {
	t.Run("missing task and operation", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		cleanup := Cleanup{OperationID: "operation-missing", Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, "task-missing", cleanup); !errors.Is(err, ErrTaskNotFound) {
			t.Fatalf("missing task error = %v", err)
		}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("missing operation error = %v", err)
		}
	})

	t.Run("same run older generation succeeds", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-old-generation")
		operation := registeredOperation(exec, "operation-cleanup-old-generation")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		current := fixture.head.Binding
		current.Generation++
		setWorldBindingForEvidenceTest(t, fixture.store, fixture.head, current)
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), current, created.Task.ID, cleanup); err != nil {
			t.Fatalf("old-generation cleanup error = %v", err)
		}
	})

	t.Run("prior run operation is stale", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-old-run")
		operation := registeredOperation(exec, "operation-cleanup-old-run")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		current := fixture.head.Binding
		current.RunID = "run-new"
		current.Generation++
		setWorldBindingForEvidenceTest(t, fixture.store, fixture.head, current)
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), current, created.Task.ID, cleanup); !errors.Is(err, ErrGenerationStale) {
			t.Fatalf("prior-run cleanup error = %v, want ErrGenerationStale", err)
		}
	})

	t.Run("identity graph ambiguity fails closed", func(t *testing.T) {
		fixture := newWorldIdentityWriteFixture(t)
		fixture.injectDuplicateIdentity(t, "operation")
		cleanup := Cleanup{OperationID: fixture.targetOperation.ID, Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, fixture.target.Task.ID, cleanup); !errors.Is(err, ErrTaskConflict) {
			t.Fatalf("ambiguous identity cleanup error = %v, want ErrTaskConflict", err)
		}
	})

	t.Run("ambiguous task id across owners fails closed", func(t *testing.T) {
		fixture, target, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, target.Task, "cleanup-ambiguous-task")
		operation := registeredOperation(exec, "operation-cleanup-ambiguous-task")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		otherOwner := target.Task.Owner
		otherOwner.EntityID = "actor-ambiguous-cleanup"
		otherExec, otherSpec := createInputs(fixture.head, fixture.clock, otherOwner,
			"ambiguous-e", "ambiguous-t", "ambiguous-c", "ambiguous-cleanup")
		other, err := fixture.svc.Create(context.Background(), otherExec, otherSpec, Admission{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.db.Exec(`DELETE FROM task_wakeups WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
			otherOwner.GameID, otherOwner.WorldID, otherOwner.EntityID, other.Task.ID); err != nil {
			t.Fatal(err)
		}
		corrupt := other.Task
		corrupt.ID = target.Task.ID
		recordJSON := mustJSONBytes(t, corrupt)
		createJSON := mustJSONBytes(t, initialCreateResult(corrupt))
		if _, err := fixture.store.db.Exec(`UPDATE tasks SET task_id = ?, record_json = ?, create_response_json = ?, create_response_hash = ?
			WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
			corrupt.ID, recordJSON, createJSON, independentSHA256Hex(createJSON),
			otherOwner.GameID, otherOwner.WorldID, otherOwner.EntityID, other.Task.ID); err != nil {
			t.Fatal(err)
		}
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, target.Task.ID, cleanup); !errors.Is(err, ErrTaskConflict) {
			t.Fatalf("ambiguous task cleanup error = %v, want ErrTaskConflict", err)
		}
	})

	t.Run("oversized reason and fault roll back", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{MaxTaskBytes: 20 << 10})
		exec := operationExecution(fixture, created.Task, "cleanup-capacity")
		operation := registeredOperation(exec, "operation-cleanup-capacity")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		large := Cleanup{OperationID: operation.ID, Status: CleanupStatusUnconfirmed, Reason: string(bytes.Repeat([]byte("x"), 30<<10))}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, large); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("oversized cleanup error = %v, want ErrInvalidTaskSpec", err)
		}
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("oversized cleanup changed durable bytes")
		}

		fixture.store.testAfterNoRevisionStage = func(_ context.Context, stage string) error {
			if stage == noRevisionStageCleanupMerged {
				return errors.New("private cleanup fault")
			}
			return nil
		}
		short := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, short); err == nil {
			t.Fatal("faulted RecordCleanup() returned nil")
		}
		fixture.store.testAfterNoRevisionStage = nil
		faultTask, faultWakes, faultHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, faultTask) || !bytes.Equal(beforeWakes, faultWakes) || !bytes.Equal(beforeHistory, faultHistory) {
			t.Fatal("faulted cleanup changed durable bytes")
		}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, short); err != nil {
			t.Fatalf("minimal cleanup after rejection error = %v", err)
		}
	})

	t.Run("C1 exact nonterminal reserve supports every minimal cleanup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tasks.sqlite")
		fixture, created, _ := newIntentFixture(t, StoreOptions{Path: path})
		execOne := operationExecution(fixture, created.Task, "cleanup-nonterminal-one")
		execTwo := operationExecution(fixture, created.Task, "cleanup-nonterminal-two")
		operationOne := registeredOperation(execOne, "operation-cleanup-nonterminal-one")
		operationTwo := registeredOperation(execTwo, "operation-cleanup-nonterminal-two")
		withOperations := created.Task
		withOperations.Operations = []Operation{operationOne, operationTwo}
		recordJSON := mustJSONBytes(t, withOperations)
		createJSON := mustJSONBytes(t, initialCreateResult(created.Task))
		exactC1Limit := len(recordJSON) + len(createJSON) + len([]byte("[]")) + len(recordJSON) + intentTerminalStructuralReserve
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: exactC1Limit})
		svc := NewService(reopened)
		if _, err := svc.RegisterOperation(context.Background(), execOne, operationOne); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RegisterOperation(context.Background(), execTwo, operationTwo); err != nil {
			t.Fatal(err)
		}
		for _, operation := range []Operation{operationOne, operationTwo} {
			if err := svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID,
				Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}); err != nil {
				t.Fatalf("minimal nonterminal cleanup %s error = %v", operation.ID, err)
			}
		}
		stored, err := svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || stored.Revision != 1 || stored.State != StateWaiting || len(stored.Cleanup) != 2 {
			t.Fatalf("nonterminal cleanup record = (%+v, %v)", stored, err)
		}
	})

	t.Run("larger reason uses available capacity and keeps terminal path", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-large-available")
		operation := registeredOperation(exec, "operation-cleanup-large-available")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		cleanup := Cleanup{
			OperationID: operation.ID, Status: CleanupStatusUnconfirmed,
			Reason: string(bytes.Repeat([]byte("r"), intentTerminalStructuralReserve+1024)),
		}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup); err != nil {
			t.Fatalf("large in-budget cleanup error = %v", err)
		}
		evidence := operationEvidence(operation, created.Task.ID, "fact-cleanup-large-terminal", EvidenceKindSatisfied)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		settled, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		if err != nil || settled.Task.State != StateSucceeded || len(settled.Task.Cleanup) != 1 {
			t.Fatalf("terminal after large cleanup = (%+v, %v)", settled, err)
		}
	})
}

func TestRecordCleanupPreservesTerminalResultAndRacesWithWriters(t *testing.T) {
	t.Run("terminal result fingerprint", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-result")
		operation := registeredOperation(exec, "operation-cleanup-result")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		evidence := operationEvidence(operation, created.Task.ID, "fact-cleanup-result", EvidenceKindSatisfied)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		settled, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		if err != nil {
			t.Fatal(err)
		}
		fingerprint := mustJSONBytes(t, settled.Task.Result)
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusHandedOff}
		if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup); err != nil {
			t.Fatal(err)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || stored.Revision != settled.Task.Revision || !bytes.Equal(mustJSONBytes(t, stored.Result), fingerprint) {
			t.Fatalf("result changed after cleanup: (%+v, %v)", stored.Result, err)
		}
	})

	t.Run("race with evidence merge", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-race")
		operation := registeredOperation(exec, "operation-cleanup-race")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		evidence := operationEvidence(operation, created.Task.ID, "fact-cleanup-race", EvidenceKindProgress)
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		var cleanupErr, evidenceErr error
		var added bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cleanupErr = fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup)
		}()
		go func() {
			defer wg.Done()
			added, evidenceErr = fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
		}()
		wg.Wait()
		if cleanupErr != nil || evidenceErr != nil || !added {
			t.Fatalf("race cleanup=%v evidence=(%v,%v)", cleanupErr, added, evidenceErr)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || len(stored.Cleanup) != 1 || len(stored.Evidence) != 1 || !stored.NeedsReconcile {
			t.Fatalf("race lost merge: (%+v, %v)", stored, err)
		}
	})

	t.Run("race with intent", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-intent-race")
		operation := registeredOperation(exec, "operation-cleanup-intent-race")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		nextWakeAt := int64(250)
		intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "cleanup-intent-race")
		var cleanupErr, intentErr error
		var waited Record
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cleanupErr = fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup)
		}()
		go func() {
			defer wg.Done()
			waited, intentErr = fixture.svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: &nextWakeAt})
		}()
		wg.Wait()
		if cleanupErr != nil || intentErr != nil || waited.Revision != 2 {
			t.Fatalf("race cleanup=%v intent=(%+v,%v)", cleanupErr, waited, intentErr)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || stored.Revision != 2 || len(stored.Cleanup) != 1 || stored.Cleanup[0] != cleanup {
			t.Fatalf("intent race lost cleanup: (%+v, %v)", stored, err)
		}
	})

	t.Run("race with reconciliation", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "cleanup-reconcile-race")
		operation := registeredOperation(exec, "operation-cleanup-reconcile-race")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		evidence := operationEvidence(operation, created.Task.ID, "fact-cleanup-reconcile-race", EvidenceKindProgress)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
		var cleanupErr, reconcileErr error
		var reconciled ReconcileResult
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cleanupErr = fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup)
		}()
		go func() {
			defer wg.Done()
			reconciled, reconcileErr = fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		}()
		wg.Wait()
		if cleanupErr != nil || reconcileErr != nil || reconciled.Task.Revision != 2 {
			t.Fatalf("race cleanup=%v reconcile=(%+v,%v)", cleanupErr, reconciled, reconcileErr)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || stored.Revision != 2 || !stored.Evidence[0].Applied || len(stored.Cleanup) != 1 {
			t.Fatalf("reconcile race lost merge: (%+v, %v)", stored, err)
		}
	})
}
