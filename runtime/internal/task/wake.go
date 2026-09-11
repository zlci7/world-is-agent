package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"
)

const wakeSelectSQL = `SELECT
	w.wake_id, w.game_id, w.world_id, w.entity_id, w.task_id, w.clock_id,
	w.expected_revision, w.due_tick, w.reason, w.status, w.claim_id, w.claimed_by,
	w.generation, w.attempt, w.retry_after_unix_ms, w.wake_json, t.clock_id
	FROM task_wakeups w
	JOIN tasks t ON t.game_id = w.game_id AND t.world_id = w.world_id
		AND t.entity_id = w.entity_id AND t.task_id = w.task_id`

const admissionWakeSelectSQL = `SELECT
	w.wake_id, w.game_id, w.world_id, w.entity_id, w.task_id, w.clock_id,
	w.expected_revision, w.due_tick, w.reason, w.status, w.claim_id, w.claimed_by,
	w.generation, w.attempt, w.retry_after_unix_ms, w.wake_json
	FROM task_wakeups w`

const claimDueWakeSQL = admissionWakeSelectSQL + ` WHERE
	w.game_id = ? AND w.world_id = ? AND w.clock_id = ? AND w.status = 'pending'
	AND w.generation = ? AND w.due_tick <= ? AND w.retry_after_unix_ms <= ?
	ORDER BY w.due_tick, w.entity_id, w.task_id, w.wake_id LIMIT ?`

type durableWake struct {
	wake   Wake
	record storedIntentTask
	raw    []byte
	clock  string
}

type preparedBeginMutation struct {
	record Record
	raw    []byte
}

func (s *Service) ClaimDue(ctx context.Context, binding Binding, clock Clock, limit int) ([]Wake, error) {
	if err := validateService(s, ctx); err != nil {
		return nil, err
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if err := clock.Validate(); err != nil || clock.Sequence > uint64(math.MaxInt64) || limit < 0 {
		return nil, ErrInvalidTaskSpec
	}

	batchLimit := limit
	if batchLimit > s.store.options.MaxTasksPerWorld {
		batchLimit = s.store.options.MaxTasksPerWorld
	}
	claimIDs := make([]string, batchLimit)
	seen := make(map[string]struct{}, batchLimit)
	for index := range claimIDs {
		claimIDs[index] = s.newID("claim")
		if !requiredIdentity(claimIDs[index]) {
			return nil, ErrInvalidTaskSpec
		}
		if _, duplicate := seen[claimIDs[index]]; duplicate {
			return nil, ErrInvalidTaskSpec
		}
		seen[claimIDs[index]] = struct{}{}
	}
	nowUnixMS := s.nowUnixMS()
	if nowUnixMS < 0 {
		return nil, ErrInvalidTaskSpec
	}

	var claimed []Wake
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateWakeWorldAuthority(head, binding, s.claimantID); err != nil {
			return err
		}
		if err := validateExactWakeClock(head.Head.Clock, clock); err != nil {
			return err
		}
		wakes, err := s.store.loadDueWakeCandidatesTx(ctx, tx, head, clock, nowUnixMS, batchLimit)
		if err != nil {
			return err
		}
		if limit == 0 {
			claimed = []Wake{}
			return nil
		}
		claimed = make([]Wake, 0, len(wakes))
		for index, candidate := range wakes {
			if candidate.wake.Attempt >= math.MaxInt64 {
				return ErrInvalidTaskSpec
			}
			updated := candidate.wake
			updated.Status = wakeStatusClaimed
			updated.ClaimID = claimIDs[index]
			updated.ClaimedBy = s.claimantID
			updated.Attempt++
			updated.RetryAfterUnixMS = 0
			if err := s.store.updateWakeCASTx(ctx, tx, candidate, updated, wakeStatusClaimed); err != nil {
				return err
			}
			claimed = append(claimed, updated)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]Wake, len(claimed))
	copy(result, claimed)
	return result, nil
}

