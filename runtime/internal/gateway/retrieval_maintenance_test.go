package gateway

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/llm/fake"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tool"
	"gameagent/runtime/internal/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type queueWaitEventHandler struct {
	started, release, maintained chan struct{}
}

func (l queueWaitEventHandler) HandleEvent(ctx context.Context, _ agent.Environment, _ agent.ConnectionContext, _ session.AgentSessionKey, _ *protocol.EntityRef, _ *tool.EnvironmentToolCatalog, _ *protocol.GameEvent) error {
	close(l.started)
	select {
	case <-l.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l queueWaitEventHandler) MaintainHistory(context.Context, session.AgentSessionKey, *memory.GameTimeSnapshot) {
	close(l.maintained)
}

type queueWaitAckStream struct {
	protocol.GameAgentGateway_ConnectServer
}

func (queueWaitAckStream) Send(*protocol.RuntimeMessage) error { return nil }

func TestGatewayMaintenanceLogsQueueWaitBehindPlayer(t *testing.T) {
	logs := captureStandardLog(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lanes, err := session.NewLaneStore(ctx, session.DefaultQueueSize)
	if err != nil {
		t.Fatal(err)
	}
	defer lanes.CloseAndWait()
	env := newStreamEnvironment(queueWaitAckStream{})
	defer env.close()
	handler := queueWaitEventHandler{started: make(chan struct{}), release: make(chan struct{}), maintained: make(chan struct{})}
	server := NewServer(handler)
	event := npcInteractionEventMessageWithIDs(1).GetEvent()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: event.WorldId, EntityID: event.TargetEntityId}
	if err := server.dispatchGameEvent(env, lanes, map[string]struct{}{}, agent.ConnectionContext{GameID: key.GameID}, nil, "queue-wait", event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handler.started:
	case <-ctx.Done():
		t.Fatal("event did not start")
	}
	lane, err := lanes.GetOrCreate(key)
	if err != nil {
		t.Fatal(err)
	}
	playerStarted, releasePlayer := make(chan struct{}), make(chan struct{})
	if err := lane.Enqueue(session.Task{ID: "queued-player", Run: func(ctx context.Context) {
		close(playerStarted)
		select {
		case <-releasePlayer:
		case <-ctx.Done():
		}
	}}); err != nil {
		t.Fatal(err)
	}
	close(handler.release)
	select {
	case <-playerStarted:
	case <-ctx.Done():
		t.Fatal("queued player did not start")
	}
	const blockedFor = 25 * time.Millisecond
	timer := time.NewTimer(blockedFor)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("queued player timed out")
	}
	if strings.Contains(logs.String(), "stage=queue") {
		t.Fatal("queue wait logged before maintenance started")
	}
	close(releasePlayer)
	select {
	case <-handler.maintained:
	case <-ctx.Done():
		t.Fatal("maintenance did not start")
	}
	prefix := "history maintenance owner=" + key.DiagnosticID() + " stage=queue wait_ms="
	for _, line := range strings.Split(logs.String(), "\n") {
		if value, ok := strings.CutPrefix(line, prefix); ok {
			waitMS, err := strconv.ParseInt(value, 10, 64)
			if err != nil || waitMS < blockedFor.Milliseconds() {
				t.Fatalf("queue wait omitted blocked player time: %q", line)
			}
			return
		}
	}
	t.Fatalf("missing numeric owner-scoped queue wait: %s", logs.String())
}

type legacyGatewayEventHandler struct{ called chan struct{} }

func (l legacyGatewayEventHandler) HandleEvent(context.Context, agent.Environment, agent.ConnectionContext, session.AgentSessionKey, *protocol.EntityRef, *tool.EnvironmentToolCatalog, *protocol.GameEvent) error {
	close(l.called)
	return nil
}

func TestGatewayMaintenanceInterfaceRemainsOptional(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	handler := legacyGatewayEventHandler{called: make(chan struct{})}
	protocol.RegisterGameAgentGatewayServer(server, NewServer(handler))
	startGatewayServer(t, server, listener)
	conn := dialGateway(t, ctx, listener)
	defer conn.Close()
	stream := connectReadyStreamWithCapabilities(t, ctx, protocol.NewGameAgentGatewayClient(conn), "legacy-handler", capabilityListMessage)
	if err := stream.Send(npcInteractionEventMessageWithIDs(1)); err != nil {
		t.Fatal(err)
	}
	ack := recvRuntimeMessage(t, stream).GetEventAck()
	if ack == nil || ack.Status != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("legacy handler rejected: %+v", ack)
	}
	select {
	case <-handler.called:
	case <-ctx.Done():
		t.Fatal("legacy handler did not run")
	}
}

