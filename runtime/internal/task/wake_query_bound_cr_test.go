package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestWakeClaimQueryIsBoundedAndDoesNotDecodeHistory(t *testing.T) {
	t.Run("zero limit validates authority without wake history", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		insertPoisonedConsumedWakeHistory(t, fixture.store, created.Task, 128)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 0)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("zero-limit ClaimDue = (%#v, %v)", got, err)
		}
	})

	t.Run("consumed history does not affect one selected wake", func(t *testing.T) {
		fixture, created, pending := newIntentFixture(t, StoreOptions{})
		insertPoisonedConsumedWakeHistory(t, fixture.store, created.Task, 256)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
		if err != nil || len(got) != 1 || got[0].ID != pending.ID {
			t.Fatalf("bounded ClaimDue = (%+v, %v)", got, err)
		}
	})

	t.Run("limit excludes later poisoned candidate until selected", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		first := createWakeTask(t, fixture, "bounded-first", "actor-a", 190, 300)
		second := createWakeTask(t, fixture, "bounded-second", "actor-b", 200, 300)
		secondWake := loadTaskWakes(t, fixture.store, second.Task.Owner, second.Task.ID)[0]
		poisonWakeJSON(t, fixture.store, secondWake.ID)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
		if err != nil || len(got) != 1 || got[0].TaskID != first.Task.ID {
			t.Fatalf("first bounded ClaimDue = (%+v, %v)", got, err)
		}
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1); !errors.Is(err, ErrInvalidTaskSpec) || got != nil {
			t.Fatalf("selected poison ClaimDue = (%+v, %v), want invalid_task_spec", got, err)
		}
	})
}

func TestWakeTargetLookupIgnoresHistoryAndValidatesTarget(t *testing.T) {
	t.Run("MarkEnqueued", func(t *testing.T) {
		fixture, created, wake, _ := claimedWakeForQueryTest(t)
		insertPoisonedConsumedWakeHistory(t, fixture.store, created.Task, 128)
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
			t.Fatalf("MarkEnqueued with history error = %v", err)
		}
	})

	t.Run("ReleaseClaim", func(t *testing.T) {
		fixture, created, wake, _ := claimedWakeForQueryTest(t)
		insertPoisonedConsumedWakeHistory(t, fixture.store, created.Task, 128)
		if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(1000)); err != nil {
			t.Fatalf("ReleaseClaim with history error = %v", err)
		}
	})

	t.Run("BeginWake", func(t *testing.T) {
		fixture, created, wake, _ := claimedWakeForQueryTest(t)
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
			t.Fatal(err)
		}
		insertPoisonedConsumedWakeHistory(t, fixture.store, created.Task, 128)
		if _, record, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil || record.State != StateRunning {
			t.Fatalf("BeginWake with history = (%+v, %v)", record, err)
		}
	})

	t.Run("target remains strict", func(t *testing.T) {
		fixture, _, wake, _ := claimedWakeForQueryTest(t)
		poisonWakeJSON(t, fixture.store, wake.ID)
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("MarkEnqueued poisoned target error = %v, want invalid_task_spec", err)
		}
	})
}

func TestIntentConsumptionIgnoresConsumedWakeHistory(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	insertPoisonedConsumedWakeHistory(t, fixture.store, created.Task, 256)
	nextWake := int64(250)
	exec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "bounded-history")
	got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "wait", NextWakeAt: &nextWake})
	if err != nil {
		t.Fatalf("ApplyIntent with consumed history error = %v", err)
	}
	if got.State != StateWaiting || got.Revision != created.Task.Revision+1 || got.NextWakeAt == nil || *got.NextWakeAt != nextWake {
		t.Fatalf("ApplyIntent with consumed history = %+v", got)
	}
}

func TestIntentConsumptionRejectsWakeTaskRelationCorruption(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	corrupt := wake
	corrupt.ExpectedRevision++
	setWakeForTest(t, fixture.store, corrupt)
	beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	exec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "corrupt-wake-relation")
	if got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "stop"}); !errors.Is(err, ErrInvalidTaskSpec) || got.ID != "" {
		t.Fatalf("ApplyIntent with corrupt wake relation = (%+v, %v), want invalid_task_spec", got, err)
	}
	assertTaskSnapshotEqual(t, fixture.store, created.Task, beforeTask, beforeWakes, beforeHistory, "corrupt wake relation")
}

func TestReconcileConsumptionRejectsWakeTaskRelationCorruption(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-corrupt-wake-relation", EvidenceKindSatisfied)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("AdmitEvidence = (%v, %v)", added, err)
	}
	current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := wake
	corrupt.ExpectedRevision++
	setWakeForTest(t, fixture.store, corrupt)
	beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	if got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, current, current.Revision)); !errors.Is(err, ErrInvalidTaskSpec) || got.Task.ID != "" {
		t.Fatalf("Reconcile with corrupt wake relation = (%+v, %v), want invalid_task_spec", got, err)
	}
	assertTaskSnapshotEqual(t, fixture.store, created.Task, beforeTask, beforeWakes, beforeHistory, "reconcile corrupt wake relation")
}

func claimedWakeForQueryTest(t *testing.T) (createFixture, CreateResult, Wake, Clock) {
	t.Helper()
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "query-target", "actor", 200, 300)
	clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
	claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
	}
	return fixture, created, claimed[0], clock
}

func insertPoisonedConsumedWakeHistory(t *testing.T, store *SQLiteStore, record Record, count int) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for index := 0; index < count; index++ {
		wake := Wake{
			ID: fmt.Sprintf("consumed-query-history-%04d", index), TaskID: record.ID, Owner: record.Owner,
			ExpectedRevision: record.Revision, DueTick: record.Spec.WakeAt,
			Reason: fmt.Sprintf("consumed-query-history-%04d", index), Status: wakeStatusConsumed,
			Generation: 1,
		}
		if err := wake.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO task_wakeups (
			wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
			due_tick, reason, status, claim_id, claimed_by, generation, attempt,
			retry_after_unix_ms, wake_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
			record.Spec.ClockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
			wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS,
			[]byte(`{"poisoned_consumed_history":`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func poisonWakeJSON(t *testing.T, store *SQLiteStore, wakeID string) {
	t.Helper()
	result, err := store.db.Exec(`UPDATE task_wakeups SET wake_json = ? WHERE wake_id = ?`, json.RawMessage(`{"poisoned":`), wakeID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("poison wake rows = %d, %v", affected, err)
	}
}
