package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const noRevisionStageOperationMerged = "operation_merged"

func (s *Service) RegisterOperation(ctx context.Context, exec ExecutionContext, operation Operation) (Operation, error) {
	if err := validateService(s, ctx); err != nil {
		return Operation{}, err
	}
	if err := validateOperationExecution(exec); err != nil {
		return Operation{}, err
	}
	if !requiredIdentity(operation.ID) {
		return Operation{}, ErrInvalidTaskSpec
	}
	cloned, err := cloneOperation(operation)
	if err != nil {
		return Operation{}, ErrInvalidTaskSpec
	}

	var result Operation
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.loadWorldHeadForMutationTx(ctx, tx, exec.Binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateCreateAuthority(head, exec); err != nil {
			return err
		}

		current, err := s.store.loadIntentTaskTx(ctx, tx, exec.Owner, exec.TaskID)
		if err != nil {
			return err
		}
		graph, err := s.store.loadWorldTaskIdentityGraphTx(ctx, tx, exec.Binding.World)
		if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
			return ErrIdempotencyConflict
		}
		if err != nil {
			return err
		}
		stored, found := graph.operations[cloned.ID]
		if found {
			if stored.owner != exec.Owner || stored.taskID != exec.TaskID || !operationsEqual(stored.operation, cloned) {
				return ErrIdempotencyConflict
			}
			if stored.clockID != exec.Clock.ID {
				return ErrClockMismatch
			}
			if stored.operation.Binding.World != exec.Binding.World {
				return ErrWorldMismatch
			}
			if stored.operation.Binding.RunID != exec.Binding.RunID || stored.operation.Binding.Generation > exec.Binding.Generation {
				return ErrGenerationStale
			}
			result = stored.operation
			return nil
		}

		if err := validateNewOperation(exec, cloned); err != nil {
			return err
		}
		if current.record.Spec.ClockID != exec.Clock.ID {
			return ErrClockMismatch
		}
		if current.record.NeedsReconcile || recordHasUnappliedEvidence(current.record) {
			return ErrTaskChanged
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
		if _, err := NextDurableCounter(current.record.Revision); err != nil {
			return err
		}

		updated := current.record
		updated.Operations = append(cloneOperations(current.record.Operations), cloned)
		prepared, err := s.store.prepareNoRevisionMutation(current, updated)
		if err != nil {
			return err
		}
		if err := s.store.updateNoRevisionRecordTx(ctx, tx, current.record, prepared, noRevisionStageOperationMerged); err != nil {
			return err
		}
		result = cloned
		return nil
	})
	if err != nil {
		return Operation{}, err
	}
	return result, nil
}

func validateOperationExecution(exec ExecutionContext) error {
	if err := exec.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(exec.TaskID) || exec.ExpectedRevision == 0 {
		return ErrInvalidTaskSpec
	}
	return nil
}

func validateNewOperation(exec ExecutionContext, operation Operation) error {
	if err := operation.Validate(); err != nil {
		return err
	}
	if operation.Status != OperationStatusRegistered || len(operation.Receipt) != 0 {
		return ErrInvalidTaskSpec
	}
	if operation.Binding.World != exec.Binding.World {
		return ErrWorldMismatch
	}
	if operation.Binding != exec.Binding {
		return ErrGenerationStale
	}
	if operation.StartRevision != exec.ExpectedRevision {
		return ErrTaskChanged
	}
	return nil
}

func cloneOperation(operation Operation) (Operation, error) {
	data, err := json.Marshal(operation)
	if err != nil {
		return Operation{}, err
	}
	var cloned Operation
	if err := json.Unmarshal(data, &cloned); err != nil {
		return Operation{}, err
	}
	return cloned, nil
}

func cloneOperations(operations []Operation) []Operation {
	if len(operations) == 0 {
		return []Operation{}
	}
	cloned := make([]Operation, len(operations))
	copy(cloned, operations)
	return cloned
}

func operationsEqual(left, right Operation) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
