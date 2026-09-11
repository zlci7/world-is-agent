package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const wakeReasonRestartReconcile = "restart_reconcile"

type restartRecoveryPlan struct {
	deliveries []durableWake
	running    []durableWake
}

type preparedRestartRecovery struct {
	current     durableWake
	mutation    preparedReconcileMutation
	replacement Wake
}

func (s *SQLiteStore) planRestartRecoveryTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, claimantID string) (restartRecoveryPlan, error) {
	if !requiredIdentity(claimantID) {
		return restartRecoveryPlan{}, ErrTaskConflict
	}
	graph, err := s.loadWorldTaskIdentityGraphTx(ctx, tx, head.Head.Binding.World)
	if err != nil {
		if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
			return restartRecoveryPlan{}, ErrInvalidTaskSpec
		}
		return restartRecoveryPlan{}, err
	}
	if len(graph.tasks) > s.options.MaxTasksPerWorld {
		return restartRecoveryPlan{}, ErrTaskCapacityExceeded
	}
	wakes, err := s.loadWorldDurableWakesTx(ctx, tx, head, graph)
	if err != nil {
		return restartRecoveryPlan{}, err
	}

	plan := restartRecoveryPlan{}
	executable := make(map[worldTaskIdentity]int)
	for _, current := range wakes {
		switch current.wake.Status {
		case wakeStatusPending, wakeStatusClaimed, wakeStatusEnqueued, wakeStatusRunning:
			identity := worldTaskIdentity{owner: current.wake.Owner, taskID: current.wake.TaskID}
			executable[identity]++
			if executable[identity] > 1 {
				return restartRecoveryPlan{}, ErrInvalidTaskSpec
			}
		}
		if current.wake.ClaimedBy == claimantID {
			continue
		}
		switch current.wake.Status {
		case wakeStatusClaimed, wakeStatusEnqueued:
			plan.deliveries = append(plan.deliveries, current)
		case wakeStatusRunning:
			plan.running = append(plan.running, current)
		}
	}
	return plan, nil
}

func (s *SQLiteStore) loadWorldDurableWakesTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, graph worldTaskIdentityGraph) ([]durableWake, error) {
	rows, err := tx.QueryContext(ctx, admissionWakeSelectSQL+` WHERE w.game_id = ? AND w.world_id = ?
		ORDER BY w.entity_id, w.task_id, w.wake_id`, head.Head.Binding.World.GameID, head.Head.Binding.World.WorldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wakes := make([]durableWake, 0)
	for rows.Next() {
		wake, raw, clockID, err := scanAdmissionWakeRow(rows)
		if err != nil {
			return nil, err
		}
		record, found := graph.tasks[worldTaskIdentity{owner: wake.Owner, taskID: wake.TaskID}]
		if !found {
			return nil, ErrInvalidTaskSpec
		}
		current := durableWake{wake: wake, record: record, raw: raw, clock: clockID}
		if err := validateDurableWakeRelation(head, current); err != nil {
			return nil, err
		}
		wakes = append(wakes, current)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return wakes, nil
}

func (s *SQLiteStore) applyRestartRecoveryTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, plan restartRecoveryPlan, candidateWakeIDs []string) error {
	prepared := make([]preparedRestartRecovery, len(plan.running))
	for index, current := range plan.running {
		if index >= len(candidateWakeIDs) {
			return ErrTaskChanged
		}
		updated, err := recoveredRunningRecord(current.record.record, head.Head.Clock.Tick)
		if err != nil {
			return err
		}
		mutation, err := s.prepareReconcileMutation(current.record, updated)
		if err != nil {
			return err
		}
		replacement := Wake{
			ID: candidateWakeIDs[index], TaskID: updated.ID, Owner: updated.Owner,
			ExpectedRevision: updated.Revision, DueTick: *updated.NextWakeAt,
			Reason: wakeReasonRestartReconcile, Status: wakeStatusPending,
			Generation: head.Head.Binding.Generation,
		}
		if err := validateTaskWakePair(updated, replacement); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM task_wakeups WHERE wake_id = ?)`, replacement.ID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrTaskConflict
		}
		prepared[index] = preparedRestartRecovery{current: current, mutation: mutation, replacement: replacement}
	}

	for _, current := range plan.deliveries {
		updated := current.wake
		updated.Status = wakeStatusPending
		updated.ClaimID = ""
		updated.ClaimedBy = ""
		updated.RetryAfterUnixMS = 0
		if err := s.updateWakeCASTx(ctx, tx, current, updated, "restart_delivery_reset"); err != nil {
			return err
		}
	}
	for _, recovery := range prepared {
		if err := s.updateRestartRecordTx(ctx, tx, recovery.current.record.record, recovery.mutation); err != nil {
			return err
		}
		consumed := recovery.current.wake
		consumed.Status = wakeStatusConsumed
		consumed.RetryAfterUnixMS = 0
		if err := s.updateWakeCASTx(ctx, tx, recovery.current, consumed, "restart_running_consumed"); err != nil {
			return err
		}
		if err := s.insertWakeTx(ctx, tx, recovery.mutation.record, recovery.replacement); err != nil {
			return err
		}
		if err := s.afterWakeMutationStage(ctx, "restart_reconcile_inserted"); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func recoveredRunningRecord(current Record, nowTick int64) (Record, error) {
	nextRevision, err := NextDurableCounter(current.Revision)
	if err != nil {
		return Record{}, err
	}
	updated := current
	updated.Revision = nextRevision
	updated.State = StateWaiting
	updated.NeedsReconcile = true
	updated.PauseReason = ""
	due := nowTick
	if due > current.Spec.DeadlineAt {
		due = current.Spec.DeadlineAt
	}
	updated.NextWakeAt = &due
	updated.Operations = cloneOperations(current.Operations)
	for index := range updated.Operations {
		if updated.Operations[index].Status == OperationStatusRegistered {
			updated.Operations[index].Status = OperationStatusUncertain
		}
	}
	return updated, nil
}

func (s *SQLiteStore) updateRestartRecordTx(ctx context.Context, tx *sql.Tx, before Record, prepared preparedReconcileMutation) error {
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
	return s.afterWakeMutationStage(ctx, "restart_record_updated")
}

func (s *SQLiteStore) afterWakeMutationStage(ctx context.Context, stage string) error {
	if s.testAfterWakeStage != nil {
		return s.testAfterWakeStage(ctx, stage)
	}
	return ctx.Err()
}
