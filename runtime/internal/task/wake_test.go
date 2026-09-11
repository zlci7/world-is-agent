package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWakeValidationStatusAndClaimShape(t *testing.T) {
	base := testWake()
	tests := []struct {
		name   string
		mutate func(*Wake)
		valid  bool
	}{
		{name: "pending", mutate: func(w *Wake) {
			w.Status, w.ClaimID, w.ClaimedBy, w.Attempt, w.RetryAfterUnixMS = "pending", "", "", 0, 123
		}, valid: true},
		{name: "claimed", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS = "claimed", 0 }, valid: true},
		{name: "enqueued", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS = "enqueued", 0 }, valid: true},
		{name: "running", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS = "running", 0 }, valid: true},
		{name: "consumed without claim", mutate: func(w *Wake) { w.Status, w.ClaimID, w.ClaimedBy, w.RetryAfterUnixMS = "consumed", "", "", 0 }, valid: true},
		{name: "consumed with audit claim", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS = "consumed", 0 }, valid: true},
		{name: "unknown status", mutate: func(w *Wake) { w.Status = "queued" }},
		{name: "missing reason", mutate: func(w *Wake) { w.Reason = "" }},
		{name: "pending claim id", mutate: func(w *Wake) { w.Status, w.ClaimedBy = "pending", "" }},
		{name: "pending claimant", mutate: func(w *Wake) { w.Status, w.ClaimID = "pending", "" }},
		{name: "claimed retry", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS = "claimed", 123 }},
		{name: "claimed zero attempt", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS, w.Attempt = "claimed", 0, 0 }},
		{name: "enqueued missing claim", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS, w.ClaimID = "enqueued", 0, "" }},
		{name: "running missing claimant", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS, w.ClaimedBy = "running", 0, "" }},
		{name: "consumed partial claim", mutate: func(w *Wake) { w.Status, w.RetryAfterUnixMS, w.ClaimedBy = "consumed", 0, "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wake := base
			tt.mutate(&wake)
			err := wake.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Wake.Validate() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("Wake.Validate() error = %v, want invalid_task_spec", err)
			}
		})
	}
}

func TestWakeExecutablePartialUniqueIndex(t *testing.T) {
	fixture, created, first := newIntentFixture(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	var indexSQL string
	if err := fixture.store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_task_wakeups_executable_task'`).Scan(&indexSQL); err != nil {
		t.Fatalf("executable wake index: %v", err)
	}
	normalized := strings.Join(strings.Fields(indexSQL), " ")
	for _, clause := range []string{"UNIQUE INDEX", "game_id, world_id, entity_id, task_id", "status IN ('pending', 'claimed', 'enqueued', 'running')"} {
		if !strings.Contains(normalized, clause) {
			t.Errorf("index %q missing %q", normalized, clause)
		}
	}

	second := first
	second.ID = "wake-second"
	second.Reason = "duplicate executable"
	raw, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.db.Exec(`INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		second.ID, second.Owner.GameID, second.Owner.WorldID, second.Owner.EntityID, second.TaskID,
		created.Task.Spec.ClockID, int64(second.ExpectedRevision), second.DueTick, second.Reason, second.Status,
		second.ClaimID, second.ClaimedBy, int64(second.Generation), second.Attempt, second.RetryAfterUnixMS, raw)
	if err == nil {
		t.Fatal("second executable wake insert succeeded")
	}

	consumed := first
	consumed.ID, consumed.Status = "wake-consumed", "consumed"
	consumed.Reason = "audit"
	consumed.ClaimID, consumed.ClaimedBy = "", ""
	raw, err = json.Marshal(consumed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		consumed.ID, consumed.Owner.GameID, consumed.Owner.WorldID, consumed.Owner.EntityID, consumed.TaskID,
		created.Task.Spec.ClockID, int64(consumed.ExpectedRevision), consumed.DueTick, consumed.Reason, consumed.Status,
		consumed.ClaimID, consumed.ClaimedBy, int64(consumed.Generation), consumed.Attempt, consumed.RetryAfterUnixMS, raw); err != nil {
		t.Fatalf("consumed audit insert: %v", err)
	}

	corrupt := first
	corrupt.Status = "mystery"
	raw, err = json.Marshal(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE task_wakeups SET status = ?, wake_json = ? WHERE wake_id = ?`, corrupt.Status, raw, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.loadWake(context.Background(), first.Owner, first.ID); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("load malformed wake error = %v, want invalid_task_spec", err)
	}
}

