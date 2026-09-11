package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
)

func TestRestartRecoversForeignDeliveryWithoutStealingOwnClaim(t *testing.T) {
	for _, status := range []string{wakeStatusClaimed, wakeStatusEnqueued} {
		t.Run(status, func(t *testing.T) {
			fixture, created, wake, clock := claimedWakeForRestart(t, status)
			beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)

			if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
				CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); err != nil {
				t.Fatalf("same-service ActivateWorld error = %v", err)
			}
			assertTaskSnapshotEqual(t, fixture.store, created.Task, beforeTask, beforeWakes, beforeHistory, "same-service activation")

			restarted := deterministicRestartService(fixture.store)
			head, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
				CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
			if err != nil {
				t.Fatalf("new-service ActivateWorld error = %v", err)
			}
			afterTask, _, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(afterTask, beforeTask) || !bytes.Equal(afterHistory, beforeHistory) {
				t.Fatal("delivery recovery changed Task or intent history")
			}
			stored, err := fixture.store.loadWake(context.Background(), created.Task.Owner, wake.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := wake
			want.Status, want.ClaimID, want.ClaimedBy, want.RetryAfterUnixMS = wakeStatusPending, "", "", 0
			want.Generation = head.Binding.Generation
			if stored != want {
				t.Fatalf("recovered wake = %+v, want %+v", stored, want)
			}
			if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); !errors.Is(err, ErrGenerationStale) {
				t.Fatalf("old claim MarkEnqueued error = %v, want generation_stale", err)
			}
		})
	}
}

