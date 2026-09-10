package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestExecutionLaneRunsTasksFIFO(t *testing.T) {
	lane, err := NewExecutionLane(context.Background(), 2)
	if err != nil {
		t.Fatalf("NewExecutionLane returned error: %v", err)
	}
	defer lane.Close()

	events := make(chan string, 3)
	releaseFirst := make(chan struct{})

	mustEnqueue(t, lane, Task{
		ID: "first",
		Run: func(ctx context.Context) {
			events <- "first:start"
			<-releaseFirst
			events <- "first:end"
		},
	})
	mustEnqueue(t, lane, Task{
		ID: "second",
		Run: func(ctx context.Context) {
			events <- "second"
		},
	})

	if got := receiveString(t, events); got != "first:start" {
		t.Fatalf("first event = %q, want first:start", got)
	}

	close(releaseFirst)

	if got := receiveString(t, events); got != "first:end" {
		t.Fatalf("second event = %q, want first:end", got)
	}
	if got := receiveString(t, events); got != "second" {
		t.Fatalf("third event = %q, want second", got)
	}
}

func TestExecutionLaneRejectsWhenQueueIsFull(t *testing.T) {
	lane, err := NewExecutionLane(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewExecutionLane returned error: %v", err)
	}
	defer lane.Close()

	admitted := make(chan struct{})
	mustEnqueue(t, lane, Task{ID: "active", Admitted: admitted})
	mustEventuallyEnqueue(t, lane, Task{ID: "queued"})

	err = lane.Enqueue(Task{ID: "overflow"})
	if !errors.Is(err, ErrLaneFull) {
		t.Fatalf("Enqueue overflow error = %v, want %v", err, ErrLaneFull)
	}
}

func TestExecutionLaneCloseAbortsTasksThatHaveNotStarted(t *testing.T) {
	lane, err := NewExecutionLane(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewExecutionLane returned error: %v", err)
	}

	aborted := make(chan string, 2)
	admitted := make(chan struct{})

	mustEnqueue(t, lane, Task{
		ID:       "dequeued",
		Admitted: admitted,
		Abort: func(reason AbortReason) {
			aborted <- "dequeued:" + string(reason)
		},
	})
	mustEventuallyEnqueue(t, lane, Task{
		ID: "queued",
		Abort: func(reason AbortReason) {
			aborted <- "queued:" + string(reason)
		},
	})

	lane.Close()
	<-lane.Done()

	got := []string{
		receiveString(t, aborted),
		receiveString(t, aborted),
	}
	sort.Strings(got)

	want := []string{
		"dequeued:connection_closed",
		"queued:connection_closed",
	}
	if !equalStrings(got, want) {
		t.Fatalf("aborted tasks = %#v, want %#v", got, want)
	}
}

func TestExecutionLaneDoesNotStartQueuedTaskAfterClose(t *testing.T) {
	for i := 0; i < 64; i++ {
		lane, err := NewExecutionLane(context.Background(), 1)
		if err != nil {
			t.Fatalf("NewExecutionLane returned error: %v", err)
		}

		activeStarted := make(chan struct{})
		releaseActive := make(chan struct{})
		mustEnqueue(t, lane, Task{
			ID: "active",
			Run: func(ctx context.Context) {
				close(activeStarted)
				<-releaseActive
			},
		})
		<-activeStarted

		queuedRan := make(chan struct{}, 1)
		queuedAborted := make(chan AbortReason, 1)
		mustEnqueue(t, lane, Task{
			ID: "queued",
			Run: func(ctx context.Context) {
				queuedRan <- struct{}{}
			},
			Abort: func(reason AbortReason) {
				queuedAborted <- reason
			},
		})

		lane.Close()
		close(releaseActive)
		<-lane.Done()

		select {
		case <-queuedRan:
			t.Fatalf("queued task ran after Close in trial %d", i)
		default:
		}

		select {
		case reason := <-queuedAborted:
			if reason != AbortReasonConnectionClosed {
				t.Fatalf("queued abort reason = %q, want %q", reason, AbortReasonConnectionClosed)
			}
		default:
			t.Fatalf("queued task was not aborted in trial %d", i)
		}
	}
}

func TestExecutionLaneRejectsEnqueueAfterClose(t *testing.T) {
	lane, err := NewExecutionLane(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewExecutionLane returned error: %v", err)
	}

	lane.Close()
	<-lane.Done()

	err = lane.Enqueue(Task{ID: "late"})
	if !errors.Is(err, ErrLaneClosed) {
		t.Fatalf("Enqueue after Close error = %v, want %v", err, ErrLaneClosed)
	}
}

