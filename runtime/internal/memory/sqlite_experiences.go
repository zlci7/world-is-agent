package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const experienceSchema = `CREATE TABLE experience_index (
 source_id TEXT PRIMARY KEY REFERENCES session_history(source_id),
 fact_id TEXT NOT NULL,
 clock_id TEXT NOT NULL,
 occurred_tick INTEGER NOT NULL,
 focus_until_tick INTEGER NOT NULL,
 priority INTEGER NOT NULL
)`

func prepareExperienceDatabase(ctx context.Context, conn *sql.Conn) error {
	var statement string
	err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='experience_index'`).Scan(&statement)
	if err == sql.ErrNoRows {
		tx, beginErr := beginSQLiteWriteTransaction(ctx, conn)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, strings.Replace(experienceSchema, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)); err != nil {
			return err
		}
		// The index contains only derivations of immutable raw sources and can be rebuilt after loss.
		if _, err = tx.ExecContext(ctx, `INSERT INTO experience_index(source_id,fact_id,clock_id,occurred_tick,focus_until_tick,priority)
 SELECT h.source_id,json_extract(h.payload_json,'$.event.id'),json_extract(h.payload_json,'$.experience.clock_id'),
 json_extract(h.payload_json,'$.experience.occurred_tick'),json_extract(h.payload_json,'$.experience.focus_until_tick'),json_extract(h.payload_json,'$.experience.priority')
 FROM session_history AS h WHERE h.kind='experience' AND h.availability='available'
 AND NOT EXISTS(SELECT 1 FROM experience_index e WHERE e.source_id=h.source_id)`); err != nil {
			return err
		}
		if err = commitSQLiteTransaction(ctx, tx); err != nil {
			return err
		}
		err = conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='experience_index'`).Scan(&statement)
	}
	if err != nil {
		return err
	}
	if strings.Join(strings.Fields(statement), " ") != strings.Join(strings.Fields(experienceSchema), " ") {
		return fmt.Errorf("%w: incompatible experience_index", ErrSchemaMismatch)
	}
	return nil
}

func indexExperience(ctx context.Context, db sqliteConnector, source HistorySource) error {
	if source.Batch.Experience == nil {
		return nil
	}
	r := source.Batch.Experience
	_, err := db.ExecContext(ctx, `INSERT INTO experience_index(source_id,fact_id,clock_id,occurred_tick,focus_until_tick,priority) VALUES(?,?,?,?,?,?)`, source.ID, source.Batch.Event.ID, r.ClockID, r.OccurredTick, r.FocusUntilTick, r.Priority)
	return err
}

func (s *SQLiteHistoryStore) ReadExperiences(ctx context.Context, q ExperienceQuery) ([]HistorySource, error) {
	if !s.validSnapshot(q.Snapshot) {
		return nil, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	if q.View == nil || len(q.View.ValidFactIDs) == 0 || q.Limit <= 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(q.View.ValidFactIDs))
	for id := range q.View.ValidFactIDs {
		ids = append(ids, id)
	}
	encoded, _ := json.Marshal(ids)
	conn, closeConn, err := s.openHistoryConn(ctx, q.Snapshot.Owner)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	owner := q.Snapshot.Owner
	rows, err := conn.QueryContext(ctx, `SELECT e.source_id FROM experience_index AS e JOIN session_history AS h ON h.source_id=e.source_id
 WHERE h.game_id=? AND h.world_id=? AND h.entity_id=? AND h.sequence<=? AND h.availability=? AND h.kind=?
 AND e.clock_id=? AND e.occurred_tick<=? AND e.focus_until_tick>? AND e.fact_id IN (SELECT value FROM json_each(?))
 ORDER BY e.priority DESC,e.occurred_tick DESC,h.sequence DESC LIMIT ?`, owner.GameID, owner.WorldID, owner.EntityID, q.Snapshot.Watermark, HistoryAvailable, HistoryKindExperience, q.View.ClockID, q.View.NowTick, q.View.NowTick, string(encoded), min(q.Limit, 64))
	if err != nil {
		return nil, err
	}
	var sourceIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sourceIDs = append(sourceIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var sources []HistorySource
	for _, id := range sourceIDs {
		source, err := readHistorySource(ctx, conn, owner, id)
		if err != nil {
			return nil, err
		}
		if ExperienceVisible(source, q.View) {
			sources = append(sources, source)
		}
	}
	return sources, nil
}

var _ ExperienceStore = (*SQLiteHistoryStore)(nil)
