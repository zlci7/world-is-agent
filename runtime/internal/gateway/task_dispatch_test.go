package gateway

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

type dispatchFaults struct {
	*task.Service
	db      *sql.DB
	mark    func(context.Context, task.Binding, string, string) error
	release func(context.Context, task.Binding, string, string, time.Time) error
	begin   func(context.Context, task.Binding, string, string) (task.ExecutionContext, task.Record, error)
	inspect func(context.Context, task.Binding, string) (task.WakeInspection, error)
}

func (f *dispatchFaults) MarkEnqueued(c context.Context, b task.Binding, w, k string) error {
	if f.mark != nil {
		return f.mark(c, b, w, k)
	}
	return f.Service.MarkEnqueued(c, b, w, k)
}
func (f *dispatchFaults) ReleaseClaim(c context.Context, b task.Binding, w, k string, at time.Time) error {
	if f.release != nil {
		return f.release(c, b, w, k, at)
	}
	return f.Service.ReleaseClaim(c, b, w, k, at)
}
func (f *dispatchFaults) BeginWake(c context.Context, b task.Binding, w, k string) (task.ExecutionContext, task.Record, error) {
	if f.begin != nil {
		return f.begin(c, b, w, k)
	}
	return f.Service.BeginWake(c, b, w, k)
}
func (f *dispatchFaults) InspectWake(c context.Context, b task.Binding, w string) (task.WakeInspection, error) {
	if f.inspect != nil {
		return f.inspect(c, b, w)
	}
	return f.Service.InspectWake(c, b, w)
}

func dispatchFixture(t *testing.T) (*Server, *worldTestStream, *dispatchFaults, WorldEntry, task.Wake) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := task.NewService(store)
	server := NewServer(nil, WithTaskService(svc))
	t.Cleanup(func() { server.Close(context.Background()) })
	stream, _ := taskHandshake(t, server)
	bindTestWorld(t, stream, worldRequest())
	entry, _ := server.worlds.Current(task.WorldKey{GameID: "sim", WorldID: "world"})
	source := task.SourceRef{Kind: task.SourceKindInteraction, EventID: "event", TurnID: "turn", CallID: "call"}
	exec := task.ExecutionContext{Owner: session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}, Binding: entry.Head.Binding, Clock: entry.Head.Clock, Source: source}
	_, err = svc.Create(context.Background(), exec, task.TaskSpec{Instruction: "Inspect generic world", ClockID: "game", WakeAt: 11, DeadlineAt: 100, ResultContract: task.ResultContractAuthoritativeEvidence, Source: source}, task.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	clock := task.Clock{ID: "game", Tick: 11, Sequence: 2}
	head, err := svc.UpdateClock(context.Background(), entry.Head.Binding, clock)
	if err != nil {
		t.Fatal(err)
	}
	slot := server.worlds.slot(entry.Head.Binding.World, false)
	slot.mu.Lock()
	slot.head = head
	slot.mu.Unlock()
	entry.Head = head
	wakes, err := svc.ClaimDue(context.Background(), head.Binding, clock, 1)
	if err != nil || len(wakes) != 1 {
		t.Fatal(wakes, err)
	}
	return server, stream, &dispatchFaults{Service: svc, db: db}, entry, wakes[0]
}
func dispatchConfig() task.DispatcherConfig {
	return task.DispatcherConfig{ScanInterval: time.Hour, BatchSize: 4, RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond}
}
func waitDispatch(t *testing.T, ch <-chan task.Record) task.Record {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not finish")
		return task.Record{}
	}
}

