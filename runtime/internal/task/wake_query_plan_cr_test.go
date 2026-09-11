package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWakeClaimPlanUsesPendingGenerationIndexWithoutTempSort(t *testing.T) {
	fixture, _, pending := newIntentFixture(t, StoreOptions{})
	insertPoisonedConsumedWakeHistory(t, fixture.store, pendingRecordForWakePlan(t, fixture.store, pending), 10_000)

	rows, err := fixture.store.db.Query(`EXPLAIN QUERY PLAN `+claimDueWakeSQL,
		fixture.world.GameID, fixture.world.WorldID, fixture.clock.ID,
		int64(fixture.head.Binding.Generation), int64(200), int64(1_700_000_000_123), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, " | ")
	if !strings.Contains(plan, "USING INDEX idx_task_wakeups_claim_due") ||
		strings.Contains(plan, "SCAN ") || strings.Contains(plan, "USE TEMP B-TREE") {
		t.Fatalf("claim query plan = %q, want bounded pending-generation index without scan/temp sort", plan)
	}

	clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
	claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != pending.ID {
		t.Fatalf("ClaimDue with 10k consumed history = (%+v, %v)", claimed, err)
	}
}

func TestWakeAdmissionExposesOrphanCorruption(t *testing.T) {
	t.Run("ClaimDue fails closed before later candidate", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		created := createWakeTask(t, fixture, "after-orphan", "actor-z", 200, 300)
		orphan := insertOrphanWakeForAdmissionTest(t, fixture, wakeStatusPending)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)

		claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
		if !errors.Is(err, ErrInvalidTaskSpec) || claimed != nil {
			t.Fatalf("ClaimDue with first orphan %q = (%+v, %v), want invalid_task_spec", orphan.ID, claimed, err)
		}
		var status string
		if err := fixture.store.db.QueryRow(`SELECT status FROM task_wakeups WHERE task_id = ?`, created.Task.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != wakeStatusPending {
			t.Fatalf("later candidate status = %q, want pending", status)
		}
	})

	for _, test := range []struct {
		name   string
		status string
		call   func(createFixture, Wake) error
	}{
		{name: "MarkEnqueued", status: wakeStatusClaimed, call: func(f createFixture, wake Wake) error {
			return f.svc.MarkEnqueued(context.Background(), f.head.Binding, wake.ID, wake.ClaimID)
		}},
		{name: "ReleaseClaim", status: wakeStatusClaimed, call: func(f createFixture, wake Wake) error {
			return f.svc.ReleaseClaim(context.Background(), f.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(1_700_000_000_999))
		}},
		{name: "BeginWake", status: wakeStatusEnqueued, call: func(f createFixture, wake Wake) error {
			_, _, err := f.svc.BeginWake(context.Background(), f.head.Binding, wake.ID, wake.ClaimID)
			return err
		}},
	} {
		t.Run(test.name+" rejects existing orphan", func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			wake := insertOrphanWakeForAdmissionTest(t, fixture, test.status)
			if err := test.call(fixture, wake); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("%s orphan error = %v, want invalid_task_spec", test.name, err)
			}
		})
	}

	t.Run("genuinely missing wake remains not found", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, "missing-wake", "missing-claim"); !errors.Is(err, ErrTaskNotFound) {
			t.Fatalf("missing wake error = %v, want task_not_found", err)
		}
	})
}

func pendingRecordForWakePlan(t *testing.T, store *SQLiteStore, wake Wake) Record {
	t.Helper()
	record, err := store.loadTask(context.Background(), wake.Owner, wake.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func insertOrphanWakeForAdmissionTest(t *testing.T, fixture createFixture, status string) Wake {
	t.Helper()
	owner := fixture.exec.Owner
	owner.EntityID = "0-orphan"
	wake := Wake{
		ID: "wake-orphan-" + status, TaskID: "task-orphan-" + status, Owner: owner,
		ExpectedRevision: 1, DueTick: 1, Reason: "orphan-" + status,
		Status: status, Generation: fixture.head.Binding.Generation,
	}
	if status != wakeStatusPending {
		wake.ClaimID = "claim-orphan"
		wake.ClaimedBy = fixture.svc.claimantID
		wake.Attempt = 1
	}
	if err := wake.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(wake)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := fixture.store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_, insertErr := conn.ExecContext(context.Background(), `INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wake.ID, owner.GameID, owner.WorldID, owner.EntityID, wake.TaskID, fixture.clock.ID,
		int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status, wake.ClaimID,
		wake.ClaimedBy, int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS, raw)
	_, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
	closeErr := conn.Close()
	if insertErr != nil || restoreErr != nil || closeErr != nil {
		t.Fatalf("inject orphan errors = insert:%v restore:%v close:%v", insertErr, restoreErr, closeErr)
	}
	var foreignKeys int
	if err := fixture.store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys after orphan injection = %d, %v", foreignKeys, err)
	}
	return wake
}
