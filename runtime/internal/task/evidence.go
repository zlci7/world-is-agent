package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
)

const noRevisionStageEvidenceMerged = "evidence_merged"

type evidenceSourceIdentity struct {
	eventID string
	turnID  string
	callID  string
}

type worldEvidenceIdentityLookup struct {
	fact        Evidence
	factRecord  Record
	factFound   bool
	sourceFound bool
}

func (s *Service) AdmitEvidence(ctx context.Context, binding Binding, evidence Evidence) (bool, error) {
	if err := validateService(s, ctx); err != nil {
		return false, err
	}
	if err := binding.Validate(); err != nil {
		return false, err
	}
	cloned, err := cloneEvidence(evidence)
	if err != nil {
		return false, ErrInvalidTaskSpec
	}
	if err := cloned.Validate(); err != nil {
		return false, err
	}
	if cloned.Applied {
		return false, ErrEvidenceConflict
	}
	if cloned.Binding.World != binding.World || cloned.RevalidatedIn != nil && cloned.RevalidatedIn.World != binding.World {
		return false, ErrWorldMismatch
	}

	added := false
	err = s.store.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
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

		current, err := s.store.loadWorldTaskByIDTx(ctx, tx, binding.World, cloned.TaskID)
		if err != nil {
			return err
		}
		if current.record.Spec.ClockID != head.Head.Clock.ID {
			return ErrClockMismatch
		}
		identity, err := s.store.loadWorldEvidenceIdentityTx(ctx, tx, binding.World, cloned.FactID, cloned.Source)
		if err != nil {
			return err
		}
		if identity.factFound {
			if identity.factRecord.Owner != current.record.Owner || identity.factRecord.ID != current.record.ID ||
				!evidenceEqualIgnoringApplied(identity.fact, cloned) {
				return ErrEvidenceConflict
			}
			added = false
			return nil
		}
		if identity.sourceFound {
			return ErrEvidenceConflict
		}

		if err := validateNewEvidence(head.Head, current.record, cloned); err != nil {
			return err
		}
		if _, err := NextDurableCounter(current.record.Revision); err != nil {
			return err
		}
		updated := current.record
		updated.Evidence = append(cloneEvidenceSlice(current.record.Evidence), cloned)
		updated.NeedsReconcile = true
		prepared, err := s.store.prepareNoRevisionMutation(current, updated)
		if err != nil {
			return err
		}
		if err := s.store.updateNoRevisionRecordTx(ctx, tx, current.record, prepared, noRevisionStageEvidenceMerged); err != nil {
			return err
		}
		added = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return added, nil
}

func validateEvidenceAuthority(head worldHeadRow, binding Binding) error {
	if head.Head.Status != worldHeadStatusReady {
		return ErrWorldNotReady
	}
	if head.BarrierStatus != "" || head.SaveRequestID != "" {
		return ErrSaveInProgress
	}
	if head.Head.Binding.World != binding.World {
		return ErrWorldMismatch
	}
	if head.Head.Binding != binding {
		return ErrGenerationStale
	}
	return nil
}

func validateNewEvidence(head Head, record Record, evidence Evidence) error {
	if record.Spec.ClockID != head.Clock.ID {
		return ErrClockMismatch
	}
	if taskStateTerminal(record.State) {
		return ErrTaskTerminal
	}
	if record.Result != nil {
		return ErrInvalidTaskSpec
	}
	if evidence.OccurredAt > head.Clock.Tick {
		return ErrTaskChanged
	}
	if evidence.WaitUntil != nil && (evidence.OccurredAt >= *evidence.WaitUntil || *evidence.WaitUntil > record.Spec.DeadlineAt) {
		return ErrInvalidTaskSpec
	}

	if evidence.OperationID == "" {
		if evidence.RevalidatedIn != nil {
			return ErrEvidenceConflict
		}
		if err := validateCurrentEvidenceBinding(evidence.Binding, head.Binding); err != nil {
			return err
		}
		if evidence.StartRevision != record.Revision {
			return ErrTaskChanged
		}
		return nil
	}

	operation, found := findRecordOperation(record, evidence.OperationID)
	if !found || operation.Status != OperationStatusRegistered {
		return ErrEvidenceConflict
	}
	if evidence.StartRevision != operation.StartRevision {
		return ErrTaskChanged
	}
	if evidence.RevalidatedIn == nil {
		if err := validateCurrentEvidenceBinding(evidence.Binding, head.Binding); err != nil {
			return err
		}
		if operation.Binding != evidence.Binding {
			return ErrEvidenceConflict
		}
		return nil
	}

	if *evidence.RevalidatedIn != head.Binding {
		if evidence.RevalidatedIn.World != head.Binding.World {
			return ErrWorldMismatch
		}
		return ErrGenerationStale
	}
	if evidence.Binding.World != head.Binding.World {
		return ErrWorldMismatch
	}
	if evidence.Binding.RunID != head.Binding.RunID {
		return ErrGenerationStale
	}
	if evidence.Binding.Generation >= head.Binding.Generation {
		return ErrEvidenceConflict
	}
	if operation.Binding != evidence.Binding {
		return ErrEvidenceConflict
	}
	return nil
}

func validateCurrentEvidenceBinding(got, current Binding) error {
	if got.World != current.World {
		return ErrWorldMismatch
	}
	if got != current {
		return ErrGenerationStale
	}
	return nil
}

func findRecordOperation(record Record, operationID string) (Operation, bool) {
	for _, operation := range record.Operations {
		if operation.ID == operationID {
			return operation, true
		}
	}
	return Operation{}, false
}

