package protocolv1alpha2

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestDurableTaskLegacyAdapterHelloFieldsRemainDecodable(t *testing.T) {
	legacy := make([]byte, 0)
	for number, value := range map[protowire.Number]string{
		1: "adapter", 2: "1.0", 3: "v1alpha2", 4: "game", 5: "1.6", 6: "session",
	} {
		legacy = protowire.AppendTag(legacy, number, protowire.BytesType)
		legacy = protowire.AppendString(legacy, value)
	}

	var hello AdapterHello
	if err := proto.Unmarshal(legacy, &hello); err != nil {
		t.Fatalf("unmarshal legacy AdapterHello: %v", err)
	}
	if hello.GetAdapterId() != "adapter" || hello.GetSessionId() != "session" {
		t.Fatalf("legacy AdapterHello = %+v, want original field-number values", hello)
	}
	if got := hello.GetSupportedExtensions(); len(got) != 0 {
		t.Fatalf("legacy supported extensions = %v, want empty", got)
	}
}

func TestDurableTaskWireRoundTripsNestedTaskValues(t *testing.T) {
	details, err := structpb.NewStruct(map[string]any{"reason": "meeting"})
	if err != nil {
		t.Fatal(err)
	}
	scope := &TaskScope{GameId: "stardew-smapi", WorldId: "Farm_1", WorldRunId: "run_9", ExecutionGeneration: 42}
	clock := &WorldClock{ClockId: "main", NowTick: 1234, Sequence: 99}
	evidence := &TaskEvidence{
		FactId: "fact_1", TaskId: "task_1", OperationId: "op_1", Scope: scope,
		StartRevision: 18446744073709551615, OccurredAt: 1234, Outcome: "progress",
		WaitUntil: proto.Int64(1400), Details: details,
		GameTime: &GameTime{Year: proto.Int32(2), Tick: proto.Int64(1234)},
		ContextFacts: []*ContextFact{{Kind: "task_progress", ScopeId: "task_1", Text: "waiting"}},
	}
	proposal := &TaskProposal{Clock: clock, WakeAt: 1400, DeadlineAt: 2000, ParticipantEntityIds: []string{"npc:Abigail", "player:local"}, EquivalenceKey: "meet:abigail", Payload: details}

	adapter := &AdapterMessage{MessageId: "binding_1", CorrelationId: "corr_1", Payload: &AdapterMessage_WorldBinding{WorldBinding: &WorldBinding{
		Scope: scope, Clock: clock, Entities: []*EntityRef{{EntityId: "npc:Abigail", EntityType: "npc", DefinitionId: "npc:Abigail"}},
		Checkpoint: &TaskCheckpointRef{SchemaVersion: 1, GameId: "stardew-smapi", WorldId: "Farm_1", Status: "confirmed", CheckpointId: "checkpoint_1", Checksum: "sha256", Reason: "load"},
	}}}
	encoded, err := proto.Marshal(adapter)
	if err != nil {
		t.Fatalf("marshal WorldBinding: %v", err)
	}
	var decoded AdapterMessage
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal WorldBinding: %v", err)
	}
	binding := decoded.GetWorldBinding()
	if binding == nil || binding.GetScope().GetExecutionGeneration() != 42 || binding.GetClock().GetSequence() != 99 || binding.GetCheckpoint().GetStatus() != "confirmed" {
		t.Fatalf("WorldBinding round trip lost nested values: %+v", binding)
	}

	action := &ActionRequest{ActionId: "action_1", EntityId: "npc:Abigail", Capability: "move_to", WorldId: "Farm_1", TaskSource: &TaskActionSource{
		TaskId: "task_1", StartRevision: evidence.GetStartRevision(), WakeId: "wake_1", OperationId: "op_1", Scope: scope, TaskContract: proposal,
	}}
	result := &ActionResult{ActionId: "action_1", Status: ActionStatus_ACTION_STATUS_SUCCEEDED, TaskProposal: proposal, TaskEvidence: []*TaskEvidence{evidence}}
	event := &GameEvent{EventId: "event_1", EventType: "task_progress", TaskEvidence: []*TaskEvidence{evidence}, InteractionSource: &InteractionSource{SourceId: "source_1", Scope: scope, PlayerEntityId: "player:local", TaskId: "task_1", OperationId: "op_1", Kind: "task_arrival"}}
	observation := &Observation{EntityId: "npc:Abigail", TaskEvidence: []*TaskEvidence{evidence}}
	actionBytes, err := proto.Marshal(action)
	if err != nil {
		t.Fatalf("marshal ActionRequest: %v", err)
	}
	var decodedAction ActionRequest
	if err := proto.Unmarshal(actionBytes, &decodedAction); err != nil {
		t.Fatalf("unmarshal ActionRequest: %v", err)
	}
	resultBytes, err := proto.Marshal(result)
	if err != nil {
		t.Fatalf("marshal ActionResult: %v", err)
	}
	var decodedResult ActionResult
	if err := proto.Unmarshal(resultBytes, &decodedResult); err != nil {
		t.Fatalf("unmarshal ActionResult: %v", err)
	}
	eventBytes, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal GameEvent: %v", err)
	}
	var decodedEvent GameEvent
	if err := proto.Unmarshal(eventBytes, &decodedEvent); err != nil {
		t.Fatalf("unmarshal GameEvent: %v", err)
	}
	observationBytes, err := proto.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal Observation: %v", err)
	}
	var decodedObservation Observation
	if err := proto.Unmarshal(observationBytes, &decodedObservation); err != nil {
		t.Fatalf("unmarshal Observation: %v", err)
	}
	if decodedAction.GetTaskSource().GetStartRevision() != evidence.GetStartRevision() {
		t.Fatalf("TaskActionSource.start_revision = %d, want %d", decodedAction.GetTaskSource().GetStartRevision(), evidence.GetStartRevision())
	}
	decodedEvidence := decodedResult.GetTaskEvidence()[0]
	if decodedEvidence.WaitUntil == nil || decodedEvidence.GetWaitUntil() != 1400 {
		t.Fatalf("TaskEvidence.wait_until presence/value = %t/%d, want true/1400", decodedEvidence.WaitUntil != nil, decodedEvidence.GetWaitUntil())
	}
	if decodedEvent.GetInteractionSource().GetKind() != "task_arrival" || decodedObservation.GetTaskEvidence()[0].GetFactId() != "fact_1" {
		t.Fatal("GameEvent or Observation round trip lost durable-task values")
	}
}

