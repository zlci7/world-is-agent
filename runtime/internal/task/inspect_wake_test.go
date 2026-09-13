package task

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestInspectWakeStatesDetachedAndReadOnly(t *testing.T) {
	f, created, wake := newIntentFixture(t, StoreOptions{})
	ctx := context.Background()
	inspect := func(status string, revision uint64) {
		t.Helper()
		var beforeChanges, afterChanges int64
		if err := f.store.db.QueryRow(`SELECT total_changes()`).Scan(&beforeChanges); err != nil {
			t.Fatal(err)
		}
		beforeTask, beforeWake := rawTaskJSON(t, f.store, created.Task), rawOnlyWakeJSON(t, f.store, created.Task)
		got, err := f.svc.InspectWake(ctx, f.head.Binding, wake.ID)
		if err != nil || got.Wake.Status != status || got.Task.Revision != revision || got.Head.Binding != f.head.Binding {
			t.Fatalf("inspection = %+v, %v", got, err)
		}
		if status == "claimed" || status == "enqueued" || status == "running" {
			if got.Wake.ClaimID != wake.ClaimID || got.Wake.ClaimedBy != wake.ClaimedBy {
				t.Fatal("claim identity lost")
			}
		}
		if got.Task.NextWakeAt != nil {
			*got.Task.NextWakeAt = 999
		}
		if len(got.Task.Spec.Source.Facts) > 0 {
			got.Task.Spec.Source.Facts[0] = 'x'
		}
		again, err := f.svc.InspectWake(ctx, f.head.Binding, wake.ID)
		if err != nil || again.Task.Revision != revision {
			t.Fatal(again, err)
		}
		if err := f.store.db.QueryRow(`SELECT total_changes()`).Scan(&afterChanges); err != nil {
			t.Fatal(err)
		}
		if beforeChanges != afterChanges {
			t.Fatal("inspection performed database writes")
		}
		if !reflect.DeepEqual(beforeTask, rawTaskJSON(t, f.store, created.Task)) || !reflect.DeepEqual(beforeWake, rawOnlyWakeJSON(t, f.store, created.Task)) {
			t.Fatal("inspection wrote durable state")
		}
	}
	inspect("pending", 1)
	clock := Clock{ID: f.clock.ID, Tick: created.Task.Spec.WakeAt, Sequence: 2}
	if _, err := f.svc.UpdateClock(ctx, f.head.Binding, clock); err != nil {
		t.Fatal(err)
	}
	wakes, err := f.svc.ClaimDue(ctx, f.head.Binding, clock, 1)
	if err != nil || len(wakes) != 1 {
		t.Fatal(wakes, err)
	}
	wake = wakes[0]
	inspect("claimed", 1)
	if err := f.svc.MarkEnqueued(ctx, f.head.Binding, wake.ID, wake.ClaimID); err != nil {
		t.Fatal(err)
	}
	inspect("enqueued", 1)
	_, record, err := f.svc.BeginWake(ctx, f.head.Binding, wake.ID, wake.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	inspect("running", 2)
	exec := intentExecution(f, record, wake.ID, record.Revision, "consume")
	exec.Clock = clock
	if _, err := f.svc.ApplyIntent(ctx, exec, Intent{Kind: "cancel", Reason: "done"}); err != nil {
		t.Fatal(err)
	}
	inspect("consumed", 3)
}

func TestInspectWakeDoesNotExpireSaveBarrier(t *testing.T) {
	f, created, wake := newIntentFixture(t, StoreOptions{})
	now := f.svc.nowUnixMS()
	prepared, err := f.svc.PrepareCheckpoint(context.Background(), f.head.Binding, f.clock, "inspection-save", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.nowUnixMS = func() int64 { return now + checkpointBarrierTimeoutMS + 1 }
	before, err := f.store.loadWorldHead(context.Background(), f.world)
	if err != nil {
		t.Fatal(err)
	}
	beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, f.store, created.Task.Owner, created.Task.ID)
	if _, err := f.svc.InspectWake(context.Background(), prepared.Head.Binding, wake.ID); err != nil {
		t.Fatal(err)
	}
	after, err := f.store.loadWorldHead(context.Background(), f.world)
	if err != nil || before != after || after.SaveRequestID != "inspection-save" {
		t.Fatal("inspection changed save barrier", after, err)
	}
	assertTaskSnapshotEqual(t, f.store, created.Task, beforeTask, beforeWakes, beforeHistory, "inspect")
}

func TestInspectWakeRejectsInvalidAssociations(t *testing.T) {
	for _, which := range []string{"binding", "missing_world", "missing_wake", "owner", "revision", "generation", "claim", "clock", "consumed_revision", "claimant"} {
		t.Run(which, func(t *testing.T) {
			f, _, w := newIntentFixture(t, StoreOptions{})
			binding := f.head.Binding
			id := w.ID
			want := ErrInvalidTaskSpec
			switch which {
			case "binding":
				binding.Generation++
				want = ErrGenerationStale
			case "missing_world":
				binding.World.WorldID = "missing"
				want = ErrWorldNotReady
			case "missing_wake":
				id = "missing"
				want = ErrTaskNotFound
			case "owner":
				w.Owner.EntityID = "foreign"
				setWakeForTest(t, f.store, w)
			case "revision":
				w.ExpectedRevision++
				setWakeForTest(t, f.store, w)
			case "generation":
				w.Generation++
				setWakeForTest(t, f.store, w)
			case "claim":
				w.Status = "claimed"
				setWakeForTest(t, f.store, w)
			case "clock":
				if _, err := f.store.db.Exec(`UPDATE task_wakeups SET clock_id='foreign'`); err != nil {
					t.Fatal(err)
				}
			case "consumed_revision":
				w.Status = "consumed"
				w.ExpectedRevision++
				setWakeForTest(t, f.store, w)
			case "claimant":
				w.Status = "claimed"
				w.ClaimID = "claim"
				w.ClaimedBy = "foreign-runtime"
				w.Attempt = 1
				setWakeForTest(t, f.store, w)
			}
			if _, err := f.svc.InspectWake(context.Background(), binding, id); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
}

func TestInspectWakeSnapshotConsistency(t *testing.T) {
	f, _, wake, clock := readyEnqueuedWake(t, 200, 300, 200)
	db, err := sql.Open("sqlite", taskSQLiteDSN(f.store.path, f.store.options.BusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := &SQLiteStore{db: db, options: f.store.options}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		err := writer.withImmediateTransaction(context.Background(), func(tx *sql.Tx) error {
			head, e := scanWorldHeadRow(tx.QueryRow(worldHeadSelectSQL, f.world.GameID, f.world.WorldID))
			if e != nil {
				return e
			}
			current, e := writer.findStrictWorldWakeTx(context.Background(), tx, head, wake.ID)
			if e != nil {
				return e
			}
			prepared, e := writer.prepareBeginMutation(current.record)
			if e != nil {
				return e
			}
			if e = writer.updateBeginRecordTx(context.Background(), tx, current.record.record, prepared); e != nil {
				return e
			}
			updated := current.wake
			updated.Status = "running"
			if e = writer.updateWakeCASTx(context.Background(), tx, current, updated, "running"); e != nil {
				return e
			}
			next := head
			next.Head.Clock.Tick++
			next.Head.Clock.Sequence++
			return writer.updateWorldHeadCASTx(context.Background(), tx, head, next, "snapshot")
		})
		if err != nil {
			t.Error(err)
		}
	}()
	close(start)
	for i := 0; i < 100; i++ {
		got, err := f.svc.InspectWake(context.Background(), f.head.Binding, wake.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Wake.Status == "enqueued" {
			if got.Task.Revision != 1 || got.Head.Clock != clock {
				t.Fatalf("mixed old snapshot: %+v", got)
			}
		} else if got.Wake.Status == "running" {
			if got.Task.Revision != 2 || got.Head.Clock.Tick != 201 {
				t.Fatalf("mixed new snapshot: %+v", got)
			}
		} else {
			t.Fatal(got)
		}
	}
	wg.Wait()
}
