package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
)

const (
	checkpointSchemaVersion     = 1
	checkpointStatusConfirmed   = "confirmed"
	checkpointStatusUnconfirmed = "unconfirmed"
	checkpointBarrierPrepared   = "prepared"
	checkpointBarrierTimeoutMS  = int64(10_000)
	worldHeadStatusPaused       = "paused"
)

type checkpointPrepareRequest struct {
	Binding       Binding    `json:"binding"`
	Clock         Clock      `json:"clock"`
	SaveRequestID string     `json:"save_request_id"`
	Evidence      []Evidence `json:"evidence"`
}

type checkpointTaskSnapshot struct {
	Record             Record          `json:"record"`
	CreateFingerprint  string          `json:"create_fingerprint"`
	CreateResponseJSON json.RawMessage `json:"create_response"`
	CreateResponseHash string          `json:"create_response_hash"`
	IntentHistoryJSON  json.RawMessage `json:"intent_history"`
	IntentHistoryHash  string          `json:"intent_history_hash"`
}

type checkpointWakeSnapshot struct {
	ClockID string `json:"clock_id"`
	Wake    Wake   `json:"wake"`
}

type checkpointWakeKey struct {
	owner            string
	taskID           string
	expectedRevision uint64
	reason           string
}

type checkpointCallKey struct {
	entityID string
	eventID  string
	turnID   string
	callID   string
}

type checkpointEquivalenceKey struct {
	entityID      string
	equivalenceID string
}

type checkpointSnapshot struct {
	SchemaVersion int                      `json:"schema_version"`
	World         WorldKey                 `json:"world"`
	PreparedHead  Head                     `json:"prepared_head"`
	Request       checkpointPrepareRequest `json:"request"`
	Tasks         []checkpointTaskSnapshot `json:"tasks"`
	Wakes         []checkpointWakeSnapshot `json:"wakes"`
}

func (s *Service) loadWorldHeadForMutationTx(ctx context.Context, tx *sql.Tx, world WorldKey) (worldHeadRow, bool, error) {
	current, found, err := s.store.loadWorldHeadTx(ctx, tx, world)
	if err != nil || !found {
		return current, found, err
	}
	current, err = s.releaseExpiredCheckpointBarrierTx(ctx, tx, current)
	if err != nil {
		return worldHeadRow{}, false, err
	}
	return current, true, nil
}

func (s *Service) releaseExpiredCheckpointBarrierTx(ctx context.Context, tx *sql.Tx, current worldHeadRow) (worldHeadRow, error) {
	if current.BarrierStatus != checkpointBarrierPrepared || current.SaveRequestID == "" ||
		current.BarrierPreparedAtUnixMS <= 0 || current.RuntimeInstanceID != s.claimantID {
		return current, nil
	}
	now := s.nowUnixMS()
	if now <= 0 {
		return worldHeadRow{}, ErrInvalidTaskSpec
	}
	if now < current.BarrierPreparedAtUnixMS || now-current.BarrierPreparedAtUnixMS < checkpointBarrierTimeoutMS {
		return current, nil
	}
	if err := s.store.validateRestartBarrierTx(ctx, tx, current); err != nil {
		return worldHeadRow{}, err
	}
	updated := current
	updated.SaveRequestID = ""
	updated.BarrierStatus = ""
	updated.BarrierPreparedAtUnixMS = 0
	if err := s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "checkpoint_barrier_expired"); err != nil {
		return worldHeadRow{}, err
	}
	return updated, nil
}

func (s *Service) UpdateClock(ctx context.Context, binding Binding, clock Clock) (Head, error) {
	if err := validateService(s, ctx); err != nil {
		return Head{}, err
	}
	if err := binding.Validate(); err != nil {
		return Head{}, err
	}
	if err := clock.Validate(); err != nil || clock.Sequence > uint64(math.MaxInt64) {
		return Head{}, ErrInvalidTaskSpec
	}

	var result Head
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if err := validateWakeWorldAuthority(current, binding, s.claimantID); err != nil {
			return err
		}
		if current.Head.Clock.ID != clock.ID {
			return ErrClockMismatch
		}
		if err := validateMonotonicClock(current.Head.Clock, clock); err != nil {
			return err
		}
		if clock == current.Head.Clock {
			result = current.Head
			return nil
		}
		updated := current
		updated.Head.Clock = clock
		if err := s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "clock_updated"); err != nil {
			return err
		}
		result = updated.Head
		return nil
	})
	if err != nil {
		return Head{}, err
	}
	return result, nil
}

func (s *Service) DeactivateWorld(ctx context.Context, binding Binding, reason string) error {
	if err := validateService(s, ctx); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil || !requiredIdentity(reason) {
		return ErrInvalidTaskSpec
	}
	return s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if current.Head.Binding != binding {
			return ErrGenerationStale
		}
		if current.RuntimeInstanceID != s.claimantID {
			return ErrTaskChanged
		}
		if current.BarrierStatus != "" || current.SaveRequestID != "" {
			return ErrSaveInProgress
		}
		if current.Head.Status == worldHeadStatusPaused && current.Head.Reason == reason {
			return nil
		}
		if current.Head.Status != worldHeadStatusReady {
			return ErrWorldNotReady
		}
		updated := current
		updated.Head.Status = worldHeadStatusPaused
		updated.Head.Reason = reason
		return s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "world_deactivated")
	})
}