func TestTaskDispatchBeginWakeCommittedButUnconfirmed(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	var begins, models atomic.Int32
	reconciled := make(chan task.Record, 1)
	svc.begin = func(ctx context.Context, b task.Binding, w, k string) (task.ExecutionContext, task.Record, error) {
		begins.Add(1)
		_, _, err := svc.Service.BeginWake(ctx, b, w, k)
		if err != nil {
			return task.ExecutionContext{}, task.Record{}, err
		}
		return task.ExecutionContext{}, task.Record{}, task.WrapError(task.CodeTaskConflict, errors.New("response lost"))
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error { models.Add(1); return nil }, func(_ context.Context, _ task.ExecutionContext, r task.Record) error { reconciled <- r; return nil })
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	record := waitDispatch(t, reconciled)
	if record.Revision != 2 || record.State != task.StateRunning || begins.Load() != 1 || models.Load() != 0 {
		t.Fatalf("record=%+v begin=%d model=%d", record, begins.Load(), models.Load())
	}
	got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
	if err != nil || got.Task.Revision != 2 || got.Wake.Status != "running" {
		t.Fatal(got, err)
	}
}

func TestTaskDispatchBeginWakeRollbackThenRecovers(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	if _, err := svc.db.Exec(`CREATE TRIGGER fail_begin AFTER UPDATE OF revision ON tasks BEGIN SELECT RAISE(ABORT,'rollback before commit'); END`); err != nil {
		t.Fatal(err)
	}
	var begins atomic.Int32
	called := make(chan task.Record, 1)
	svc.begin = func(ctx context.Context, b task.Binding, w, k string) (task.ExecutionContext, task.Record, error) {
		if begins.Add(1) == 1 {
			exec, record, err := svc.Service.BeginWake(ctx, b, w, k)
			if err == nil {
				t.Error("SQLite trigger did not roll back")
			}
			inspection, inspectErr := svc.Service.InspectWake(ctx, b, w)
			if inspectErr != nil || inspection.Wake.Status != "enqueued" || inspection.Task.Revision != 1 {
				t.Error("partial BeginWake commit", inspection, inspectErr)
			}
			if _, e := svc.db.Exec(`DROP TRIGGER fail_begin`); e != nil {
				t.Error(e)
			}
			return exec, record, err
		}
		return svc.Service.BeginWake(ctx, b, w, k)
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(_ context.Context, _ task.ExecutionContext, r task.Record) error { called <- r; return nil }, func(context.Context, task.ExecutionContext, task.Record) error {
		t.Error("rollback masqueraded as committed")
		return nil
	})
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	if r := waitDispatch(t, called); r.Revision != 2 || begins.Load() != 2 {
		t.Fatal(r, begins.Load())
	}
}

