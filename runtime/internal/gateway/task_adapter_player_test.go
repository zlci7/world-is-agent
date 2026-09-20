package gateway

import (
	"os"
	"path/filepath"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestOrdinaryEventWithoutTaskSourceDoesNotGainTaskAuthority(t *testing.T) {
	f := newTaskWireFixture(t, false)
	slot := f.server.worlds.slot(f.head.Binding.World, false)
	slot.mu.Lock()
	delete(slot.entities, f.key.EntityID)
	slot.mu.Unlock()
	f.model.mode = "ordinary_without_task_source"
	f.event("ordinary-input", nil, nil)
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("ordinary event rejected")
	}
	f.observe(f.next())
	request := f.next().GetAction()
	if request.GetCapability() != "inspect_contract" || request.TaskSource != nil {
		t.Fatal("ordinary action unavailable", request)
	}
	f.result(request, nil, &protocol.TaskProposal{Clock: worldRequest().Clock, WakeAt: 11, DeadlineAt: 100})
	if completion := f.next().GetTurnCompletion(); completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("ordinary turn interrupted: %v", completion)
	}
	records, err := f.service.ListActive(f.ctx, f.key, 10)
	if err != nil || len(records) != 0 {
		t.Fatal("unregistered task persisted", records, err)
	}
	if _, ready := f.server.worlds.Current(f.head.Binding.World); !ready {
		t.Fatal("world paused")
	}
}

func TestAdapterPlayerEventCreatesDurableTaskBeforeConfirmation(t *testing.T) {
	for _, kind := range []string{"click", "option", "free_text"} {
		t.Run(kind, func(t *testing.T) {
			// Protocol fixtures cover the supported player interaction sources.
			data, err := os.ReadFile(filepath.Join("../../../protocol/tests/fixtures/player-interactions", "player-"+kind+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var event protocol.GameEvent
			if err := protojson.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			binding := worldRequest()
			binding.Entities = []*protocol.EntityRef{{EntityId: "player", EntityType: "player", DefinitionId: "player"}}
			f := newTaskWireFixtureWithBinding(t, true, protocol.ExecutionMode_EXECUTION_MODE_SYNC, binding)
			f.model.mode = "adapter_player"
			f.send(&protocol.AdapterMessage{MessageId: event.EventId, Payload: &protocol.AdapterMessage_Event{Event: &event}})
			if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatal("player event rejected")
			}
			entry, ready := f.server.worlds.Current(f.head.Binding.World)
			if !ready || entry.Entities["actor"].GetDefinitionId() != "actor" || len(entry.Entities) != 2 {
				t.Fatalf("event target not registered: %+v", entry.Entities)
			}
			f.observe(f.next())
			request := f.next().GetAction()
			if request.GetCapability() != "inspect_contract" {
				t.Fatal("missing proposal action", request)
			}
			f.result(request, nil, &protocol.TaskProposal{Clock: worldRequest().Clock, WakeAt: 11, DeadlineAt: 100, ParticipantEntityIds: []string{"actor", "player"}, EquivalenceKey: "agreement"})
			confirmation := f.next().GetAction()
			if confirmation.GetCapability() != "confirm_agreement" {
				t.Fatal("creation did not reach confirmation", confirmation)
			}
			records, err := f.service.ListActive(f.ctx, f.key, 10)
			if err != nil || len(records) != 1 || records[0].ID == "" || records[0].State != task.StateWaiting {
				t.Fatalf("confirmation without durable task: %+v, %v", records, err)
			}
			// create_task executes locally: the next wire action is the confirmation.
			f.result(confirmation, nil, nil)
			if f.next().GetTurnCompletion().GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
				t.Fatal("turn failed")
			}
			if f.model.calls.Load() != 3 {
				t.Fatal("expected resolve, create, confirm in three steps")
			}
			if kind == "free_text" {
				rebinding := proto.Clone(binding).(*protocol.WorldBinding)
				rebinding.Scope = taskScopeToProtocol(f.head.Binding)
				for _, entity := range event.Entities {
					if entity.EntityId == event.TargetEntityId {
						rebinding.Entities = append(rebinding.Entities, proto.Clone(entity).(*protocol.EntityRef))
					}
				}
				f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: rebinding}})
				ready := f.next().GetWorldBindingReady()
				if ready.GetStatus() != "ready" {
					t.Fatalf("same-run rebind: %v", ready)
				}
				f.head.Binding.Generation = ready.Scope.ExecutionGeneration
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 11, Sequence: 2}}}})
			f.observe(f.next())
			action := f.next().GetAction()
			source := action.GetTaskSource()
			if action.GetCapability() != "follow_route" || source.GetTaskId() != records[0].ID || source.GetOperationId() == "" {
				t.Fatalf("clock did not dispatch the registered owner: %v", action)
			}
			running, err := f.service.Read(f.ctx, f.key, records[0].ID)
			if err != nil || len(running.Operations) != 1 || running.Operations[0].ActionID != action.ActionId {
				t.Fatalf("action sent before operation registration: %+v %v", running, err)
			}
			waitUntil := int64(20)
			f.result(action, []*protocol.TaskEvidence{{FactId: "arrived", TaskId: source.TaskId, OperationId: source.OperationId, Scope: source.Scope, StartRevision: source.StartRevision, OccurredAt: 11, Outcome: "progress", WaitUntil: &waitUntil}}, nil)
			if completion := f.next().GetTurnCompletion(); completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
				t.Fatalf("wake turn failed: %v", completion)
			}
			f.awaitRecord(records[0].ID, func(r task.Record) bool {
				return r.State == task.StateWaiting && r.NextWakeAt != nil && *r.NextWakeAt == 20
			})
			if _, ready := f.server.worlds.Current(f.head.Binding.World); !ready {
				t.Fatal("valid wake paused the world")
			}
		})
	}
}