func TestWakeUnknownStatusFailsIntentWriteClosed(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	corrupt := wake
	corrupt.Status = "mystery"
	setWakeForTest(t, fixture.store, corrupt)
	exec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "unknown-wake")
	if got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "stop"}); !errors.Is(err, ErrInvalidTaskSpec) || got.ID != "" {
		t.Fatalf("ApplyIntent with unknown wake = (%+v, %v), want invalid_task_spec", got, err)
	}
	stored, err := fixture.store.loadTask(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || stored.State != StateWaiting || stored.Revision != created.Task.Revision {
		t.Fatalf("unknown wake mutation changed task = (%+v, %v)", stored, err)
	}
}

func TestWakeClaimDueTimingRetryOrderingAndLimit(t *testing.T) {
	t.Run("timing retry and no task mutation", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		fixture.svc.nowUnixMS = func() int64 { return 1_700_000_000_000 }
		beforeTask := rawTaskJSON(t, fixture.store, created.Task)
		setWakeRetryForTest(t, fixture.store, wake, 1_700_000_000_001)

		at199 := Clock{ID: fixture.clock.ID, Tick: 199, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, at199)
		got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at199, 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("ClaimDue before due = (%+v, %v), want empty", got, err)
		}
		at200 := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 3}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, at200)
		got, err = fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at200, 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("ClaimDue before retry = (%+v, %v), want empty", got, err)
		}
		fixture.svc.nowUnixMS = func() int64 { return 1_700_000_000_001 }
		got, err = fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at200, 10)
		if err != nil || len(got) != 1 {
			t.Fatalf("ClaimDue exact due/retry = (%+v, %v), want one", got, err)
		}
		if got[0].ID != wake.ID || got[0].Status != "claimed" || got[0].ClaimID == "" || got[0].ClaimedBy == "" || got[0].Attempt != 1 || got[0].RetryAfterUnixMS != 0 {
			t.Fatalf("claimed wake = %+v", got[0])
		}
		got[0].Reason = "caller mutation"
		stored, err := fixture.store.loadWake(context.Background(), wake.Owner, wake.ID)
		if err != nil || stored.Reason != wake.Reason {
			t.Fatalf("detachment stored = %+v, %v", stored, err)
		}
		if after := rawTaskJSON(t, fixture.store, created.Task); !bytes.Equal(after, beforeTask) {
			t.Fatal("ClaimDue mutated task JSON")
		}
	})

	t.Run("deterministic bounded batch", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{MaxTasksPerWorld: 3})
		fixture.svc.nowUnixMS = func() int64 { return 1234 }
		created := []CreateResult{
			createWakeTask(t, fixture, "b", "actor-b", 200, 300),
			createWakeTask(t, fixture, "late", "actor-a", 201, 300),
			createWakeTask(t, fixture, "a", "actor-a", 200, 300),
		}
		clock := Clock{ID: fixture.clock.ID, Tick: 250, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].TaskID != created[2].Task.ID || got[1].TaskID != created[0].Task.ID {
			t.Fatalf("claim order = %+v", got)
		}
		if got[0].ClaimID == got[1].ClaimID {
			t.Fatalf("batch reused claim id %q", got[0].ClaimID)
		}
		remaining, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 99)
		if err != nil || len(remaining) != 1 || remaining[0].TaskID != created[1].Task.ID {
			t.Fatalf("bounded follow-up = (%+v, %v)", remaining, err)
		}
	})

	t.Run("authority and limit", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		created := createWakeTask(t, fixture, "authority", "actor", 200, 300)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 0); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("zero limit = (%#v, %v)", got, err)
		}
		if _, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, -1); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("negative limit error = %v", err)
		}
		rewound := Clock{ID: clock.ID, Tick: 199, Sequence: clock.Sequence}
		if _, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, rewound, 1); !errors.Is(err, ErrClockRewound) {
			t.Fatalf("rewound error = %v", err)
		}
		forward := Clock{ID: clock.ID, Tick: 201, Sequence: clock.Sequence + 1}
		if _, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, forward, 1); !errors.Is(err, ErrClockMismatch) {
			t.Fatalf("forward mismatch error = %v", err)
		}
		foreign := fixture.head.Binding
		foreign.RunID = "other-run"
		if _, err := fixture.svc.ClaimDue(context.Background(), foreign, clock, 1); !errors.Is(err, ErrGenerationStale) {
			t.Fatalf("foreign binding error = %v", err)
		}
		setWorldHeadStateForIntentTest(t, fixture.store, Head{Binding: fixture.head.Binding, Clock: clock, Status: worldHeadStatusReady}, worldHeadStatusReady, "", "save-a", "saving")
		if _, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1); !errors.Is(err, ErrSaveInProgress) {
			t.Fatalf("barrier error = %v", err)
		}
		if got, err := fixture.store.loadTask(context.Background(), created.Task.Owner, created.Task.ID); err != nil || got.Revision != 1 {
			t.Fatalf("authority checks changed task = (%+v, %v)", got, err)
		}
	})
}

