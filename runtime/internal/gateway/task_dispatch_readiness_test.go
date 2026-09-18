package gateway

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

// readinessLoop is an agent core that can report whether it is able to serve turns.
type readinessLoop struct {
	ready    atomic.Bool
	dispatch atomic.Int64
}

func (l *readinessLoop) HandleEvent(context.Context, agent.Environment, agent.ConnectionContext, session.AgentSessionKey, *protocol.EntityRef, *tool.EnvironmentToolCatalog, *protocol.GameEvent) error {
	return nil
}

func (l *readinessLoop) Ready() bool { return l.ready.Load() }

// A wake claim is a durable state change, and the dispatcher claims before the handler
// runs. While the agent core cannot serve turns, the due wake must therefore stay
// untouched: the same wake has to still be dispatchable once the core reports ready.
func TestTaskDispatcherWaitsForAReadyAgentCore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	svc := task.NewService(store)

	loop := &readinessLoop{}
	server := NewServer(loop, WithTaskService(svc))
	t.Cleanup(func() { server.Close(context.Background()) })
	stream, _ := taskHandshake(t, server)
	bindTestWorld(t, stream, worldRequest())
	entry, _ := server.worlds.Current(task.WorldKey{GameID: "sim", WorldID: "world"})

	source := task.SourceRef{Kind: task.SourceKindInteraction, EventID: "event", TurnID: "turn", CallID: "call"}
	exec := task.ExecutionContext{Owner: session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}, Binding: entry.Head.Binding, Clock: entry.Head.Clock, Source: source}
	if _, err := svc.Create(context.Background(), exec, task.TaskSpec{Instruction: "Inspect generic world", ClockID: "game", WakeAt: 11, DeadlineAt: 100, ResultContract: task.ResultContractAuthoritativeEvidence, Source: source}, task.Admission{}); err != nil {
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

	config := task.DispatcherConfig{ScanInterval: 5 * time.Millisecond, BatchSize: 4, RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond}
	if err := server.StartTaskDispatcher(context.Background(), config, func(context.Context, task.ExecutionContext, task.Record) error {
		loop.dispatch.Add(1)
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Several scans while the core is not ready.
	time.Sleep(60 * time.Millisecond)
	if got := loop.dispatch.Load(); got != 0 {
		t.Fatalf("dispatched %d wakes while the agent core was not ready; the claim is durable and must wait", got)
	}

	loop.ready.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && loop.dispatch.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := loop.dispatch.Load(); got == 0 {
		t.Fatal("the due wake was never dispatched after the agent core became ready; an earlier scan consumed it")
	}
}
