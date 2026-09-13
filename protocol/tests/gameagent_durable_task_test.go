package protocolv1alpha2_test

import (
	"testing"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
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

	var hello protocolv1alpha2.AdapterHello
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
	scope := &protocolv1alpha2.TaskScope{GameId: "stardew-smapi", WorldId: "Farm_1", WorldRunId: "run_9", ExecutionGeneration: 42}
	clock := &protocolv1alpha2.WorldClock{ClockId: "main", NowTick: 1234, Sequence: 99}
	evidence := &protocolv1alpha2.TaskEvidence{
		FactId: "fact_1", TaskId: "task_1", OperationId: "op_1", Scope: scope,
		StartRevision: 18446744073709551615, OccurredAt: 1234, Outcome: "progress",
		WaitUntil: proto.Int64(1400), Details: details,
		GameTime:     &protocolv1alpha2.GameTime{Year: proto.Int32(2), Tick: proto.Int64(1234)},
		ContextFacts: []*protocolv1alpha2.ContextFact{{Kind: "task_progress", ScopeId: "task_1", Text: "waiting"}},
	}
	proposal := &protocolv1alpha2.TaskProposal{Clock: clock, WakeAt: 1400, DeadlineAt: 2000, ParticipantEntityIds: []string{"npc:Abigail", "player:local"}, EquivalenceKey: "meet:abigail", Payload: details}

	adapter := &protocolv1alpha2.AdapterMessage{MessageId: "binding_1", CorrelationId: "corr_1", Payload: &protocolv1alpha2.AdapterMessage_WorldBinding{WorldBinding: &protocolv1alpha2.WorldBinding{
		Scope: scope, Clock: clock, Entities: []*protocolv1alpha2.EntityRef{{EntityId: "npc:Abigail", EntityType: "npc", DefinitionId: "npc:Abigail"}},
		Checkpoint: &protocolv1alpha2.TaskCheckpointRef{SchemaVersion: 1, GameId: "stardew-smapi", WorldId: "Farm_1", Status: "confirmed", CheckpointId: "checkpoint_1", Checksum: "sha256", Reason: "load"},
	}}}
	encoded, err := proto.Marshal(adapter)
	if err != nil {
		t.Fatalf("marshal WorldBinding: %v", err)
	}
	var decoded protocolv1alpha2.AdapterMessage
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal WorldBinding: %v", err)
	}
	binding := decoded.GetWorldBinding()
	if binding == nil || binding.GetScope().GetExecutionGeneration() != 42 || binding.GetClock().GetSequence() != 99 || binding.GetCheckpoint().GetStatus() != "confirmed" {
		t.Fatalf("WorldBinding round trip lost nested values: %+v", binding)
	}

	action := &protocolv1alpha2.ActionRequest{ActionId: "action_1", EntityId: "npc:Abigail", Capability: "move_to", WorldId: "Farm_1", TaskSource: &protocolv1alpha2.TaskActionSource{
		TaskId: "task_1", StartRevision: evidence.GetStartRevision(), WakeId: "wake_1", OperationId: "op_1", Scope: scope, TaskContract: proposal,
	}}
	result := &protocolv1alpha2.ActionResult{ActionId: "action_1", Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED, TaskProposal: proposal, TaskEvidence: []*protocolv1alpha2.TaskEvidence{evidence}}
	event := &protocolv1alpha2.GameEvent{EventId: "event_1", EventType: "task_progress", TaskEvidence: []*protocolv1alpha2.TaskEvidence{evidence}, InteractionSource: &protocolv1alpha2.InteractionSource{SourceId: "source_1", Scope: scope, PlayerEntityId: "player:local", TaskId: "task_1", OperationId: "op_1", Kind: "task_arrival"}}
	observation := &protocolv1alpha2.Observation{EntityId: "npc:Abigail", TaskEvidence: []*protocolv1alpha2.TaskEvidence{evidence}}
	actionBytes, err := proto.Marshal(action)
	if err != nil {
		t.Fatalf("marshal ActionRequest: %v", err)
	}
	var decodedAction protocolv1alpha2.ActionRequest
	if err := proto.Unmarshal(actionBytes, &decodedAction); err != nil {
		t.Fatalf("unmarshal ActionRequest: %v", err)
	}
	resultBytes, err := proto.Marshal(result)
	if err != nil {
		t.Fatalf("marshal ActionResult: %v", err)
	}
	var decodedResult protocolv1alpha2.ActionResult
	if err := proto.Unmarshal(resultBytes, &decodedResult); err != nil {
		t.Fatalf("unmarshal ActionResult: %v", err)
	}
	eventBytes, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal GameEvent: %v", err)
	}
	var decodedEvent protocolv1alpha2.GameEvent
	if err := proto.Unmarshal(eventBytes, &decodedEvent); err != nil {
		t.Fatalf("unmarshal GameEvent: %v", err)
	}
	observationBytes, err := proto.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal Observation: %v", err)
	}
	var decodedObservation protocolv1alpha2.Observation
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
	scope := &protocolv1alpha2.TaskScope{GameId: "game", WorldId: "world", WorldRunId: "run", ExecutionGeneration: 7}
	adapterMessages := []*protocolv1alpha2.AdapterMessage{
		{Payload: &protocolv1alpha2.AdapterMessage_WorldClock{WorldClock: &protocolv1alpha2.WorldClockUpdate{Scope: scope, Clock: &protocolv1alpha2.WorldClock{ClockId: "clock", NowTick: 8, Sequence: 9}}}},
		{Payload: &protocolv1alpha2.AdapterMessage_CheckpointPrepare{CheckpointPrepare: &protocolv1alpha2.CheckpointPrepare{Scope: scope, Clock: &protocolv1alpha2.WorldClock{ClockId: "clock"}, SaveRequestId: "save_1"}}},
		{Payload: &protocolv1alpha2.AdapterMessage_CheckpointFinish{CheckpointFinish: &protocolv1alpha2.CheckpointFinish{Scope: scope, SaveRequestId: "save_1", Saved: true}}},
		{Payload: &protocolv1alpha2.AdapterMessage_TaskControlResult{TaskControlResult: &protocolv1alpha2.TaskControlResult{Scope: scope, TaskId: "task_1", OperationId: "op_1", RequestId: "request_1", Status: "released"}}},
	}
	runtimeMessages := []*protocolv1alpha2.RuntimeMessage{
		{Payload: &protocolv1alpha2.RuntimeMessage_WorldBindingReady{WorldBindingReady: &protocolv1alpha2.WorldBindingReady{Scope: scope, Status: "ready"}}},
		{Payload: &protocolv1alpha2.RuntimeMessage_CheckpointPrepared{CheckpointPrepared: &protocolv1alpha2.CheckpointPrepared{Scope: scope, SaveRequestId: "save_1", Checkpoint: &protocolv1alpha2.TaskCheckpointRef{Status: "confirmed"}}}},
		{Payload: &protocolv1alpha2.RuntimeMessage_TaskControl{TaskControl: &protocolv1alpha2.TaskControlRequest{Scope: scope, TaskId: "task_1", OperationId: "op_1", RequestId: "request_1", Reason: "player_interaction"}}},
	}
	for index, message := range adapterMessages {
		encoded, err := proto.Marshal(message)
		if err != nil {
			t.Fatalf("marshal adapter control envelope: %v", err)
		}
		var decoded protocolv1alpha2.AdapterMessage
		if err := proto.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		field := decoded.ProtoReflect().WhichOneof(decoded.ProtoReflect().Descriptor().Oneofs().ByName("payload"))
		if field == nil || field.Number() != []protoreflect.FieldNumber{19, 20, 21, 22}[index] {
			t.Fatalf("adapter control oneof lost: %v", &decoded)
		}
		if !proto.Equal(message, &decoded) {
			t.Fatalf("adapter payload sentinels lost: got %v want %v", &decoded, message)
		}
	}
	for index, message := range runtimeMessages {
		encoded, err := proto.Marshal(message)
		if err != nil {
			t.Fatalf("marshal runtime control envelope: %v", err)
		}
		var decoded protocolv1alpha2.RuntimeMessage
		if err := proto.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		field := decoded.ProtoReflect().WhichOneof(decoded.ProtoReflect().Descriptor().Oneofs().ByName("payload"))
		if field == nil || field.Number() != []protoreflect.FieldNumber{18, 19, 20}[index] {
			t.Fatalf("runtime control oneof lost: %v", &decoded)
		}
		if !proto.Equal(message, &decoded) {
			t.Fatalf("runtime payload sentinels lost: got %v want %v", &decoded, message)
		}
	}
}
