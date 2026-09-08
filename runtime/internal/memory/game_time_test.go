package memory_test

import (
	"encoding/json"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
)

func TestProjectorDistinguishesEmptyAndZeroGameTime(t *testing.T) {
	project := func(gameTime *protocolv1alpha2.GameTime) memory.Record {
		t.Helper()
		record, err := memory.NewProjector(func() time.Time { return time.Unix(100, 0) }).Project(memory.ProjectInput{
			SessionKey:     session.AgentSessionKey{GameID: "fake-game", WorldID: "world", EntityID: "entity"},
			TurnID:         "turn",
			ProjectionKind: memory.ProjectionKindSettledTurn,
			Event:          &protocolv1alpha2.GameEvent{EventId: "event", GameTime: gameTime},
		})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	empty := project(&protocolv1alpha2.GameTime{})
	zero := project(&protocolv1alpha2.GameTime{Tick: ptrInt64(0)})
	if empty.GameTime != nil {
		t.Errorf("empty optional fields projected as %+v, want unknown time", empty.GameTime)
	}
	if zero.GameTime == nil {
		t.Fatal("explicit zero tick was discarded")
	}
	emptyJSON, err := json.Marshal(empty.GameTime)
	if err != nil {
		t.Fatal(err)
	}
	zeroJSON, err := json.Marshal(zero.GameTime)
	if err != nil {
		t.Fatal(err)
	}
	if string(emptyJSON) == string(zeroJSON) {
		t.Errorf("empty and zero time have identical persisted payload: %s", zeroJSON)
	}
	if zero.ProjectionBatchKey != `["fake-game","world","entity","turn","event","settled_turn",2]` {
		t.Errorf("presence-aware projection batch key = %s", zero.ProjectionBatchKey)
	}
}

func TestLegacyGameTimeSnapshotPreservesV1JSON(t *testing.T) {
	const original = `{"Year":1,"Season":2,"Day":3,"Hour":6,"Minute":20,"Tick":9001}`
	var snapshot memory.GameTimeSnapshot
	if err := json.Unmarshal([]byte(original), &snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("v1 payload changed: got %s, want %s", got, original)
	}
}