func TestWakeClaimDueAtomicConcurrencyAndRollback(t *testing.T) {
	t.Run("concurrent scanners claim once", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		created := createWakeTask(t, fixture, "one", "actor", 200, 300)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		services := []*Service{fixture.svc, fixture.svc}
		var wg sync.WaitGroup
		results := make(chan []Wake, len(services))
		errs := make(chan error, len(services))
		for _, svc := range services {
			wg.Add(1)
			go func(svc *Service) {
				defer wg.Done()
				got, err := svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
				results <- got
				errs <- err
			}(svc)
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent ClaimDue error = %v", err)
			}
		}
		claimed := 0
		for result := range results {
			claimed += len(result)
		}
		if claimed != 1 {
			t.Fatalf("successful claims = %d, want 1", claimed)
		}
		wake := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)[0]
		if wake.Status != "claimed" || wake.Attempt != 1 {
			t.Fatalf("stored wake = %+v", wake)
		}
	})

	t.Run("multi row fault rolls back", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		first := createWakeTask(t, fixture, "first", "actor-a", 200, 300)
		second := createWakeTask(t, fixture, "second", "actor-b", 200, 300)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		before := map[string][]byte{
			first.Task.ID:  rawOnlyWakeJSON(t, fixture.store, first.Task),
			second.Task.ID: rawOnlyWakeJSON(t, fixture.store, second.Task),
		}
		calls := 0
		fixture.store.testAfterWakeStage = func(_ context.Context, stage string) error {
			if stage == "claimed" {
				calls++
				if calls == 1 {
					return errors.New("injected claim failure")
				}
			}
			return nil
		}
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 2); err == nil || got != nil {
			t.Fatalf("fault ClaimDue = (%+v, %v)", got, err)
		}
		fixture.store.testAfterWakeStage = nil
		for _, task := range []Record{first.Task, second.Task} {
			if after := rawOnlyWakeJSON(t, fixture.store, task); !bytes.Equal(after, before[task.ID]) {
				t.Fatalf("wake %q changed after rollback", task.ID)
			}
		}
	})

	t.Run("attempt overflow and generated identity failures are atomic", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		created := createWakeTask(t, fixture, "overflow", "actor", 200, 300)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		wake := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)[0]
		wake.Attempt = math.MaxInt64
		setWakeForTest(t, fixture.store, wake)
		before := rawOnlyWakeJSON(t, fixture.store, created.Task)
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1); !errors.Is(err, ErrInvalidTaskSpec) || got != nil {
			t.Fatalf("overflow ClaimDue = (%+v, %v)", got, err)
		}
		if after := rawOnlyWakeJSON(t, fixture.store, created.Task); !bytes.Equal(after, before) {
			t.Fatal("overflow changed wake")
		}
		wake.Attempt = 0
		setWakeForTest(t, fixture.store, wake)
		fixture.svc.newID = func(string) string { return " " }
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1); !errors.Is(err, ErrInvalidTaskSpec) || got != nil {
			t.Fatalf("invalid id ClaimDue = (%+v, %v)", got, err)
		}
	})
}

