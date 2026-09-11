package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"gameagent/runtime/internal/idgen"
	"gameagent/runtime/internal/session"
)

const (
	checkpointStatusAbsent = "absent"
	worldHeadStatusReady   = "ready"
	wakeReasonCreated      = "created"
	wakeStatusPending      = "pending"
)

type Service struct {
	store     *SQLiteStore
	newID     func(string) string
	nowUnixMS func() int64
}

func NewService(store *SQLiteStore) *Service {
	return &Service{
		store:     store,
		newID:     idgen.New,
		nowUnixMS: func() int64 { return time.Now().UnixMilli() },
	}
}

func (s *Service) ActivateWorld(ctx context.Context, world WorldKey, runID string, clock Clock, ref CheckpointRef) (Head, error) {
	if err := validateService(s, ctx); err != nil {
		return Head{}, err
	}
	if err := validateFreshActivation(world, runID, clock, ref); err != nil {
		return Head{}, err
	}

	head := Head{
		Binding: Binding{World: world, RunID: runID, Generation: 1},
		Clock:   clock,
		Status:  worldHeadStatusReady,
	}
	row := worldHeadRow{Head: head}
	headJSON, err := json.Marshal(row)
	if err != nil {
		return Head{}, ErrInvalidTaskSpec
	}
	var result Head
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.store.loadWorldHeadTx(ctx, tx, world)
		if err != nil {
			return err
		}
		if found {
			if err := validateActivationRetry(current, runID, clock); err != nil {
				return err
			}
			result = current.Head
			return nil
		}
		hasState, err := s.store.worldHasDurableStateTx(ctx, tx, world)
		if err != nil {
			return err
		}
		if hasState {
			return ErrWorldNotReady
		}
		if err := s.store.insertWorldHeadTx(ctx, tx, row, headJSON); err != nil {
			return err
		}
		result = head
		return nil
	})
	if err != nil {
		return Head{}, err
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, exec ExecutionContext, spec TaskSpec, admission Admission) (CreateResult, error) {
	if err := validateService(s, ctx); err != nil {
		return CreateResult{}, err
	}
	if err := validateCreateInput(exec, spec, admission); err != nil {
		return CreateResult{}, err
	}

	clonedSpec, err := cloneTaskSpec(spec)
	if err != nil {
		return CreateResult{}, ErrInvalidTaskSpec
	}
	fingerprint, err := taskSpecFingerprint(clonedSpec)
	if err != nil {
		return CreateResult{}, ErrInvalidTaskSpec
	}
	nextWakeAt := clonedSpec.WakeAt
	record := Record{
		ID:                s.newID("task"),
		Owner:             exec.Owner,
		Spec:              clonedSpec,
		State:             StateWaiting,
		Revision:          1,
		CreatedAtGameTick: exec.Clock.Tick,
		CreatedAtUnixMS:   s.nowUnixMS(),
		NextWakeAt:        &nextWakeAt,
		Operations:        []Operation{},
		Evidence:          []Evidence{},
		Cleanup:           []Cleanup{},
	}
	wake := Wake{
		ID:               s.newID("wake"),
		TaskID:           record.ID,
		Owner:            exec.Owner,
		ExpectedRevision: 1,
		DueTick:          clonedSpec.WakeAt,
		Reason:           wakeReasonCreated,
		Status:           wakeStatusPending,
		Generation:       exec.Binding.Generation,
	}
	prepared, prepareErr := s.store.prepareTaskCreate(record, wake)
	var createdResult CreateResult
	if prepareErr == nil {
		if err := json.Unmarshal(prepared.createResponseJSON, &createdResult); err != nil {
			prepareErr = ErrInvalidTaskSpec
		}
	}

	var result CreateResult
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.store.loadWorldHeadTx(ctx, tx, exec.Binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateCreateAuthority(head, exec); err != nil {
			return err
		}

		exact, found, err := s.store.loadExactCreateTx(ctx, tx, exec.Owner, exec.Source, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result = exact
			return nil
		}
		if equivalent, found, err := s.store.loadEquivalentTaskTx(ctx, tx, exec.Owner, clonedSpec.EquivalenceKey); err != nil {
			return err
		} else if found {
			result = CreateResult{Task: equivalent, Created: false}
			return nil
		}
		if err := enforceAdmissionTx(ctx, tx, exec.Owner, admission); err != nil {
			return err
		}
		if exec.Clock.Tick >= clonedSpec.WakeAt || clonedSpec.WakeAt > clonedSpec.DeadlineAt {
			return ErrInvalidTaskSpec
		}
		if prepareErr != nil {
			return prepareErr
		}
		if err := s.store.insertTaskAndWakeTx(ctx, tx, prepared); err != nil {
			return err
		}
		result = createdResult
		return nil
	})
	if err != nil {
		return CreateResult{}, err
	}
	return result, nil
}

