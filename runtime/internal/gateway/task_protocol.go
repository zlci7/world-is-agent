package gateway

import (
	"encoding/json"
	"errors"
	"strings"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

type taskWorldBinding struct {
	Binding    task.Binding
	Clock      task.Clock
	Entities   []*protocolv1alpha2.EntityRef
	Checkpoint *task.CheckpointRef
}

type taskProposal struct {
	Clock                task.Clock      `json:"clock"`
	WakeAt               int64           `json:"wake_at"`
	DeadlineAt           int64           `json:"deadline_at"`
	ParticipantEntityIDs []string        `json:"participant_entity_ids"`
	EquivalenceKey       string          `json:"equivalence_key"`
	Payload              json.RawMessage `json:"payload,omitempty"`
}

type taskActionSource struct {
	TaskID        string
	WakeID        string
	OperationID   string
	StartRevision uint64
	Binding       task.Binding
	Contract      taskProposal
}

func taskWorldBindingFromProtocol(value *protocolv1alpha2.WorldBinding) (taskWorldBinding, error) {
	if value == nil {
		return taskWorldBinding{}, task.ErrInvalidTaskSpec
	}
	binding, err := taskBindingFromProtocol(value.GetScope())
	if err != nil {
		return taskWorldBinding{}, err
	}
	clock, err := taskClockFromProtocol(value.GetClock())
	if err != nil {
		return taskWorldBinding{}, err
	}
	entities := make([]*protocolv1alpha2.EntityRef, 0, len(value.GetEntities()))
	for _, entity := range value.GetEntities() {
		if entity == nil {
			return taskWorldBinding{}, task.ErrInvalidTaskSpec
		}
		entities = append(entities, cloneEntityRef(entity))
	}

	result := taskWorldBinding{Binding: binding, Clock: clock, Entities: entities}
	if value.GetCheckpoint() != nil {
		checkpoint, err := taskCheckpointFromProtocol(value.GetCheckpoint())
		if err != nil {
			return taskWorldBinding{}, err
		}
		result.Checkpoint = &checkpoint
	}
	return result, nil
}

func taskWorldBindingToProtocol(value taskWorldBinding) (*protocolv1alpha2.WorldBinding, error) {
	clock, err := taskClockToProtocol(value.Clock)
	if err != nil {
		return nil, err
	}
	entities := make([]*protocolv1alpha2.EntityRef, 0, len(value.Entities))
	for _, entity := range value.Entities {
		if entity == nil {
			return nil, task.ErrInvalidTaskSpec
		}
		entities = append(entities, cloneEntityRef(entity))
	}
	result := &protocolv1alpha2.WorldBinding{Scope: taskScopeToProtocol(value.Binding), Clock: clock, Entities: entities}
	if value.Checkpoint != nil {
		checkpoint, err := taskCheckpointToProtocol(*value.Checkpoint)
		if err != nil {
			return nil, err
		}
		result.Checkpoint = checkpoint
	}
	return result, nil
}

func taskBindingFromProtocol(value *protocolv1alpha2.TaskScope) (task.Binding, error) {
	if value == nil {
		return task.Binding{}, task.ErrInvalidTaskSpec
	}
	binding := task.Binding{
		World: task.WorldKey{GameID: value.GetGameId(), WorldID: value.GetWorldId()},
		RunID: value.GetWorldRunId(), Generation: value.GetExecutionGeneration(),
	}
	if binding.Generation == 0 {
		probe := binding
		probe.Generation = 1
		if err := probe.Validate(); err != nil {
			return task.Binding{}, err
		}
		return binding, nil
	}
	if err := binding.Validate(); err != nil {
		return task.Binding{}, err
	}
	return binding, nil
}

func taskScopeToProtocol(value task.Binding) *protocolv1alpha2.TaskScope {
	return &protocolv1alpha2.TaskScope{
		GameId: value.World.GameID, WorldId: value.World.WorldID, WorldRunId: value.RunID, ExecutionGeneration: value.Generation,
	}
}

func taskClockFromProtocol(value *protocolv1alpha2.WorldClock) (task.Clock, error) {
	if value == nil {
		return task.Clock{}, task.ErrInvalidTaskSpec
	}
	clock := task.Clock{ID: value.GetClockId(), Tick: value.GetNowTick(), Sequence: value.GetSequence()}
	if err := clock.Validate(); err != nil {
		return task.Clock{}, err
	}
	return clock, nil
}

func taskClockToProtocol(value task.Clock) (*protocolv1alpha2.WorldClock, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return &protocolv1alpha2.WorldClock{ClockId: value.ID, NowTick: value.Tick, Sequence: value.Sequence}, nil
}

func taskCheckpointFromProtocol(value *protocolv1alpha2.TaskCheckpointRef) (task.CheckpointRef, error) {
	if value == nil {
		return task.CheckpointRef{}, task.ErrCheckpointInvalid
	}
	result := task.CheckpointRef{
		SchemaVersion: int(value.GetSchemaVersion()),
		World:         task.WorldKey{GameID: value.GetGameId(), WorldID: value.GetWorldId()},
		Status:        value.GetStatus(), ID: value.GetCheckpointId(), Checksum: value.GetChecksum(), Reason: value.GetReason(),
	}
	if err := validateTaskCheckpoint(result); err != nil {
		return task.CheckpointRef{}, err
	}
	return result, nil
}

func taskCheckpointToProtocol(value task.CheckpointRef) (*protocolv1alpha2.TaskCheckpointRef, error) {
	if err := validateTaskCheckpoint(value); err != nil {
		return nil, err
	}
	return &protocolv1alpha2.TaskCheckpointRef{
		SchemaVersion: uint32(value.SchemaVersion), GameId: value.World.GameID, WorldId: value.World.WorldID,
		Status: value.Status, CheckpointId: value.ID, Checksum: value.Checksum, Reason: value.Reason,
	}, nil
}

func validateTaskCheckpoint(value task.CheckpointRef) error {
	if err := value.World.Validate(); err != nil {
		return task.ErrCheckpointInvalid
	}
	switch value.Status {
	case "absent":
		if value.ID == "" && value.Checksum == "" && value.SchemaVersion == 0 && optionalProtocolIdentity(value.Reason) {
			return nil
		}
	case "unconfirmed":
		if value.ID == "" && value.Checksum == "" && value.SchemaVersion == 1 && requiredProtocolIdentity(value.Reason) {
			return nil
		}
	case "confirmed":
		if requiredProtocolIdentity(value.ID) && len(value.Checksum) == 64 && value.SchemaVersion == 1 && value.Reason == "" {
			for _, character := range value.Checksum {
				if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
					return task.ErrCheckpointInvalid
				}
			}
			return nil
		}
	}
	return task.ErrCheckpointInvalid
}