func TestDurableTaskControlEnvelopeVariantsRoundTrip(t *testing.T) {
	scope := &TaskScope{GameId: "game", WorldId: "world", WorldRunId: "run", ExecutionGeneration: 7}
	adapterMessages := []*AdapterMessage{
		{Payload: &AdapterMessage_WorldClock{WorldClock: &WorldClockUpdate{Scope: scope, Clock: &WorldClock{ClockId: "clock", NowTick: 8, Sequence: 9}}}},
		{Payload: &AdapterMessage_CheckpointPrepare{CheckpointPrepare: &CheckpointPrepare{Scope: scope, Clock: &WorldClock{ClockId: "clock"}, SaveRequestId: "save_1"}}},
		{Payload: &AdapterMessage_CheckpointFinish{CheckpointFinish: &CheckpointFinish{Scope: scope, SaveRequestId: "save_1", Saved: true}}},
		{Payload: &AdapterMessage_TaskControlResult{TaskControlResult: &TaskControlResult{Scope: scope, TaskId: "task_1", OperationId: "op_1", RequestId: "request_1", Status: "released"}}},
	}
	runtimeMessages := []*RuntimeMessage{
		{Payload: &RuntimeMessage_WorldBindingReady{WorldBindingReady: &WorldBindingReady{Scope: scope, Status: "ready"}}},
		{Payload: &RuntimeMessage_CheckpointPrepared{CheckpointPrepared: &CheckpointPrepared{Scope: scope, SaveRequestId: "save_1", Checkpoint: &TaskCheckpointRef{Status: "confirmed"}}}},
		{Payload: &RuntimeMessage_TaskControl{TaskControl: &TaskControlRequest{Scope: scope, TaskId: "task_1", OperationId: "op_1", RequestId: "request_1", Reason: "player_interaction"}}},
	}
	for _, message := range adapterMessages {
		encoded, err := proto.Marshal(message)
		if err != nil {
			t.Fatalf("marshal adapter control envelope: %v", err)
		}
		if len(encoded) == 0 {
			t.Fatal("adapter control envelope encoded empty")
		}
	}
	for _, message := range runtimeMessages {
		encoded, err := proto.Marshal(message)
		if err != nil {
			t.Fatalf("marshal runtime control envelope: %v", err)
		}
		if len(encoded) == 0 {
			t.Fatal("runtime control envelope encoded empty")
		}
	}
}