func (s *Service) PrepareCheckpoint(ctx context.Context, binding Binding, clock Clock, saveRequestID string, evidence []Evidence) (Prepared, error) {
	if err := validateService(s, ctx); err != nil {
		return Prepared{}, err
	}
	request, err := prepareCheckpointRequest(binding, clock, saveRequestID, evidence)
	if err != nil {
		return Prepared{}, err
	}
	checkpointID := s.newID("checkpoint")
	if !requiredIdentity(checkpointID) {
		return Prepared{}, ErrInvalidTaskSpec
	}
	wakeIDCount, err := s.store.countWorldTasks(ctx, binding.World)
	if err != nil {
		return Prepared{}, err
	}
	wakeIDs, err := s.checkpointWakeIDs(wakeIDCount)
	if err != nil {
		return Prepared{}, err
	}

	var prepared Prepared
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}

		prior, priorFound, err := s.store.loadCheckpointBySaveRequestTx(ctx, tx, binding.World, saveRequestID)
		if err != nil {
			return err
		}
		if priorFound {
			return prepareCheckpointRetry(current, prior, request, &prepared)
		}

		if err := validateWakeWorldAuthority(current, binding, s.claimantID); err != nil {
			return err
		}
		if err := validateExactWakeClock(current.Head.Clock, clock); err != nil {
			return err
		}
		for _, item := range request.Evidence {
			if _, err := s.store.admitEvidenceTx(ctx, tx, current, item); err != nil {
				return err
			}
		}

		nextGeneration, err := NextDurableCounter(current.Head.Binding.Generation)
		if err != nil {
			return err
		}
		preparedHead := current.Head
		preparedHead.Binding.Generation = nextGeneration
		preparedHead.Clock = clock
		preparedHead.CheckpointID = checkpointID
		preparedHead.Status = worldHeadStatusReady
		preparedHead.Reason = ""

		if err := s.store.normalizeCheckpointWorkingSetTx(ctx, tx, current, preparedHead.Binding.Generation, wakeIDs); err != nil {
			return err
		}
		snapshot, snapshotJSON, err := s.store.captureCheckpointSnapshotTx(ctx, tx, preparedHead, request)
		if err != nil {
			return err
		}
		if len(snapshotJSON) > s.store.options.MaxSnapshotBytes {
			return ErrInvalidTaskSpec
		}
		row := checkpointRow{
			ID: checkpointID, World: binding.World, SaveRequestID: saveRequestID,
			SchemaVersion: checkpointSchemaVersion, Clock: clock,
			Checksum: sha256Hex(snapshotJSON), Snapshot: snapshotJSON,
		}
		if err := validateDecodedCheckpoint(row, snapshot); err != nil {
			return err
		}
		if err := s.store.insertCheckpointTx(ctx, tx, row); err != nil {
			return err
		}

		barrierPreparedAt := s.nowUnixMS()
		if barrierPreparedAt <= 0 {
			return ErrInvalidTaskSpec
		}
		updated := current
		updated.Head = preparedHead
		updated.SaveRequestID = saveRequestID
		updated.BarrierStatus = checkpointBarrierPrepared
		updated.BarrierPreparedAtUnixMS = barrierPreparedAt
		if err := s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "checkpoint_prepared"); err != nil {
			return err
		}
		prepared = preparedFromCheckpoint(row, snapshot)
		return nil
	})
	if err != nil {
		return Prepared{}, err
	}
	return prepared, nil
}

func (s *Service) FinishCheckpoint(ctx context.Context, binding Binding, saveRequestID string, saved bool) error {
	if err := validateService(s, ctx); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil || !requiredIdentity(saveRequestID) {
		return ErrInvalidTaskSpec
	}
	_ = saved
	return s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.loadWorldHeadForMutationTx(ctx, tx, binding.World)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if current.Head.Binding != binding {
			return ErrGenerationStale
		}
		if current.RuntimeInstanceID != s.claimantID {
			return ErrTaskChanged
		}
		checkpoint, checkpointFound, err := s.store.loadCheckpointBySaveRequestTx(ctx, tx, binding.World, saveRequestID)
		if err != nil {
			return err
		}
		if !checkpointFound || current.Head.CheckpointID != checkpoint.ID {
			return ErrTaskChanged
		}
		if current.SaveRequestID == "" && current.BarrierStatus == "" {
			return nil
		}
		if current.SaveRequestID != saveRequestID || current.BarrierStatus != checkpointBarrierPrepared {
			return ErrTaskChanged
		}
		updated := current
		updated.SaveRequestID = ""
		updated.BarrierStatus = ""
		updated.BarrierPreparedAtUnixMS = 0
		return s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "checkpoint_finished")
	})
}

// CheckpointBarrier reports the save barrier a world currently holds.
type CheckpointBarrier struct {
	Held            bool   `json:"held"`
	SaveRequestID   string `json:"save_request_id,omitempty"`
	ExpiresAtUnixMS int64  `json:"expires_at_unix_ms,omitempty"`
}

// ReadCheckpointBarrier retires a prepared barrier that outlived its bound and reports the
// remaining barrier for the world. A caller that observes no held barrier may resume binding;
// the held request identity stays authoritative so a stale caller cannot retire a newer save.
func (s *Service) ReadCheckpointBarrier(ctx context.Context, world WorldKey) (CheckpointBarrier, error) {
	if err := validateService(s, ctx); err != nil {
		return CheckpointBarrier{}, err
	}
	if err := world.Validate(); err != nil {
		return CheckpointBarrier{}, err
	}
	var result CheckpointBarrier
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.loadWorldHeadForMutationTx(ctx, tx, world)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if current.RuntimeInstanceID != s.claimantID {
			return ErrTaskChanged
		}
		if current.BarrierStatus == "" && current.SaveRequestID == "" {
			return nil
		}
		result = CheckpointBarrier{Held: true, SaveRequestID: current.SaveRequestID}
		if current.BarrierPreparedAtUnixMS > 0 {
			result.ExpiresAtUnixMS = current.BarrierPreparedAtUnixMS + checkpointBarrierTimeoutMS
		}
		return nil
	})
	if err != nil {
		return CheckpointBarrier{}, err
	}
	return result, nil
}

func prepareCheckpointRequest(binding Binding, clock Clock, saveRequestID string, evidence []Evidence) (checkpointPrepareRequest, error) {
	if err := binding.Validate(); err != nil {
		return checkpointPrepareRequest{}, err
	}
	if err := clock.Validate(); err != nil || clock.Sequence > uint64(math.MaxInt64) || !requiredIdentity(saveRequestID) {
		return checkpointPrepareRequest{}, ErrInvalidTaskSpec
	}
	cloned := make([]Evidence, len(evidence))
	for index, item := range evidence {
		copyItem, err := cloneEvidence(item)
		if err != nil {
			return checkpointPrepareRequest{}, ErrInvalidTaskSpec
		}
		if err := copyItem.Validate(); err != nil {
			return checkpointPrepareRequest{}, err
		}
		if copyItem.Applied {
			return checkpointPrepareRequest{}, ErrEvidenceConflict
		}
		if copyItem.Binding.World != binding.World || copyItem.RevalidatedIn != nil && copyItem.RevalidatedIn.World != binding.World {
			return checkpointPrepareRequest{}, ErrWorldMismatch
		}
		cloned[index] = copyItem
	}
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].FactID < cloned[j].FactID })
	normalized := cloned[:0]
	for _, item := range cloned {
		if len(normalized) > 0 && normalized[len(normalized)-1].FactID == item.FactID {
			if !evidenceEqualIgnoringApplied(normalized[len(normalized)-1], item) {
				return checkpointPrepareRequest{}, ErrEvidenceConflict
			}
			continue
		}
		normalized = append(normalized, item)
	}
	return checkpointPrepareRequest{Binding: binding, Clock: clock, SaveRequestID: saveRequestID, Evidence: normalized}, nil
}

