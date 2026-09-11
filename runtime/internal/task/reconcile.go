package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

const (
	wakeReasonEvidenceWait = "evidence_wait"

	reconcileStageRecordUpdated = "record_updated"
	reconcileStageWakesConsumed = "wakes_consumed"
	reconcileStageWakeInserted  = "wake_inserted"
)

type preparedReconcileMutation struct {
	record     Record
	recordJSON []byte
}

func (s *Service) Reconcile(ctx context.Context, exec ExecutionContext) (ReconcileResult, error) {
	if err := validateService(s, ctx); err != nil {
		return ReconcileResult{}, err
	}
	if err := validateReconcileExecution(exec); err != nil {
		return ReconcileResult{}, err
	}
	candidateResultID := s.newID("result")
	candidateWakeID := s.newID("wake")

	var result ReconcileResult
	err := s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
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

		current, err := s.store.loadIntentTaskTx(ctx, tx, exec.Owner, exec.TaskID)
		if err != nil {
			return err
		}
		if current.record.Spec.ClockID != exec.Clock.ID {
			return ErrClockMismatch
		}
		if _, err := s.store.loadWorldTaskIdentityGraphTx(ctx, tx, exec.Binding.World); err != nil {
			if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
				return ErrTaskConflict
			}
			return err
		}

		pending := pendingEvidence(current.record)
		if len(pending) == 0 {
			result = routeReconcileWithoutPending(current.record, exec)
			if result.Next == "" {
				return ErrTaskChanged
			}
			return nil
		}
		if taskStateTerminal(current.record.State) {
			return ErrInvalidTaskSpec
		}
		if current.record.Result != nil {
			return ErrInvalidTaskSpec
		}
		if current.record.Revision != exec.ExpectedRevision {
			return ErrTaskChanged
		}
		updated, err := reconciledRecord(current.record, pending, candidateResultID)
		if err != nil {
			return err
		}
		prepared, err := s.store.prepareReconcileMutation(current, updated)
		if err != nil {
			return err
		}
		if err := s.store.updateReconcileRecordTx(ctx, tx, current.record, prepared); err != nil {
			return err
		}
		if err := s.store.consumeReconcileWakesTx(ctx, tx, current.record); err != nil {
			return err
		}
		next := ReconcileNextSettled
		switch updated.State {
		case StateWaiting:
			nextWakeAt := *updated.NextWakeAt
			wake := Wake{
				ID: candidateWakeID, TaskID: updated.ID, Owner: updated.Owner,
				ExpectedRevision: updated.Revision, DueTick: nextWakeAt,
				Reason: wakeReasonEvidenceWait, Status: wakeStatusPending,
				Generation: head.Head.Binding.Generation,
			}
			if err := s.store.insertReconcileWakeTx(ctx, tx, prepared.record, wake); err != nil {
				return err
			}
			if nextWakeAt <= head.Head.Clock.Tick {
				next = ReconcileNextObserve
			}
		case StateRunning:
			next = ReconcileNextDecide
			if head.Head.Clock.Tick >= updated.Spec.DeadlineAt {
				next = ReconcileNextObserve
			}
		}
		result = ReconcileResult{Task: prepared.record, Next: next}
		return nil
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	return result, nil
}

// reconciledRecord derives the same deterministic candidate for admission and
// execution. It reads typed evidence fields and preserves opaque payload bytes.
func reconciledRecord(record Record, pending []Evidence, resultID string) (Record, error) {
	nextRevision, err := NextDurableCounter(record.Revision)
	if err != nil {
		return Record{}, err
	}
	updated := record
	updated.Revision = nextRevision
	updated.Evidence = cloneEvidenceSlice(record.Evidence)
	for index := range updated.Evidence {
		updated.Evidence[index].Applied = true
	}
	updated.NeedsReconcile = false
	updated.PauseReason = ""
	updated.NoProgressAttempts = 0
	updated.ReconcileAttempts = 0
	updated.NextWakeAt = nil
	updated.Result = nil
	if terminal, found := selectTerminalEvidence(pending); found {
		updated.State = terminalResultState(terminal.Kind)
		updated.Result = &Result{
			ID: resultID, TaskID: updated.ID, Revision: updated.Revision,
			State: updated.State, Reason: terminal.Kind, OccurredAt: terminal.OccurredAt,
			EvidenceRefs: []string{terminal.FactID}, Source: terminal.Source,
		}
		return updated, nil
	}
	updated.State = StateRunning
	if wait, found := selectWaitEvidence(pending); found {
		updated.State = StateWaiting
		nextWakeAt := *wait.WaitUntil
		updated.NextWakeAt = &nextWakeAt
	}
	if progress, found := selectProgressEvidence(pending); found {
		updated.Progress = append(json.RawMessage(nil), progress.Details...)
	}
	return updated, nil
}

