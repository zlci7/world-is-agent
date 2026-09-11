package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReconcilePreservesCreateIntentMetadataAndCapacity(t *testing.T) {
	t.Run("create intent responses and indexes", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		nextWakeAt := int64(250)
		intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "metadata")
		intent := Intent{Kind: "wait", NextWakeAt: &nextWakeAt, ProgressNote: "opaque progress"}
		waited, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
		if err != nil {
			t.Fatal(err)
		}
		beforeCreate, beforeHistory := taskIdempotencyMetadata(t, fixture.store, created.Task.Owner, created.Task.ID)
		evidence := taskEvidence(fixture.head.Binding, waited, "fact-metadata", EvidenceKindProgress)
		evidence.StartRevision = waited.Revision
		evidence.Details = json.RawMessage(`{"integer":9007199254740993,"decimal":1.2300}`)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		reconciled, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, waited, waited.Revision))
		if err != nil {
			t.Fatal(err)
		}
		afterCreate, afterHistory := taskIdempotencyMetadata(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeCreate, afterCreate) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("reconciliation changed create response or intent history bytes")
		}
		createRetry, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
		if err != nil || !reflect.DeepEqual(createRetry, created) {
			t.Fatalf("Create exact retry = (%+v, %v), want %+v", createRetry, err, created)
		}
		intentRetry, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
		if err != nil || !reflect.DeepEqual(intentRetry, waited) {
			t.Fatalf("ApplyIntent exact retry = (%+v, %v), want %+v", intentRetry, err, waited)
		}
		var state, clockID string
		var revision int64
		var next sql.NullInt64
		if err := fixture.store.db.QueryRow(`SELECT state, revision, clock_id, next_wake_at FROM tasks
			WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
			created.Task.Owner.GameID, created.Task.Owner.WorldID, created.Task.Owner.EntityID, created.Task.ID,
		).Scan(&state, &revision, &clockID, &next); err != nil {
			t.Fatal(err)
		}
		if state != string(reconciled.Task.State) || revision != int64(reconciled.Task.Revision) ||
			clockID != reconciled.Task.Spec.ClockID || next.Valid != (reconciled.Task.NextWakeAt != nil) {
			t.Fatalf("indexed state = (%s,%d,%s,%+v), record = %+v", state, revision, clockID, next, reconciled.Task)
		}
	})

	t.Run("combined reserve reaches terminal and all minimal cleanups", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tasks.sqlite")
		fixture, created, _ := newIntentFixture(t, StoreOptions{Path: path})
		execOne := operationExecution(fixture, created.Task, "capacity-one")
		execTwo := operationExecution(fixture, created.Task, "capacity-two")
		operationOne := registeredOperation(execOne, "operation-capacity-one")
		operationTwo := registeredOperation(execTwo, "operation-capacity-two")
		evidence := operationEvidence(operationOne, created.Task.ID, "fact-capacity-terminal", EvidenceKindSatisfied)
		evidence.Details = json.RawMessage(`{"opaque":"satisfied"}`)

		pending := created.Task
		pending.Operations = []Operation{operationOne, operationTwo}
		pending.Evidence = []Evidence{evidence}
		pending.NeedsReconcile = true
		pendingProjected := pending
		pendingProjected.Cleanup = []Cleanup{
			{OperationID: operationOne.ID, Status: CleanupStatusReleased},
			{OperationID: operationTwo.ID, Status: CleanupStatusReleased},
		}
		pendingProjectedJSON := mustJSONBytes(t, pendingProjected)
		terminal := pendingProjected
		terminal.State = StateSucceeded
		terminal.Revision = 2
		terminal.NextWakeAt = nil
		terminal.NeedsReconcile = false
		terminal.Evidence[0].Applied = true
		terminal.Result = &Result{
			ID: "result_capacity", TaskID: terminal.ID, Revision: terminal.Revision,
			State: terminal.State, Reason: evidence.Kind, OccurredAt: evidence.OccurredAt,
			EvidenceRefs: []string{evidence.FactID}, Source: evidence.Source,
		}
		terminalJSON := mustJSONBytes(t, terminal)
		createJSON := mustJSONBytes(t, initialCreateResult(created.Task))
		nonterminalCombinedLimit := len(pendingProjectedJSON) + len(createJSON) + len([]byte("[]")) + len(pendingProjectedJSON) + intentTerminalStructuralReserve
		terminalCombinedLimit := len(terminalJSON) + len(createJSON) + len([]byte("[]")) + len(terminalJSON)
		exactCombinedLimit := nonterminalCombinedLimit
		if terminalCombinedLimit > exactCombinedLimit {
			exactCombinedLimit = terminalCombinedLimit
		}
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}

		reopened := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: exactCombinedLimit})
		svc := NewService(reopened)
		svc.newID = func(prefix string) string { return prefix + "_capacity" }
		svc.nowUnixMS = func() int64 { return 1_700_000_000_123 }
		if _, err := svc.RegisterOperation(context.Background(), execOne, operationOne); err != nil {
			t.Fatalf("RegisterOperation(one) error = %v", err)
		}
		if _, err := svc.RegisterOperation(context.Background(), execTwo, operationTwo); err != nil {
			t.Fatalf("RegisterOperation(two) error = %v", err)
		}
		if added, err := svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("near-limit AdmitEvidence() = (%v, %v)", added, err)
		}
		settled, err := svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		if err != nil || settled.Task.State != StateSucceeded {
			t.Fatalf("near-limit Reconcile() = (%+v, %v)", settled, err)
		}
		for _, operation := range []Operation{operationOne, operationTwo} {
			cleanup := Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}
			if err := svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, cleanup); err != nil {
				t.Fatalf("minimal cleanup %s error = %v", operation.ID, err)
			}
		}
		stored, err := svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || len(stored.Cleanup) != 2 || stored.Revision != settled.Task.Revision ||
			!bytes.Equal(mustJSONBytes(t, stored.Result), mustJSONBytes(t, settled.Task.Result)) {
			t.Fatalf("near-limit final Record = (%+v, %v)", stored, err)
		}
	})
}
