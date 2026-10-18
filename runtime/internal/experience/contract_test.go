package experience

import (
	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"os"
	"testing"
)

func TestAdapterExperienceWireFixtures(t *testing.T) {
	data, err := os.ReadFile("../../../protocol/tests/fixtures/experiences/record.json")
	if err != nil {
		t.Fatal(err)
	}
	event := &protocol.GameEvent{}
	if err = protojson.Unmarshal(data, event); err != nil {
		t.Fatal(err)
	}
	record, err := ParseEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("../../../protocol/tests/fixtures/experiences/view.json")
	if err != nil {
		t.Fatal(err)
	}
	observation := &protocol.Observation{}
	if err = protojson.Unmarshal(data, observation); err != nil {
		t.Fatal(err)
	}
	view, err := ParseView(observation)
	if err != nil || !view.Allows(event.EventId, &record) || record.OccurredTick != 50390 || record.FocusUntilTick != 54710 {
		t.Fatalf("wire contract: %+v %+v %v", record, view, err)
	}
}

func TestConfirmedExperienceContract(t *testing.T) {
	payload, _ := structpb.NewStruct(map[string]any{Extension: map[string]any{"version": 1, "clock_id": "fake.calendar", "occurred_tick": "100", "focus_until_tick": "400", "priority": 50}})
	event := &protocol.GameEvent{EventId: "receipt", TargetEntityId: "agent", GameTime: &protocol.GameTime{}, Payload: payload, Entities: []*protocol.EntityRef{{EntityId: "player"}, {EntityId: "agent"}}, ContextFacts: []*protocol.ContextFact{{Kind: "interaction", ActorEntityId: "player", TargetEntityId: "agent", Text: "A confirmed shared event."}}}
	if _, err := ParseEvent(event); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{100.0, "-1", "01", "900719925474099300000"} {
		value, _ := structpb.NewValue(bad)
		payload.Fields[Extension].GetStructValue().Fields["occurred_tick"] = value
		if _, err := ParseEvent(event); err == nil {
			t.Fatalf("accepted invalid tick %v", bad)
		}
	}
	payload.Fields[Extension].GetStructValue().Fields["occurred_tick"] = structpb.NewStringValue("100")
	event.ContextFacts[0].Kind = "utterance"
	if _, err := ParseEvent(event); err == nil {
		t.Fatal("speech admitted as confirmed experience")
	}
}

func TestExperienceViewRequiresExplicitSaveMembership(t *testing.T) {
	if view, err := ParseView(&protocol.Observation{}); view != nil || err != nil {
		t.Fatalf("legacy view=%+v err=%v", view, err)
	}
	ext, _ := structpb.NewStruct(map[string]any{Extension: map[string]any{"version": 1, "clock_id": "fake.calendar", "now_tick": "150", "valid_fact_ids": []any{"receipt"}}})
	view, err := ParseView(&protocol.Observation{Extensions: ext})
	if err != nil {
		t.Fatal(err)
	}
	record := &Record{ClockID: "fake.calendar", OccurredTick: 100, FocusUntilTick: 400, Priority: 50}
	if !view.Allows("receipt", record) || view.Allows("future-branch", record) {
		t.Fatal("save membership ignored")
	}
	record.ClockID = "other.clock"
	if view.Allows("receipt", record) {
		t.Fatal("clock scope ignored")
	}
}