func (s *Service) Read(ctx context.Context, owner session.AgentSessionKey, taskID string) (Record, error) {
	if err := validateService(s, ctx); err != nil {
		return Record{}, err
	}
	if err := validateTaskLookup(owner, taskID); err != nil {
		return Record{}, err
	}
	record, err := s.store.loadTask(ctx, owner, taskID)
	if err != nil {
		return Record{}, err
	}
	if err := s.store.validateWorldTaskIdentityGraph(ctx, WorldKey{GameID: owner.GameID, WorldID: owner.WorldID}); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) List(ctx context.Context, owner session.AgentSessionKey, limit int) ([]Record, error) {
	if err := validateService(s, ctx); err != nil {
		return nil, err
	}
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []Record{}, nil
	}
	if err := s.store.validateWorldTaskIdentityGraph(ctx, WorldKey{GameID: owner.GameID, WorldID: owner.WorldID}); err != nil {
		return nil, err
	}
	return s.store.listTasksLimit(ctx, owner, limit)
}

func (s *Service) ApplyIntent(ctx context.Context, exec ExecutionContext, intent Intent) (Record, error) {
	if err := validateService(s, ctx); err != nil {
		return Record{}, err
	}
	if err := validateApplyIntentInput(exec, intent); err != nil {
		return Record{}, err
	}
	request, err := prepareIntentRequest(exec, intent)
	if err != nil {
		return Record{}, err
	}
	clonedIntent := request.request.Intent
	candidateResultID := s.newID("result")
	candidateWakeID := s.newID("wake")

	var result Record
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.store.loadWorldHeadTx(ctx, tx, exec.Binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateCreateAuthority(head, exec); err != nil {
			return err
		}

		exact, found, err := s.store.loadExactIntentTx(ctx, tx, exec.Owner, request)
		if err != nil {
			return err
		}
		if found {
			if exact.Spec.ClockID != exec.Clock.ID {
				return ErrClockMismatch
			}
			result = exact
			return nil
		}

		current, err := s.store.loadIntentTaskTx(ctx, tx, exec.Owner, exec.TaskID)
		if err != nil {
			return err
		}
		if current.record.Spec.ClockID != exec.Clock.ID {
			return ErrClockMismatch
		}
		if taskStateTerminal(current.record.State) {
			return ErrTaskTerminal
		}
		if current.record.Result != nil {
			return ErrInvalidTaskSpec
		}
		if current.record.Revision != exec.ExpectedRevision {
			return ErrTaskChanged
		}
		if current.record.NeedsReconcile || recordHasUnappliedEvidence(current.record) {
			return ErrTaskChanged
		}
		nextRevision, err := NextDurableCounter(current.record.Revision)
		if err != nil {
			return err
		}

		updated := current.record
		updated.Revision = nextRevision
		updated.NeedsReconcile = false
		updated.PauseReason = ""
		updated.NoProgressAttempts = 0
		updated.ReconcileAttempts = 0
		switch clonedIntent.Kind {
		case "wait":
			if clonedIntent.NextWakeAt == nil || exec.Clock.Tick >= *clonedIntent.NextWakeAt || *clonedIntent.NextWakeAt > current.record.Spec.DeadlineAt {
				return ErrInvalidTaskSpec
			}
			updated.State = StateWaiting
			nextWake := *clonedIntent.NextWakeAt
			updated.NextWakeAt = &nextWake
			updated.Result = nil
			if clonedIntent.ProgressNote != "" {
				progress, err := modelProgressJSON(clonedIntent.ProgressNote)
				if err != nil {
					return ErrInvalidTaskSpec
				}
				updated.Progress = progress
			}
		case "cancel":
			updated.State = StateCancelled
			updated.NextWakeAt = nil
			updated.Result = &Result{
				ID: candidateResultID, TaskID: updated.ID, Revision: nextRevision,
				State: StateCancelled, Reason: clonedIntent.Reason, OccurredAt: exec.Clock.Tick,
				EvidenceRefs: []string{}, Source: request.request.Source,
			}
		default:
			return ErrInvalidTaskSpec
		}

		prepared, err := s.store.prepareIntentMutation(current, updated, request)
		if err != nil {
			return err
		}
		if err := s.store.updateIntentRecordTx(ctx, tx, current.record, prepared); err != nil {
			return err
		}
		if err := s.store.consumeIntentWakesTx(ctx, tx, current.record); err != nil {
			return err
		}
		if clonedIntent.Kind == "wait" {
			wake := Wake{
				ID: candidateWakeID, TaskID: updated.ID, Owner: updated.Owner,
				ExpectedRevision: updated.Revision, DueTick: *updated.NextWakeAt,
				Reason: wakeReasonIntentWait, Status: wakeStatusPending,
				Generation: exec.Binding.Generation,
			}
			if err := s.store.insertIntentWakeTx(ctx, tx, updated, wake); err != nil {
				return err
			}
		}
		if err := s.store.storeIntentHistoryTx(ctx, tx, current.record, prepared); err != nil {
			return err
		}
		result = prepared.record
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

func validateApplyIntentInput(exec ExecutionContext, intent Intent) error {
	if err := exec.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(exec.TaskID) || exec.ExpectedRevision == 0 {
		return ErrInvalidTaskSpec
	}
	if !requiredIdentity(exec.Source.EventID) || !requiredIdentity(exec.Source.TurnID) || !requiredIdentity(exec.Source.CallID) {
		return ErrSourceInvalid
	}
	return intent.Validate()
}

func taskStateTerminal(state State) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCancelled
}

func recordHasUnappliedEvidence(record Record) bool {
	for _, evidence := range record.Evidence {
		if !evidence.Applied {
			return true
		}
	}
	return false
}

func validateService(service *Service, ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidTaskSpec
	}
	if service == nil || service.store == nil || service.store.db == nil || service.newID == nil || service.nowUnixMS == nil {
		return ErrTaskConflict
	}
	if err := ctx.Err(); err != nil {
		return WrapError(CodeTaskConflict, err)
	}
	return nil
}

