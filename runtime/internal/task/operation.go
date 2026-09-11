package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"gameagent/runtime/internal/session"
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

		storedOperation, storedOwner, storedTaskID, storedClockID, found, err := s.store.loadWorldOperationTx(ctx, tx, exec.Binding.World, cloned.ID)
		if err != nil {
			return err
		}
		if found {
			if storedOwner != exec.Owner || storedTaskID != exec.TaskID || !operationsEqual(storedOperation, cloned) {
				return ErrIdempotencyConflict
			}
			if storedClockID != exec.Clock.ID {
				return ErrClockMismatch
			}
			if storedOperation.Binding != exec.Binding {
				return ErrGenerationStale
			}
			result = storedOperation
			return nil
		}

		if err := validateNewOperation(exec, cloned); err != nil {
			return err
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

func (s *SQLiteStore) loadWorldOperationTx(ctx context.Context, tx *sql.Tx, world WorldKey, operationID string) (Operation, session.AgentSessionKey, string, string, bool, error) {
	rows, err := tx.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id`,
		world.GameID, world.WorldID)
	if err != nil {
		return Operation{}, session.AgentSessionKey{}, "", "", false, err
	}
	defer rows.Close()

	var (
		matchedOperation Operation
		matchedOwner     session.AgentSessionKey
		matchedTaskID    string
		matchedClockID   string
		matches          int
	)
	for rows.Next() {
		record, err := scanTaskRow(rows)
		if err != nil {
			return Operation{}, session.AgentSessionKey{}, "", "", false, err
		}
		for _, operation := range record.Operations {
			if operation.ID != operationID {
				continue
			}
			matches++
			matchedOperation, matchedOwner, matchedTaskID, matchedClockID = operation, record.Owner, record.ID, record.Spec.ClockID
		}
	}
	if err := rows.Err(); err != nil {
		return Operation{}, session.AgentSessionKey{}, "", "", false, err
	}
	if matches > 1 {
		return Operation{}, session.AgentSessionKey{}, "", "", false, ErrIdempotencyConflict
	}
	if matches == 0 {
		return Operation{}, session.AgentSessionKey{}, "", "", false, nil
	}
	return matchedOperation, matchedOwner, matchedTaskID, matchedClockID, true, nil
}