func TestWakeMarkEnqueuedAndReleaseClaimTransitions(t *testing.T) {
	claimOne := func(t *testing.T) (createFixture, CreateResult, Wake, Clock) {
		t.Helper()
		fixture := newCreateFixture(t, StoreOptions{})
		created := createWakeTask(t, fixture, "transition", "actor", 200, 300)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
		}
		return fixture, created, claimed[0], clock
	}

	t.Run("mark and exact retry", func(t *testing.T) {
		fixture, _, wake, _ := claimOne(t)
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
			t.Fatalf("MarkEnqueued error = %v", err)
		}
		stored, err := fixture.store.loadWake(context.Background(), wake.Owner, wake.ID)
		if err != nil || stored.Status != "enqueued" || stored.ClaimID != wake.ClaimID || stored.ClaimedBy != wake.ClaimedBy || stored.Attempt != wake.Attempt {
			t.Fatalf("stored after mark = (%+v, %v)", stored, err)
		}
		fixture.store.testAfterWakeStage = func(context.Context, string) error { return errors.New("retry must not write") }
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
			t.Fatalf("exact Mark retry error = %v", err)
		}
		fixture.store.testAfterWakeStage = nil
		if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(1000)); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("release enqueued error = %v", err)
		}
	})

	t.Run("release preserves audit and supports future retry", func(t *testing.T) {
		fixture, _, wake, clock := claimOne(t)
		retryAt := time.UnixMilli(2_000)
		fixture.svc.nowUnixMS = func() int64 { return 1_999 }
		if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, retryAt); err != nil {
			t.Fatalf("ReleaseClaim error = %v", err)
		}
		stored, err := fixture.store.loadWake(context.Background(), wake.Owner, wake.ID)
		if err != nil || stored.Status != "pending" || stored.ClaimID != "" || stored.ClaimedBy != "" || stored.Attempt != wake.Attempt || stored.RetryAfterUnixMS != 2_000 {
			t.Fatalf("stored after release = (%+v, %v)", stored, err)
		}
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1); err != nil || len(got) != 0 {
			t.Fatalf("claim before retry = (%+v, %v)", got, err)
		}
		fixture.svc.nowUnixMS = func() int64 { return 2_000 }
		reclaimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
		if err != nil || len(reclaimed) != 1 || reclaimed[0].ClaimID == wake.ClaimID || reclaimed[0].Attempt != wake.Attempt+1 {
			t.Fatalf("reclaim = (%+v, %v)", reclaimed, err)
		}
		if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, retryAt); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("old claim release error = %v", err)
		}
	})

	t.Run("consuming released pending wake clears retry", func(t *testing.T) {
		fixture, created, wake, clock := claimOne(t)
		if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(9_000)); err != nil {
			t.Fatalf("ReleaseClaim error = %v", err)
		}
		exec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "cancel-released")
		exec.Owner = created.Task.Owner
		exec.Clock = clock
		if _, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "cancel while backoff pending"}); err != nil {
			t.Fatalf("ApplyIntent(cancel) error = %v", err)
		}
		stored, err := fixture.store.loadWake(context.Background(), wake.Owner, wake.ID)
		if err != nil || stored.Status != wakeStatusConsumed || stored.RetryAfterUnixMS != 0 {
			t.Fatalf("consumed released wake = (%+v, %v)", stored, err)
		}
	})

	t.Run("immediate elapsed retry and retry input validation", func(t *testing.T) {
		fixture, _, wake, clock := claimOne(t)
		fixture.svc.nowUnixMS = func() int64 { return 10_000 }
		if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(9_999)); err != nil {
			t.Fatalf("elapsed retry release error = %v", err)
		}
		if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1); err != nil || len(got) != 1 {
			t.Fatalf("immediate retry claim = (%+v, %v)", got, err)
		}
		for _, retryAt := range []time.Time{time.Time{}, time.UnixMilli(-1), time.Unix(1<<62, 0)} {
			if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, "some-claim", retryAt); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Errorf("ReleaseClaim(%v) error = %v", retryAt, err)
			}
		}
	})

	t.Run("wrong claim instance state and foreign wake", func(t *testing.T) {
		fixture, _, wake, _ := claimOne(t)
		other := NewService(fixture.store)
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, "wrong-claim"); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("wrong claim mark error = %v", err)
		}
		if err := other.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("wrong instance mark error = %v", err)
		}
		if err := other.ReleaseClaim(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(1000)); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("wrong instance release error = %v", err)
		}
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, "foreign-wake", wake.ClaimID); !errors.Is(err, ErrTaskNotFound) {
			t.Fatalf("foreign wake mark error = %v", err)
		}
		pendingFixture := newCreateFixture(t, StoreOptions{})
		pending := createWakeTask(t, pendingFixture, "pending", "actor", 200, 300)
		pendingWake := loadTaskWakes(t, pendingFixture.store, pending.Task.Owner, pending.Task.ID)[0]
		if err := pendingFixture.svc.MarkEnqueued(context.Background(), pendingFixture.head.Binding, pendingWake.ID, "claim"); !errors.Is(err, ErrTaskChanged) {
			t.Fatalf("pending mark error = %v", err)
		}
	})

	for _, operation := range []string{"mark fault", "mark cancellation", "release fault", "release cancellation"} {
		t.Run(operation+" rolls back", func(t *testing.T) {
			fixture, created, wake, _ := claimOne(t)
			before := rawOnlyWakeJSON(t, fixture.store, created.Task)
			ctx := context.Background()
			if strings.Contains(operation, "cancellation") {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				fixture.store.testAfterWakeStage = func(context.Context, string) error { cancel(); return nil }
			} else {
				fixture.store.testAfterWakeStage = func(context.Context, string) error { return errors.New("injected wake transition failure") }
			}
			var err error
			if strings.HasPrefix(operation, "mark") {
				err = fixture.svc.MarkEnqueued(ctx, fixture.head.Binding, wake.ID, wake.ClaimID)
			} else {
				err = fixture.svc.ReleaseClaim(ctx, fixture.head.Binding, wake.ID, wake.ClaimID, time.UnixMilli(1000))
			}
			if err == nil {
				t.Fatal("transition fault returned nil")
			}
			fixture.store.testAfterWakeStage = nil
			if after := rawOnlyWakeJSON(t, fixture.store, created.Task); !bytes.Equal(after, before) {
				t.Fatalf("%s changed wake", operation)
			}
		})
	}
}