type maintenanceTimeTestEnvironment struct {
	agent.Environment
	observation *protocol.Observation
}

func (e maintenanceTimeTestEnvironment) Observe(context.Context, string, string) (*protocol.Observation, error) {
	return e.observation, nil
}

func TestGatewayMaintenanceTimeRequiresObservationScope(t *testing.T) {
	for _, owner := range []session.AgentSessionKey{{WorldID: "another-world", EntityID: "agent"}, {WorldID: "world", EntityID: "another-agent"}} {
		tick := int64(20)
		env := &historyMaintenanceEnvironment{Environment: maintenanceTimeTestEnvironment{observation: &protocol.Observation{WorldId: owner.WorldID, EntityId: owner.EntityID, GameTime: &protocol.GameTime{Tick: &tick}}}}
		if _, err := env.Observe(context.Background(), "world", "agent"); err != nil {
			t.Fatal(err)
		}
		if env.currentTime != nil {
			t.Fatalf("foreign observation established prune time: %+v", env.currentTime)
		}
	}
}

type gatewayMaintenanceStart struct {
	writes, releases int32
	err              error
}
type gatewayMaintenanceStore struct {
	*memory.InMemoryHistoryStore
	writes, releases   atomic.Int32
	failWrite          bool
	started            chan gatewayMaintenanceStart
	pruned             chan memory.HistoryPruneRequest
	cancelled, cleanup chan struct{}
}

func newGatewayMaintenanceStore() *gatewayMaintenanceStore {
	return &gatewayMaintenanceStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{}), started: make(chan gatewayMaintenanceStart, 10), pruned: make(chan memory.HistoryPruneRequest, 10)}
}
func (s *gatewayMaintenanceStore) AppendHistory(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
	defer s.writes.Add(1)
	if s.failWrite {
		return memory.HistorySource{}, errors.New("terminal write failed")
	}
	return s.InMemoryHistoryStore.AppendHistory(ctx, batch)
}
func (s *gatewayMaintenanceStore) ReleaseHistorySnapshot(snapshot memory.HistorySnapshot) {
	s.InMemoryHistoryStore.ReleaseHistorySnapshot(snapshot)
	s.releases.Add(1)
}
func (s *gatewayMaintenanceStore) RebuildHistoryIndex(ctx context.Context, _ memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
	s.started <- gatewayMaintenanceStart{s.writes.Load(), s.releases.Load(), ctx.Err()}
	if s.cancelled != nil {
		<-ctx.Done()
		close(s.cancelled)
		<-s.cleanup
		return memory.HistoryMaintenanceResult{}, ctx.Err()
	}
	return memory.HistoryMaintenanceResult{}, nil
}
func (s *gatewayMaintenanceStore) PruneHistory(_ context.Context, req memory.HistoryPruneRequest) (memory.HistoryMaintenanceResult, error) {
	s.pruned <- req
	return memory.HistoryMaintenanceResult{}, nil
}

func maintenanceGatewayStream(t *testing.T, ctx context.Context, store *gatewayMaintenanceStore, retention int) (protocol.GameAgentGateway_ConnectClient, *grpc.Server) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	cfg := gatewayTestConfig(t)
	cfg.RetentionDays = retention
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
	protocol.RegisterGameAgentGatewayServer(server, NewServer(loop))
	startGatewayServer(t, server, listener)
	conn := dialGateway(t, ctx, listener)
	t.Cleanup(func() { conn.Close() })
	stream := connectReadyStreamWithCapabilities(t, ctx, protocol.NewGameAgentGatewayClient(conn), "session:maintenance", capabilityListMessage)
	return stream, server
}

func finishMaintenanceGatewayTurn(t *testing.T, stream protocol.GameAgentGateway_ConnectClient, observe *protocol.RuntimeMessage, eventID string, tick *int64) {
	t.Helper()
	message := observationMessage(observe.MessageId)
	if tick != nil {
		message.GetObservation().GameTime = &protocol.GameTime{Tick: tick}
	}
	if err := stream.Send(message); err != nil {
		t.Fatal(err)
	}
	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil {
		t.Fatal("expected action")
	}
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatal(err)
	}
	recvTurnCompletion(t, stream, eventID, "npc:Linus", protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
}