func prepareCheckpointRetry(current worldHeadRow, row checkpointRow, request checkpointPrepareRequest, result *Prepared) error {
	snapshot, err := decodeCheckpointRow(row)
	if err != nil {
		return err
	}
	got, err := json.Marshal(snapshot.Request)
	if err != nil {
		return ErrCheckpointInvalid
	}
	want, err := json.Marshal(request)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	if !bytes.Equal(got, want) {
		return ErrIdempotencyConflict
	}
	if current.Head.Binding != snapshot.PreparedHead.Binding || current.Head.CheckpointID != row.ID {
		return ErrTaskChanged
	}
	if current.SaveRequestID != "" && (current.SaveRequestID != row.SaveRequestID || current.BarrierStatus != checkpointBarrierPrepared) {
		return ErrTaskChanged
	}
	*result = preparedFromCheckpoint(row, snapshot)
	return nil
}

func preparedFromCheckpoint(row checkpointRow, snapshot checkpointSnapshot) Prepared {
	return Prepared{
		Head: snapshot.PreparedHead,
		Reference: CheckpointRef{
			Status: checkpointStatusConfirmed, ID: row.ID, Checksum: row.Checksum,
			SchemaVersion: row.SchemaVersion, World: row.World,
		},
		SaveRequestID: row.SaveRequestID,
	}
}

func (s *Service) activateCheckpoint(ctx context.Context, world WorldKey, runID string, clock Clock, ref CheckpointRef) (Head, error) {
	var result Head
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		row, found, err := s.store.loadCheckpointTx(ctx, tx, world, ref.ID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCheckpointMissing
		}
		if row.SchemaVersion != ref.SchemaVersion || row.Checksum != ref.Checksum || row.World != ref.World {
			return ErrCheckpointInvalid
		}
		snapshot, err := decodeCheckpointRow(row)
		if err != nil {
			return err
		}
		if clock.ID != snapshot.PreparedHead.Clock.ID {
			return ErrClockMismatch
		}
		if clock.Tick < snapshot.PreparedHead.Clock.Tick {
			return ErrClockRewound
		}

		current, currentFound, err := s.store.loadWorldHeadTx(ctx, tx, world)
		if err != nil {
			return err
		}
		if currentFound && current.Head.Binding.RunID == runID {
			return ErrTaskChanged
		}
		baseGeneration := snapshot.PreparedHead.Binding.Generation
		if currentFound && current.Head.Binding.Generation > baseGeneration {
			baseGeneration = current.Head.Binding.Generation
		}
		generation, err := NextDurableCounter(baseGeneration)
		if err != nil {
			return err
		}
		head := Head{
			Binding: Binding{World: world, RunID: runID, Generation: generation},
			Clock:   clock, CheckpointID: row.ID, Status: worldHeadStatusReady,
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM task_wakeups WHERE game_id = ? AND world_id = ?`, world.GameID, world.WorldID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE game_id = ? AND world_id = ?`, world.GameID, world.WorldID); err != nil {
			return err
		}
		if err := s.store.afterCheckpointStage(ctx, "restore_working_set_cleared"); err != nil {
			return err
		}

		records := make(map[worldTaskIdentity]Record, len(snapshot.Tasks))
		for _, item := range snapshot.Tasks {
			if err := s.store.insertCheckpointTaskTx(ctx, tx, item); err != nil {
				return err
			}
			records[worldTaskIdentity{owner: item.Record.Owner, taskID: item.Record.ID}] = item.Record
		}
		for _, item := range snapshot.Wakes {
			wake := item.Wake
			if wake.Status == wakeStatusPending {
				wake.Generation = generation
				wake.ClaimID = ""
				wake.ClaimedBy = ""
			}
			record, found := records[worldTaskIdentity{owner: wake.Owner, taskID: wake.TaskID}]
			if !found {
				return ErrCheckpointInvalid
			}
			if err := s.store.insertCheckpointWakeTx(ctx, tx, record, item.ClockID, wake); err != nil {
				return err
			}
		}

		updated := worldHeadRow{Head: head, RuntimeInstanceID: s.claimantID}
		if currentFound {
			if err := s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "checkpoint_restored"); err != nil {
				return err
			}
		} else {
			raw, err := json.Marshal(updated)
			if err != nil {
				return ErrCheckpointInvalid
			}
			if err := s.store.insertWorldHeadTx(ctx, tx, updated, raw); err != nil {
				return err
			}
			if err := s.store.afterCheckpointStage(ctx, "checkpoint_restored"); err != nil {
				return err
			}
		}
		result = head
		return nil
	})
	if err != nil {
		return Head{}, err
	}
	return result, nil
}

func (s *Service) pauseWorldForCheckpointFailure(ctx context.Context, world WorldKey, runID string, clock Clock, failure error) error {
	code, ok := checkpointRecoveryCode(failure)
	if !ok {
		return ErrCheckpointInvalid
	}
	return s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.store.loadWorldHeadTx(ctx, tx, world)
		if err != nil {
			return err
		}
		if !found {
			head := Head{
				Binding: Binding{World: world, RunID: runID, Generation: 1},
				Clock:   clock, Status: worldHeadStatusPaused, Reason: string(code),
			}
			row := worldHeadRow{
				Head: head, RuntimeInstanceID: s.claimantID,
				RecoveryRunID: runID, RecoveryError: code,
			}
			if err := row.validate(); err != nil {
				return err
			}
			raw, err := json.Marshal(row)
			if err != nil {
				return ErrCheckpointInvalid
			}
			if err := s.store.insertWorldHeadTx(ctx, tx, row, raw); err != nil {
				return err
			}
			return s.store.afterCheckpointStage(ctx, "checkpoint_restore_paused")
		}
		if current.RecoveryRunID == "" && current.Head.Binding.RunID == runID {
			return nil
		}
		if current.Head.Status == worldHeadStatusPaused && current.Head.Reason == string(code) &&
			current.RecoveryRunID == runID && current.RecoveryError == code {
			return nil
		}
		updated := current
		updated.Head.Status = worldHeadStatusPaused
		updated.Head.Reason = string(code)
		updated.SaveRequestID = ""
		updated.BarrierStatus = ""
		updated.BarrierPreparedAtUnixMS = 0
		updated.RecoveryRunID = runID
		updated.RecoveryError = code
		return s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "checkpoint_restore_paused")
	})
}