func TestWakeBeginAdmissionAndExecutionContext(t *testing.T) {
	fixture, created, wake, clock := readyEnqueuedWake(t, 200, 300, 200)
	before := created.Task
	gotExec, got, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID)
	if err != nil {
		t.Fatalf("BeginWake error = %v", err)
	}
	if got.ID != before.ID || got.State != StateRunning || got.Revision != before.Revision+1 || got.NextWakeAt != nil {
		t.Fatalf("begun task = %+v", got)
	}
	if got.Owner != before.Owner || !taskSpecsEqual(got.Spec, before.Spec) || got.CreatedAtGameTick != before.CreatedAtGameTick || got.CreatedAtUnixMS != before.CreatedAtUnixMS ||
		!bytes.Equal(got.Progress, before.Progress) || got.NeedsReconcile != before.NeedsReconcile || got.NoProgressAttempts != before.NoProgressAttempts ||
		got.ReconcileAttempts != before.ReconcileAttempts || !reflectDeepEqual(got.Operations, before.Operations) || !reflectDeepEqual(got.Evidence, before.Evidence) ||
		!reflectDeepEqual(got.Result, before.Result) || !reflectDeepEqual(got.Cleanup, before.Cleanup) {
		t.Fatalf("BeginWake changed preserved task fields: before=%+v after=%+v", before, got)
	}
	wantExec := ExecutionContext{
		Owner: got.Owner, Binding: fixture.head.Binding, Clock: clock,
		Source: SourceRef{Kind: SourceKindTaskWake, CallID: wake.ID},
		TaskID: got.ID, WakeID: wake.ID, ExpectedRevision: got.Revision,
	}
	if !reflectDeepEqual(gotExec, wantExec) {
		t.Fatalf("execution context = %+v, want %+v", gotExec, wantExec)
	}
	storedWake, err := fixture.store.loadWake(context.Background(), wake.Owner, wake.ID)
	if err != nil || storedWake.Status != wakeStatusRunning || storedWake.ExpectedRevision != wake.ExpectedRevision ||
		storedWake.ClaimID != wake.ClaimID || storedWake.ClaimedBy != wake.ClaimedBy || storedWake.Generation != wake.Generation ||
		storedWake.Attempt != wake.Attempt || storedWake.Reason != wake.Reason {
		t.Fatalf("running wake = (%+v, %v)", storedWake, err)
	}
	got.Progress = json.RawMessage(`{"mutated":true}`)
	got.Spec.Instruction = "mutated"
	gotExec.Source.CallID = "mutated"
	storedTask, err := fixture.store.loadTask(context.Background(), before.Owner, before.ID)
	if err != nil || storedTask.Spec.Instruction != before.Spec.Instruction || bytes.Equal(storedTask.Progress, got.Progress) {
		t.Fatalf("caller mutation reached durable task = (%+v, %v)", storedTask, err)
	}
	if retryExec, retryTask, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); !errors.Is(err, ErrTaskChanged) || !reflectDeepEqual(retryExec, ExecutionContext{}) || retryTask.ID != "" {
		t.Fatalf("duplicate BeginWake = (%+v, %+v, %v)", retryExec, retryTask, err)
	}
}

