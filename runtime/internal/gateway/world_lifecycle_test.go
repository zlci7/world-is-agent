package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

func TestWorldBindingInlineTaskPayloadBeforeReady(t *testing.T) {
	messages := []*protocol.AdapterMessage{
		{Payload: &protocol.AdapterMessage_Event{Event: &protocol.GameEvent{TaskEvidence: []*protocol.TaskEvidence{{}}}}},
		{Payload: &protocol.AdapterMessage_Observation{Observation: &protocol.Observation{TaskEvidence: []*protocol.TaskEvidence{{}}}}},
		{Payload: &protocol.AdapterMessage_ActionResult{ActionResult: &protocol.ActionResult{TaskProposal: &protocol.TaskProposal{}}}},
		{Payload: &protocol.AdapterMessage_ActionResult{ActionResult: &protocol.ActionResult{TaskEvidence: []*protocol.TaskEvidence{{}}}}},
	}
	for i, message := range messages {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			server, _, _ := taskTestServer(t)
			s, _ := taskHandshake(t, server)
			s.incoming <- message
			select {
			case m := <-s.sent:
				if m.GetError().GetCode() != "world_not_ready" {
					t.Fatal(m)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("inline task payload bypassed ready gating")
			}
		})
	}
}

func TestLegacyProtocolOrdinaryEvents(t *testing.T) {
	for _, extensions := range [][]string{nil, {"unknown", "unknown"}} {
		t.Run("negotiation", func(t *testing.T) {
			server, _, _ := taskTestServer(t)
			called := make(chan struct{})
			server.agentLoop = legacyGatewayEventHandler{called: called}
			s, _ := newWorldTestStream(t, server, extensions)
			if ready := s.recvSent(t).GetEnvironmentReady(); ready == nil || len(ready.AcceptedExtensions) != 0 {
				t.Fatal(ready)
			}
			if m := s.recvSent(t); m.GetCapabilityRequest() == nil {
				t.Fatal(m)
			}
			sendWorldCapabilities(s)
			s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: &protocol.GameEvent{EventId: "event", EventType: "interaction", WorldId: "world", TargetEntityId: "actor", Entities: worldRequest().Entities}}}
			if ack := s.recvSent(t).GetEventAck(); ack == nil || ack.Status != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatal(ack)
			}
			select {
			case <-called:
			case <-time.After(time.Second):
				t.Fatal("legacy event not handled")
			}
			if len(server.WorldRegistry().ReadyWorlds()) != 0 {
				t.Fatal("legacy stream enabled task world")
			}
			select {
			case m := <-s.sent:
				t.Fatalf("unexpected task bootstrap message: %v", m)
			default:
			}
		})
	}
}

func TestWorldBindingDisconnectWaitsBothEntityLanes(t *testing.T) {
	server, service, _ := taskTestServer(t)
	s, done := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())
	registry := server.WorldRegistry()
	entry, _ := registry.Current(task.WorldKey{GameID: "sim", WorldID: "world"})
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	for _, id := range []string{"actor", "other"} {
		lane, err := entry.Lanes.GetOrCreate(session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: id})
		if err != nil {
			t.Fatal(err)
		}
		if err := lane.Enqueue(session.Task{ID: id, Run: func(ctx context.Context) { started <- struct{}{}; <-ctx.Done(); cancelled <- struct{}{}; <-release }}); err != nil {
			t.Fatal(err)
		}
	}
	<-started
	<-started
	close(s.incoming)
	<-cancelled
	<-cancelled
	if _, ok := registry.Current(entry.Head.Binding.World); ok {
		t.Fatal("disconnect admission remained open")
	}
	select {
	case <-entry.Environment.(*streamEnvironment).closed:
	default:
		t.Fatal("action admission remained open")
	}
	// Persistence is still active while lane cleanup is outstanding.
	if _, err := service.UpdateClock(context.Background(), entry.Head.Binding, entry.Head.Clock); err != nil {
		t.Fatalf("deactivated before lane cleanup: %v", err)
	}
	select {
	case <-done:
		t.Fatal("disconnect did not wait for lane cleanup")
	default:
	}
	release <- struct{}{}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateClock(context.Background(), entry.Head.Binding, entry.Head.Clock); !errors.Is(err, task.ErrWorldNotReady) {
		t.Fatal(err)
	}
}

func TestWorldBindingRebindPersistenceFailureRepliesPaused(t *testing.T) {
	server, _, store := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	ready := bindTestWorld(t, s, worldRequest())
	entry, _ := server.WorldRegistry().Current(task.WorldKey{GameID: "sim", WorldID: "world"})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	request := worldRequest()
	request.Scope = ready.Scope
	response := bindTestWorld(t, s, request)
	if response.Status != "paused" || response.Error == nil {
		t.Fatal(response)
	}
	if _, ok := server.WorldRegistry().Current(entry.Head.Binding.World); ok {
		t.Fatal("failed rebind remained admitted")
	}
	if _, err := entry.Lanes.GetOrCreate(session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}); !errors.Is(err, session.ErrLaneClosed) {
		t.Fatal(err)
	}
}