func checkpointRecoveryCode(failure error) (Code, bool) {
	var taskErr *Error
	if !errors.As(failure, &taskErr) || taskErr == nil || !taskErr.Code.Valid() {
		return "", false
	}
	return taskErr.Code, true
}

func checkpointRecoveryError(head worldHeadRow) error {
	if head.RecoveryRunID == "" || !head.RecoveryError.Valid() {
		return ErrCheckpointInvalid
	}
	return &Error{Code: head.RecoveryError}
}

func (s *Service) activatePausedWorkingHead(ctx context.Context, world WorldKey, runID string, clock Clock) (Head, error) {
	wakeIDCount, err := s.store.countWorldTasks(ctx, world)
	if err != nil {
		return Head{}, err
	}
	wakeIDs, err := s.checkpointWakeIDs(wakeIDCount)
	if err != nil {
		return Head{}, err
	}

	var result Head
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.loadWorldHeadForMutationTx(ctx, tx, world)
		if err != nil {
			return err
		}
		if !found {
			return ErrWorldNotReady
		}
		if current.Head.Binding.RunID != runID {
			return ErrTaskChanged
		}
		if current.Head.Status != worldHeadStatusPaused {
			return ErrTaskChanged
		}
		if current.RecoveryRunID != "" {
			return checkpointRecoveryError(current)
		}
		if current.BarrierStatus != "" || current.SaveRequestID != "" {
			return ErrSaveInProgress
		}
		if err := validateMonotonicClock(current.Head.Clock, clock); err != nil {
			return err
		}
		generation, err := NextDurableCounter(current.Head.Binding.Generation)
		if err != nil {
			return err
		}
		normalizationHead := current
		normalizationHead.Head.Clock = clock
		if err := s.store.normalizeCheckpointWorkingSetTx(ctx, tx, normalizationHead, generation, wakeIDs); err != nil {
			return err
		}
		updated := normalizationHead
		updated.Head.Binding.Generation = generation
		updated.Head.Clock = clock
		updated.Head.Status = worldHeadStatusReady
		updated.Head.Reason = ""
		updated.RuntimeInstanceID = s.claimantID
		if err := s.store.updateWorldHeadCASTx(ctx, tx, current, updated, "working_head_reactivated"); err != nil {
			return err
		}
		result = updated.Head
		return nil
	})
	if err != nil {
		return Head{}, err
	}
	return result, nil
}

func (s *Service) checkpointWakeIDs(count int) ([]string, error) {
	if count < 0 {
		return nil, ErrInvalidTaskSpec
	}
	ids := make([]string, count)
	seen := make(map[string]struct{}, count)
	for index := range ids {
		ids[index] = s.newID("wake")
		if !requiredIdentity(ids[index]) {
			return nil, ErrInvalidTaskSpec
		}
		if _, duplicate := seen[ids[index]]; duplicate {
			return nil, ErrTaskConflict
		}
		seen[ids[index]] = struct{}{}
	}
	return ids, nil
}