func TestWakeBeginStaleDeadlineConcurrencyAndRollback(t *testing.T) {
	t.Run("exact and skipped deadline coordinate", func(t *testing.T) {
		for _, tt := range []struct {
			name                  string
			wakeAt, deadline, now int64
		}{
			{name: "exact deadline", wakeAt: 200, deadline: 200, now: 200},
			{name: "skipped deadline", wakeAt: 200, deadline: 250, now: 300},
		} {
			t.Run(tt.name, func(t *testing.T) {
				fixture, _, wake, _ := readyEnqueuedWake(t, tt.wakeAt, tt.deadline, tt.now)
				exec, record, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID)
				if err != nil || record.State != StateRunning || exec.Clock.Tick != tt.now {
					t.Fatalf("BeginWake at deadline = (%+v, %+v, %v)", exec, record, err)
				}
			})
		}
	})

	t.Run("not due loses admission", func(t *testing.T) {
		fixture, created, wake, _ := readyEnqueuedWake(t, 200, 300, 200)
		beforeTask, beforeWake := rawTaskJSON(t, fixture.store, created.Task), rawOnlyWakeJSON(t, fixture.store, created.Task)
		behind := Clock{ID: fixture.clock.ID, Tick: 199, Sequence: 3}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, behind)
		if exec, record, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); !errors.Is(err, ErrTaskChanged) || !reflectDeepEqual(exec, ExecutionContext{}) || record.ID != "" {
			t.Fatalf("not-due BeginWake = (%+v, %+v, %v)", exec, record, err)
		}
		if !bytes.Equal(rawTaskJSON(t, fixture.store, created.Task), beforeTask) || !bytes.Equal(rawOnlyWakeJSON(t, fixture.store, created.Task), beforeWake) {
			t.Fatal("not-due BeginWake mutated rows")
		}
	})

	t.Run("revision overflow and capacity fail closed", func(t *testing.T) {
		fixture, created, wake, _ := readyEnqueuedWake(t, 200, 300, 200)
		record, err := fixture.store.loadTask(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		record.Revision = math.MaxInt64
		setRecordForIntentTest(t, fixture.store, record)
		wake.ExpectedRevision = math.MaxInt64
		wake.Status = wakeStatusEnqueued
		setWakeForTest(t, fixture.store, wake)
		if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("overflow BeginWake error = %v", err)
		}

		path := filepath.Join(t.TempDir(), "capacity.sqlite")
		capacityFixture := newCreateFixture(t, StoreOptions{Path: path})
		capacityCreated := createWakeTask(t, capacityFixture, "capacity", "actor", 200, 300)
		capacityClock := Clock{ID: capacityFixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, capacityFixture.store, capacityFixture.head, capacityClock)
		claimed, err := capacityFixture.svc.ClaimDue(context.Background(), capacityFixture.head.Binding, capacityClock, 1)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("capacity ClaimDue = (%+v, %v)", claimed, err)
		}
		if err := capacityFixture.svc.MarkEnqueued(context.Background(), capacityFixture.head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
			t.Fatal(err)
		}
		claimant := capacityFixture.svc.claimantID
		if err := capacityFixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: 1})
		limited := NewService(reopened)
		limited.claimantID = claimant
		if _, _, err := limited.BeginWake(context.Background(), capacityFixture.head.Binding, claimed[0].ID, claimed[0].ClaimID); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("capacity BeginWake error = %v", err)
		}
		stored, err := reopened.loadTask(context.Background(), capacityCreated.Task.Owner, capacityCreated.Task.ID)
		if err != nil || stored.State != StateWaiting || stored.Revision != 1 {
			t.Fatalf("capacity failure changed task = (%+v, %v)", stored, err)
		}
	})

	t.Run("concurrent begin grants one execution", func(t *testing.T) {
		fixture, _, wake, _ := readyEnqueuedWake(t, 200, 300, 200)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		success, changed := 0, 0
		for err := range errs {
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrTaskChanged):
				changed++
			default:
				t.Fatalf("concurrent BeginWake error = %v", err)
			}
		}
		if success != 1 || changed != 1 {
			t.Fatalf("concurrent results success=%d changed=%d", success, changed)
		}
	})

	for _, failStage := range []string{"begin_record_updated", wakeStatusRunning} {
		t.Run("fault after "+failStage+" rolls back", func(t *testing.T) {
			fixture, created, wake, _ := readyEnqueuedWake(t, 200, 300, 200)
			beforeTask, beforeWake := rawTaskJSON(t, fixture.store, created.Task), rawOnlyWakeJSON(t, fixture.store, created.Task)
			fixture.store.testAfterWakeStage = func(_ context.Context, stage string) error {
				if stage == failStage {
					return errors.New("injected begin failure")
				}
				return nil
			}
			if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err == nil {
				t.Fatal("fault BeginWake returned nil")
			}
			fixture.store.testAfterWakeStage = nil
			if !bytes.Equal(rawTaskJSON(t, fixture.store, created.Task), beforeTask) || !bytes.Equal(rawOnlyWakeJSON(t, fixture.store, created.Task), beforeWake) {
				t.Fatal("fault BeginWake did not roll back both rows")
			}
		})
	}
}