func cloneEvidence(evidence Evidence) (Evidence, error) {
	data, err := json.Marshal(evidence)
	if err != nil {
		return Evidence{}, err
	}
	var cloned Evidence
	if err := json.Unmarshal(data, &cloned); err != nil {
		return Evidence{}, err
	}
	return cloned, nil
}

func cloneEvidenceSlice(evidence []Evidence) []Evidence {
	if len(evidence) == 0 {
		return []Evidence{}
	}
	cloned := make([]Evidence, len(evidence))
	copy(cloned, evidence)
	return cloned
}

func evidenceEqualIgnoringApplied(stored, incoming Evidence) bool {
	stored.Applied = false
	incoming.Applied = false
	storedJSON, storedErr := json.Marshal(stored)
	incomingJSON, incomingErr := json.Marshal(incoming)
	return storedErr == nil && incomingErr == nil && bytes.Equal(storedJSON, incomingJSON)
}

func completeEvidenceSourceIdentity(source SourceRef) (evidenceSourceIdentity, bool) {
	if !requiredIdentity(source.EventID) || !requiredIdentity(source.TurnID) || !requiredIdentity(source.CallID) {
		return evidenceSourceIdentity{}, false
	}
	return evidenceSourceIdentity{eventID: source.EventID, turnID: source.TurnID, callID: source.CallID}, true
}

func (s *SQLiteStore) loadWorldTaskByIDTx(ctx context.Context, tx *sql.Tx, world WorldKey, taskID string) (storedIntentTask, error) {
	if !requiredIdentity(taskID) {
		return storedIntentTask{}, ErrInvalidTaskSpec
	}
	rows, err := tx.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND task_id = ? ORDER BY entity_id`,
		world.GameID, world.WorldID, taskID)
	if err != nil {
		return storedIntentTask{}, err
	}
	defer rows.Close()

	var (
		match   storedIntentTask
		matches int
	)
	for rows.Next() {
		record, _, _, history, err := scanTaskRowWithMetadata(rows)
		if err != nil {
			return storedIntentTask{}, err
		}
		matches++
		match = storedIntentTask{record: record, history: history}
	}
	if err := rows.Err(); err != nil {
		return storedIntentTask{}, err
	}
	if matches == 0 {
		return storedIntentTask{}, ErrTaskNotFound
	}
	if matches > 1 {
		return storedIntentTask{}, ErrEvidenceConflict
	}
	return match, nil
}

func (s *SQLiteStore) loadWorldEvidenceIdentityTx(ctx context.Context, tx *sql.Tx, world WorldKey, factID string, source SourceRef) (worldEvidenceIdentityLookup, error) {
	rows, err := tx.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id`,
		world.GameID, world.WorldID)
	if err != nil {
		return worldEvidenceIdentityLookup{}, err
	}
	defer rows.Close()

	var (
		lookup      worldEvidenceIdentityLookup
		factMatches int
	)
	incomingSource, sourceComplete := completeEvidenceSourceIdentity(source)
	seenSources := make(map[evidenceSourceIdentity]struct{})
	for rows.Next() {
		record, err := scanTaskRow(rows)
		if err != nil {
			return worldEvidenceIdentityLookup{}, err
		}
		for _, evidence := range record.Evidence {
			if evidence.FactID == factID {
				factMatches++
				lookup.fact, lookup.factRecord = evidence, record
			}
			if storedSource, complete := completeEvidenceSourceIdentity(evidence.Source); complete {
				if _, duplicate := seenSources[storedSource]; duplicate {
					return worldEvidenceIdentityLookup{}, ErrEvidenceConflict
				}
				seenSources[storedSource] = struct{}{}
				if sourceComplete && storedSource == incomingSource {
					lookup.sourceFound = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return worldEvidenceIdentityLookup{}, err
	}
	if factMatches > 1 {
		return worldEvidenceIdentityLookup{}, ErrEvidenceConflict
	}
	lookup.factFound = factMatches == 1
	return lookup, nil
}

func (s *SQLiteStore) validateWorldTaskIdentityGraph(ctx context.Context, world WorldKey) error {
	if err := world.Validate(); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id`,
		world.GameID, world.WorldID)
	if err != nil {
		return classifyStoreError(err)
	}
	defer rows.Close()

	seenOperations := make(map[string]struct{})
	seenFacts := make(map[string]struct{})
	seenSources := make(map[evidenceSourceIdentity]struct{})
	for rows.Next() {
		record, err := scanTaskRow(rows)
		if err != nil {
			return classifyStoreError(err)
		}
		for _, operation := range record.Operations {
			if _, duplicate := seenOperations[operation.ID]; duplicate {
				return ErrInvalidTaskSpec
			}
			seenOperations[operation.ID] = struct{}{}
		}
		for _, evidence := range record.Evidence {
			if _, duplicate := seenFacts[evidence.FactID]; duplicate {
				return ErrInvalidTaskSpec
			}
			seenFacts[evidence.FactID] = struct{}{}
			key, complete := completeEvidenceSourceIdentity(evidence.Source)
			if !complete {
				continue
			}
			if _, duplicate := seenSources[key]; duplicate {
				return ErrInvalidTaskSpec
			}
			seenSources[key] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return classifyStoreError(err)
	}
	return nil
}

func recordEvidenceSourceIdentityValid(record Record) bool {
	seen := make(map[evidenceSourceIdentity]struct{})
	for _, evidence := range record.Evidence {
		key, complete := completeEvidenceSourceIdentity(evidence.Source)
		if !complete {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}