func TestTaskDispatchQueueAndMarkFailuresReleaseClaim(t *testing.T) {
	for _, mode := range []string{"full", "closed", "mark", "release_transient", "closed_release_transient"} {
		t.Run(mode, func(t *testing.T) {
			server, _, svc, entry, w := dispatchFixture(t)
			lane, err := entry.Lanes.GetOrCreate(w.Owner)
			if err != nil {
				t.Fatal(err)
			}
			blocker := make(chan struct{})
			defer close(blocker)
			if mode == "full" || mode == "release_transient" {
				started := make(chan struct{})
				if err := lane.Enqueue(session.Task{Run: func(ctx context.Context) {
					close(started)
					select {
					case <-blocker:
					case <-ctx.Done():
					}
				}}); err != nil {
					t.Fatal(err)
				}
				<-started
				if err := lane.Enqueue(session.Task{}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "closed" || mode == "closed_release_transient" {
				lane.Close()
				<-lane.Done()
			}
			if mode == "mark" {
				svc.mark = func(context.Context, task.Binding, string, string) error { return task.ErrInvalidTaskSpec }
			}
			var releases atomic.Int32
			if mode == "release_transient" || mode == "closed_release_transient" {
				svc.release = func(ctx context.Context, b task.Binding, w, k string, at time.Time) error {
					if releases.Add(1) == 1 {
						return task.WrapError(task.CodeTaskConflict, errors.New("busy"))
					}
					return svc.Service.ReleaseClaim(ctx, b, w, k, at)
				}
			}
			d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error {
				t.Error("failed admission executed")
				return nil
			}, nil)
			if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
			if err != nil || got.Wake.Status != "pending" || got.Wake.ClaimID != "" || got.Wake.RetryAfterUnixMS <= 0 || got.Task.Revision != 1 {
				t.Fatal(got, err)
			}
		})
	}
}

func TestTaskDispatchInspectedRunningNeverBeginsAgain(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	var begins atomic.Int32
	reconciled := make(chan task.Record, 1)
	svc.mark = func(ctx context.Context, b task.Binding, id, claim string) error {
		if err := svc.Service.MarkEnqueued(ctx, b, id, claim); err != nil {
			return err
		}
		if _, _, err := svc.Service.BeginWake(ctx, b, id, claim); err != nil {
			return err
		}
		return task.WrapError(task.CodeTaskConflict, errors.New("confirmation lost"))
	}
	svc.begin = func(ctx context.Context, b task.Binding, id, claim string) (task.ExecutionContext, task.Record, error) {
		begins.Add(1)
		return svc.Service.BeginWake(ctx, b, id, claim)
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error { t.Error("running replay"); return nil }, func(_ context.Context, _ task.ExecutionContext, r task.Record) error { reconciled <- r; return nil })
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	if r := waitDispatch(t, reconciled); r.Revision != 2 || begins.Load() != 0 {
		t.Fatal("inspected running was begun again", r.Revision, begins.Load())
	}
}

func TestTaskDispatchExhaustionAndRebind(t *testing.T) {
	server, stream, svc, entry, w := dispatchFixture(t)
	var calls atomic.Int32
	svc.begin = func(context.Context, task.Binding, string, string) (task.ExecutionContext, task.Record, error) {
		calls.Add(1)
		return task.ExecutionContext{}, task.Record{}, task.WrapError(task.CodeTaskConflict, errors.New("busy"))
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error {
		t.Error("technical retry called handler")
		return nil
	}, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, ok := server.worlds.Current(entry.Head.Binding.World)
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("admission remained open")
		}
		time.Sleep(time.Millisecond)
	}
	lane, _ := entry.Lanes.GetOrCreate(w.Owner)
	lane.Close()
	<-lane.Done()
	if calls.Load() != 3 {
		t.Fatal("attempts", calls.Load())
	}
	got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
	if err != nil || got.Head.Status != "paused" || got.Task.Revision != 1 {
		t.Fatal(got, err)
	}
	request := worldRequest()
	request.Clock.NowTick, request.Clock.Sequence = 11, 2
	ready := bindTestWorld(t, stream, request)
	if ready.Status != "ready" || ready.Scope.ExecutionGeneration != 2 {
		t.Fatal(ready)
	}
	head := server.worlds.ReadyWorlds()[0]
	wakes, err := svc.Service.ClaimDue(context.Background(), head.Binding, head.Clock, 1)
	if err != nil || len(wakes) != 1 {
		t.Fatal(wakes, err)
	}
}

func TestTaskDispatchInspectionBecomesStale(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	var begins, inspections, handlers atomic.Int32
	done := make(chan struct{})
	svc.begin = func(ctx context.Context, b task.Binding, id, claim string) (task.ExecutionContext, task.Record, error) {
		n := begins.Add(1)
		if n == 1 {
			return task.ExecutionContext{}, task.Record{}, task.WrapError(task.CodeTaskConflict, errors.New("rollback"))
		}
		exec, record, err := svc.Service.BeginWake(ctx, b, id, claim)
		if n == 2 {
			if !errors.Is(err, task.ErrTaskChanged) {
				t.Errorf("stale continuation = %v", err)
			}
			close(done)
		}
		return exec, record, err
	}
	svc.inspect = func(ctx context.Context, b task.Binding, id string) (task.WakeInspection, error) {
		inspection, err := svc.Service.InspectWake(ctx, b, id)
		if err == nil && inspections.Add(1) == 1 {
			exec := task.ExecutionContext{Owner: w.Owner, Binding: b, Clock: entry.Head.Clock, TaskID: w.TaskID, WakeID: id, ExpectedRevision: 1, Source: task.SourceRef{Kind: task.SourceKindInternal, EventID: "change", TurnID: "change", CallID: "change"}}
			if _, e := svc.Service.ApplyIntent(ctx, exec, task.Intent{Kind: "cancel", Reason: "external_change"}); e != nil {
				t.Error(e)
			}
		}
		return inspection, err
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error { handlers.Add(1); return nil }, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old continuation not CAS checked")
	}
	settled := make(chan struct{})
	lane, _ := entry.Lanes.GetOrCreate(w.Owner)
	if err := lane.Enqueue(session.Task{Run: func(context.Context) { close(settled) }}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("reinspection did not finish delivery")
	}
	if inspections.Load() != 2 {
		t.Fatal("stale continuation did not reinspect current state", inspections.Load())
	}
	if handlers.Load() != 0 {
		t.Fatal("stale inspection executed handler")
	}
}