func (s *SQLiteStore) countWorldTasks(ctx context.Context, world WorldKey) (int, error) {
	if err := world.Validate(); err != nil {
		return 0, err
	}
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE game_id = ? AND world_id = ?`, world.GameID, world.WorldID).Scan(&count); err != nil {
		return 0, classifyStoreError(err)
	}
	if count < 0 || count > int64(math.MaxInt) {
		return 0, ErrInvalidTaskSpec
	}
	return int(count), nil
}

func (s *SQLiteStore) normalizeCheckpointWorkingSetTx(ctx context.Context, tx *sql.Tx, head worldHeadRow, generation uint64, wakeIDs []string) error {
	graph, err := s.loadWorldTaskIdentityGraphTx(ctx, tx, head.Head.Binding.World)
	if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
		return ErrInvalidTaskSpec
	}
	if err != nil {
		return err
	}
	wakes, err := s.loadWorldDurableWakesTx(ctx, tx, head, graph)
	if err != nil {
		return err
	}
	active := make(map[worldTaskIdentity][]durableWake)
	for _, wake := range wakes {
		switch wake.wake.Status {
		case wakeStatusPending, wakeStatusClaimed, wakeStatusEnqueued, wakeStatusRunning:
			identity := worldTaskIdentity{owner: wake.wake.Owner, taskID: wake.wake.TaskID}
			active[identity] = append(active[identity], wake)
			if wake.wake.Status != wakeStatusPending && wake.wake.ClaimedBy != head.RuntimeInstanceID {
				return ErrInvalidTaskSpec
			}
		}
	}

	identities := sortedWorldTaskIdentities(graph.tasks)
	wakeIndex := 0
	for _, identity := range identities {
		current := graph.tasks[identity]
		executable := active[identity]
		switch current.record.State {
		case StateWaiting:
			if len(executable) != 1 || executable[0].wake.Status == wakeStatusRunning {
				return ErrInvalidTaskSpec
			}
			updated := executable[0].wake
			updated.Status = wakeStatusPending
			updated.ClaimID = ""
			updated.ClaimedBy = ""
			updated.Generation = generation
			if executable[0].wake.Status != wakeStatusPending {
				updated.RetryAfterUnixMS = 0
			}
			if err := s.updateCheckpointWakeTx(ctx, tx, executable[0], updated, "checkpoint_waiting_rebound"); err != nil {
				return err
			}
		case StateRunning:
			if len(executable) > 1 || len(executable) == 1 && executable[0].wake.Status != wakeStatusRunning {
				return ErrInvalidTaskSpec
			}
			if wakeIndex >= len(wakeIDs) {
				return ErrTaskChanged
			}
			updatedRecord, err := recoveredRunningRecord(current.record, head.Head.Clock.Tick)
			if err != nil {
				return err
			}
			mutation, err := s.prepareReconcileMutation(current, updatedRecord)
			if err != nil {
				return err
			}
			if err := s.updateRestartRecordTx(ctx, tx, current.record, mutation); err != nil {
				return err
			}
			if len(executable) == 1 {
				consumed := executable[0].wake
				consumed.Status = wakeStatusConsumed
				consumed.RetryAfterUnixMS = 0
				if err := s.updateCheckpointWakeTx(ctx, tx, executable[0], consumed, "checkpoint_running_consumed"); err != nil {
					return err
				}
			}
			replacement := Wake{
				ID: wakeIDs[wakeIndex], TaskID: updatedRecord.ID, Owner: updatedRecord.Owner,
				ExpectedRevision: updatedRecord.Revision, DueTick: *updatedRecord.NextWakeAt,
				Reason: wakeReasonRestartReconcile, Status: wakeStatusPending, Generation: generation,
			}
			wakeIndex++
			if err := s.insertWakeTx(ctx, tx, updatedRecord, replacement); err != nil {
				return err
			}
		case StatePaused, StateSucceeded, StateFailed, StateCancelled:
			if len(executable) != 0 {
				return ErrInvalidTaskSpec
			}
		default:
			return ErrInvalidTaskSpec
		}
	}
	return s.afterCheckpointStage(ctx, "working_set_fenced")
}

func sortedWorldTaskIdentities(tasks map[worldTaskIdentity]storedIntentTask) []worldTaskIdentity {
	identities := make([]worldTaskIdentity, 0, len(tasks))
	for identity := range tasks {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		left, right := identities[i], identities[j]
		if left.owner.GameID != right.owner.GameID {
			return left.owner.GameID < right.owner.GameID
		}
		if left.owner.WorldID != right.owner.WorldID {
			return left.owner.WorldID < right.owner.WorldID
		}
		if left.owner.EntityID != right.owner.EntityID {
			return left.owner.EntityID < right.owner.EntityID
		}
		return left.taskID < right.taskID
	})
	return identities
}

func (s *SQLiteStore) updateCheckpointWakeTx(ctx context.Context, tx *sql.Tx, before durableWake, updated Wake, stage string) error {
	if err := updated.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	wake := before.wake
	result, err := tx.ExecContext(ctx, `UPDATE task_wakeups SET
		expected_revision = ?, due_tick = ?, reason = ?, status = ?, claim_id = ?, claimed_by = ?,
		generation = ?, attempt = ?, retry_after_unix_ms = ?, wake_json = ?
		WHERE wake_id = ? AND game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?
			AND clock_id = ? AND expected_revision = ? AND due_tick = ? AND reason = ? AND status = ?
			AND claim_id = ? AND claimed_by = ? AND generation = ? AND attempt = ?
			AND retry_after_unix_ms = ? AND wake_json = ?`,
		int64(updated.ExpectedRevision), updated.DueTick, updated.Reason, updated.Status,
		updated.ClaimID, updated.ClaimedBy, int64(updated.Generation), updated.Attempt,
		updated.RetryAfterUnixMS, raw,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		before.clock, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
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
	return s.afterCheckpointStage(ctx, stage)
}

func (s *SQLiteStore) captureCheckpointSnapshotTx(ctx context.Context, tx *sql.Tx, head Head, request checkpointPrepareRequest) (checkpointSnapshot, []byte, error) {
	rows, err := tx.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id`,
		head.Binding.World.GameID, head.Binding.World.WorldID)
	if err != nil {
		return checkpointSnapshot{}, nil, err
	}
	tasks := make([]checkpointTaskSnapshot, 0)
	for rows.Next() {
		columns, err := scanTaskRowColumns(rows)
		if err != nil {
			_ = rows.Close()
			return checkpointSnapshot{}, nil, err
		}
		record, _, fingerprint, _, err := decodeTaskRowColumns(columns)
		if err != nil {
			_ = rows.Close()
			return checkpointSnapshot{}, nil, err
		}
		record, err = normalizeCheckpointRecord(record)
		if err != nil {
			_ = rows.Close()
			return checkpointSnapshot{}, nil, err
		}
		tasks = append(tasks, checkpointTaskSnapshot{
			Record: record, CreateFingerprint: fingerprint,
			CreateResponseJSON: append(json.RawMessage(nil), columns.createResponseJSON...),
			CreateResponseHash: columns.createResponseHash,
			IntentHistoryJSON:  append(json.RawMessage(nil), columns.intentHistoryJSON...),
			IntentHistoryHash:  columns.intentHistoryHash,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return checkpointSnapshot{}, nil, err
	}
	if err := rows.Close(); err != nil {
		return checkpointSnapshot{}, nil, err
	}

	graph, err := s.loadWorldTaskIdentityGraphTx(ctx, tx, head.Binding.World)
	if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
		return checkpointSnapshot{}, nil, ErrInvalidTaskSpec
	}
	if err != nil {
		return checkpointSnapshot{}, nil, err
	}
	wakeRows, err := s.loadWorldDurableWakesTx(ctx, tx, worldHeadRow{Head: head, RuntimeInstanceID: "snapshot"}, graph)
	if err != nil {
		return checkpointSnapshot{}, nil, err
	}
	wakes := make([]checkpointWakeSnapshot, len(wakeRows))
	for index, wake := range wakeRows {
		wakes[index] = checkpointWakeSnapshot{ClockID: wake.clock, Wake: wake.wake}
	}
	snapshot := checkpointSnapshot{
		SchemaVersion: checkpointSchemaVersion,
		World:         head.Binding.World,
		PreparedHead:  head,
		Request:       request,
		Tasks:         tasks,
		Wakes:         wakes,
	}
	if err := snapshot.validate(); err != nil {
		return checkpointSnapshot{}, nil, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return checkpointSnapshot{}, nil, ErrInvalidTaskSpec
	}
	return snapshot, raw, nil
}

func normalizeCheckpointRecord(record Record) (Record, error) {
	cloned, err := cloneRecord(record)
	if err != nil {
		return Record{}, ErrInvalidTaskSpec
	}
	sort.Slice(cloned.Operations, func(i, j int) bool { return cloned.Operations[i].ID < cloned.Operations[j].ID })
	sort.Slice(cloned.Evidence, func(i, j int) bool { return cloned.Evidence[i].FactID < cloned.Evidence[j].FactID })
	sort.Slice(cloned.Cleanup, func(i, j int) bool { return cloned.Cleanup[i].OperationID < cloned.Cleanup[j].OperationID })
	if cloned.Result != nil {
		sort.Strings(cloned.Result.EvidenceRefs)
	}
	if err := cloned.Validate(); err != nil {
		return Record{}, err
	}
	return cloned, nil
}