func TestWakeAdmissionEndToEnd(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "flow", "actor", 200, 300)
	fixture.svc.nowUnixMS = func() int64 { return 1_000 }
	at199 := Clock{ID: fixture.clock.ID, Tick: 199, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, at199)
	if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at199, 1); err != nil || len(got) != 0 {
		t.Fatalf("199 ClaimDue = (%+v, %v)", got, err)
	}

	at200 := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 3}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, at200)
	first, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at200, 1)
	if err != nil || len(first) != 1 || first[0].Attempt != 1 {
		t.Fatalf("first 200 ClaimDue = (%+v, %v)", first, err)
	}
	if duplicate, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at200, 1); err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate ClaimDue = (%+v, %v)", duplicate, err)
	}
	if err := fixture.svc.ReleaseClaim(context.Background(), fixture.head.Binding, first[0].ID, first[0].ClaimID, time.UnixMilli(1_000)); err != nil {
		t.Fatalf("lane-full ReleaseClaim error = %v", err)
	}
	second, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at200, 1)
	if err != nil || len(second) != 1 || second[0].ClaimID == first[0].ClaimID || second[0].Attempt != 2 {
		t.Fatalf("reclaim = (%+v, %v)", second, err)
	}
	if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, second[0].ID, second[0].ClaimID); err != nil {
		t.Fatalf("MarkEnqueued error = %v", err)
	}
	wakeExec, running, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, second[0].ID, second[0].ClaimID)
	if err != nil || running.Revision != 2 || running.State != StateRunning {
		t.Fatalf("BeginWake = (%+v, %+v, %v)", wakeExec, running, err)
	}
	if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, second[0].ID, second[0].ClaimID); !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("duplicate BeginWake error = %v", err)
	}
	reconciled, err := fixture.svc.Reconcile(context.Background(), wakeExec)
	if err != nil || reconciled.Next != ReconcileNextDecide || reconciled.Task.Revision != running.Revision {
		t.Fatalf("wake Reconcile = (%+v, %v)", reconciled, err)
	}

	nextWake := int64(300)
	taskTurnExec := wakeExec
	taskTurnExec.Source = SourceRef{
		Kind: SourceKindTaskWake, EventID: "task-event-flow", TurnID: "task-turn-flow", CallID: "task-call-flow",
		GameTime: json.RawMessage(`{"tick":200}`), Facts: json.RawMessage(`[{"kind":"task_context"}]`),
	}
	waiting, err := fixture.svc.ApplyIntent(context.Background(), taskTurnExec, Intent{Kind: "wait", NextWakeAt: &nextWake, ProgressNote: "continue later"})
	if err != nil || waiting.State != StateWaiting || waiting.Revision != 3 || waiting.NextWakeAt == nil || *waiting.NextWakeAt != 300 {
		t.Fatalf("ApplyIntent wait = (%+v, %v)", waiting, err)
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	if len(wakes) != 2 {
		t.Fatalf("flow wakes = %+v", wakes)
	}
	var old, replacement Wake
	for _, wake := range wakes {
		if wake.ID == second[0].ID {
			old = wake
		} else {
			replacement = wake
		}
	}
	if old.Status != wakeStatusConsumed || old.ClaimID != second[0].ClaimID || old.Attempt != 2 || replacement.Status != wakeStatusPending || replacement.DueTick != 300 || replacement.ExpectedRevision != 3 {
		t.Fatalf("old/replacement wakes = old:%+v replacement:%+v", old, replacement)
	}
	if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, old.ID, old.ClaimID); !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("old BeginWake after wait error = %v", err)
	}
	at299 := Clock{ID: fixture.clock.ID, Tick: 299, Sequence: 4}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, at299)
	if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at299, 1); err != nil || len(got) != 0 {
		t.Fatalf("299 ClaimDue = (%+v, %v)", got, err)
	}
	at300 := Clock{ID: fixture.clock.ID, Tick: 300, Sequence: 5}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, at300)
	if got, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, at300, 1); err != nil || len(got) != 1 || got[0].ID != replacement.ID || got[0].Attempt != 1 {
		t.Fatalf("300 ClaimDue = (%+v, %v)", got, err)
	}
}