func taskEvidenceFromProtocol(value *protocolv1alpha2.TaskEvidence, source task.SourceRef) (task.Evidence, error) {
	if value == nil {
		return task.Evidence{}, task.ErrInvalidTaskSpec
	}
	binding, err := taskBindingFromProtocol(value.GetScope())
	if err != nil || binding.Generation == 0 {
		return task.Evidence{}, task.ErrInvalidTaskSpec
	}
	details, err := rawJSONFromStruct(value.GetDetails())
	if err != nil {
		return task.Evidence{}, err
	}
	gameTime, err := rawJSONFromGameTime(value.GetGameTime())
	if err != nil {
		return task.Evidence{}, err
	}
	facts, err := rawJSONFromContextFacts(value.GetContextFacts())
	if err != nil {
		return task.Evidence{}, err
	}
	source.GameTime = gameTime
	source.Facts = facts
	result := task.Evidence{
		FactID: value.GetFactId(), TaskID: value.GetTaskId(), OperationID: value.GetOperationId(), Binding: binding,
		StartRevision: value.GetStartRevision(), OccurredAt: value.GetOccurredAt(), Kind: value.GetOutcome(),
		Details: details, Source: source,
	}
	if value.WaitUntil != nil {
		waitUntil := value.GetWaitUntil()
		result.WaitUntil = &waitUntil
	}
	if err := result.Validate(); err != nil {
		return task.Evidence{}, err
	}
	return result, nil
}

func taskEvidenceToProtocol(value task.Evidence) (*protocolv1alpha2.TaskEvidence, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	details, err := structFromRawJSON(value.Details)
	if err != nil {
		return nil, err
	}
	gameTime, err := gameTimeFromRawJSON(value.Source.GameTime)
	if err != nil {
		return nil, err
	}
	facts, err := contextFactsFromRawJSON(value.Source.Facts)
	if err != nil {
		return nil, err
	}
	result := &protocolv1alpha2.TaskEvidence{
		FactId: value.FactID, TaskId: value.TaskID, OperationId: value.OperationID, Scope: taskScopeToProtocol(value.Binding),
		StartRevision: value.StartRevision, OccurredAt: value.OccurredAt, Outcome: value.Kind,
		Details: details, GameTime: gameTime, ContextFacts: facts,
	}
	if value.WaitUntil != nil {
		waitUntil := *value.WaitUntil
		result.WaitUntil = &waitUntil
	}
	return result, nil
}

