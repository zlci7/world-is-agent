package gateway

import (
	"os"
	"path/filepath"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestAdapterPlayerEventCreatesDurableTaskBeforeConfirmation(t *testing.T) {
	for _, kind := range []string{"click", "option", "free_text"} {
		t.Run(kind, func(t *testing.T) {
			// These fixtures are also compared to the production C# Mapper output.
			data, err := os.ReadFile(filepath.Join("../../../adapters/stardew/tests/fixtures", "player-"+kind+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var event protocol.GameEvent
			if err := protojson.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			f := newTaskWireFixture(t, true)
			f.model.mode = "adapter_player"
			f.send(&protocol.AdapterMessage{MessageId: event.EventId, Payload: &protocol.AdapterMessage_Event{Event: &event}})
			if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatal("player event rejected")
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
		})
	}
}
