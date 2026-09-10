package agent_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
)

type historyLaneStore struct {
	*faultHistoryStore
	acquired func(memory.HistorySnapshot)
	release  func(memory.HistorySnapshot)
}

func (s *historyLaneStore) BeginHistorySnapshot(ctx context.Context, owner session.AgentSessionKey) (memory.HistorySnapshot, error) {
	snapshot, err := s.faultHistoryStore.BeginHistorySnapshot(ctx, owner)
	if err == nil {
		s.acquired(snapshot)
	}
	return snapshot, err
}

func (s *historyLaneStore) ReleaseHistorySnapshot(snapshot memory.HistorySnapshot) {
	s.release(snapshot)
}

type historyLaneProvider struct {
	*scriptedProvider
	started func()
}

func (p *historyLaneProvider) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	p.started()
	return p.scriptedProvider.Generate(ctx, request)
}

func waitHistoryLaneSignal[T any](t *testing.T, ctx context.Context, name string, signal <-chan T) T {
	t.Helper()
	select {
	case value := <-signal:
		return value
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", name, ctx.Err())
		var zero T
		return zero
	}
}

func TestHistoryExecutionLaneWaitsForCancelledWriteDeadlineAndSnapshotRelease(t *testing.T) {
	const waitTimeout = 5 * time.Second
	watchdog, stop := context.WithTimeout(context.Background(), waitTimeout)
	defer stop()
	cfg := defaultLoopConfig(t)
	cfg.History.WriteTimeoutMS = 100
	cfg.TurnTimeout = 2 * waitTimeout
	cfg.LLMTimeout = 2 * waitTimeout
	disabled := false
	cfg.Compaction.Enabled = &disabled
	key := session.AgentSessionKey{GameID: "history-lane-test", WorldID: "temporary-world", EntityID: "npc"}
	base := memory.NewInMemoryHistoryStore(cfg.History)
	prior, err := base.AppendHistory(watchdog, memory.HistoryBatch{
		Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: "prior-turn",
		Event:    memory.HistoryEvent{ID: "prior-input", Facts: []memory.SourceContextFact{{Kind: "dialogue", Text: "Do not disclose code 7319."}}},
		Terminal: memory.HistoryTerminal{Status: "completed"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	mark := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, event)
	}
	assertOrder := func(want []string) {
		t.Helper()
		mu.Lock()
		got := append([]string(nil), order...)
		mu.Unlock()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("lane cleanup order = %v, want %v", got, want)
		}
	}
	type writeStart struct {
		contextErr  error
		hasDeadline bool
		remaining   time.Duration
		batch       memory.HistoryBatch
	}
	acquired := make(chan memory.HistorySnapshot, 1)
	providerStarted := make(chan struct{}, 1)
	writeStarted := make(chan writeStart, 1)
	writeFinished := make(chan error, 1)
	releaseStarted := make(chan memory.HistorySnapshot, 1)
	allowRelease := make(chan struct{})
	store := &historyLaneStore{faultHistoryStore: &faultHistoryStore{HistoryStore: base}}
	store.acquired = func(snapshot memory.HistorySnapshot) {
		mark("snapshot_acquired")
		select {
		case acquired <- snapshot:
		case <-watchdog.Done():
		}
	}
	store.append = func(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
		deadline, hasDeadline := ctx.Deadline()
		state := writeStart{ctx.Err(), hasDeadline, time.Until(deadline), batch}
		mark("write_started")
		select {
		case writeStarted <- state:
		case <-watchdog.Done():
		}
		var err error
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-watchdog.Done():
			err = watchdog.Err()
		}
		mark("write_finished")
		select {
		case writeFinished <- err:
		case <-watchdog.Done():
		}
		return memory.HistorySource{}, err
	}
	store.release = func(snapshot memory.HistorySnapshot) {
		mark("release_started")
		select {
		case releaseStarted <- snapshot:
		case <-watchdog.Done():
		}
		// Hold the real lease until the test inspects the queued task at this boundary.
		select {
		case <-allowRelease:
		case <-watchdog.Done():
		}
		base.ReleaseHistorySnapshot(snapshot)
		mark("release_finished")
	}
	provider := &historyLaneProvider{scriptedProvider: &scriptedProvider{delay: 2 * waitTimeout}}
	provider.started = func() {
		mark("provider_started")
		select {
		case providerStarted <- struct{}{}:
		case <-watchdog.Done():
		}
	}
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, cfg, agent.WithHistoryStore(store))
	lane, err := session.NewExecutionLane(watchdog, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		lane.Close()
		stop()
		select {
		case <-lane.Done():
		case <-time.After(waitTimeout):
			t.Error("execution lane did not stop after cleanup")
		}
	})

	turnCancelReady := make(chan context.CancelFunc, 1)
	firstDone := make(chan error, 1)
	if err := lane.Enqueue(session.Task{ID: "cancelled-history-turn", Run: func(laneCtx context.Context) {
		turnCtx, cancelTurn := context.WithCancel(laneCtx)
		defer cancelTurn()
		select {
		case turnCancelReady <- cancelTurn:
		case <-watchdog.Done():
		}
		err := loop.HandleEvent(turnCtx, env, agent.ConnectionContext{GameID: key.GameID}, key,
			entityTarget(key), newSpeakRegistry(), gameEvent("cancelled-input", key))
		mark("first_task_returned")
		select {
		case firstDone <- err:
		case <-watchdog.Done():
		}
	}}); err != nil {
		t.Fatal(err)
	}
	cancelTurn := waitHistoryLaneSignal(t, watchdog, "turn child context", turnCancelReady)
	defer cancelTurn()
	snapshot := waitHistoryLaneSignal(t, watchdog, "history snapshot", acquired)
	if snapshot.LeaseID == "" || snapshot.Owner != key || snapshot.Watermark != prior.Sequence {
		t.Fatalf("turn did not acquire the expected real snapshot: %+v", snapshot)
	}
	waitHistoryLaneSignal(t, watchdog, "provider invocation", providerStarted)

	type nextResult struct {
		contextErr, oldSnapshotErr, readErr error
		snapshot                            memory.HistorySnapshot
		page                                memory.HistoryPage
	}
	secondDone := make(chan nextResult, 1)
	if err := lane.Enqueue(session.Task{ID: "queued-after-history-turn", Run: func(ctx context.Context) {
		mark("second_task_started")
		result := nextResult{contextErr: ctx.Err()}
		_, result.oldSnapshotErr = base.ReadHistorySnapshot(ctx, snapshot, 0, memory.HistoryReadLimits{})
		result.snapshot, result.readErr = base.BeginHistorySnapshot(ctx, key)
		if result.readErr == nil {
			result.page, result.readErr = base.ReadHistorySnapshot(ctx, result.snapshot, 0, memory.HistoryReadLimits{})
			base.ReleaseHistorySnapshot(result.snapshot)
		}
		select {
		case secondDone <- result:
		case <-watchdog.Done():
		}
	}}); err != nil {
		t.Fatal(err)
	}
	mark("second_task_queued")
	cancelTurn()
	state := waitHistoryLaneSignal(t, watchdog, "terminal write start", writeStarted)
	if state.contextErr != nil || !state.hasDeadline || state.remaining <= 0 || state.remaining > time.Duration(cfg.History.WriteTimeoutMS)*time.Millisecond {
		t.Fatalf("terminal write did not receive an independent bounded context: %+v", state)
	}
	if state.batch.Owner != key || state.batch.TurnID == "" || state.batch.Event.ID != "cancelled-input" || state.batch.Terminal.Status != "cancelled" {
		t.Fatalf("wrong cancelled terminal write attempt: %+v", state.batch)
	}
	if err := waitHistoryLaneSignal(t, watchdog, "terminal write deadline", writeFinished); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("terminal write returned before its deadline: %v", err)
	}
	if got := waitHistoryLaneSignal(t, watchdog, "snapshot release entry", releaseStarted); got != snapshot {
		t.Fatalf("released a different snapshot: %+v", got)
	}
	select {
	case err := <-firstDone:
		t.Fatalf("first task returned before snapshot release completed: %v", err)
	case <-secondDone:
		t.Fatal("queued task started before snapshot release completed")
	default:
	}
	if _, err := base.ReadHistorySnapshot(watchdog, snapshot, 0, memory.HistoryReadLimits{}); err != nil {
		t.Fatalf("snapshot lease ended before the release barrier: %v", err)
	}
	prefix := []string{"snapshot_acquired", "provider_started", "second_task_queued", "write_started", "write_finished", "release_started"}
	assertOrder(prefix)
	close(allowRelease)
	if err := waitHistoryLaneSignal(t, watchdog, "first task return", firstDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("write failure changed turn cancellation: %v", err)
	}
	next := waitHistoryLaneSignal(t, watchdog, "queued task execution", secondDone)
	if next.contextErr != nil || !errors.Is(next.oldSnapshotErr, memory.ErrInvalidHistory) || next.readErr != nil {
		t.Fatalf("queued task saw cancelled lane or unreleased snapshot: %+v", next)
	}
	if next.snapshot.Watermark != prior.Sequence || len(next.page.Sources) != 1 || next.page.Sources[0].ID != prior.ID || next.page.More {
		t.Fatalf("failed terminal write fabricated persisted history: %+v", next.page)
	}
	assertOrder(append(prefix, "release_finished", "first_task_returned", "second_task_started"))
	if len(provider.requests) != 1 || len(env.submittedActions) != 0 || len(env.turnCompletions) != 1 ||
		env.turnCompletions[0].Status != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED {
		t.Fatal("cancelled turn fabricated an action or successful completion")
	}
	failures := 0
	for _, event := range recorder.events {
		if event.Event == trace.EventContextUpdateFailed && event.Fields["reason"] == "history_write_failed" {
			failures++
			if event.Fields["terminal_status"] != "cancelled" {
				t.Fatalf("wrong terminal status in write failure trace: %+v", event)
			}
		}
		for _, field := range []string{"history_source_id", "history_sequence", "history_bytes"} {
			if _, ok := event.Fields[field]; ok {
				t.Fatalf("failed terminal write emitted a persistence receipt: %+v", event)
			}
		}
		if event.Event == trace.EventContextUpdated && event.Fields["terminal_status"] != nil {
			t.Fatalf("failed terminal write emitted success: %+v", event)
		}
	}
	if failures != 1 {
		t.Fatalf("history write failure traces = %d, want 1", failures)
	}
}