func taskSpecFromProposal(value *protocolv1alpha2.TaskProposal, instruction string, source task.SourceRef) (task.TaskSpec, error) {
	proposal, err := taskProposalFromProtocol(value)
	if err != nil {
		return task.TaskSpec{}, err
	}
	contract, err := json.Marshal(proposal)
	if err != nil {
		return task.TaskSpec{}, task.ErrInvalidTaskSpec
	}
	result := task.TaskSpec{
		Instruction: instruction, ClockID: proposal.Clock.ID, WakeAt: proposal.WakeAt, DeadlineAt: proposal.DeadlineAt,
		ResultContract: task.ResultContractAuthoritativeEvidence, Contract: contract, EquivalenceKey: proposal.EquivalenceKey, Source: source,
	}
	if err := result.Validate(); err != nil {
		return task.TaskSpec{}, err
	}
	return result, nil
}

func taskProposalFromSpec(value task.TaskSpec) (*protocolv1alpha2.TaskProposal, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	if len(value.Contract) == 0 {
		return nil, nil
	}
	var proposal taskProposal
	if err := json.Unmarshal(value.Contract, &proposal); err != nil || !json.Valid(proposal.Payload) && len(proposal.Payload) != 0 {
		return nil, task.ErrInvalidTaskSpec
	}
	if proposal.Clock.ID != value.ClockID || proposal.WakeAt != value.WakeAt || proposal.DeadlineAt != value.DeadlineAt || proposal.EquivalenceKey != value.EquivalenceKey {
		return nil, task.ErrInvalidTaskSpec
	}
	return taskProposalToProtocol(proposal)
}

func taskProposalFromProtocol(value *protocolv1alpha2.TaskProposal) (taskProposal, error) {
	if value == nil {
		return taskProposal{}, task.ErrInvalidTaskSpec
	}
	clock, err := taskClockFromProtocol(value.GetClock())
	if err != nil || value.GetWakeAt() < 0 || value.GetDeadlineAt() < 0 || value.GetWakeAt() > value.GetDeadlineAt() || !optionalProtocolIdentity(value.GetEquivalenceKey()) {
		return taskProposal{}, task.ErrInvalidTaskSpec
	}
	participants := append([]string(nil), value.GetParticipantEntityIds()...)
	for _, participant := range participants {
		if !requiredProtocolIdentity(participant) {
			return taskProposal{}, task.ErrInvalidTaskSpec
		}
	}
	payload, err := rawJSONFromStruct(value.GetPayload())
	if err != nil {
		return taskProposal{}, err
	}
	return taskProposal{Clock: clock, WakeAt: value.GetWakeAt(), DeadlineAt: value.GetDeadlineAt(), ParticipantEntityIDs: participants, EquivalenceKey: value.GetEquivalenceKey(), Payload: payload}, nil
}

func taskProposalToProtocol(value taskProposal) (*protocolv1alpha2.TaskProposal, error) {
	if err := value.Clock.Validate(); err != nil || value.WakeAt < 0 || value.DeadlineAt < 0 || value.WakeAt > value.DeadlineAt || !optionalProtocolIdentity(value.EquivalenceKey) {
		return nil, task.ErrInvalidTaskSpec
	}
	for _, participant := range value.ParticipantEntityIDs {
		if !requiredProtocolIdentity(participant) {
			return nil, task.ErrInvalidTaskSpec
		}
	}
	payload, err := structFromRawJSON(value.Payload)
	if err != nil {
		return nil, err
	}
	clock, err := taskClockToProtocol(value.Clock)
	if err != nil {
		return nil, err
	}
	return &protocolv1alpha2.TaskProposal{Clock: clock, WakeAt: value.WakeAt, DeadlineAt: value.DeadlineAt, ParticipantEntityIds: append([]string(nil), value.ParticipantEntityIDs...), EquivalenceKey: value.EquivalenceKey, Payload: payload}, nil
}

func taskActionSourceFromRecord(record task.Record, wakeID, operationID string) (*protocolv1alpha2.TaskActionSource, error) {
	if !requiredProtocolIdentity(record.ID) || !optionalProtocolIdentity(wakeID) || !requiredProtocolIdentity(operationID) {
		return nil, task.ErrInvalidTaskSpec
	}
	var operation task.Operation
	found := false
	for _, candidate := range record.Operations {
		if candidate.ID == operationID {
			operation, found = candidate, true
			break
		}
	}
	if !found || operation.Validate() != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	contract, err := taskProposalFromSpec(record.Spec)
	if err != nil {
		return nil, err
	}
	return &protocolv1alpha2.TaskActionSource{
		TaskId: record.ID, WakeId: wakeID, OperationId: operation.ID, StartRevision: operation.StartRevision,
		Scope: taskScopeToProtocol(operation.Binding), TaskContract: contract,
	}, nil
}