func TestTaskDispatchInvalidationAndAbort(t *testing.T) {
	for _, mode := range []string{"abort", "disconnect", "save", "stale"} {
		t.Run(mode, func(t *testing.T) {
			server, stream, svc, entry, w := dispatchFixture(t)
			lane, _ := entry.Lanes.GetOrCreate(w.Owner)
			started, unblock := make(chan struct{}), make(chan struct{})
			if err := lane.Enqueue(session.Task{Run: func(ctx context.Context) {
				close(started)
				select {
				case <-ctx.Done():
				case <-unblock:
				}
			}}); err != nil {
				t.Fatal(err)
			}
			<-started
			var called atomic.Int32
			d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error { called.Add(1); return nil }, nil)
			if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "abort":
				lane.Close()
				<-lane.Done()
			case "disconnect":
				close(stream.incoming)
				deadline := time.Now().Add(time.Second)
				for {
					_, ok := server.worlds.Current(entry.Head.Binding.World)
					if !ok {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("disconnect not fenced")
					}
					time.Sleep(time.Millisecond)
				}
			case "save":
				if err := server.worlds.SetSaveBarrier(entry.Environment.(*streamEnvironment), entry.Head.Binding, true); err != nil {
					t.Fatal(err)
				}
			case "stale":
				slot := server.worlds.slot(entry.Head.Binding.World, false)
				slot.mu.Lock()
				slot.authorityEpoch++
				slot.mu.Unlock()
			}
			close(unblock)
			lane.Close()
			<-lane.Done()
			if called.Load() != 0 {
				t.Fatal("invalidated item executed")
			}
			got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Task.Revision != 1 || got.Wake.Status != "enqueued" {
				t.Fatal(got)
			}
			if mode == "abort" && got.Head.Status != "paused" {
				t.Fatal("standalone abort did not enter binding recovery")
			}
			if mode == "save" {
				if err := server.worlds.SetSaveBarrier(entry.Environment.(*streamEnvironment), entry.Head.Binding, false); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestTaskDispatchInspectExhaustionClosesAdmission(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	var inspections atomic.Int32
	svc.mark = func(context.Context, task.Binding, string, string) error {
		return task.WrapError(task.CodeTaskConflict, errors.New("uncertain"))
	}
	svc.inspect = func(context.Context, task.Binding, string) (task.WakeInspection, error) {
		inspections.Add(1)
		return task.WakeInspection{}, task.WrapError(task.CodeTaskConflict, errors.New("unavailable"))
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error {
		t.Error("inspection retry executed")
		return nil
	}, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.worlds.Current(entry.Head.Binding.World); ok {
		t.Fatal("admission open after unavailable inspection")
	}
	if inspections.Load() != 2 {
		t.Fatal("technical failure budget", inspections.Load())
	}
	got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
	if err != nil || got.Head.Status != "paused" || got.Wake.Status != "claimed" {
		t.Fatal(got, err)
	}
}

func TestTaskDispatchMarkCommittedButUnconfirmed(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	var marks atomic.Int32
	called := make(chan task.Record, 1)
	svc.mark = func(ctx context.Context, b task.Binding, id, claim string) error {
		marks.Add(1)
		if err := svc.Service.MarkEnqueued(ctx, b, id, claim); err != nil {
			return err
		}
		return task.WrapError(task.CodeTaskConflict, errors.New("lost confirmation"))
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(_ context.Context, _ task.ExecutionContext, r task.Record) error { called <- r; return nil }, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	if r := waitDispatch(t, called); r.Revision != 2 || marks.Load() != 1 {
		t.Fatal(r, marks.Load())
	}
}

func TestTaskDispatchClockNotificationAndShutdown(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	if err := svc.Service.ReleaseClaim(context.Background(), entry.Head.Binding, w.ID, w.ClaimID, time.Now().Add(30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	called := make(chan task.Record, 1)
	if err := server.StartTaskDispatcher(context.Background(), dispatchConfig(), func(ctx context.Context, exec task.ExecutionContext, r task.Record) error {
		called <- r
		<-ctx.Done()
		_, err := svc.Service.InspectWake(context.Background(), exec.Binding, exec.WakeID)
		if err != nil {
			t.Error("store closed before lane cleanup", err)
		}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := server.worlds.UpdateClock(context.Background(), entry.Environment.(*streamEnvironment), &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(entry.Head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 12, Sequence: 3}}); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, called)
	done := make(chan error, 1)
	go func() { done <- server.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown retained dispatcher or lane")
	}
	if len(server.worlds.ReadyWorlds()) != 0 {
		t.Fatal("ready worlds after stop")
	}
}

func TestTaskDispatchClosedActiveRetryPausesAdmission(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	started := make(chan struct{})
	svc.begin = func(context.Context, task.Binding, string, string) (task.ExecutionContext, task.Record, error) {
		close(started)
		return task.ExecutionContext{}, task.Record{}, task.WrapError(task.CodeTaskConflict, errors.New("busy"))
	}
	config := dispatchConfig()
	config.RetryMin = time.Hour
	config.RetryMax = time.Hour
	d := newTaskDispatch(server.worlds, svc, config, func(context.Context, task.ExecutionContext, task.Record) error {
		t.Error("retry invoked handler")
		return nil
	}, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	<-started
	lane, _ := entry.Lanes.GetOrCreate(w.Owner)
	lane.Close()
	<-lane.Done()
	got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
	if err != nil || got.Head.Status != "paused" || got.Wake.Status != "enqueued" || got.Task.Revision != 1 {
		t.Fatal("cancelled retry left unowned durable admission", got, err)
	}
}

func TestTaskDispatchMissingHandlerPausesAdmission(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), nil, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Head.Status == "paused" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("missing handler left wake unowned", got)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTaskDispatchCancellationAfterCommittedBeginPauses(t *testing.T) {
	server, _, svc, entry, w := dispatchFixture(t)
	lane, _ := entry.Lanes.GetOrCreate(w.Owner)
	svc.begin = func(ctx context.Context, b task.Binding, id, claim string) (task.ExecutionContext, task.Record, error) {
		exec, record, err := svc.Service.BeginWake(ctx, b, id, claim)
		lane.Close()
		return exec, record, err
	}
	d := newTaskDispatch(server.worlds, svc, dispatchConfig(), func(context.Context, task.ExecutionContext, task.Record) error {
		t.Error("cancelled delivery invoked handler")
		return nil
	}, nil)
	if err := d.EnqueueWake(context.Background(), entry.Head.Binding, w); err != nil {
		t.Fatal(err)
	}
	<-lane.Done()
	got, err := svc.Service.InspectWake(context.Background(), entry.Head.Binding, w.ID)
	if err != nil || got.Head.Status != "paused" || got.Wake.Status != "running" || got.Task.Revision != 2 {
		t.Fatal(got, err)
	}
}