func TestGatewayMaintenanceRunsAfterTerminalAttemptAndSnapshotRelease(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "write failed"}[failWrite], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			store := newGatewayMaintenanceStore()
			store.failWrite = failWrite
			stream, _ := maintenanceGatewayStream(t, ctx, store, 0)
			observe := sendAcceptedNPCEvent(t, stream, 1, "npc:Linus", "Linus")
			finishMaintenanceGatewayTurn(t, stream, observe, "event_1", nil)
			select {
			case start := <-store.started:
				if start.writes != 1 || start.releases != 1 || start.err != nil {
					t.Fatalf("maintenance raced terminal lifecycle or received canceled Turn context: %+v", start)
				}
			case <-ctx.Done():
				t.Fatal("maintenance was not scheduled")
			}
			select {
			case <-store.pruned:
				t.Fatal("default retention pruned history")
			default:
			}
		})
	}
}

func TestGatewayMaintenanceUsesProcessedEventTimeOrObservation(t *testing.T) {
	for _, eventTime := range []bool{false, true} {
		t.Run(map[bool]string{false: "observation", true: "event"}[eventTime], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			store := newGatewayMaintenanceStore()
			stream, _ := maintenanceGatewayStream(t, ctx, store, 1)
			message := npcInteractionEventMessageWithIDs(1)
			eventTick, obsTick := int64(10), int64(20)
			if eventTime {
				message.GetEvent().GameTime = &protocol.GameTime{Tick: &eventTick}
			}
			if err := stream.Send(message); err != nil {
				t.Fatal(err)
			}
			if recvRuntimeMessage(t, stream).GetEventAck() == nil {
				t.Fatal("missing ack")
			}
			observe := recvRuntimeMessage(t, stream)
			finishMaintenanceGatewayTurn(t, stream, observe, "event_1", &obsTick)
			select {
			case req := <-store.pruned:
				want := obsTick
				if eventTime {
					want = eventTick
				}
				if req.CurrentTime == nil || req.CurrentTime.Tick != want || req.Owner.EntityID != "npc:Linus" {
					t.Fatalf("wrong processed time/owner: %+v", req)
				}
			case <-ctx.Done():
				t.Fatal("prune was not scheduled with provable processed time")
			}
		})
	}
}

func TestGatewayMaintenanceWaitsForQueuedPlayerAndCoalesces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := newGatewayMaintenanceStore()
	stream, _ := maintenanceGatewayStream(t, ctx, store, 0)
	observe := sendAcceptedNPCEvent(t, stream, 1, "npc:Linus", "Linus")
	if err := stream.Send(npcInteractionEventMessageWithIDs(2)); err != nil {
		t.Fatal(err)
	}
	ack := recvRuntimeMessage(t, stream).GetEventAck()
	if ack == nil || ack.Status != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("queued player rejected: %+v", ack)
	}
	finishMaintenanceGatewayTurn(t, stream, observe, "event_1", nil)
	secondObserve := recvRuntimeMessage(t, stream)
	if secondObserve.GetObserve() == nil {
		t.Fatal("queued player did not run")
	}
	select {
	case <-store.started:
		t.Fatal("maintenance ran before queued player")
	default:
	}
	finishMaintenanceGatewayTurn(t, stream, secondObserve, "event_2", nil)
	select {
	case start := <-store.started:
		if start.writes != 2 || start.releases != 2 {
			t.Fatalf("wrong coalesced maintenance lifecycle: %+v", start)
		}
	case <-ctx.Done():
		t.Fatal("coalesced maintenance missing")
	}
}

func TestGatewayMaintenanceShutdownWaitsForTransactionCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := newGatewayMaintenanceStore()
	store.cancelled = make(chan struct{})
	store.cleanup = make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(store.cleanup)
		}
	}()
	stream, server := maintenanceGatewayStream(t, ctx, store, 0)
	observe := sendAcceptedNPCEvent(t, stream, 1, "npc:Linus", "Linus")
	finishMaintenanceGatewayTurn(t, stream, observe, "event_1", nil)
	select {
	case <-store.started:
	case <-ctx.Done():
		t.Fatal("maintenance missing")
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.cancelled:
	case <-ctx.Done():
		t.Fatal("lane did not cancel maintenance")
	}
	stopped := make(chan struct{})
	go func() { server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("gateway stopped before transaction cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.cleanup)
	released = true
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("gateway did not finish after cleanup")
	}
}
