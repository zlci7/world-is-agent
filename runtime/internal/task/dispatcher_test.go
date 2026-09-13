package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatcherReadyNotificationPeriodicAndStop(t *testing.T) {
	f, _, _ := newIntentFixture(t, StoreOptions{})
	clock := Clock{ID: f.clock.ID, Tick: 200, Sequence: 2}
	head, err := f.svc.UpdateClock(context.Background(), f.head.Binding, clock)
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	var scans atomic.Int32
	delivered := make(chan Wake, 2)
	d, err := NewDispatcher(context.Background(), f.svc, DispatcherConfig{ScanInterval: 10 * time.Millisecond, BatchSize: 1, RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond}, DispatcherCallbacks{
		Guard: func(_ Binding, fn func(Head) error) error { return fn(head) },
		ReadyWorlds: func() []Head {
			scans.Add(1)
			if ready.Load() {
				return []Head{head}
			}
			return nil
		},
		EnqueueWake: func(ctx context.Context, b Binding, w Wake) error {
			delivered <- w
			return f.svc.MarkEnqueued(ctx, b, w.ID, w.ClaimID)
		},
		PauseWorld: func(context.Context, Binding, Wake, error) { t.Error("unexpected pause") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	time.Sleep(25 * time.Millisecond)
	if scans.Load() < 2 {
		t.Fatal("periodic scans missing")
	}
	select {
	case <-delivered:
		t.Fatal("claimed unready world")
	default:
	}
	ready.Store(true)
	d.Notify()
	select {
	case w := <-delivered:
		if w.Status != "claimed" || w.Attempt != 1 {
			t.Fatal(w)
		}
	case <-time.After(time.Second):
		t.Fatal("notification did not dispatch")
	}
	d.Stop()
	count := scans.Load()
	d.Notify()
	time.Sleep(20 * time.Millisecond)
	if scans.Load() != count {
		t.Fatal("scan after Stop")
	}
}

func TestDispatcherStopCancelsActiveRetry(t *testing.T) {
	f, _, _ := newIntentFixture(t, StoreOptions{})
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	paused := make(chan struct{}, 1)
	d, err := NewDispatcher(context.Background(), f.svc, DispatcherConfig{ScanInterval: time.Hour, BatchSize: 1, RetryMin: time.Hour, RetryMax: time.Hour}, DispatcherCallbacks{
		Guard:       func(_ Binding, fn func(Head) error) error { return fn(f.head) },
		ReadyWorlds: func() []Head { return []Head{f.head} }, EnqueueWake: func(context.Context, Binding, Wake) error { t.Error("unexpected dispatch"); return nil },
		PauseWorld: func(context.Context, Binding, Wake, error) { paused <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry timer survives stop")
	}
}

func TestDispatcherClockAdvanceAfterReadySnapshot(t *testing.T) {
	f, _, _ := newIntentFixture(t, StoreOptions{})
	stale := f.head
	current, err := f.svc.UpdateClock(context.Background(), f.head.Binding, Clock{ID: f.clock.ID, Tick: 200, Sequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	delivered, paused := make(chan Wake, 2), make(chan error, 1)
	d, err := NewDispatcher(context.Background(), f.svc, DispatcherConfig{ScanInterval: time.Hour, BatchSize: 1, RetryMin: time.Millisecond, RetryMax: time.Millisecond}, DispatcherCallbacks{
		Guard:       func(_ Binding, fn func(Head) error) error { return fn(current) },
		ReadyWorlds: func() []Head { return []Head{stale} },
		EnqueueWake: func(ctx context.Context, b Binding, w Wake) error {
			delivered <- w
			return f.svc.MarkEnqueued(ctx, b, w.ID, w.ClaimID)
		},
		PauseWorld: func(_ context.Context, _ Binding, _ Wake, err error) { paused <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	select {
	case err := <-paused:
		t.Fatalf("normal authoritative advance was paused: %v", err)
	case w := <-delivered:
		if w.Attempt != 1 {
			t.Fatalf("duplicate claim: %+v", w)
		}
	case <-time.After(time.Second):
		t.Fatal("stale scan did not claim current due wake")
	}
}