func taskActionSourceFromProtocol(value *protocolv1alpha2.TaskActionSource) (taskActionSource, error) {
	if value == nil || !requiredProtocolIdentity(value.GetTaskId()) || !optionalProtocolIdentity(value.GetWakeId()) || !requiredProtocolIdentity(value.GetOperationId()) {
		return taskActionSource{}, task.ErrInvalidTaskSpec
	}
	binding, err := taskBindingFromProtocol(value.GetScope())
	if err != nil || binding.Generation == 0 || task.ValidateDurableCounter(value.GetStartRevision()) != nil {
		return taskActionSource{}, task.ErrInvalidTaskSpec
	}
	contract, err := taskProposalFromProtocol(value.GetTaskContract())
	if err != nil {
		return taskActionSource{}, err
	}
	return taskActionSource{TaskID: value.GetTaskId(), WakeID: value.GetWakeId(), OperationID: value.GetOperationId(), StartRevision: value.GetStartRevision(), Binding: binding, Contract: contract}, nil
}

func taskErrorToProtocol(value error) *protocolv1alpha2.Error {
	if value == nil {
		return nil
	}
	var taskError *task.Error
	if !errors.As(value, &taskError) || taskError == nil || !taskError.Code.Valid() {
		return &protocolv1alpha2.Error{Code: string(task.CodeInvalidTaskSpec), Message: string(task.CodeInvalidTaskSpec)}
	}
	return &protocolv1alpha2.Error{Code: string(taskError.Code), Message: string(taskError.Code)}
}

func taskErrorFromProtocol(value *protocolv1alpha2.Error) (*task.Error, error) {
	if value == nil || !task.Code(value.GetCode()).Valid() {
		return nil, task.ErrInvalidTaskSpec
	}
	return task.WrapError(task.Code(value.GetCode()), nil), nil
}

func rawJSONFromStruct(value *structpb.Struct) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	result, err := protojson.Marshal(value)
	if err != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	return json.RawMessage(result), nil
}

func structFromRawJSON(value json.RawMessage) (*structpb.Struct, error) {
	if len(value) == 0 {
		return nil, nil
	}
	result := &structpb.Struct{}
	if err := protojson.Unmarshal(value, result); err != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	return result, nil
}

func rawJSONFromGameTime(value *protocolv1alpha2.GameTime) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	result, err := protojson.Marshal(value)
	if err != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	return json.RawMessage(result), nil
}

func gameTimeFromRawJSON(value json.RawMessage) (*protocolv1alpha2.GameTime, error) {
	if len(value) == 0 {
		return nil, nil
	}
	result := &protocolv1alpha2.GameTime{}
	if err := protojson.Unmarshal(value, result); err != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	return result, nil
}

func rawJSONFromContextFacts(values []*protocolv1alpha2.ContextFact) (json.RawMessage, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, task.ErrInvalidTaskSpec
		}
		encoded, err := protojson.Marshal(value)
		if err != nil {
			return nil, task.ErrInvalidTaskSpec
		}
		result = append(result, encoded)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	return json.RawMessage(encoded), nil
}

func contextFactsFromRawJSON(value json.RawMessage) ([]*protocolv1alpha2.ContextFact, error) {
	if len(value) == 0 {
		return nil, nil
	}
	var encoded []json.RawMessage
	if err := json.Unmarshal(value, &encoded); err != nil {
		return nil, task.ErrInvalidTaskSpec
	}
	result := make([]*protocolv1alpha2.ContextFact, 0, len(encoded))
	for _, item := range encoded {
		fact := &protocolv1alpha2.ContextFact{}
		if err := protojson.Unmarshal(item, fact); err != nil {
			return nil, task.ErrInvalidTaskSpec
		}
		result = append(result, fact)
	}
	return result, nil
}

func cloneEntityRef(value *protocolv1alpha2.EntityRef) *protocolv1alpha2.EntityRef {
	return &protocolv1alpha2.EntityRef{EntityId: value.GetEntityId(), EntityType: value.GetEntityType(), DisplayName: value.GetDisplayName(), DefinitionId: value.GetDefinitionId()}
}

func requiredProtocolIdentity(value string) bool { return strings.TrimSpace(value) != "" }

func optionalProtocolIdentity(value string) bool {
	return value == "" || requiredProtocolIdentity(value)
}
