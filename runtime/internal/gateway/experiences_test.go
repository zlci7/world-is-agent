package gateway

import (
	"context"
	"errors"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type unavailableExperienceStore struct {
	memory.HistoryStore
	fail bool
}

func (s *unavailableExperienceStore) AppendHistory(ctx context.Context, b memory.HistoryBatch) (memory.HistorySource, error) {
	if s.fail {
		return memory.HistorySource{}, errors.New("fixture write unavailable")
	}
	return s.HistoryStore.AppendHistory(ctx, b)
}

func TestExperienceWriteFailureCanRetryTheSameReceipt(t *testing.T) {
	store := &unavailableExperienceStore{HistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{}), fail: true}
	stream, done := newWorldTestStream(t, NewServer(nil, WithExperienceHistory(store)), []string{experience.Extension})
	stream.recvSent(t)
	stream.recvSent(t)
	sendWorldCapabilities(stream)
	event := experienceEvent()
	stream.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}}
	if ack := stream.recvSent(t).GetEventAck(); ack.GetError().GetCode() != "experience_write_failed" {
		t.Fatal(ack)
	}
	store.fail = false
	stream.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}}
	if ack := stream.recvSent(t).GetEventAck(); ack.GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal(ack)
	}
	stream.shutdown()
	<-done
}

func experienceEvent() *protocol.GameEvent {
	payload, _ := structpb.NewStruct(map[string]any{experience.Extension: map[string]any{"version": 1, "clock_id": "generic.clock", "occurred_tick": "100", "focus_until_tick": "400", "priority": 50}})
	tick := int64(100)
	return &protocol.GameEvent{EventId: "receipt", EventType: "arbitrary.game.signal", WorldId: "world", TargetEntityId: "actor", GameTime: &protocol.GameTime{Tick: &tick}, Payload: payload, Entities: worldRequest().Entities,
		ContextFacts: []*protocol.ContextFact{{Kind: "interaction", ActorEntityId: "other", TargetEntityId: "actor", Text: "A confirmed shared interaction."}}}
}

func TestExperienceAcknowledgmentIsDurableWithoutTurnAndAcrossReconnect(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	key := session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}
	for i := 0; i < 2; i++ {
		store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: root}, memory.HistoryLimits{})
		// A nil turn handler also proves record-only events never invoke a model turn.
		stream, done := newWorldTestStream(t, NewServer(nil, WithExperienceHistory(store)), []string{experience.Extension})
		ready := stream.recvSent(t).GetEnvironmentReady()
		if len(ready.GetAcceptedExtensions()) != 1 || ready.AcceptedExtensions[0] != experience.Extension {
			t.Fatal(ready)
		}
		if stream.recvSent(t).GetCapabilityRequest() == nil {
			t.Fatal("capability bootstrap missing")
		}
		sendWorldCapabilities(stream)
		event := experienceEvent()
		for retry := 0; retry < 2; retry++ {
			stream.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}}
			ack := stream.recvSent(t).GetEventAck()
			if ack.GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatal(ack)
			}
			snap, err := store.BeginHistorySnapshot(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			page, err := store.ReadHistorySnapshot(ctx, snap, 0, memory.HistoryReadLimits{})
			store.ReleaseHistorySnapshot(snap)
			if err != nil || len(page.Sources) != 1 || page.Sources[0].Batch.Event.ID != "receipt" {
				t.Fatalf("ack preceded storage: %+v %v", page, err)
			}
		}
		changed := proto.Clone(event).(*protocol.GameEvent)
		changed.ContextFacts[0].Text = "changed"
		stream.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: changed}}
		if ack := stream.recvSent(t).GetEventAck(); ack.GetError().GetCode() != "experience_conflict" {
			t.Fatal(ack)
		}
		stream.shutdown()
		<-done
	}
}

func TestUnnegotiatedExperienceIsRejectedWithoutTurn(t *testing.T) {
	stream, done := newWorldTestStream(t, NewServer(nil), []string{experience.Extension})
	if ready := stream.recvSent(t).GetEnvironmentReady(); len(ready.GetAcceptedExtensions()) != 0 {
		t.Fatal(ready)
	}
	stream.recvSent(t)
	sendWorldCapabilities(stream)
	stream.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: experienceEvent()}}
	ack := stream.recvSent(t).GetEventAck()
	if ack.GetError().GetCode() != "experience_not_negotiated" {
		t.Fatal(ack)
	}
	stream.shutdown()
	<-done
}