func TestWorldClockGuardSerializesSubmissionSnapshot(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	ready := bindTestWorld(t, s, worldRequest())
	registry := server.WorldRegistry()
	entry, _ := registry.Current(task.WorldKey{GameID: "sim", WorldID: "world"})
	env := entry.Environment.(*streamEnvironment)
	started, release := make(chan struct{}), make(chan struct{})
	guarded := make(chan error, 1)
	go func() {
		guarded <- registry.Guard(env, entry.Head.Binding, func(snapshot WorldEntry) error {
			close(started)
			<-release
			if snapshot.Head.Clock.Tick != 10 {
				return errors.New("guard clock changed")
			}
			return nil
		})
	}()
	<-started
	updated := make(chan error, 1)
	go func() {
		updated <- registry.UpdateClock(context.Background(), env, &protocol.WorldClockUpdate{Scope: ready.Scope, Clock: &protocol.WorldClock{ClockId: "game", NowTick: 11, Sequence: 2}})
	}()
	select {
	case err := <-updated:
		t.Fatalf("clock bypassed world guard: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-guarded; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := registry.Guard(env, entry.Head.Binding, func(snapshot WorldEntry) error {
		if snapshot.Head.Clock.Tick != 11 {
			return errors.New("guard read stale clock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorldBindingReadyWaitsForReplyAndRebindLanes(t *testing.T) {
	server, service, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	releaseReply := make(chan struct{})
	sending := make(chan struct{})
	s.readyGate = releaseReply
	s.readySending = sending
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldRequest()}}
	<-sending
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("task admission opened before WorldBindingReady send")
	}
	close(releaseReply)
	ready := s.recvSent(t).GetWorldBindingReady()
	if ready == nil {
		t.Fatal("missing ready reply")
	}
	var entry WorldEntry
	deadline := time.Now().Add(time.Second)
	for {
		var ok bool
		entry, ok = server.WorldRegistry().Current(task.WorldKey{GameID: "sim", WorldID: "world"})
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ready never published")
		}
		time.Sleep(time.Millisecond)
	}
	s.readyGate = nil
	lane, err := entry.Lanes.GetOrCreate(session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := lane.Enqueue(session.Task{ID: "active", Run: func(ctx context.Context) { close(started); <-ctx.Done(); close(cancelled); <-release }}); err != nil {
		t.Fatal(err)
	}
	<-started
	request := worldRequest()
	request.Scope = ready.Scope
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: request}}
	<-cancelled
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("rebind retained old admission")
	}
	if _, err := service.UpdateClock(context.Background(), entry.Head.Binding, entry.Head.Clock); err != nil {
		t.Fatal("rebind deactivated before old lane finished", err)
	}
	select {
	case m := <-s.sent:
		t.Fatal("rebind ready before old lane finished", m)
	default:
	}
	release <- struct{}{}
	rebound := s.recvSent(t).GetWorldBindingReady()
	if rebound == nil || rebound.Status != "ready" || rebound.Scope.ExecutionGeneration != 2 {
		t.Fatal(rebound)
	}
}

func TestWorldBindingReconnectFencesLateOldStream(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, done := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())
	world := task.WorldKey{GameID: "sim", WorldID: "world"}
	entry, _ := server.WorldRegistry().Current(world)
	close(s.incoming)
	<-done
	other, _ := taskHandshake(t, server)
	ready := bindTestWorld(t, other, worldRequest())
	if ready.Scope.ExecutionGeneration != 2 {
		t.Fatal(ready)
	}
	err := server.WorldRegistry().UpdateClock(context.Background(), entry.Environment.(*streamEnvironment), &protocol.WorldClockUpdate{Scope: ready.Scope, Clock: &protocol.WorldClock{ClockId: "game", NowTick: 11, Sequence: 2}})
	if !errors.Is(err, task.ErrGenerationStale) {
		t.Fatal("old stream impersonated current scope", err)
	}
	current, _ := server.WorldRegistry().Current(world)
	if current.Head.Clock.Tick != 10 {
		t.Fatal("old stream changed clock")
	}
}

func TestWorldBindingPausedRebindClosesAdmission(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	ready := bindTestWorld(t, s, worldRequest())
	request := worldRequest()
	request.Scope.ExecutionGeneration = ready.Scope.ExecutionGeneration + 1
	response := bindTestWorld(t, s, request)
	if response.Status != "paused" || response.Error.GetCode() != "generation_stale" {
		t.Fatal(response)
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("paused rebind kept task admission open")
	}
}
