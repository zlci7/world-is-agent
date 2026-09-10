package task

import (
	"context"
	"database/sql"

	"gameagent/runtime/internal/session"
)

func enforceAdmissionTx(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, admission Admission) error {
	if admission.MaxActivePerOwner == 0 {
		return nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ?
			AND state IN (?, ?, ?)`,
		owner.GameID, owner.WorldID, owner.EntityID,
		string(StateWaiting), string(StateRunning), string(StatePaused),
	).Scan(&active); err != nil {
		return err
	}
	if active >= admission.MaxActivePerOwner {
		return ErrTaskConflict
	}
	return nil
}