func (s checkpointSnapshot) validate() error {
	if s.SchemaVersion != checkpointSchemaVersion || s.World != s.PreparedHead.Binding.World ||
		s.World != s.Request.Binding.World || s.PreparedHead.Status != worldHeadStatusReady ||
		s.PreparedHead.Reason != "" || !requiredIdentity(s.PreparedHead.CheckpointID) ||
		!requiredIdentity(s.Request.SaveRequestID) || s.Tasks == nil || s.Wakes == nil || s.Request.Evidence == nil {
		return ErrCheckpointInvalid
	}
	if err := s.World.Validate(); err != nil {
		return ErrCheckpointInvalid
	}
	if err := s.PreparedHead.Binding.Validate(); err != nil {
		return ErrCheckpointInvalid
	}
	if err := s.PreparedHead.Clock.Validate(); err != nil || s.PreparedHead.Clock.Sequence > uint64(math.MaxInt64) ||
		s.PreparedHead.Clock != s.Request.Clock {
		return ErrCheckpointInvalid
	}
	bindingErr := s.Request.Binding.Validate()
	nextGeneration, generationErr := NextDurableCounter(s.Request.Binding.Generation)
	if bindingErr != nil || s.Request.Binding.RunID != s.PreparedHead.Binding.RunID ||
		generationErr != nil || nextGeneration != s.PreparedHead.Binding.Generation {
		return ErrCheckpointInvalid
	}
	for index, evidence := range s.Request.Evidence {
		if err := evidence.Validate(); err != nil || evidence.Applied || evidence.Binding.World != s.World ||
			evidence.RevalidatedIn != nil && evidence.RevalidatedIn.World != s.World {
			return ErrCheckpointInvalid
		}
		if index > 0 && s.Request.Evidence[index-1].FactID >= evidence.FactID {
			return ErrCheckpointInvalid
		}
	}

	taskMap := make(map[worldTaskIdentity]Record, len(s.Tasks))
	taskIDs := make(map[string]struct{}, len(s.Tasks))
	operationIDs := make(map[string]struct{})
	factIDs := make(map[string]struct{})
	storedEvidence := make(map[string]Evidence)
	sourceIDs := make(map[evidenceSourceIdentity]struct{})
	activeEquivalence := make(map[checkpointEquivalenceKey]struct{})
	ownerCalls := make(map[checkpointCallKey]struct{})
	for index, task := range s.Tasks {
		if index > 0 && !checkpointTaskLess(s.Tasks[index-1], task) {
			return ErrCheckpointInvalid
		}
		columns, err := checkpointTaskColumns(task)
		if err != nil {
			return ErrCheckpointInvalid
		}
		record, _, _, history, err := decodeTaskRowColumns(columns)
		if err != nil || !sameOwnerWorld(record.Owner, s.World) || record.State == StateRunning {
			return ErrCheckpointInvalid
		}
		normalized, err := normalizeCheckpointRecord(record)
		if err != nil {
			return ErrCheckpointInvalid
		}
		recordJSON, recordErr := json.Marshal(record)
		normalizedJSON, normalizedErr := json.Marshal(normalized)
		if recordErr != nil || normalizedErr != nil || !bytes.Equal(recordJSON, normalizedJSON) {
			return ErrCheckpointInvalid
		}
		identity := worldTaskIdentity{owner: record.Owner, taskID: record.ID}
		if _, duplicate := taskMap[identity]; duplicate {
			return ErrCheckpointInvalid
		}
		if _, duplicate := taskIDs[record.ID]; duplicate {
			return ErrCheckpointInvalid
		}
		taskMap[identity] = record
		taskIDs[record.ID] = struct{}{}
		call := checkpointCallIdentity(record.Owner.EntityID, record.Spec.Source)
		if !requiredIdentity(call.eventID) || !requiredIdentity(call.turnID) || !requiredIdentity(call.callID) {
			return ErrCheckpointInvalid
		}
		if _, duplicate := ownerCalls[call]; duplicate {
			return ErrCheckpointInvalid
		}
		ownerCalls[call] = struct{}{}
		for _, intent := range history {
			request, _, err := validateIntentCall(intent, record)
			if err != nil {
				return ErrCheckpointInvalid
			}
			call := checkpointCallIdentity(record.Owner.EntityID, request.Source)
			if _, duplicate := ownerCalls[call]; duplicate {
				return ErrCheckpointInvalid
			}
			ownerCalls[call] = struct{}{}
		}
		if record.Spec.EquivalenceKey != "" && !taskStateTerminal(record.State) {
			key := checkpointEquivalenceKey{entityID: record.Owner.EntityID, equivalenceID: record.Spec.EquivalenceKey}
			if _, duplicate := activeEquivalence[key]; duplicate {
				return ErrCheckpointInvalid
			}
			activeEquivalence[key] = struct{}{}
		}
		for _, operation := range record.Operations {
			if _, duplicate := operationIDs[operation.ID]; duplicate {
				return ErrCheckpointInvalid
			}
			operationIDs[operation.ID] = struct{}{}
		}
		for _, evidence := range record.Evidence {
			if _, duplicate := factIDs[evidence.FactID]; duplicate {
				return ErrCheckpointInvalid
			}
			factIDs[evidence.FactID] = struct{}{}
			storedEvidence[evidence.FactID] = evidence
			if source, complete := completeEvidenceSourceIdentity(evidence.Source); complete {
				if _, duplicate := sourceIDs[source]; duplicate {
					return ErrCheckpointInvalid
				}
				sourceIDs[source] = struct{}{}
			}
		}
	}

	activeWakes := make(map[worldTaskIdentity]int)
	wakeIDs := make(map[string]struct{}, len(s.Wakes))
	wakeKeys := make(map[checkpointWakeKey]struct{}, len(s.Wakes))
	for index, wake := range s.Wakes {
		if index > 0 && !checkpointWakeLess(s.Wakes[index-1], wake) {
			return ErrCheckpointInvalid
		}
		if err := wake.Wake.Validate(); err != nil || !requiredIdentity(wake.ClockID) || !sameOwnerWorld(wake.Wake.Owner, s.World) {
			return ErrCheckpointInvalid
		}
		if wake.Wake.Status != wakeStatusPending && wake.Wake.Status != wakeStatusConsumed {
			return ErrCheckpointInvalid
		}
		identity := worldTaskIdentity{owner: wake.Wake.Owner, taskID: wake.Wake.TaskID}
		record, found := taskMap[identity]
		if !found || wake.ClockID != record.Spec.ClockID || validateTaskWakePair(record, wake.Wake) != nil {
			return ErrCheckpointInvalid
		}
		if _, duplicate := wakeIDs[wake.Wake.ID]; duplicate {
			return ErrCheckpointInvalid
		}
		wakeIDs[wake.Wake.ID] = struct{}{}
		key := checkpointWakeKey{
			owner: wake.Wake.Owner.EntityID, taskID: wake.Wake.TaskID,
			expectedRevision: wake.Wake.ExpectedRevision, reason: wake.Wake.Reason,
		}
		if _, duplicate := wakeKeys[key]; duplicate {
			return ErrCheckpointInvalid
		}
		wakeKeys[key] = struct{}{}
		if wake.Wake.Status == wakeStatusPending {
			if wake.Wake.Generation != s.PreparedHead.Binding.Generation {
				return ErrCheckpointInvalid
			}
			activeWakes[identity]++
		}
	}
	for identity, record := range taskMap {
		want := 0
		if record.State == StateWaiting {
			want = 1
		}
		if activeWakes[identity] != want {
			return ErrCheckpointInvalid
		}
	}
	for _, candidate := range s.Request.Evidence {
		stored, found := storedEvidence[candidate.FactID]
		if !found || !evidenceEqualIgnoringApplied(stored, candidate) {
			return ErrCheckpointInvalid
		}
	}
	return nil
}

