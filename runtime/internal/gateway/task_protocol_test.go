package gateway

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/task"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestTaskProtocolWorldBindingRoundTripPreservesAbsentCheckpointZeroClockAndDefinitionID(t *testing.T) {
	input := &protocolv1alpha2.WorldBinding{
		Scope: &protocolv1alpha2.TaskScope{
			GameId:              "test-game",
			WorldId:             "world-1",
			WorldRunId:          "run-1",
			ExecutionGeneration: 0,
		},
		Clock: &protocolv1alpha2.WorldClock{ClockId: "main", NowTick: 0, Sequence: 0},
		Entities: []*protocolv1alpha2.EntityRef{{
			EntityId: "npc:abigail", EntityType: "npc", DisplayName: "Abigail", DefinitionId: "npc:abigail",
		}},
	}

	mapped, err := taskWorldBindingFromProtocol(input)
	if err != nil {
		t.Fatalf("taskWorldBindingFromProtocol() error = %v", err)
	}
	if mapped.Binding.Generation != 0 {
		t.Fatalf("reported generation = %d, want 0", mapped.Binding.Generation)
	}
	if mapped.Clock != (task.Clock{ID: "main", Tick: 0, Sequence: 0}) {
		t.Fatalf("clock = %+v, want main/0/0", mapped.Clock)
	}
	if mapped.Checkpoint != nil {
		t.Fatalf("checkpoint = %+v, want absent nil checkpoint", mapped.Checkpoint)
	}
	if got := mapped.Entities[0].GetDefinitionId(); got != "npc:abigail" {
		t.Fatalf("definition_id = %q, want npc:abigail", got)
	}

	roundTrip, err := taskWorldBindingToProtocol(mapped)
	if err != nil {
		t.Fatalf("taskWorldBindingToProtocol() error = %v", err)
	}
	if roundTrip.GetCheckpoint() != nil {
		t.Fatalf("round-trip checkpoint = %+v, want nil", roundTrip.GetCheckpoint())
	}
	if roundTrip.GetClock().GetNowTick() != 0 || roundTrip.GetClock().GetSequence() != 0 {
		t.Fatalf("round-trip zero clock = %+v", roundTrip.GetClock())
	}
	if got := roundTrip.GetEntities()[0].GetDefinitionId(); got != "npc:abigail" {
		t.Fatalf("round-trip definition_id = %q, want npc:abigail", got)
	}
}