func TestRestartRebindsPendingWakeToFreshGenerationAndLatestClock(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "restart-pending-generation", "actor", 200, 400)
	advanced := Clock{ID: fixture.clock.ID, Tick: 150, Sequence: fixture.clock.Sequence + 1}
	restarted := deterministicRestartService(fixture.store)

	head, err := restarted.ActivateWorld(context.Background(), fixture.world, fixture.head.Binding.RunID, advanced,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
	if err != nil {
		t.Fatalf("ActivateWorld at latest Clock error = %v", err)
	}
	if head.Clock != advanced || head.Binding.Generation != fixture.head.Binding.Generation+1 {
		t.Fatalf("rebound Head = %+v, want Clock %+v and generation %d", head, advanced, fixture.head.Binding.Generation+1)
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	if len(wakes) != 1 || wakes[0].Status != wakeStatusPending || wakes[0].Generation != head.Binding.Generation {
		t.Fatalf("rebound pending wakes = %+v, want generation %d", wakes, head.Binding.Generation)
	}
	if claimed, err := restarted.ClaimDue(context.Background(), fixture.head.Binding, advanced, 1); !errors.Is(err, ErrGenerationStale) || len(claimed) != 0 {
		t.Fatalf("old generation ClaimDue = (%+v, %v), want generation_stale", claimed, err)
	}
}

func TestRestartRecoversRunningAtLatestReconnectClock(t *testing.T) {
	fixture, created, _, _, running, clock := runningWakeForRestart(t, 200)
	advanced := Clock{ID: clock.ID, Tick: 250, Sequence: clock.Sequence + 1}
	restarted := deterministicRestartService(fixture.store)

	head, err := restarted.ActivateWorld(context.Background(), fixture.world, fixture.head.Binding.RunID, advanced,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
	if err != nil {
		t.Fatalf("ActivateWorld at latest Clock error = %v", err)
	}
	recovered, err := restarted.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Revision != running.Revision+1 || recovered.NextWakeAt == nil || *recovered.NextWakeAt != advanced.Tick {
		t.Fatalf("recovered Task = %+v, want immediate reconciliation at %d", recovered, advanced.Tick)
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	var replacement Wake
	for _, wake := range wakes {
		if wake.Status == wakeStatusPending {
			replacement = wake
		}
	}
	if replacement.ID == "" || replacement.Generation != head.Binding.Generation || replacement.DueTick != advanced.Tick {
		t.Fatalf("replacement Wake = %+v, want generation %d and due tick %d", replacement, head.Binding.Generation, advanced.Tick)
	}
}

func TestRestartRecoversExistingWorkingSetAboveCurrentCreateLimit(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{MaxTasksPerWorld: 2})
	created := []CreateResult{
		createWakeTask(t, fixture, "restart-over-limit-a", "actor-a", 200, 400),
		createWakeTask(t, fixture, "restart-over-limit-b", "actor-b", 200, 400),
	}
	clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: fixture.clock.Sequence + 1}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
	claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, len(created))
	if err != nil || len(claimed) != len(created) {
		t.Fatalf("ClaimDue = (%+v, %v), want %d", claimed, err, len(created))
	}
	for _, wake := range claimed {
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
			t.Fatal(err)
		}
	}

	path := fixture.store.options.Path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.store = openTaskTestStore(t, StoreOptions{Path: path, MaxTasksPerWorld: 1})
	restarted := deterministicRestartService(fixture.store)
	advanced := Clock{ID: clock.ID, Tick: 250, Sequence: clock.Sequence + 1}
	head, err := restarted.ActivateWorld(context.Background(), fixture.world, fixture.head.Binding.RunID, advanced,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
	if err != nil {
		t.Fatalf("ActivateWorld above current creation limit error = %v", err)
	}
	if head.Binding.Generation != fixture.head.Binding.Generation+1 || head.Clock != advanced {
		t.Fatalf("restarted Head = %+v", head)
	}
	for _, result := range created {
		recovered, err := restarted.Read(context.Background(), result.Task.Owner, result.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.State != StateWaiting || !recovered.NeedsReconcile || recovered.NextWakeAt == nil || *recovered.NextWakeAt != advanced.Tick {
			t.Fatalf("recovered Task = %+v", recovered)
		}
		wakes := loadTaskWakes(t, fixture.store, result.Task.Owner, result.Task.ID)
		active := 0
		for _, wake := range wakes {
			if wake.Status == wakeStatusPending {
				active++
				if wake.Generation != head.Binding.Generation {
					t.Fatalf("replacement Wake = %+v, want generation %d", wake, head.Binding.Generation)
				}
			}
		}
		if active != 1 {
			t.Fatalf("active wakes for %s = %d, all = %+v", result.Task.ID, active, wakes)
		}
	}

	exec, spec := createInputs(head, advanced, testOwner(), "event-over-limit", "turn-over-limit", "call-over-limit", "eq-over-limit")
	spec.WakeAt, spec.DeadlineAt = 300, 400
	if _, err := restarted.Create(context.Background(), exec, spec, Admission{}); !errors.Is(err, ErrTaskCapacityExceeded) {
		t.Fatalf("Create above current limit error = %v, want task_capacity_exceeded", err)
	}
}

func TestRestartRebindsPendingAndPreservesTaskHistory(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "unchanged", "actor", 200, 300)
	beforeTask, _, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	restarted := deterministicRestartService(fixture.store)
	head, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
	if err != nil {
		t.Fatal(err)
	}
	afterTask, _, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	if !bytes.Equal(afterTask, beforeTask) || !bytes.Equal(afterHistory, beforeHistory) {
		t.Fatal("pending activation changed Task or intent history")
	}

	clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, head, clock)
	claimed, err := restarted.ClaimDue(context.Background(), head.Binding, clock, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
	}
	if err := restarted.MarkEnqueued(context.Background(), head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
		t.Fatal(err)
	}
	exec, running, err := restarted.BeginWake(context.Background(), head.Binding, claimed[0].ID, claimed[0].ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	next := int64(250)
	exec.Source = SourceRef{Kind: SourceKindTaskWake, EventID: "event-consume", TurnID: "turn-consume", CallID: "call-consume"}
	waiting, err := restarted.ApplyIntent(context.Background(), exec, Intent{Kind: "wait", NextWakeAt: &next})
	if err != nil || waiting.Revision != running.Revision+1 {
		t.Fatalf("ApplyIntent = (%+v, %v)", waiting, err)
	}
	beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	secondRestart := deterministicRestartService(fixture.store)
	secondHead, err := secondRestart.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
	if err != nil {
		t.Fatal(err)
	}
	afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	if !bytes.Equal(afterTask, beforeTask) || !bytes.Equal(afterHistory, beforeHistory) {
		t.Fatal("second activation changed Task or intent history")
	}
	if bytes.Equal(afterWakes, beforeWakes) {
		t.Fatal("second activation did not rebind the pending wake")
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	var consumed, pending int
	for _, wake := range wakes {
		switch wake.Status {
		case wakeStatusConsumed:
			consumed++
		case wakeStatusPending:
			pending++
			if wake.Generation != secondHead.Binding.Generation {
				t.Fatalf("pending wake generation = %d, want %d", wake.Generation, secondHead.Binding.Generation)
			}
		}
	}
	if len(wakes) != 2 || consumed != 1 || pending != 1 {
		t.Fatalf("second activation wakes = %+v", wakes)
	}
}

func TestRestartRunningAttemptBecomesImmediateReconciliation(t *testing.T) {
	for _, now := range []int64{250, 350} {
		t.Run(fmt.Sprintf("clock_%d", now), func(t *testing.T) {
			fixture, created, oldWake, exec, running, clock := runningWakeForRestart(t, now)
			first := registeredOperation(exec, "operation-restart-registered")
			second := registeredOperation(exec, "operation-restart-uncertain")
			if _, err := fixture.svc.RegisterOperation(context.Background(), exec, first); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.svc.RegisterOperation(context.Background(), exec, second); err != nil {
				t.Fatal(err)
			}
			current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.Operations[1].Status = OperationStatusUncertain
			storeD2TaskForTest(t, fixture.store, current)

			restarted := deterministicRestartService(fixture.store)
			head, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
				CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
			if err != nil {
				t.Fatalf("ActivateWorld error = %v", err)
			}
			if head.Binding.World != fixture.head.Binding.World || head.Binding.RunID != fixture.head.Binding.RunID ||
				head.Binding.Generation != fixture.head.Binding.Generation+1 || head.Clock != clock || head.Status != worldHeadStatusReady {
				t.Fatalf("returned head = %+v", head)
			}
			recovered, err := restarted.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantDue := now
			if wantDue > created.Task.Spec.DeadlineAt {
				wantDue = created.Task.Spec.DeadlineAt
			}
			if recovered.Revision != running.Revision+1 || recovered.State != StateWaiting || !recovered.NeedsReconcile ||
				recovered.PauseReason != "" || recovered.NextWakeAt == nil || *recovered.NextWakeAt != wantDue ||
				recovered.Operations[0].Status != OperationStatusUncertain || recovered.Operations[1].Status != OperationStatusUncertain {
				t.Fatalf("recovered Task = %+v", recovered)
			}
			old, err := fixture.store.loadWake(context.Background(), created.Task.Owner, oldWake.ID)
			if err != nil || old.Status != wakeStatusConsumed || old.ClaimID != oldWake.ClaimID ||
				old.ClaimedBy != oldWake.ClaimedBy || old.Attempt != oldWake.Attempt || old.ExpectedRevision != oldWake.ExpectedRevision {
				t.Fatalf("old wake = (%+v, %v)", old, err)
			}
			wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
			if len(wakes) != 2 {
				t.Fatalf("wake count = %d, want 2", len(wakes))
			}
			var replacement Wake
			for _, wake := range wakes {
				if wake.ID != oldWake.ID {
					replacement = wake
				}
			}
			if replacement.Status != wakeStatusPending || replacement.Reason != "restart_reconcile" ||
				replacement.ExpectedRevision != recovered.Revision || replacement.DueTick != wantDue ||
				replacement.Generation != head.Binding.Generation || replacement.Attempt != 0 ||
				replacement.ClaimID != "" || replacement.ClaimedBy != "" || replacement.RetryAfterUnixMS != 0 {
				t.Fatalf("replacement wake = %+v", replacement)
			}

			claimed, err := restarted.ClaimDue(context.Background(), head.Binding, clock, 2)
			if err != nil || len(claimed) != 1 || claimed[0].ID != replacement.ID {
				t.Fatalf("ClaimDue replacement = (%+v, %v)", claimed, err)
			}
			if err := restarted.MarkEnqueued(context.Background(), head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
				t.Fatal(err)
			}
			reconcileExec, admitted, err := restarted.BeginWake(context.Background(), head.Binding, claimed[0].ID, claimed[0].ClaimID)
			if err != nil || admitted.Revision != recovered.Revision+1 {
				t.Fatalf("BeginWake replacement = (%+v, %+v, %v)", reconcileExec, admitted, err)
			}
			routed, err := restarted.Reconcile(context.Background(), reconcileExec)
			if err != nil || routed.Next != ReconcileNextObserve || routed.Task.Revision != admitted.Revision {
				t.Fatalf("Reconcile replacement = (%+v, %v)", routed, err)
			}
			if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, oldWake.ID, oldWake.ClaimID); !errors.Is(err, ErrGenerationStale) {
				t.Fatalf("old BeginWake error = %v, want generation_stale", err)
			}
		})
	}
}

