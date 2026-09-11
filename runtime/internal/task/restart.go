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
	running    []restartRunningTask
}

type restartRunningTask struct {
	record  storedIntentTask
	wake    durableWake
	hasWake bool
}

type preparedRestartRecovery struct {
	current     restartRunningTask
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
	wakes, err := s.loadWorldDurableWakesTx(ctx, tx, head, graph)
	if err != nil {
		return restartRecoveryPlan{}, err
	}

	executable := make(map[worldTaskIdentity][]durableWake)
	for _, current := range wakes {
		switch current.wake.Status {
		case wakeStatusPending, wakeStatusClaimed, wakeStatusEnqueued, wakeStatusRunning:
			identity := worldTaskIdentity{owner: current.wake.Owner, taskID: current.wake.TaskID}
			executable[identity] = append(executable[identity], current)
			if current.wake.Status != wakeStatusPending && current.wake.ClaimedBy != head.RuntimeInstanceID {
				return restartRecoveryPlan{}, ErrInvalidTaskSpec
			}
		}
	}
	identities := sortedWorldTaskIdentities(graph.tasks)

	plan := restartRecoveryPlan{}
	recoverPriorInstance := head.RuntimeInstanceID != claimantID
	for _, identity := range identities {
		record := graph.tasks[identity]
		active := executable[identity]
		switch record.record.State {
		case StateWaiting:
			if len(active) != 1 || active[0].wake.Status == wakeStatusRunning {
				return restartRecoveryPlan{}, ErrInvalidTaskSpec
			}
			if recoverPriorInstance {
				plan.deliveries = append(plan.deliveries, active[0])
			}
		case StateRunning:
			if len(active) > 1 || len(active) == 1 && active[0].wake.Status != wakeStatusRunning {
				return restartRecoveryPlan{}, ErrInvalidTaskSpec
			}
			if recoverPriorInstance {
				running := restartRunningTask{record: record}
				if len(active) == 1 {
					running.wake, running.hasWake = active[0], true
				}
				plan.running = append(plan.running, running)
			}
		case StatePaused, StateSucceeded, StateFailed, StateCancelled:
			if len(active) != 0 {
				return restartRecoveryPlan{}, ErrInvalidTaskSpec
			}
		default:
			return restartRecoveryPlan{}, ErrInvalidTaskSpec
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

func (s *SQLiteStore) applyRestartRecoveryTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, plan restartRecoveryPlan, candidateWakeIDs []string, clock Clock, runtimeInstanceID string) (Head, error) {
	if !requiredIdentity(runtimeInstanceID) || runtimeInstanceID == head.RuntimeInstanceID {
		return Head{}, ErrTaskChanged
	}
	if err := validateRestartActivation(head, head.Head.Binding.RunID, clock); err != nil {
		return Head{}, err
	}
	if err := s.validateRestartBarrierTx(ctx, tx, head); err != nil {
		return Head{}, err
	}
	generation, err := NextDurableCounter(head.Head.Binding.Generation)
	if err != nil {
		return Head{}, err
	}
	prepared := make([]preparedRestartRecovery, len(plan.running))
	for index, current := range plan.running {
		if index >= len(candidateWakeIDs) {
			return Head{}, ErrTaskChanged
		}
		updated, err := recoveredRunningRecord(current.record.record, clock.Tick)
		if err != nil {
			return Head{}, err
		}
		mutation, err := s.prepareReconcileMutation(current.record, updated)
		if err != nil {
			return Head{}, err
		}
		replacement := Wake{
			ID: candidateWakeIDs[index], TaskID: updated.ID, Owner: updated.Owner,
			ExpectedRevision: updated.Revision, DueTick: *updated.NextWakeAt,
			Reason: wakeReasonRestartReconcile, Status: wakeStatusPending,
			Generation: generation,
		}
		if err := validateTaskWakePair(updated, replacement); err != nil {
			return Head{}, err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM task_wakeups WHERE wake_id = ?)`, replacement.ID).Scan(&exists); err != nil {
			return Head{}, err
		}
		if exists != 0 {
			return Head{}, ErrTaskConflict
		}
		prepared[index] = preparedRestartRecovery{current: current, mutation: mutation, replacement: replacement}
	}

	for _, current := range plan.deliveries {
		updated := current.wake
		updated.Status = wakeStatusPending
		updated.ClaimID = ""
		updated.ClaimedBy = ""
		updated.Generation = generation
		updated.RetryAfterUnixMS = 0
		if err := s.updateWakeCASTx(ctx, tx, current, updated, "restart_delivery_reset"); err != nil {
			return Head{}, err
		}
	}
	for _, recovery := range prepared {
		if err := s.updateRestartRecordTx(ctx, tx, recovery.current.record.record, recovery.mutation); err != nil {
			return Head{}, err
		}
		if recovery.current.hasWake {
			consumed := recovery.current.wake.wake
			consumed.Status = wakeStatusConsumed
			consumed.RetryAfterUnixMS = 0
			if err := s.updateWakeCASTx(ctx, tx, recovery.current.wake, consumed, "restart_running_consumed"); err != nil {
				return Head{}, err
			}
		}
		if err := s.insertWakeTx(ctx, tx, recovery.mutation.record, recovery.replacement); err != nil {
			return Head{}, err
		}
		if err := s.afterWakeMutationStage(ctx, "restart_reconcile_inserted"); err != nil {
			return Head{}, err
		}
	}
	updatedHead := head
	updatedHead.Head.Binding.Generation = generation
	updatedHead.Head.Clock = clock
	updatedHead.SaveRequestID = ""
	updatedHead.BarrierStatus = ""
	updatedHead.BarrierPreparedAtUnixMS = 0
	updatedHead.RuntimeInstanceID = runtimeInstanceID
	if err := s.updateWorldRestartHeadTx(ctx, tx, head, updatedHead); err != nil {
		return Head{}, err
	}
	return updatedHead.Head, nil
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

func (s *SQLiteStore) updateWorldRestartHeadTx(ctx context.Context, tx *sql.Tx, before, updated worldHeadRow) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	if err := updated.validate(); err != nil {
		return err
	}
	expected := before
	expected.Head.Binding.Generation = updated.Head.Binding.Generation
	expected.Head.Clock = updated.Head.Clock
	expected.SaveRequestID = updated.SaveRequestID
	expected.BarrierStatus = updated.BarrierStatus
	expected.BarrierPreparedAtUnixMS = updated.BarrierPreparedAtUnixMS
	expected.RuntimeInstanceID = updated.RuntimeInstanceID
	if updated != expected {
		return ErrInvalidTaskSpec
	}
	updatedJSON, err := json.Marshal(updated)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_world_heads SET
		generation = ?, clock_id = ?, clock_tick = ?, clock_sequence = ?,
		save_request_id = ?, barrier_status = ?, runtime_instance_id = ?, head_json = ?
		WHERE game_id = ? AND world_id = ? AND runtime_instance_id = ? AND head_json = ?`,
		int64(updated.Head.Binding.Generation), updated.Head.Clock.ID, updated.Head.Clock.Tick,
		int64(updated.Head.Clock.Sequence), updated.SaveRequestID, updated.BarrierStatus,
		updated.RuntimeInstanceID, updatedJSON,
		before.Head.Binding.World.GameID, before.Head.Binding.World.WorldID,
		before.RuntimeInstanceID, beforeJSON)
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
	return s.afterWakeMutationStage(ctx, "restart_head_rebound")
}

func (s *SQLiteStore) afterWakeMutationStage(ctx context.Context, stage string) error {
	if s.testAfterWakeStage != nil {
		return s.testAfterWakeStage(ctx, stage)
	}
	return ctx.Err()
}
