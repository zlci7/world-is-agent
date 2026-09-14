package gateway

import (
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

func TestEventTargetRegistrationRejectsInvalidIdentityWithoutPausingWorld(t *testing.T) {
	for _, scenario := range []string{"world", "run", "generation", "type", "definition", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			target := &protocol.EntityRef{EntityId: "new-actor", EntityType: "agent", DefinitionId: "generic"}
			event := &protocol.GameEvent{EventId: "invalid", EventType: "generic_input", WorldId: "world", TargetEntityId: target.EntityId, Entities: []*protocol.EntityRef{target}, InteractionSource: &protocol.InteractionSource{Kind: "player", SourceId: "input", PlayerEntityId: "player", Scope: taskScopeToProtocol(f.head.Binding)}}
			want := "source_invalid"
			switch scenario {
			case "world":
				event.WorldId = "other"
				want = "world_mismatch"
			case "run":
				event.InteractionSource.Scope.WorldRunId = "old-run"
			case "generation":
				event.InteractionSource.Scope.ExecutionGeneration++
			case "type":
				target.EntityType = ""
				want = "target_entity_invalid"
			case "definition":
				target.DefinitionId = ""
				want = "target_entity_invalid"
			case "conflict":
				target.EntityId = "actor"
				event.TargetEntityId = "actor"
				target.DefinitionId = "different"
				want = "target_entity_conflict"
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}})
			ack := f.next().GetEventAck()
			if ack.GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_REJECTED || ack.GetError().GetCode() != want {
				t.Fatalf("ack: %v, want %s", ack, want)
			}
			entry, ready := f.server.worlds.Current(f.head.Binding.World)
			if !ready || len(entry.Entities) != 2 || entry.Entities["actor"].GetDefinitionId() != "generic" {
				t.Fatalf("invalid event mutated world: %+v", entry)
			}
			if f.model.calls.Load() != 0 {
				t.Fatal("invalid target called the model")
			}
		})
	}
}

func TestEventTargetRegistrationOnlyRegistersTargetAndDuplicateDoesNotExecute(t *testing.T) {
	f := newTaskWireFixture(t, false)
	f.model.mode = "ordinary_after_pause"
	event := &protocol.GameEvent{EventId: "ordinary", EventType: "generic_input", WorldId: "world", TargetEntityId: "actor", Entities: append(worldRequest().Entities, &protocol.EntityRef{EntityId: "bystander", EntityType: "agent", DefinitionId: "generic"})}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}})
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("ordinary event rejected")
	}
	f.observe(f.next())
	f.result(f.next().GetAction(), nil, nil)
	if f.next().GetTurnCompletion().GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatal("ordinary turn failed")
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}})
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_DUPLICATE || f.model.calls.Load() != 2 {
		t.Fatal("duplicate event executed")
	}
	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	if len(entry.Entities) != 2 || entry.Entities["bystander"] != nil {
		t.Fatalf("bystander registered: %v", entry.Entities)
	}
	records, err := f.service.ListActive(f.ctx, f.key, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("chat created a task: %v %v", records, err)
	}
}

func TestReplacedConnectionCannotRegisterEventTarget(t *testing.T) {
	server, _, _ := taskTestServer(t)
	stream, _ := taskHandshake(t, server)
	ready := bindTestWorld(t, stream, worldRequest())
	entry, _ := server.worlds.Current(task.WorldKey{GameID: "sim", WorldID: "world"})
	previous := entry.Environment.(*streamEnvironment).taskAuthority
	request := worldRequest()
	request.Scope = ready.Scope
	bindTestWorld(t, stream, request)
	err := previous.RegisterEventTarget(&protocol.GameEvent{WorldId: "world"}, &protocol.EntityRef{EntityId: "stale", EntityType: "agent", DefinitionId: "generic"})
	if err.GetCode() != "generation_stale" {
		t.Fatalf("old connection registered target: %v", err)
	}
	current, ok := server.worlds.Current(entry.Head.Binding.World)
	if !ok || current.Entities["stale"] != nil {
		t.Fatal("old connection mutated directory")
	}
}