func validateFreshActivation(world WorldKey, runID string, clock Clock, ref CheckpointRef) error {
	if err := world.Validate(); err != nil || !requiredIdentity(runID) {
		return ErrInvalidTaskSpec
	}
	if err := clock.Validate(); err != nil || clock.Sequence > uint64(math.MaxInt64) {
		return ErrInvalidTaskSpec
	}
	if ref.Status != checkpointStatusAbsent {
		switch ref.Status {
		case "unconfirmed":
			return ErrCheckpointUnconfirmed
		case "confirmed":
			return ErrCheckpointMissing
		default:
			return ErrCheckpointInvalid
		}
	}
	if ref.World != world || ref.ID != "" || ref.Checksum != "" || ref.SchemaVersion != 0 || !optionalIdentity(ref.Reason) {
		return ErrCheckpointInvalid
	}
	return nil
}

func validateActivationRetry(current worldHeadRow, runID string, clock Clock) error {
	if current.Head.Status != worldHeadStatusReady {
		return ErrWorldNotReady
	}
	if current.BarrierStatus != "" || current.SaveRequestID != "" {
		return ErrSaveInProgress
	}
	if current.Head.Binding.RunID != runID || current.Head.CheckpointID != "" {
		return ErrTaskChanged
	}
	if current.Head.Clock.ID != clock.ID {
		return ErrClockMismatch
	}
	if clock.Tick < current.Head.Clock.Tick || clock.Sequence < current.Head.Clock.Sequence {
		return ErrClockRewound
	}
	if current.Head.Clock != clock {
		return ErrClockMismatch
	}
	return nil
}

func validateCreateInput(exec ExecutionContext, spec TaskSpec, admission Admission) error {
	if err := exec.Validate(); err != nil {
		return err
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	if err := admission.Validate(); err != nil {
		return ErrInvalidTaskSpec
	}
	if !requiredIdentity(exec.Source.EventID) || !requiredIdentity(exec.Source.TurnID) || !requiredIdentity(exec.Source.CallID) {
		return ErrSourceInvalid
	}
	if spec.ClockID != exec.Clock.ID {
		return ErrClockMismatch
	}
	if !sourceRefsEqual(exec.Source, spec.Source) {
		return ErrSourceInvalid
	}
	return nil
}

func validateCreateAuthority(head worldHeadRow, exec ExecutionContext) error {
	if head.Head.Status != worldHeadStatusReady {
		return ErrWorldNotReady
	}
	if head.BarrierStatus != "" || head.SaveRequestID != "" {
		return ErrSaveInProgress
	}
	if head.Head.Binding != exec.Binding {
		return ErrGenerationStale
	}
	if head.Head.Clock.ID != exec.Clock.ID {
		return ErrClockMismatch
	}
	if exec.Clock.Tick < head.Head.Clock.Tick || exec.Clock.Sequence < head.Head.Clock.Sequence {
		return ErrClockRewound
	}
	if head.Head.Clock != exec.Clock {
		return ErrClockMismatch
	}
	return nil
}

func sourceRefsEqual(left, right SourceRef) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneTaskSpec(spec TaskSpec) (TaskSpec, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return TaskSpec{}, err
	}
	var cloned TaskSpec
	if err := json.Unmarshal(data, &cloned); err != nil {
		return TaskSpec{}, err
	}
	return cloned, nil
}