func TestRestartRecoveryIsAtomicAndIdempotent(t *testing.T) {
	t.Run("multi task injected failure", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		first := createWakeTask(t, fixture, "restart-atomic-a", "actor-a", 200, 300)
		second := createWakeTask(t, fixture, "restart-atomic-b", "actor-b", 200, 300)
		clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
		claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 2)
		if err != nil || len(claimed) != 2 {
			t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
		}
		for _, wake := range claimed {
			if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID); err != nil {
				t.Fatal(err)
			}
		}
		beforeFirst := snapshotD2Rows(t, fixture.store, first.Task)
		beforeSecond := snapshotD2Rows(t, fixture.store, second.Task)
		fixture.store.testAfterWakeStage = func(context.Context, string) error { return errors.New("injected restart failure") }
		_, err = deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
		fixture.store.testAfterWakeStage = nil
		if err == nil {
			t.Fatal("ActivateWorld fault returned nil")
		}
		assertD2Rows(t, fixture.store, first.Task, beforeFirst)
		assertD2Rows(t, fixture.store, second.Task, beforeSecond)
	})

	t.Run("success retry is byte identical", func(t *testing.T) {
		fixture, created, _, _, _, clock := runningWakeForRestart(t, 200)
		restarted := deterministicRestartService(fixture.store)
		if _, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); err != nil {
			t.Fatal(err)
		}
		before := snapshotD2Rows(t, fixture.store, created.Task)
		if _, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); err != nil {
			t.Fatal(err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})

	t.Run("head marker rolls back with batch", func(t *testing.T) {
		fixture, created, _, _, _, clock := runningWakeForRestart(t, 200)
		before := snapshotD2Rows(t, fixture.store, created.Task)
		beforeInstance := runtimeInstanceIDForRestart(t, fixture.store, fixture.world)
		restarted := deterministicRestartService(fixture.store)
		fixture.store.testAfterWakeStage = func(_ context.Context, stage string) error {
			if stage == "restart_head_rebound" {
				return errors.New("head marker fault")
			}
			return nil
		}
		_, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
		fixture.store.testAfterWakeStage = nil
		if err == nil {
			t.Fatal("head marker fault returned nil")
		}
		assertD2Rows(t, fixture.store, created.Task, before)
		if afterInstance := runtimeInstanceIDForRestart(t, fixture.store, fixture.world); afterInstance != beforeInstance {
			t.Fatalf("runtime instance = %q, want rolled-back %q", afterInstance, beforeInstance)
		}
		if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); err != nil {
			t.Fatalf("prior active Service retry error = %v", err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})
}