func checkpointCallIdentity(entityID string, source SourceRef) checkpointCallKey {
	return checkpointCallKey{entityID: entityID, eventID: source.EventID, turnID: source.TurnID, callID: source.CallID}
}

func checkpointTaskLess(left, right checkpointTaskSnapshot) bool {
	if left.Record.Owner.EntityID != right.Record.Owner.EntityID {
		return left.Record.Owner.EntityID < right.Record.Owner.EntityID
	}
	return left.Record.ID < right.Record.ID
}

func checkpointWakeLess(left, right checkpointWakeSnapshot) bool {
	if left.Wake.Owner.EntityID != right.Wake.Owner.EntityID {
		return left.Wake.Owner.EntityID < right.Wake.Owner.EntityID
	}
	if left.Wake.TaskID != right.Wake.TaskID {
		return left.Wake.TaskID < right.Wake.TaskID
	}
	return left.Wake.ID < right.Wake.ID
}

func checkpointTaskColumns(task checkpointTaskSnapshot) (taskRowColumns, error) {
	recordJSON, err := json.Marshal(task.Record)
	if err != nil {
		return taskRowColumns{}, err
	}
	nextWake := sql.NullInt64{}
	if task.Record.NextWakeAt != nil {
		nextWake = sql.NullInt64{Int64: *task.Record.NextWakeAt, Valid: true}
	}
	return taskRowColumns{
		gameID: task.Record.Owner.GameID, worldID: task.Record.Owner.WorldID,
		entityID: task.Record.Owner.EntityID, taskID: task.Record.ID,
		state: string(task.Record.State), revision: int64(task.Record.Revision),
		clockID: task.Record.Spec.ClockID, nextWakeAt: nextWake,
		createEventID: task.Record.Spec.Source.EventID, createTurnID: task.Record.Spec.Source.TurnID,
		createCallID: task.Record.Spec.Source.CallID, createFingerprint: task.CreateFingerprint,
		equivalenceKey: task.Record.Spec.EquivalenceKey, recordJSON: recordJSON,
		createResponseJSON: append([]byte(nil), task.CreateResponseJSON...),
		createResponseHash: task.CreateResponseHash,
		intentHistoryJSON:  append([]byte(nil), task.IntentHistoryJSON...),
		intentHistoryHash:  task.IntentHistoryHash,
	}, nil
}

func validateDecodedCheckpoint(row checkpointRow, snapshot checkpointSnapshot) error {
	if err := row.validate(); err != nil || row.SchemaVersion != checkpointSchemaVersion ||
		row.World != snapshot.World || row.SaveRequestID != snapshot.Request.SaveRequestID ||
		row.Clock != snapshot.PreparedHead.Clock || row.ID != snapshot.PreparedHead.CheckpointID ||
		len(row.Checksum) != 64 {
		return ErrCheckpointInvalid
	}
	if _, err := hex.DecodeString(row.Checksum); err != nil {
		return ErrCheckpointInvalid
	}
	if err := snapshot.validate(); err != nil {
		return err
	}
	return nil
}

func decodeCheckpointRow(row checkpointRow) (checkpointSnapshot, error) {
	if err := row.validate(); err != nil || row.SchemaVersion != checkpointSchemaVersion || len(row.Snapshot) == 0 ||
		sha256Hex(row.Snapshot) != row.Checksum {
		return checkpointSnapshot{}, ErrCheckpointInvalid
	}
	var snapshot checkpointSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		return checkpointSnapshot{}, ErrCheckpointInvalid
	}
	canonical, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(canonical, row.Snapshot) {
		return checkpointSnapshot{}, ErrCheckpointInvalid
	}
	if err := validateDecodedCheckpoint(row, snapshot); err != nil {
		return checkpointSnapshot{}, err
	}
	return snapshot, nil
}