func (s *Service) MarkEnqueued(ctx context.Context, binding Binding, wakeID, claimID string) error {
	if err := validateService(s, ctx); err != nil {
		return err
	}
	if err := validateWakeIdentityInput(binding, wakeID, claimID); err != nil {
		return err
	}
	return s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateWakeWorldAuthority(head, binding, s.claimantID); err != nil {
			return err
		}
		current, err := s.store.findStrictWorldWakeTx(ctx, tx, head, wakeID)
		if err != nil {
			return err
		}
		if current.wake.ClaimID != claimID || current.wake.ClaimedBy != s.claimantID {
			return ErrTaskChanged
		}
		if current.wake.Status == wakeStatusEnqueued {
			return nil
		}
		if current.wake.Status != wakeStatusClaimed {
			return ErrTaskChanged
		}
		updated := current.wake
		updated.Status = wakeStatusEnqueued
		return s.store.updateWakeCASTx(ctx, tx, current, updated, wakeStatusEnqueued)
	})
}

func (s *Service) ReleaseClaim(ctx context.Context, binding Binding, wakeID, claimID string, retryAt time.Time) error {
	if err := validateService(s, ctx); err != nil {
		return err
	}
	if err := validateWakeIdentityInput(binding, wakeID, claimID); err != nil {
		return err
	}
	retryAfterUnixMS, err := checkedUnixMilli(retryAt)
	if err != nil {
		return err
	}
	return s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateWakeWorldAuthority(head, binding, s.claimantID); err != nil {
			return err
		}
		current, err := s.store.findStrictWorldWakeTx(ctx, tx, head, wakeID)
		if err != nil {
			return err
		}
		if current.wake.Status != wakeStatusClaimed || current.wake.ClaimID != claimID || current.wake.ClaimedBy != s.claimantID {
			return ErrTaskChanged
		}
		updated := current.wake
		updated.Status = wakeStatusPending
		updated.ClaimID = ""
		updated.ClaimedBy = ""
		updated.RetryAfterUnixMS = retryAfterUnixMS
		return s.store.updateWakeCASTx(ctx, tx, current, updated, "released")
	})
}

func (s *Service) BeginWake(ctx context.Context, binding Binding, wakeID, claimID string) (ExecutionContext, Record, error) {
	if err := validateService(s, ctx); err != nil {
		return ExecutionContext{}, Record{}, err
	}
	if err := validateWakeIdentityInput(binding, wakeID, claimID); err != nil {
		return ExecutionContext{}, Record{}, err
	}
	var resultExec ExecutionContext
	var resultRecord Record
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		head, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateWakeWorldAuthority(head, binding, s.claimantID); err != nil {
			return err
		}
		current, err := s.store.findStrictWorldWakeTx(ctx, tx, head, wakeID)
		if err != nil {
			return err
		}
		if current.wake.Status != wakeStatusEnqueued || current.wake.ClaimID != claimID || current.wake.ClaimedBy != s.claimantID {
			return ErrTaskChanged
		}
		if current.wake.DueTick > head.Head.Clock.Tick {
			return ErrTaskChanged
		}
		prepared, err := s.store.prepareBeginMutation(current.record)
		if err != nil {
			return err
		}
		if err := s.store.updateBeginRecordTx(ctx, tx, current.record.record, prepared); err != nil {
			return err
		}
		updatedWake := current.wake
		updatedWake.Status = wakeStatusRunning
		if err := s.store.updateWakeCASTx(ctx, tx, current, updatedWake, wakeStatusRunning); err != nil {
			return err
		}
		resultRecord = prepared.record
		resultExec = ExecutionContext{
			Owner: prepared.record.Owner, Binding: head.Head.Binding, Clock: head.Head.Clock,
			Source: SourceRef{Kind: SourceKindTaskWake, CallID: current.wake.ID},
			TaskID: prepared.record.ID, WakeID: current.wake.ID, ExpectedRevision: prepared.record.Revision,
		}
		return nil
	})
	if err != nil {
		return ExecutionContext{}, Record{}, err
	}
	detached, err := cloneRecord(resultRecord)
	if err != nil {
		return ExecutionContext{}, Record{}, ErrInvalidTaskSpec
	}
	return resultExec, detached, nil
}