func readyEnqueuedWake(t *testing.T, wakeAt, deadline, now int64) (createFixture, CreateResult, Wake, Clock) {
	t.Helper()
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "begin", "actor", wakeAt, deadline)
	clock := Clock{ID: fixture.clock.ID, Tick: now, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
	claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
	}
	if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
		t.Fatalf("MarkEnqueued error = %v", err)
	}
	return fixture, created, claimed[0], clock
}

func reflectDeepEqual(left, right any) bool {
	return reflect.DeepEqual(left, right)
}

func createWakeTask(t *testing.T, fixture createFixture, suffix, entity string, wakeAt, deadline int64) CreateResult {
	t.Helper()
	exec, spec := fixture.exec, fixture.spec
	exec.Owner.EntityID = entity
	exec.Source.EventID, exec.Source.TurnID, exec.Source.CallID = "event-"+suffix, "turn-"+suffix, "call-"+suffix
	spec.Source = exec.Source
	spec.WakeAt, spec.DeadlineAt, spec.EquivalenceKey = wakeAt, deadline, "eq-"+suffix
	got, err := fixture.svc.Create(context.Background(), exec, spec, Admission{})
	if err != nil {
		t.Fatalf("Create(%s) error = %v", suffix, err)
	}
	return got
}

func rawTaskJSON(t *testing.T, store *SQLiteStore, record Record) []byte {
	t.Helper()
	var raw []byte
	if err := store.db.QueryRow(`SELECT record_json FROM tasks WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), raw...)
}

func rawOnlyWakeJSON(t *testing.T, store *SQLiteStore, record Record) []byte {
	t.Helper()
	var raw []byte
	if err := store.db.QueryRow(`SELECT wake_json FROM task_wakeups WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), raw...)
}

func setWakeRetryForTest(t *testing.T, store *SQLiteStore, wake Wake, retry int64) {
	t.Helper()
	wake.RetryAfterUnixMS = retry
	setWakeForTest(t, store, wake)
}

func setWakeForTest(t *testing.T, store *SQLiteStore, wake Wake) {
	t.Helper()
	raw, err := json.Marshal(wake)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.db.Exec(`UPDATE task_wakeups SET expected_revision = ?, due_tick = ?, reason = ?, status = ?, claim_id = ?, claimed_by = ?, generation = ?, attempt = ?, retry_after_unix_ms = ?, wake_json = ? WHERE wake_id = ?`,
		int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status, wake.ClaimID, wake.ClaimedBy,
		int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS, raw, wake.ID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("set wake rows = %d, %v", affected, err)
	}
}