func TestExecutionLaneConcurrentEnqueueAndCloseSettlesEveryAcceptedTask(t *testing.T) {
	const taskCount = 64

	lane, err := NewExecutionLane(context.Background(), taskCount+1)
	if err != nil {
		t.Fatalf("NewExecutionLane returned error: %v", err)
	}

	handled := make(chan string, taskCount+1)
	admitted := make(chan struct{})
	mustEnqueue(t, lane, Task{
		ID:       "active",
		Admitted: admitted,
		Abort: func(reason AbortReason) {
			handled <- "active:" + string(reason)
		},
	})

	start := make(chan struct{})
	results := make(chan error, taskCount)
	var wg sync.WaitGroup

	for i := 0; i < taskCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id := fmt.Sprintf("task_%d", i)
			results <- lane.Enqueue(Task{
				ID: id,
				Run: func(ctx context.Context) {
					handled <- "run:" + id
				},
				Abort: func(reason AbortReason) {
					handled <- "abort:" + id + ":" + string(reason)
				},
			})
		}()
	}

	closeDone := make(chan struct{})
	go func() {
		<-start
		lane.Close()
		close(closeDone)
	}()

	close(start)
	wg.Wait()
	<-closeDone
	<-lane.Done()
	close(results)

	accepted := 1 // active task was accepted before the concurrent race.
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrLaneClosed):
		default:
			t.Fatalf("concurrent Enqueue returned unexpected error: %v", err)
		}
	}

	if got := len(handled); got != accepted {
		t.Fatalf("handled accepted tasks = %d, want %d", got, accepted)
	}
}

func TestLaneStoreGetOrCreateIsStablePerAgentSessionKey(t *testing.T) {
	store, err := NewLaneStore(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewLaneStore returned error: %v", err)
	}
	defer store.Close()

	key := mustResolve(t, "stardew-valley", "Farm_123", "npc:Linus")
	sameKey := mustResolve(t, "stardew-valley", "Farm_123", "npc:Linus")
	otherKey := mustResolve(t, "stardew-valley", "Farm_123", "npc:Abigail")

	lane1, err := store.GetOrCreate(key)
	if err != nil {
		t.Fatalf("GetOrCreate returned error: %v", err)
	}
	lane2, err := store.GetOrCreate(sameKey)
	if err != nil {
		t.Fatalf("GetOrCreate returned error: %v", err)
	}
	lane3, err := store.GetOrCreate(otherKey)
	if err != nil {
		t.Fatalf("GetOrCreate returned error: %v", err)
	}

	if lane1 != lane2 {
		t.Fatal("expected same key to return same lane")
	}
	if lane1 == lane3 {
		t.Fatal("expected different key to return different lane")
	}
}

func TestLaneStoreCloseClosesExistingLanesAndStopsCreation(t *testing.T) {
	store, err := NewLaneStore(context.Background(), 1)
	if err != nil {
		t.Fatalf("NewLaneStore returned error: %v", err)
	}

	key := mustResolve(t, "stardew-valley", "Farm_123", "npc:Linus")
	lane, err := store.GetOrCreate(key)
	if err != nil {
		t.Fatalf("GetOrCreate returned error: %v", err)
	}

	store.Close()
	<-lane.Done()

	_, err = store.GetOrCreate(key)
	if !errors.Is(err, ErrLaneClosed) {
		t.Fatalf("GetOrCreate after Close error = %v, want %v", err, ErrLaneClosed)
	}
}

func TestLaneStoreCloseAndWaitCancelsAllLanesBeforeWaiting(t *testing.T) {
	store, err := NewLaneStore(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	for _, entity := range []string{"entity:a", "entity:b"} {
		lane, err := store.GetOrCreate(mustResolve(t, "game", "world", entity))
		if err != nil {
			t.Fatal(err)
		}
		mustEnqueue(t, lane, Task{Run: func(ctx context.Context) {
			started <- struct{}{}
			<-ctx.Done()
			cancelled <- struct{}{}
			<-release
		}})
	}
	for i := 0; i < 2; i++ {
		<-started
	}
	done := make(chan struct{})
	go func() { store.CloseAndWait(); close(done) }()
	for i := 0; i < 2; i++ {
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("shutdown did not cancel every lane")
		}
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before task cleanup")
	default:
	}
	// Each task must complete its final work before shutdown returns.
	release <- struct{}{}
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after task cleanup")
	}
	store.CloseAndWait()
}

func mustEnqueue(t *testing.T, lane *ExecutionLane, task Task) {
	t.Helper()

	if err := lane.Enqueue(task); err != nil {
		t.Fatalf("Enqueue(%q) returned error: %v", task.ID, err)
	}
}

func mustEventuallyEnqueue(t *testing.T, lane *ExecutionLane, task Task) {
	t.Helper()

	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	for {
		err := lane.Enqueue(task)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrLaneFull) {
			t.Fatalf("Enqueue(%q) returned error: %v", task.ID, err)
		}

		select {
		case <-deadline:
			t.Fatalf("timed out waiting to enqueue %q", task.ID)
		case <-ticker.C:
		}
	}
}

func receiveString(t *testing.T, ch <-chan string) string {
	t.Helper()

	select {
	case got := <-ch:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		return ""
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
