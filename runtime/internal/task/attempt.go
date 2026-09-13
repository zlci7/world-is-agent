package task

import (
	"context"
	"database/sql"
	"encoding/json"
)

const (
	pauseReasonNoProgress          = "no_progress"
	pauseReasonEvidenceUnconfirmed = "evidence_unconfirmed"
	maxConsecutiveAttemptFailures  = 3
)

func (s *Service) FinishAttempt(ctx context.Context, exec ExecutionContext, outcome AttemptOutcome) (Record, error) {
	if err := validateService(s, ctx); err != nil {
		return Record{}, err
	}
	if err := validateFinishAttemptInput(exec, outcome); err != nil {
		return Record{}, err
	}

	var result Record
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
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
		if err := validateWakeWorldAuthority(head, exec.Binding, s.claimantID); err != nil {
			return err
		}
		current, err := s.store.findStrictWorldWakeTx(ctx, tx, head, exec.WakeID)
		if err != nil {
			return err
		}
		record := current.record.record
		if record.Owner != exec.Owner || record.ID != exec.TaskID {
			return ErrTaskChanged
		}
		if record.Spec.ClockID != exec.Clock.ID {
			return ErrClockMismatch
		}
		if record.Revision != exec.ExpectedRevision {
			return ErrTaskChanged
		}
		if taskStateTerminal(record.State) {
			return ErrTaskTerminal
		}
		if record.State != StateRunning || record.Result != nil || record.NextWakeAt != nil || record.PauseReason != "" {
			return ErrTaskChanged
		}
		if current.wake.ClaimedBy != s.claimantID || current.wake.Generation != exec.Binding.Generation ||
			current.wake.ExpectedRevision >= record.Revision ||
			current.wake.Status != wakeStatusRunning && current.wake.Status != wakeStatusConsumed {
			return ErrTaskChanged
		}
		if err := s.store.requireAttemptWakeTopologyTx(ctx, tx, head, current); err != nil {
			return err
		}

		updated, pause, err := applyAttemptOutcome(record, outcome)
		if err != nil {
			return err
		}
		prepared, err := s.store.prepareReconcileMutation(current.record, updated)
		if err != nil {
			return err
		}
		if err := s.store.updateAttemptRecordTx(ctx, tx, record, prepared); err != nil {
			return err
		}
		if pause && current.wake.Status == wakeStatusRunning {
			consumed := current.wake
			consumed.Status = wakeStatusConsumed
			consumed.RetryAfterUnixMS = 0
			if err := s.store.updateWakeCASTx(ctx, tx, current, consumed, "attempt_wake_consumed"); err != nil {
				return err
			}
		}
		result = prepared.record
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	detached, err := cloneRecord(result)
	if err != nil {
		return Record{}, ErrInvalidTaskSpec
	}
	return detached, nil
}

func validateFinishAttemptInput(exec ExecutionContext, outcome AttemptOutcome) error {
	if err := exec.Validate(); err != nil {
		return err
	}
	if err := outcome.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(exec.TaskID) || !requiredIdentity(exec.WakeID) || exec.ExpectedRevision == 0 {
		return ErrInvalidTaskSpec
	}
	if exec.Source.Kind != SourceKindTaskWake {
		return ErrSourceInvalid
	}
	if exec.Source.EventID == "" && exec.Source.TurnID == "" {
		if exec.Source.CallID != exec.WakeID {
			return ErrSourceInvalid
		}
		return nil
	}
	if !requiredIdentity(exec.Source.EventID) || !requiredIdentity(exec.Source.TurnID) || !requiredIdentity(exec.Source.CallID) {
		return ErrSourceInvalid
	}
	return nil
}

func applyAttemptOutcome(current Record, outcome AttemptOutcome) (Record, bool, error) {
	nextRevision, err := NextDurableCounter(current.Revision)
	if err != nil {
		return Record{}, false, err
	}
	if current.NoProgressAttempts >= maxConsecutiveAttemptFailures || current.ReconcileAttempts >= maxConsecutiveAttemptFailures {
		return Record{}, false, ErrInvalidTaskSpec
	}
	updated := current
	updated.Revision = nextRevision
	updated.PauseReason = ""
	pause := false
	switch outcome.Kind {
	case AttemptOutcomeKindNoProgress:
		if current.NeedsReconcile {
			return Record{}, false, ErrTaskChanged
		}
		updated.NoProgressAttempts++
		updated.ReconcileAttempts = 0
		updated.NeedsReconcile = true
		if updated.NoProgressAttempts == maxConsecutiveAttemptFailures {
			updated.State = StatePaused
			updated.PauseReason = pauseReasonNoProgress
			updated.NeedsReconcile = false
			pause = true
		}
	case AttemptOutcomeKindReconcileFailed, AttemptOutcomeKindObservationFailed:
		if (outcome.Kind == AttemptOutcomeKindReconcileFailed) != current.NeedsReconcile {
			return Record{}, false, ErrTaskChanged
		}
		updated.NeedsReconcile = true
		updated.ReconcileAttempts++
		if updated.ReconcileAttempts == maxConsecutiveAttemptFailures {
			updated.State = StatePaused
			updated.PauseReason = pauseReasonEvidenceUnconfirmed
			updated.NeedsReconcile = false
			pause = true
		}
	case AttemptOutcomeKindProgress:
		updated.NeedsReconcile = false
		updated.ReconcileAttempts = 0
		if !current.NeedsReconcile {
			updated.NoProgressAttempts = 0
		}
	default:
		return Record{}, false, ErrInvalidTaskSpec
	}
	if err := updated.Validate(); err != nil {
		return Record{}, false, err
	}
	return updated, pause, nil
}

func (s *SQLiteStore) requireAttemptWakeTopologyTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, selected durableWake) error {
	rows, err := tx.QueryContext(ctx, admissionWakeSelectSQL+` WHERE w.game_id = ? AND w.world_id = ?
		AND w.entity_id = ? AND w.task_id = ? AND w.status IN ('pending', 'claimed', 'enqueued', 'running')
		ORDER BY w.wake_id LIMIT 2`, selected.wake.Owner.GameID, selected.wake.Owner.WorldID,
		selected.wake.Owner.EntityID, selected.wake.TaskID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		wake, raw, clockID, err := scanAdmissionWakeRow(rows)
		if err != nil {
			return err
		}
		candidate := durableWake{wake: wake, record: selected.record, raw: raw, clock: clockID}
		if err := validateDurableWakeRelation(head, candidate); err != nil {
			return err
		}
		if wake.ID != selected.wake.ID || wake.Status != wakeStatusRunning {
			return ErrInvalidTaskSpec
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	want := 1
	if selected.wake.Status == wakeStatusConsumed {
		want = 0
	}
	if count != want {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (s *SQLiteStore) updateAttemptRecordTx(ctx context.Context, tx *sql.Tx, before Record, prepared preparedReconcileMutation) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, revision = ?, next_wake_at = ?, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?
			AND state = ? AND revision = ? AND clock_id = ? AND next_wake_at IS NULL AND record_json = ?`,
		string(prepared.record.State), int64(prepared.record.Revision), nullableTick(prepared.record.NextWakeAt), prepared.recordJSON,
		before.Owner.GameID, before.Owner.WorldID, before.Owner.EntityID, before.ID,
		string(before.State), int64(before.Revision), before.Spec.ClockID, beforeJSON)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTaskChanged
	}
	return s.afterWakeMutationStage(ctx, "attempt_record_updated")
}
