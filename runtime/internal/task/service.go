package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"gameagent/runtime/internal/idgen"
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
	if err := admission.Validate(); err != nil || admission.MaxActivePerOwner != 0 {
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
