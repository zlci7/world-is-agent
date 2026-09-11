package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const noRevisionStageCleanupMerged = "cleanup_merged"

func (s *Service) RecordCleanup(ctx context.Context, binding Binding, taskID string, cleanup Cleanup) error {
	if err := validateService(s, ctx); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(taskID) {
		return ErrInvalidTaskSpec
	}
	cloned, err := cloneCleanup(cleanup)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	if err := cloned.Validate(); err != nil {
		return err
	}

	return s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.store.loadWorldHeadTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateEvidenceAuthority(head, binding); err != nil {
			return err
		}

		current, err := s.store.loadWorldTaskByIDTx(ctx, tx, binding.World, taskID)
		if errors.Is(err, ErrEvidenceConflict) {
			return ErrTaskConflict
		}
		if err != nil {
			return err
		}
		if current.record.Spec.ClockID != head.Head.Clock.ID {
			return ErrClockMismatch
		}
		graph, err := s.store.loadWorldTaskIdentityGraphTx(ctx, tx, binding.World)
		if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
			return ErrTaskConflict
		}
		if err != nil {
			return err
		}
		identity, found := graph.operations[cloned.OperationID]
		if !found || identity.owner != current.record.Owner || identity.taskID != current.record.ID {
			return ErrTaskChanged
		}
		if identity.operation.Binding.World != binding.World {
			return ErrWorldMismatch
		}
		if identity.operation.Binding.RunID != binding.RunID || identity.operation.Binding.Generation > binding.Generation {
			return ErrGenerationStale
		}

		updated := current.record
		updated.Cleanup = append([]Cleanup(nil), current.record.Cleanup...)
		for index, existing := range current.record.Cleanup {
			if existing.OperationID != cloned.OperationID {
				continue
			}
			if cleanupsEqual(existing, cloned) {
				return nil
			}
			if existing.Status != CleanupStatusUnconfirmed ||
				(cloned.Status != CleanupStatusReleased && cloned.Status != CleanupStatusHandedOff) {
				return ErrIdempotencyConflict
			}
			updated.Cleanup[index] = cloned
			return s.store.persistCleanupTx(ctx, tx, current, updated)
		}

		updated.Cleanup = append(updated.Cleanup, cloned)
		return s.store.persistCleanupTx(ctx, tx, current, updated)
	})
}

func cloneCleanup(cleanup Cleanup) (Cleanup, error) {
	data, err := json.Marshal(cleanup)
	if err != nil {
		return Cleanup{}, err
	}
	var cloned Cleanup
	if err := json.Unmarshal(data, &cloned); err != nil {
		return Cleanup{}, err
	}
	return cloned, nil
}

func (s *SQLiteStore) persistCleanupTx(ctx context.Context, tx *sql.Tx, current storedIntentTask, updated Record) error {
	prepared, err := s.prepareCleanupMutation(current, updated)
	if err != nil {
		return err
	}
	return s.updateNoRevisionRecordTx(ctx, tx, current.record, prepared, noRevisionStageCleanupMerged)
}

func (s *SQLiteStore) prepareCleanupMutation(current storedIntentTask, updated Record) (preparedNoRevisionMutation, error) {
	if err := updated.Validate(); err != nil {
		return preparedNoRevisionMutation{}, err
	}
	expected := current.record
	expected.Cleanup = updated.Cleanup
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return preparedNoRevisionMutation{}, ErrInvalidTaskSpec
	}
	recordJSON, err := json.Marshal(updated)
	if err != nil || !bytes.Equal(recordJSON, expectedJSON) {
		return preparedNoRevisionMutation{}, ErrInvalidTaskSpec
	}
	createResponseJSON, err := json.Marshal(initialCreateResult(current.record))
	if err != nil {
		return preparedNoRevisionMutation{}, ErrInvalidTaskSpec
	}
	intentHistoryJSON, err := json.Marshal(current.history)
	if err != nil {
		return preparedNoRevisionMutation{}, ErrInvalidTaskSpec
	}
	baseRecordJSON, projectedJSON, cleanupFootprint, err := cleanupCapacityRecords(updated)
	if err != nil {
		return preparedNoRevisionMutation{}, err
	}
	parts := []int{len(baseRecordJSON), len(createResponseJSON), len(intentHistoryJSON)}
	if taskStateTerminal(updated.State) {
		parts = append(parts, len(projectedJSON))
	} else {
		structuralAndCleanupReserve := intentTerminalStructuralReserve
		if cleanupFootprint > structuralAndCleanupReserve {
			structuralAndCleanupReserve = cleanupFootprint
		}
		parts = append(parts, len(baseRecordJSON), structuralAndCleanupReserve)
	}
	if !taskBytesFit(s.options.MaxTaskBytes, parts...) {
		return preparedNoRevisionMutation{}, ErrInvalidTaskSpec
	}
	return preparedNoRevisionMutation{record: updated, recordJSON: recordJSON}, nil
}

func cleanupsEqual(left, right Cleanup) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