func TestTaskProtocolCheckpointAndInboundIdentityValidation(t *testing.T) {
	absent, err := taskCheckpointFromProtocol(&protocolv1alpha2.TaskCheckpointRef{GameId: "test-game", WorldId: "world-1", Status: "absent"})
	if err != nil || absent.Status != "absent" || absent.SchemaVersion != 0 {
		t.Fatalf("absent task checkpoint = (%+v, %v), want status absent with schema version 0", absent, err)
	}
	absentRoundTrip, err := taskCheckpointToProtocol(absent)
	if err != nil || absentRoundTrip.GetStatus() != "absent" {
		t.Fatalf("absent task checkpoint round trip = (%+v, %v)", absentRoundTrip, err)
	}

	checkpoint, err := taskCheckpointFromProtocol(&protocolv1alpha2.TaskCheckpointRef{
		SchemaVersion: 1, GameId: "test-game", WorldId: "world-1", Status: "confirmed", CheckpointId: "checkpoint-1", Checksum: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("taskCheckpointFromProtocol() error = %v", err)
	}
	if checkpoint != (task.CheckpointRef{SchemaVersion: 1, World: task.WorldKey{GameID: "test-game", WorldID: "world-1"}, Status: "confirmed", ID: "checkpoint-1", Checksum: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}) {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}

	for _, input := range []*protocolv1alpha2.TaskCheckpointRef{
		{GameId: "test-game", WorldId: "world-1", Status: "latest"},
		{GameId: "test-game", WorldId: "world-1", Status: "confirmed", SchemaVersion: 1, CheckpointId: "checkpoint-1", Checksum: "not-a-checksum"},
	} {
		if _, err := taskCheckpointFromProtocol(input); !errors.Is(err, task.ErrCheckpointInvalid) {
			t.Fatalf("taskCheckpointFromProtocol(%+v) error = %v, want ErrCheckpointInvalid", input, err)
		}
	}

	if _, err := taskBindingFromProtocol(&protocolv1alpha2.TaskScope{GameId: " ", WorldId: "world-1", WorldRunId: "run-1", ExecutionGeneration: 1}); !errors.Is(err, task.ErrInvalidTaskSpec) {
		t.Fatalf("missing identity error = %v, want ErrInvalidTaskSpec", err)
	}
}

func TestTaskProtocolEvidenceRoundTripPreservesOriginalFactContentAndWaitUntilPresence(t *testing.T) {
	details := mustTaskProtocolStruct(t, map[string]any{"state": "waiting", "distance": 2.0})
	attributes := mustTaskProtocolStruct(t, map[string]any{"location": "town", "nearby": true})
	waitUntil := int64(0)
	input := &protocolv1alpha2.TaskEvidence{
		FactId:        "fact-1",
		TaskId:        "task-1",
		OperationId:   "operation-1",
		Scope:         &protocolv1alpha2.TaskScope{GameId: "test-game", WorldId: "world-1", WorldRunId: "run-1", ExecutionGeneration: 3},
		StartRevision: 4,
		OccurredAt:    0,
		Outcome:       "progress",
		WaitUntil:     &waitUntil,
		Details:       details,
		GameTime:      &protocolv1alpha2.GameTime{Year: proto.Int32(2), Day: proto.Int32(5), Tick: proto.Int64(0)},
		ContextFacts: []*protocolv1alpha2.ContextFact{{
			Kind: "task_progress", ActorEntityId: "npc:abigail", ScopeId: "task-1", Text: "Waiting at town", Label: "arrival", Attributes: attributes,
		}},
	}
	source := task.SourceRef{Kind: task.SourceKindEnvironment, EventID: "event-1", TurnID: "turn-1", CallID: "call-1"}

	mapped, err := taskEvidenceFromProtocol(input, source)
	if err != nil {
		t.Fatalf("taskEvidenceFromProtocol() error = %v", err)
	}
	if mapped.RevalidatedIn != nil {
		t.Fatalf("inbound RevalidatedIn = %+v, want nil", mapped.RevalidatedIn)
	}
	if mapped.WaitUntil == nil || *mapped.WaitUntil != 0 {
		t.Fatalf("wait_until presence/value = %t/%d, want true/0", mapped.WaitUntil != nil, valueOrMinusOne(mapped.WaitUntil))
	}
	if mapped.OccurredAt != 0 || mapped.StartRevision != 4 || mapped.Kind != task.EvidenceKindProgress {
		t.Fatalf("mapped evidence identity = %+v", mapped)
	}
	if !jsonEqual(mapped.Details, []byte(`{"distance":2,"state":"waiting"}`)) {
		t.Fatalf("details = %s", mapped.Details)
	}
	if !jsonEqual(mapped.Source.GameTime, []byte(`{"day":5,"tick":"0","year":2}`)) {
		t.Fatalf("game_time = %s", mapped.Source.GameTime)
	}
	if !jsonEqual(mapped.Source.Facts, []byte(`[{"actorEntityId":"npc:abigail","attributes":{"location":"town","nearby":true},"kind":"task_progress","label":"arrival","scopeId":"task-1","text":"Waiting at town"}]`)) {
		t.Fatalf("context facts = %s", mapped.Source.Facts)
	}

	roundTrip, err := taskEvidenceToProtocol(mapped)
	if err != nil {
		t.Fatalf("taskEvidenceToProtocol() error = %v", err)
	}
	if roundTrip.WaitUntil == nil || roundTrip.GetWaitUntil() != 0 {
		t.Fatalf("round-trip wait_until presence/value = %t/%d, want true/0", roundTrip.WaitUntil != nil, roundTrip.GetWaitUntil())
	}
	if roundTrip.GetOccurredAt() != 0 || roundTrip.GetGameTime().Tick == nil || roundTrip.GetGameTime().GetTick() != 0 {
		t.Fatalf("round-trip original occurred/game time = %+v", roundTrip)
	}
	if !proto.Equal(roundTrip.GetDetails(), details) || !proto.Equal(roundTrip.GetContextFacts()[0].GetAttributes(), attributes) {
		t.Fatalf("round-trip details or context attributes changed: %+v", roundTrip)
	}

	withoutWait := proto.Clone(input).(*protocolv1alpha2.TaskEvidence)
	withoutWait.WaitUntil = nil
	mappedWithoutWait, err := taskEvidenceFromProtocol(withoutWait, source)
	if err != nil {
		t.Fatalf("taskEvidenceFromProtocol(absent wait_until) error = %v", err)
	}
	if mappedWithoutWait.WaitUntil != nil {
		t.Fatalf("absent wait_until mapped to %+v, want nil", mappedWithoutWait.WaitUntil)
	}
}

func TestTaskProtocolRejectsUnknownEvidenceAndErrorCodes(t *testing.T) {
	base := &protocolv1alpha2.TaskEvidence{
		FactId: "fact-1", TaskId: "task-1", Scope: &protocolv1alpha2.TaskScope{GameId: "test-game", WorldId: "world-1", WorldRunId: "run-1", ExecutionGeneration: 1},
		StartRevision: 1, OccurredAt: 0, Outcome: "unknown",
	}
	if _, err := taskEvidenceFromProtocol(base, task.SourceRef{Kind: task.SourceKindEnvironment}); !errors.Is(err, task.ErrInvalidTaskSpec) {
		t.Fatalf("unknown outcome error = %v, want ErrInvalidTaskSpec", err)
	}
	if _, err := taskErrorFromProtocol(&protocolv1alpha2.Error{Code: "made_up", Message: "unstable"}); !errors.Is(err, task.ErrInvalidTaskSpec) {
		t.Fatalf("unknown error code error = %v, want ErrInvalidTaskSpec", err)
	}

	mapped := taskErrorToProtocol(task.ErrTaskChanged)
	if mapped.GetCode() != "task_changed" || mapped.GetMessage() != "task_changed" || mapped.GetDetails() != nil {
		t.Fatalf("taskErrorToProtocol() = %+v, want stable task_changed code only", mapped)
	}
	back, err := taskErrorFromProtocol(mapped)
	if err != nil || !errors.Is(back, task.ErrTaskChanged) {
		t.Fatalf("taskErrorFromProtocol() = (%v, %v), want ErrTaskChanged/nil", back, err)
	}
}

func TestTaskProtocolLeavesInvalidEvidenceIdentityAndSourceForDomainValidation(t *testing.T) {
	valid := &protocolv1alpha2.TaskEvidence{
		FactId: "fact-1", TaskId: "task-1", Scope: &protocolv1alpha2.TaskScope{GameId: "test-game", WorldId: "world-1", WorldRunId: "run-1", ExecutionGeneration: 1},
		StartRevision: 1, OccurredAt: 0, Outcome: "progress",
	}
	missingIdentity := proto.Clone(valid).(*protocolv1alpha2.TaskEvidence)
	missingIdentity.FactId = " "
	if _, err := taskEvidenceFromProtocol(missingIdentity, task.SourceRef{Kind: task.SourceKindEnvironment}); !errors.Is(err, task.ErrInvalidTaskSpec) {
		t.Fatalf("missing fact identity error = %v, want ErrInvalidTaskSpec", err)
	}
	if _, err := taskEvidenceFromProtocol(valid, task.SourceRef{Kind: "adapter_guess"}); !errors.Is(err, task.ErrSourceInvalid) {
		t.Fatalf("unknown source kind error = %v, want ErrSourceInvalid", err)
	}
}

func TestTaskProtocolProposalRoundTripPreservesStoredContract(t *testing.T) {
	payload := mustTaskProtocolStruct(t, map[string]any{"landmark_id": "bridge", "window": map[string]any{"start": 900.0, "end": 1100.0}})
	proposal := &protocolv1alpha2.TaskProposal{
		Clock: &protocolv1alpha2.WorldClock{ClockId: "main", NowTick: 800, Sequence: 12}, WakeAt: 900, DeadlineAt: 1100,
		ParticipantEntityIds: []string{"npc:abigail", "player:local"}, EquivalenceKey: "meet:bridge", Payload: payload,
	}
	source := task.SourceRef{Kind: task.SourceKindInteraction, EventID: "event-1", TurnID: "turn-1", CallID: "call-1"}

	spec, err := taskSpecFromProposal(proposal, "Meet the player at the bridge", source)
	if err != nil {
		t.Fatalf("taskSpecFromProposal() error = %v", err)
	}
	if spec.ClockID != "main" || spec.WakeAt != 900 || spec.DeadlineAt != 1100 || spec.EquivalenceKey != "meet:bridge" {
		t.Fatalf("spec = %+v", spec)
	}
	roundTrip, err := taskProposalFromSpec(spec)
	if err != nil {
		t.Fatalf("taskProposalFromSpec() error = %v", err)
	}
	if roundTrip.GetClock().GetNowTick() != 800 || roundTrip.GetClock().GetSequence() != 12 || !reflect.DeepEqual(roundTrip.GetParticipantEntityIds(), []string{"npc:abigail", "player:local"}) || roundTrip.GetEquivalenceKey() != "meet:bridge" || !proto.Equal(roundTrip.GetPayload(), payload) {
		t.Fatalf("proposal round trip = %+v", roundTrip)
	}
}

func TestTaskProtocolStartRevisionRoundTrip(t *testing.T) {
	binding := task.Binding{World: task.WorldKey{GameID: "test-game", WorldID: "world-1"}, RunID: "run-1", Generation: 3}
	proposal := &protocolv1alpha2.TaskProposal{Clock: &protocolv1alpha2.WorldClock{ClockId: "main", NowTick: 100, Sequence: 2}, WakeAt: 120, DeadlineAt: 200, ParticipantEntityIds: []string{"npc:abigail"}, EquivalenceKey: "meet:bridge", Payload: mustTaskProtocolStruct(t, map[string]any{"landmark_id": "bridge"})}
	spec, err := taskSpecFromProposal(proposal, "Meet at bridge", task.SourceRef{Kind: task.SourceKindTaskWake, EventID: "event-1", TurnID: "turn-1", CallID: "call-1"})
	if err != nil {
		t.Fatal(err)
	}
	record := task.Record{
		ID: "task-1", Revision: 5, Spec: spec,
		Operations: []task.Operation{{ID: "operation-1", ActionID: "action-1", CommandFingerprint: "fingerprint-1", StartRevision: 4, Binding: binding, Status: task.OperationStatusRegistered}},
	}

	actionSource, err := taskActionSourceFromRecord(record, "wake-1", "operation-1")
	if err != nil {
		t.Fatalf("taskActionSourceFromRecord() error = %v", err)
	}
	if actionSource.GetStartRevision() != 4 {
		t.Fatalf("TaskActionSource.start_revision = %d, want registered operation revision 4", actionSource.GetStartRevision())
	}

	evidence, err := taskEvidenceFromProtocol(&protocolv1alpha2.TaskEvidence{
		FactId: "fact-1", TaskId: "task-1", OperationId: "operation-1", Scope: taskScopeToProtocol(binding), StartRevision: actionSource.GetStartRevision(), OccurredAt: 150, Outcome: "progress",
	}, task.SourceRef{Kind: task.SourceKindEnvironment, EventID: "event-2", TurnID: "turn-2", CallID: "call-2"})
	if err != nil {
		t.Fatalf("taskEvidenceFromProtocol() error = %v", err)
	}
	if evidence.StartRevision != 4 {
		t.Fatalf("TaskEvidence.start_revision = %d, want 4", evidence.StartRevision)
	}
	roundTrip, err := taskEvidenceToProtocol(evidence)
	if err != nil || roundTrip.GetStartRevision() != 4 {
		t.Fatalf("TaskEvidence round trip = (%+v, %v), want start_revision 4", roundTrip, err)
	}
}

func TestLegacyProtocolMessagesRemainAccepted(t *testing.T) {
	legacy := &protocolv1alpha2.GameEvent{EventId: "event-legacy", EventType: "player_spoke", WorldId: "world-1", TargetEntityId: "npc:abigail", Entities: []*protocolv1alpha2.EntityRef{{EntityId: "npc:abigail", EntityType: "npc"}}}
	if _, err := resolveAgentTarget(agent.ConnectionContext{GameID: "test-game"}, legacy); err != nil {
		t.Fatalf("legacy GameEvent rejected: %v", err)
	}
}

func mustTaskProtocolStruct(t *testing.T, fields map[string]any) *structpb.Struct {
	t.Helper()
	value, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func valueOrMinusOne(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

func jsonEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}
