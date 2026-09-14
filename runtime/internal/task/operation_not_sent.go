package task

import (
	"context"
	"database/sql"

	"gameagent/runtime/internal/session"
)

// MarkOperationNotSent records a transport fact known only by the Runtime that
// registered the action. It must never be called after transport handoff starts.
func (s *Service) MarkOperationNotSent(ctx context.Context, binding Binding, owner session.AgentSessionKey, taskID, operationID, actionID string) error {
	if err := validateService(s, ctx); err != nil {
		return err
	}
	if binding.Validate() != nil || owner.GameID != binding.World.GameID || owner.WorldID != binding.World.WorldID || !requiredIdentity(owner.EntityID) || !requiredIdentity(taskID) || !requiredIdentity(operationID) || !requiredIdentity(actionID) {
		return ErrInvalidTaskSpec
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
		if head.RuntimeInstanceID != s.claimantID {
			return ErrGenerationStale
		}
		current, err := s.store.loadIntentTaskTx(ctx, tx, owner, taskID)
		if err != nil {
			return err
		}
		for i, op := range current.record.Operations {
			if op.ID != operationID {
				continue
			}
			if op.ActionID != actionID || op.Binding != binding {
				return ErrTaskChanged
			}
			for _, fact := range current.record.Evidence {
				if fact.OperationID == op.ID {
					return ErrEvidenceConflict
				}
			}
			if op.Status == OperationStatusNotSent {
				return nil
			}
			if op.Status != OperationStatusRegistered {
				return ErrTaskChanged
			}
			updated := current.record
			updated.Operations = cloneOperations(current.record.Operations)
			updated.Operations[i].Status = OperationStatusNotSent
			prepared, err := s.store.prepareNoRevisionMutation(current, updated)
			if err != nil {
				return err
			}
			return s.store.updateNoRevisionRecordTx(ctx, tx, current.record, prepared, "operation_not_sent")
		}
		return ErrTaskChanged
	})
}