func (s *SQLiteStore) prepareBeginMutation(current storedIntentTask) (preparedBeginMutation, error) {
	nextRevision, err := NextDurableCounter(current.record.Revision)
	if err != nil {
		return preparedBeginMutation{}, err
	}
	updated := current.record
	updated.State = StateRunning
	updated.Revision = nextRevision
	updated.NextWakeAt = nil
	if err := updated.Validate(); err != nil {
		return preparedBeginMutation{}, err
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return preparedBeginMutation{}, ErrInvalidTaskSpec
	}
	createResponseJSON, err := json.Marshal(initialCreateResult(current.record))
	if err != nil {
		return preparedBeginMutation{}, ErrInvalidTaskSpec
	}
	historyJSON, err := json.Marshal(current.history)
	if err != nil {
		return preparedBeginMutation{}, ErrInvalidTaskSpec
	}
	if err := s.validateTaskMutationCapacity(updated, createResponseJSON, historyJSON); err != nil {
		return preparedBeginMutation{}, err
	}
	return preparedBeginMutation{record: updated, raw: raw}, nil
}

func (s *SQLiteStore) updateBeginRecordTx(ctx context.Context, tx *sql.Tx, before Record, prepared preparedBeginMutation) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, revision = ?, next_wake_at = NULL, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?
			AND state = ? AND revision = ? AND clock_id = ? AND next_wake_at = ? AND record_json = ?`,
		string(prepared.record.State), int64(prepared.record.Revision), prepared.raw,
		before.Owner.GameID, before.Owner.WorldID, before.Owner.EntityID, before.ID,
		string(before.State), int64(before.Revision), before.Spec.ClockID, nullableTick(before.NextWakeAt), beforeJSON)
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
	if s.testAfterWakeStage != nil {
		return s.testAfterWakeStage(ctx, "begin_record_updated")
	}
	return ctx.Err()
}

func cloneRecord(record Record) (Record, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	var cloned Record
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return Record{}, err
	}
	return cloned, nil
}

func checkedUnixMilli(value time.Time) (int64, error) {
	if value.IsZero() {
		return 0, ErrInvalidTaskSpec
	}
	seconds := value.Unix()
	if seconds < 0 || seconds > math.MaxInt64/1000 {
		return 0, ErrInvalidTaskSpec
	}
	milliseconds := int64(value.Nanosecond()) / int64(time.Millisecond)
	base := seconds * 1000
	if milliseconds > math.MaxInt64-base {
		return 0, ErrInvalidTaskSpec
	}
	return base + milliseconds, nil
}

func validateWakeWorldAuthority(head worldHeadRow, binding Binding, claimantID string) error {
	if head.Head.Status != worldHeadStatusReady {
		return ErrWorldNotReady
	}
	if head.BarrierStatus != "" || head.SaveRequestID != "" {
		return ErrSaveInProgress
	}
	if head.Head.Binding != binding {
		return ErrGenerationStale
	}
	if head.RuntimeInstanceID != claimantID {
		return ErrTaskChanged
	}
	return nil
}

func validateExactWakeClock(current, supplied Clock) error {
	if current.ID != supplied.ID {
		return ErrClockMismatch
	}
	if supplied.Tick < current.Tick || supplied.Sequence < current.Sequence {
		return ErrClockRewound
	}
	if current != supplied {
		return ErrClockMismatch
	}
	return nil
}

func (s *SQLiteStore) loadWorldTaskGraphForWakeTx(ctx context.Context, tx *sql.Tx, world WorldKey) (worldTaskIdentityGraph, error) {
	graph, err := s.loadWorldTaskIdentityGraphTx(ctx, tx, world)
	if err != nil {
		if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
			return worldTaskIdentityGraph{}, ErrInvalidTaskSpec
		}
		return worldTaskIdentityGraph{}, err
	}
	return graph, nil
}

func (s *SQLiteStore) loadDueWakeCandidatesTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, clock Clock, nowUnixMS int64, limit int) ([]durableWake, error) {
	graph, err := s.loadWorldTaskGraphForWakeTx(ctx, tx, head.Head.Binding.World)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		return []durableWake{}, nil
	}
	rows, err := tx.QueryContext(ctx, claimDueWakeSQL,
		head.Head.Binding.World.GameID, head.Head.Binding.World.WorldID, clock.ID,
		int64(head.Head.Binding.Generation), clock.Tick, nowUnixMS, limit)
	if err != nil {
		return nil, err
	}
	var wakes []durableWake
	for rows.Next() {
		wake, raw, clockID, err := scanAdmissionWakeRow(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		wakes = append(wakes, durableWake{wake: wake, raw: raw, clock: clockID})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range wakes {
		key := worldTaskIdentity{owner: wakes[index].wake.Owner, taskID: wakes[index].wake.TaskID}
		record, found := graph.tasks[key]
		if !found {
			return nil, ErrInvalidTaskSpec
		}
		wakes[index].record = record
		if err := validateDurableWakeRelation(head, wakes[index]); err != nil {
			return nil, err
		}
	}
	return wakes, nil
}

func (s *SQLiteStore) findStrictWorldWakeTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, wakeID string) (durableWake, error) {
	graph, err := s.loadWorldTaskGraphForWakeTx(ctx, tx, head.Head.Binding.World)
	if err != nil {
		return durableWake{}, err
	}
	wake, raw, clockID, err := scanAdmissionWakeRow(tx.QueryRowContext(ctx, admissionWakeSelectSQL+` WHERE w.game_id = ? AND w.world_id = ? AND w.wake_id = ?`,
		head.Head.Binding.World.GameID, head.Head.Binding.World.WorldID, wakeID))
	if errors.Is(err, sql.ErrNoRows) {
		return durableWake{}, ErrTaskNotFound
	}
	if err != nil {
		return durableWake{}, err
	}
	record, found := graph.tasks[worldTaskIdentity{owner: wake.Owner, taskID: wake.TaskID}]
	if !found {
		return durableWake{}, ErrInvalidTaskSpec
	}
	candidate := durableWake{wake: wake, record: record, raw: raw, clock: clockID}
	if err := validateDurableWakeRelation(head, candidate); err != nil {
		return durableWake{}, err
	}
	return candidate, nil
}

func scanWakeRow(scanner rowScanner) (Wake, []byte, error) {
	var (
		indexed                      Wake
		clockID, taskClockID         string
		expectedRevision, generation int64
		raw                          []byte
	)
	if err := scanner.Scan(
		&indexed.ID, &indexed.Owner.GameID, &indexed.Owner.WorldID, &indexed.Owner.EntityID,
		&indexed.TaskID, &clockID, &expectedRevision, &indexed.DueTick, &indexed.Reason,
		&indexed.Status, &indexed.ClaimID, &indexed.ClaimedBy, &generation, &indexed.Attempt,
		&indexed.RetryAfterUnixMS, &raw, &taskClockID,
	); err != nil {
		return Wake{}, nil, err
	}
	wake, canonicalRaw, err := validateScannedWake(indexed, expectedRevision, generation, raw)
	if err != nil || clockID != taskClockID {
		return Wake{}, nil, ErrInvalidTaskSpec
	}
	return wake, canonicalRaw, nil
}

func scanAdmissionWakeRow(scanner rowScanner) (Wake, []byte, string, error) {
	var (
		indexed                      Wake
		clockID                      string
		expectedRevision, generation int64
		raw                          []byte
	)
	if err := scanner.Scan(
		&indexed.ID, &indexed.Owner.GameID, &indexed.Owner.WorldID, &indexed.Owner.EntityID,
		&indexed.TaskID, &clockID, &expectedRevision, &indexed.DueTick, &indexed.Reason,
		&indexed.Status, &indexed.ClaimID, &indexed.ClaimedBy, &generation, &indexed.Attempt,
		&indexed.RetryAfterUnixMS, &raw,
	); err != nil {
		return Wake{}, nil, "", err
	}
	wake, canonicalRaw, err := validateScannedWake(indexed, expectedRevision, generation, raw)
	if err != nil || !requiredIdentity(clockID) {
		return Wake{}, nil, "", ErrInvalidTaskSpec
	}
	return wake, canonicalRaw, clockID, nil
}

func validateScannedWake(indexed Wake, expectedRevision, generation int64, raw []byte) (Wake, []byte, error) {
	if expectedRevision <= 0 || generation <= 0 {
		return Wake{}, nil, ErrInvalidTaskSpec
	}
	indexed.ExpectedRevision = uint64(expectedRevision)
	indexed.Generation = uint64(generation)
	var stored Wake
	if err := json.Unmarshal(raw, &stored); err != nil || stored.Validate() != nil ||
		!wakeIndexedValuesEqual(stored, indexed) {
		return Wake{}, nil, ErrInvalidTaskSpec
	}
	canonical, err := json.Marshal(stored)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Wake{}, nil, ErrInvalidTaskSpec
	}
	return stored, append([]byte(nil), raw...), nil
}

func validateDurableWakeRelation(head worldHeadRow, candidate durableWake) error {
	wake, record := candidate.wake, candidate.record.record
	if wake.Owner != record.Owner || wake.TaskID != record.ID || wake.DueTick > record.Spec.DeadlineAt ||
		candidate.clock != record.Spec.ClockID || record.Spec.ClockID != head.Head.Clock.ID {
		return ErrInvalidTaskSpec
	}
	switch wake.Status {
	case wakeStatusPending, wakeStatusClaimed, wakeStatusEnqueued:
		if wake.Generation != head.Head.Binding.Generation || taskStateTerminal(record.State) || record.State != StateWaiting ||
			record.Result != nil || record.NextWakeAt == nil || *record.NextWakeAt != wake.DueTick || record.Revision != wake.ExpectedRevision {
			return ErrInvalidTaskSpec
		}
	case wakeStatusRunning:
		if wake.Generation != head.Head.Binding.Generation || record.State != StateRunning || record.Result != nil ||
			record.NextWakeAt != nil || record.Revision <= wake.ExpectedRevision {
			return ErrInvalidTaskSpec
		}
	case wakeStatusConsumed:
	default:
		return ErrInvalidTaskSpec
	}
	return nil
}

func (s *SQLiteStore) updateWakeCASTx(ctx context.Context, tx *sql.Tx, before durableWake, updated Wake, stage string) error {
	if err := updated.Validate(); err != nil {
		return err
	}
	if updated.ID != before.wake.ID || updated.TaskID != before.wake.TaskID || updated.Owner != before.wake.Owner ||
		updated.ExpectedRevision != before.wake.ExpectedRevision || updated.DueTick != before.wake.DueTick ||
		updated.Reason != before.wake.Reason {
		return ErrInvalidTaskSpec
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	wake := before.wake
	result, err := tx.ExecContext(ctx, `UPDATE task_wakeups SET
		status = ?, claim_id = ?, claimed_by = ?, generation = ?, attempt = ?, retry_after_unix_ms = ?, wake_json = ?
		WHERE wake_id = ? AND game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?
			AND clock_id = ? AND expected_revision = ? AND due_tick = ? AND reason = ? AND status = ?
			AND claim_id = ? AND claimed_by = ? AND generation = ? AND attempt = ?
			AND retry_after_unix_ms = ? AND wake_json = ?`,
		updated.Status, updated.ClaimID, updated.ClaimedBy, int64(updated.Generation), updated.Attempt, updated.RetryAfterUnixMS, raw,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		before.record.record.Spec.ClockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
		wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt,
		wake.RetryAfterUnixMS, before.raw)
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
	if s.testAfterWakeStage != nil {
		return s.testAfterWakeStage(ctx, stage)
	}
	return ctx.Err()
}

func validateWakeIdentityInput(binding Binding, wakeID, claimID string) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(wakeID) || !requiredIdentity(claimID) {
		return ErrInvalidTaskSpec
	}
	return nil
}