func validateReconcileExecution(exec ExecutionContext) error {
	if err := exec.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(exec.TaskID) || exec.ExpectedRevision == 0 {
		return ErrInvalidTaskSpec
	}
	if !requiredIdentity(exec.Source.EventID) || !requiredIdentity(exec.Source.TurnID) || !requiredIdentity(exec.Source.CallID) {
		return ErrSourceInvalid
	}
	return nil
}

func pendingEvidence(record Record) []Evidence {
	pending := make([]Evidence, 0, len(record.Evidence))
	for _, evidence := range record.Evidence {
		if !evidence.Applied {
			pending = append(pending, evidence)
		}
	}
	return pending
}

func selectTerminalEvidence(pending []Evidence) (Evidence, bool) {
	terminal := make([]Evidence, 0, len(pending))
	for _, evidence := range pending {
		switch evidence.Kind {
		case EvidenceKindSatisfied, EvidenceKindUnsatisfied, EvidenceKindInterrupted:
			terminal = append(terminal, evidence)
		}
	}
	if len(terminal) == 0 {
		return Evidence{}, false
	}
	sort.Slice(terminal, func(i, j int) bool {
		if terminal[i].OccurredAt != terminal[j].OccurredAt {
			return terminal[i].OccurredAt < terminal[j].OccurredAt
		}
		if terminalKindPriority(terminal[i].Kind) != terminalKindPriority(terminal[j].Kind) {
			return terminalKindPriority(terminal[i].Kind) < terminalKindPriority(terminal[j].Kind)
		}
		return terminal[i].FactID < terminal[j].FactID
	})
	return terminal[0], true
}

func terminalKindPriority(kind string) int {
	switch kind {
	case EvidenceKindSatisfied:
		return 0
	case EvidenceKindUnsatisfied:
		return 1
	case EvidenceKindInterrupted:
		return 2
	default:
		return 3
	}
}

func terminalResultState(kind string) State {
	if kind == EvidenceKindSatisfied {
		return StateSucceeded
	}
	return StateFailed
}

func routeReconcileWithoutPending(record Record, exec ExecutionContext) ReconcileResult {
	if taskStateTerminal(record.State) {
		return ReconcileResult{Task: record, Next: ReconcileNextSettled}
	}
	if record.Revision != exec.ExpectedRevision {
		if isFutureEvidenceWaitRetry(record, exec) {
			return ReconcileResult{Task: record, Next: ReconcileNextSettled}
		}
		return ReconcileResult{}
	}
	if exec.Clock.Tick >= record.Spec.DeadlineAt || record.NeedsReconcile {
		return ReconcileResult{Task: record, Next: ReconcileNextObserve}
	}
	if record.State == StateRunning {
		return ReconcileResult{Task: record, Next: ReconcileNextDecide}
	}
	return ReconcileResult{Task: record, Next: ReconcileNextSettled}
}

func isFutureEvidenceWaitRetry(record Record, exec ExecutionContext) bool {
	if record.State != StateWaiting || record.NextWakeAt == nil || *record.NextWakeAt <= exec.Clock.Tick ||
		record.Revision != exec.ExpectedRevision+1 || record.NeedsReconcile {
		return false
	}
	for _, evidence := range record.Evidence {
		if evidence.Applied && evidence.Kind == EvidenceKindProgress && evidence.WaitUntil != nil &&
			*evidence.WaitUntil == *record.NextWakeAt {
			return true
		}
	}
	return false
}