func TestRestartRecoveryRejectsUnsafeStateWithoutPartialWrite(t *testing.T) {
	t.Run("revision overflow", func(t *testing.T) {
		fixture, created, wake, _, running, clock := runningWakeForRestart(t, 200)
		running.Revision = uint64(math.MaxInt64)
		storeD2TaskForTest(t, fixture.store, running)
		wake.ExpectedRevision = uint64(math.MaxInt64 - 1)
		setWakeForTest(t, fixture.store, wake)
		before := snapshotD2Rows(t, fixture.store, created.Task)
		_, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
		if !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("overflow error = %v", err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})

	t.Run("replacement id collision", func(t *testing.T) {
		fixture, created, wake, _, _, clock := runningWakeForRestart(t, 200)
		fixture.store.options.MaxTasksPerWorld = 1
		restarted := NewService(fixture.store)
		restarted.newID = func(string) string { return wake.ID }
		before := snapshotD2Rows(t, fixture.store, created.Task)
		_, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
		if err == nil {
			t.Fatal("collision activation returned nil")
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})

	t.Run("capacity", func(t *testing.T) {
		fixture, created, _, _, _, clock := runningWakeForRestart(t, 200)
		fixture.store.options.MaxTaskBytes = 1
		before := snapshotD2Rows(t, fixture.store, created.Task)
		_, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
		if !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("capacity error = %v", err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})

	t.Run("cancellation", func(t *testing.T) {
		fixture, created, _, _, _, clock := runningWakeForRestart(t, 200)
		before := snapshotD2Rows(t, fixture.store, created.Task)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := deterministicRestartService(fixture.store).ActivateWorld(ctx, fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
		if err == nil {
			t.Fatal("cancelled activation returned nil")
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})
}

func TestRestartFailsClosedOnCanonicalIndexOrWakeGraphCorruption(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, createFixture, CreateResult)
	}{
		{name: "task canonical", corrupt: func(t *testing.T, fixture createFixture, created CreateResult) {
			raw := append(rawTaskJSON(t, fixture.store, created.Task), ' ')
			if _, err := fixture.store.db.Exec(`UPDATE tasks SET record_json = ? WHERE task_id = ?`, raw, created.Task.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wake index", corrupt: func(t *testing.T, fixture createFixture, created CreateResult) {
			if _, err := fixture.store.db.Exec(`UPDATE task_wakeups SET reason = 'indexed-corrupt' WHERE task_id = ?`, created.Task.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "orphan wake", corrupt: func(t *testing.T, fixture createFixture, created CreateResult) {
			wake := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)[0]
			wake.ID, wake.TaskID = "wake-orphan", "task-missing"
			if _, err := fixture.store.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			insertD2WakeRaw(t, fixture.store.db, created.Task.Spec.ClockID, wake)
			if _, err := fixture.store.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate executable", corrupt: func(t *testing.T, fixture createFixture, created CreateResult) {
			if _, err := fixture.store.db.Exec(`DROP INDEX idx_task_wakeups_executable_task`); err != nil {
				t.Fatal(err)
			}
			wake := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)[0]
			wake.ID, wake.Reason = "wake-duplicate", "duplicate"
			insertD2WakeRaw(t, fixture.store.db, created.Task.Spec.ClockID, wake)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created := createWakeTask(t, fixture, "corrupt", "actor", 200, 300)
			tt.corrupt(t, fixture, created)
			_, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock,
				CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
			if !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("ActivateWorld error = %v, want invalid_task_spec", err)
			}
		})
	}
}

func TestRestartRejectsNonCanonicalWorldHeadBeforeRecovery(t *testing.T) {
	tests := []struct {
		name   string
		poison func(*testing.T, []byte) []byte
	}{
		{name: "tail whitespace", poison: func(_ *testing.T, raw []byte) []byte {
			return append(append([]byte(nil), raw...), ' ')
		}},
		{name: "key reorder", poison: func(t *testing.T, raw []byte) []byte {
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			return mustJSONBytes(t, decoded)
		}},
		{name: "unknown field", poison: func(_ *testing.T, raw []byte) []byte {
			return append([]byte(`{"unknown":true,`), raw[1:]...)
		}},
		{name: "duplicate field", poison: func(_ *testing.T, raw []byte) []byte {
			return append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"barrier_status":""}`)...)
		}},
		{name: "malformed json", poison: func(_ *testing.T, _ []byte) []byte {
			return []byte(`{"head"`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, _, _, _, clock := runningWakeForRestart(t, 200)
			before := snapshotD2Rows(t, fixture.store, created.Task)
			raw := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world)
			poisoned := tt.poison(t, raw)
			if _, err := fixture.store.db.Exec(`UPDATE task_world_heads SET head_json = ? WHERE game_id = ? AND world_id = ?`,
				poisoned, fixture.world.GameID, fixture.world.WorldID); err != nil {
				t.Fatal(err)
			}
			if _, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", clock,
				CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("ActivateWorld error = %v, want invalid_task_spec", err)
			}
			assertD2Rows(t, fixture.store, created.Task, before)
			if after := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world); !bytes.Equal(after, poisoned) {
				t.Fatal("failed recovery changed poisoned world head")
			}
		})
	}

	t.Run("runtime marker index mismatch", func(t *testing.T) {
		fixture, created, _, _, _, clock := runningWakeForRestart(t, 200)
		before := snapshotD2Rows(t, fixture.store, created.Task)
		beforeHead := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world)
		if _, err := fixture.store.db.Exec(`UPDATE task_world_heads SET runtime_instance_id = ? WHERE game_id = ? AND world_id = ?`,
			"runtime-index-poison", fixture.world.GameID, fixture.world.WorldID); err != nil {
			t.Fatal(err)
		}
		if _, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("ActivateWorld error = %v, want invalid_task_spec", err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
		if afterHead := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world); !bytes.Equal(afterHead, beforeHead) {
			t.Fatal("failed recovery changed world head JSON")
		}
		if afterInstance := runtimeInstanceIDForRestart(t, fixture.store, fixture.world); afterInstance != "runtime-index-poison" {
			t.Fatalf("failed recovery changed runtime instance to %q", afterInstance)
		}
	})
}

func TestRestartRecoversRunningTaskWithoutExecutableWake(t *testing.T) {
	fixture, created, initialWake := newIntentFixture(t, StoreOptions{})
	progress := taskEvidence(fixture.head.Binding, created.Task, "fact-restart-no-wake", EvidenceKindProgress)
	progress.Details = json.RawMessage(`{"progress":"ordinary"}`)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, progress); err != nil || !added {
		t.Fatalf("AdmitEvidence = (%v, %v)", added, err)
	}
	reconciled, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, created.Task.Revision))
	if err != nil || reconciled.Next != ReconcileNextDecide || reconciled.Task.State != StateRunning {
		t.Fatalf("Reconcile = (%+v, %v)", reconciled, err)
	}
	oldWake, err := fixture.store.loadWake(context.Background(), created.Task.Owner, initialWake.ID)
	if err != nil || oldWake.Status != wakeStatusConsumed {
		t.Fatalf("old wake = (%+v, %v)", oldWake, err)
	}

	beforeSame := snapshotD2Rows(t, fixture.store, created.Task)
	beforeSameHead := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world)
	if active := runtimeInstanceIDForRestart(t, fixture.store, fixture.world); active != fixture.svc.claimantID {
		t.Fatalf("fresh runtime instance = %q, want %q", active, fixture.svc.claimantID)
	}
	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); err != nil {
		t.Fatalf("same-service ActivateWorld error = %v", err)
	}
	assertD2Rows(t, fixture.store, created.Task, beforeSame)
	if afterSameHead := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world); !bytes.Equal(afterSameHead, beforeSameHead) {
		t.Fatal("same-service activation changed world head")
	}

	restarted := deterministicRestartService(fixture.store)
	head, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world})
	if err != nil {
		t.Fatalf("new-service ActivateWorld error = %v", err)
	}
	recovered, err := restarted.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Revision != reconciled.Task.Revision+1 || recovered.State != StateWaiting || !recovered.NeedsReconcile ||
		recovered.NextWakeAt == nil || *recovered.NextWakeAt != fixture.clock.Tick {
		t.Fatalf("recovered Task = %+v", recovered)
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	if len(wakes) != 2 {
		t.Fatalf("wake count = %d, want consumed audit plus replacement", len(wakes))
	}
	var replacement Wake
	for _, wake := range wakes {
		if wake.Status == wakeStatusPending {
			replacement = wake
		}
	}
	if replacement.ID == "" || replacement.Reason != wakeReasonRestartReconcile || replacement.ExpectedRevision != recovered.Revision {
		t.Fatalf("replacement Wake = %+v", replacement)
	}
	if active := runtimeInstanceIDForRestart(t, fixture.store, fixture.world); active != restarted.claimantID {
		t.Fatalf("recovered runtime instance = %q, want %q", active, restarted.claimantID)
	}

	beforeRetry := snapshotD2Rows(t, fixture.store, created.Task)
	beforeRetryHead := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world)
	if _, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); err != nil {
		t.Fatalf("response-loss activation retry error = %v", err)
	}
	assertD2Rows(t, fixture.store, created.Task, beforeRetry)
	if afterRetryHead := rawWorldHeadJSONForRestart(t, fixture.store, fixture.world); !bytes.Equal(afterRetryHead, beforeRetryHead) {
		t.Fatal("response-loss retry changed world head")
	}
	if claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, fixture.clock, 1); !errors.Is(err, ErrGenerationStale) || len(claimed) != 0 {
		t.Fatalf("stale Service ClaimDue = (%+v, %v), want generation_stale", claimed, err)
	}
	if claimed, err := restarted.ClaimDue(context.Background(), head.Binding, fixture.clock, 1); err != nil || len(claimed) != 1 || claimed[0].ID != replacement.ID {
		t.Fatalf("active Service ClaimDue = (%+v, %v)", claimed, err)
	}
}

func TestRestartRejectsInvalidExecutableTopologyAndPriorClaimant(t *testing.T) {
	t.Run("waiting without executable wake", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		wake.Status = wakeStatusConsumed
		setWakeForTest(t, fixture.store, wake)
		before := snapshotD2Rows(t, fixture.store, created.Task)
		if _, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("ActivateWorld error = %v, want invalid_task_spec", err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})

	t.Run("claimant differs from prior active instance", func(t *testing.T) {
		fixture, created, wake, clock := claimedWakeForRestart(t, wakeStatusClaimed)
		wake.ClaimedBy = "runtime-rogue"
		setWakeForTest(t, fixture.store, wake)
		before := snapshotD2Rows(t, fixture.store, created.Task)
		if _, err := deterministicRestartService(fixture.store).ActivateWorld(context.Background(), fixture.world, "run-a", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("ActivateWorld error = %v, want invalid_task_spec", err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})
}

func claimedWakeForRestart(t *testing.T, status string) (createFixture, CreateResult, Wake, Clock) {
	t.Helper()
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "restart-"+status, "actor", 200, 300)
	clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
	claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
	}
	if status == wakeStatusEnqueued {
		if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
			t.Fatal(err)
		}
		claimed[0].Status = wakeStatusEnqueued
	}
	return fixture, created, claimed[0], clock
}

func runningWakeForRestart(t *testing.T, now int64) (createFixture, CreateResult, Wake, ExecutionContext, Record, Clock) {
	t.Helper()
	fixture, created, wake, clock := readyEnqueuedWake(t, 200, 300, now)
	exec, running, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, wake.ID, wake.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, created, wake, exec, running, clock
}

func deterministicRestartService(store *SQLiteStore) *Service {
	svc := NewService(store)
	var counter atomic.Uint64
	svc.newID = func(prefix string) string { return fmt.Sprintf("%s_restart_%d", prefix, counter.Add(1)) }
	svc.nowUnixMS = func() int64 { return 1_700_000_000_123 }
	return svc
}

type d2Rows struct{ task, wakes, history []byte }

func snapshotD2Rows(t *testing.T, store *SQLiteStore, record Record) d2Rows {
	t.Helper()
	task, wakes, history := snapshotIntentRows(t, store, record.Owner, record.ID)
	return d2Rows{task: task, wakes: wakes, history: history}
}

func assertD2Rows(t *testing.T, store *SQLiteStore, record Record, want d2Rows) {
	t.Helper()
	assertTaskSnapshotEqual(t, store, record, want.task, want.wakes, want.history, "rollback")
}

func assertTaskSnapshotEqual(t *testing.T, store *SQLiteStore, record Record, task, wakes, history []byte, label string) {
	t.Helper()
	gotTask, gotWakes, gotHistory := snapshotIntentRows(t, store, record.Owner, record.ID)
	if !bytes.Equal(gotTask, task) || !bytes.Equal(gotWakes, wakes) || !bytes.Equal(gotHistory, history) {
		t.Fatalf("%s changed durable rows", label)
	}
}

func storeD2TaskForTest(t *testing.T, store *SQLiteStore, record Record) {
	t.Helper()
	raw := mustJSONBytes(t, record)
	if _, err := store.db.Exec(`UPDATE tasks SET state = ?, revision = ?, clock_id = ?, next_wake_at = ?, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		string(record.State), int64(record.Revision), record.Spec.ClockID, nullableTick(record.NextWakeAt), raw,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID); err != nil {
		t.Fatal(err)
	}
}

func insertD2WakeRaw(t *testing.T, db *sql.DB, clockID string, wake Wake) {
	t.Helper()
	raw, err := json.Marshal(wake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		clockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
		wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS, raw); err != nil {
		t.Fatal(err)
	}
}

func rawWorldHeadJSONForRestart(t *testing.T, store *SQLiteStore, world WorldKey) []byte {
	t.Helper()
	var raw []byte
	if err := store.db.QueryRow(`SELECT head_json FROM task_world_heads WHERE game_id = ? AND world_id = ?`,
		world.GameID, world.WorldID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), raw...)
}

func runtimeInstanceIDForRestart(t *testing.T, store *SQLiteStore, world WorldKey) string {
	t.Helper()
	var instance string
	if err := store.db.QueryRow(`SELECT runtime_instance_id FROM task_world_heads WHERE game_id = ? AND world_id = ?`,
		world.GameID, world.WorldID).Scan(&instance); err != nil {
		t.Fatal(err)
	}
	return instance
}