func (s *SQLiteStore) insertCheckpointTx(ctx context.Context, tx *sql.Tx, row checkpointRow) error {
	if err := row.validate(); err != nil {
		return err
	}
	if len(row.Snapshot) > s.options.MaxSnapshotBytes {
		return ErrInvalidTaskSpec
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO task_checkpoints (
		checkpoint_id, game_id, world_id, save_request_id, schema_version,
		clock_id, clock_tick, clock_sequence, checksum, snapshot_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.World.GameID, row.World.WorldID, row.SaveRequestID, row.SchemaVersion,
		row.Clock.ID, row.Clock.Tick, int64(row.Clock.Sequence), row.Checksum, append([]byte(nil), row.Snapshot...))
	if err != nil {
		return err
	}
	return s.afterCheckpointStage(ctx, "snapshot_inserted")
}

func (s *SQLiteStore) insertCheckpointTaskTx(ctx context.Context, tx *sql.Tx, task checkpointTaskSnapshot) error {
	columns, err := checkpointTaskColumns(task)
	if err != nil {
		return ErrCheckpointInvalid
	}
	if _, _, _, _, err := decodeTaskRowColumns(columns); err != nil {
		return ErrCheckpointInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks (
		game_id, world_id, entity_id, task_id, state, revision, clock_id, next_wake_at,
		create_event_id, create_turn_id, create_call_id, create_fingerprint, equivalence_key,
		record_json, create_response_json, create_response_hash, intent_history_json, intent_history_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		columns.gameID, columns.worldID, columns.entityID, columns.taskID, columns.state,
		columns.revision, columns.clockID, nullableTick(task.Record.NextWakeAt),
		columns.createEventID, columns.createTurnID, columns.createCallID, columns.createFingerprint,
		columns.equivalenceKey, columns.recordJSON, columns.createResponseJSON, columns.createResponseHash,
		columns.intentHistoryJSON, columns.intentHistoryHash)
	if err != nil {
		return err
	}
	return s.afterCheckpointStage(ctx, "restore_task_inserted")
}

func (s *SQLiteStore) insertCheckpointWakeTx(ctx context.Context, tx *sql.Tx, record Record, clockID string, wake Wake) error {
	if clockID != record.Spec.ClockID || validateTaskWakePair(record, wake) != nil {
		return ErrCheckpointInvalid
	}
	raw, err := json.Marshal(wake)
	if err != nil {
		return ErrCheckpointInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		clockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
		wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt,
		wake.RetryAfterUnixMS, raw)
	if err != nil {
		return err
	}
	return s.afterCheckpointStage(ctx, "restore_wake_inserted")
}

func (s *SQLiteStore) loadCheckpointBySaveRequestTx(ctx context.Context, tx *sql.Tx, world WorldKey, saveRequestID string) (checkpointRow, bool, error) {
	if !requiredIdentity(saveRequestID) {
		return checkpointRow{}, false, ErrInvalidTaskSpec
	}
	row, err := scanCheckpointRow(tx.QueryRowContext(ctx, `SELECT checkpoint_id, game_id, world_id, save_request_id,
		schema_version, clock_id, clock_tick, clock_sequence, checksum, snapshot_json
		FROM task_checkpoints WHERE game_id = ? AND world_id = ? AND save_request_id = ?`,
		world.GameID, world.WorldID, saveRequestID))
	if errors.Is(err, sql.ErrNoRows) {
		return checkpointRow{}, false, nil
	}
	if err != nil {
		return checkpointRow{}, false, err
	}
	return row, true, nil
}

func (s *SQLiteStore) loadCheckpointTx(ctx context.Context, tx *sql.Tx, world WorldKey, checkpointID string) (checkpointRow, bool, error) {
	row, err := scanCheckpointRow(tx.QueryRowContext(ctx, `SELECT checkpoint_id, game_id, world_id, save_request_id,
		schema_version, clock_id, clock_tick, clock_sequence, checksum, snapshot_json
		FROM task_checkpoints WHERE game_id = ? AND world_id = ? AND checkpoint_id = ?`,
		world.GameID, world.WorldID, checkpointID))
	if errors.Is(err, sql.ErrNoRows) {
		return checkpointRow{}, false, nil
	}
	if err != nil {
		return checkpointRow{}, false, err
	}
	return row, true, nil
}

func (s *SQLiteStore) validateRestartBarrierTx(ctx context.Context, tx *sql.Tx, head worldHeadRow) error {
	if head.SaveRequestID == "" && head.BarrierStatus == "" {
		return nil
	}
	if head.BarrierStatus != checkpointBarrierPrepared || !requiredIdentity(head.SaveRequestID) ||
		!requiredIdentity(head.Head.CheckpointID) {
		return ErrCheckpointInvalid
	}
	row, found, err := s.loadCheckpointBySaveRequestTx(ctx, tx, head.Head.Binding.World, head.SaveRequestID)
	if err != nil {
		return err
	}
	if !found || row.ID != head.Head.CheckpointID {
		return ErrCheckpointInvalid
	}
	snapshot, err := decodeCheckpointRow(row)
	if err != nil || snapshot.PreparedHead != head.Head {
		return ErrCheckpointInvalid
	}
	return nil
}

func scanCheckpointRow(scanner rowScanner) (checkpointRow, error) {
	var row checkpointRow
	var sequence int64
	if err := scanner.Scan(&row.ID, &row.World.GameID, &row.World.WorldID, &row.SaveRequestID,
		&row.SchemaVersion, &row.Clock.ID, &row.Clock.Tick, &sequence, &row.Checksum, &row.Snapshot); err != nil {
		return checkpointRow{}, err
	}
	if sequence < 0 {
		return checkpointRow{}, ErrCheckpointInvalid
	}
	row.Clock.Sequence = uint64(sequence)
	if err := row.validate(); err != nil {
		return checkpointRow{}, ErrCheckpointInvalid
	}
	return row, nil
}

func (s *SQLiteStore) updateWorldHeadCASTx(ctx context.Context, tx *sql.Tx, before, updated worldHeadRow, stage string) error {
	if err := before.validate(); err != nil {
		return err
	}
	if err := updated.validate(); err != nil || before.Head.Binding.World != updated.Head.Binding.World {
		return ErrInvalidTaskSpec
	}
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	updatedJSON, err := json.Marshal(updated)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_world_heads SET
		run_id = ?, generation = ?, clock_id = ?, clock_tick = ?, clock_sequence = ?,
		checkpoint_id = ?, save_request_id = ?, barrier_status = ?, runtime_instance_id = ?,
		status = ?, reason = ?, head_json = ?
		WHERE game_id = ? AND world_id = ? AND head_json = ?`,
		updated.Head.Binding.RunID, int64(updated.Head.Binding.Generation), updated.Head.Clock.ID,
		updated.Head.Clock.Tick, int64(updated.Head.Clock.Sequence), updated.Head.CheckpointID,
		updated.SaveRequestID, updated.BarrierStatus, updated.RuntimeInstanceID,
		updated.Head.Status, updated.Head.Reason, updatedJSON,
		before.Head.Binding.World.GameID, before.Head.Binding.World.WorldID, beforeJSON)
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
	return s.afterCheckpointStage(ctx, stage)
}

func (s *SQLiteStore) afterCheckpointStage(ctx context.Context, stage string) error {
	if s.testAfterCheckpointStage != nil {
		return s.testAfterCheckpointStage(ctx, stage)
	}
	return ctx.Err()
}