func selectWaitEvidence(pending []Evidence) (Evidence, bool) {
	waits := make([]Evidence, 0, len(pending))
	for _, evidence := range pending {
		if evidence.Kind == EvidenceKindProgress && evidence.WaitUntil != nil {
			waits = append(waits, evidence)
		}
	}
	if len(waits) == 0 {
		return Evidence{}, false
	}
	sort.Slice(waits, func(i, j int) bool {
		if *waits[i].WaitUntil != *waits[j].WaitUntil {
			return *waits[i].WaitUntil < *waits[j].WaitUntil
		}
		if waits[i].OccurredAt != waits[j].OccurredAt {
			return waits[i].OccurredAt < waits[j].OccurredAt
		}
		return waits[i].FactID < waits[j].FactID
	})
	return waits[0], true
}

func selectProgressEvidence(pending []Evidence) (Evidence, bool) {
	progress := make([]Evidence, 0, len(pending))
	for _, evidence := range pending {
		if evidence.Kind == EvidenceKindProgress && len(evidence.Details) != 0 {
			progress = append(progress, evidence)
		}
	}
	if len(progress) == 0 {
		return Evidence{}, false
	}
	sort.Slice(progress, func(i, j int) bool {
		if progress[i].OccurredAt != progress[j].OccurredAt {
			return progress[i].OccurredAt > progress[j].OccurredAt
		}
		return progress[i].FactID < progress[j].FactID
	})
	return progress[0], true
}

func (s *SQLiteStore) prepareReconcileMutation(current storedIntentTask, updated Record) (preparedReconcileMutation, error) {
	if err := updated.Validate(); err != nil {
		return preparedReconcileMutation{}, err
	}
	if current.record.ID != updated.ID || current.record.Owner != updated.Owner ||
		!taskSpecsEqual(current.record.Spec, updated.Spec) ||
		current.record.CreatedAtGameTick != updated.CreatedAtGameTick ||
		current.record.CreatedAtUnixMS != updated.CreatedAtUnixMS || updated.Revision != current.record.Revision+1 {
		return preparedReconcileMutation{}, ErrInvalidTaskSpec
	}
	recordJSON, err := json.Marshal(updated)
	if err != nil {
		return preparedReconcileMutation{}, ErrInvalidTaskSpec
	}
	createResponseJSON, err := json.Marshal(initialCreateResult(current.record))
	if err != nil {
		return preparedReconcileMutation{}, ErrInvalidTaskSpec
	}
	intentHistoryJSON, err := json.Marshal(current.history)
	if err != nil {
		return preparedReconcileMutation{}, ErrInvalidTaskSpec
	}
	if err := s.validateTaskMutationCapacity(updated, createResponseJSON, intentHistoryJSON); err != nil {
		return preparedReconcileMutation{}, err
	}
	return preparedReconcileMutation{record: updated, recordJSON: recordJSON}, nil
}

func (s *SQLiteStore) updateReconcileRecordTx(ctx context.Context, tx *sql.Tx, before Record, prepared preparedReconcileMutation) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, revision = ?, clock_id = ?, next_wake_at = ?, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?
			AND state = ? AND revision = ? AND clock_id = ? AND record_json = ?`,
		string(prepared.record.State), int64(prepared.record.Revision), prepared.record.Spec.ClockID,
		nullableTick(prepared.record.NextWakeAt), prepared.recordJSON,
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
	return s.afterReconcileStage(ctx, reconcileStageRecordUpdated)
}

func (s *SQLiteStore) consumeReconcileWakesTx(ctx context.Context, tx *sql.Tx, record Record) error {
	if err := s.consumeExecutableWakesTx(ctx, tx, record); err != nil {
		return err
	}
	return s.afterReconcileStage(ctx, reconcileStageWakesConsumed)
}

func (s *SQLiteStore) insertReconcileWakeTx(ctx context.Context, tx *sql.Tx, record Record, wake Wake) error {
	if err := s.insertWakeTx(ctx, tx, record, wake); err != nil {
		return err
	}
	return s.afterReconcileStage(ctx, reconcileStageWakeInserted)
}

func (s *SQLiteStore) afterReconcileStage(ctx context.Context, stage string) error {
	if s.testAfterReconcileStage != nil {
		return s.testAfterReconcileStage(ctx, stage)
	}
	return ctx.Err()
}
