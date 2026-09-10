package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gameagent/runtime/internal/idgen"
)

var _ SummaryStore = (*SQLiteHistoryStore)(nil)

func (s *SQLiteHistoryStore) ReadSummary(ctx context.Context, snapshot HistorySnapshot, currentTime *GameTimeSnapshot, limits SummaryReadLimits) (SummaryRead, error) {
	if !s.validSnapshot(snapshot) {
		return SummaryRead{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.limits.ReadTimeoutMS)*time.Millisecond)
	defer cancel()
	conn, closeConn, err := s.openHistoryConn(ctx, snapshot.Owner)
	if err != nil {
		return SummaryRead{}, err
	}
	defer closeConn()
	return readSummaryWithAccess(ctx, snapshot, currentTime, boundedSummaryReadLimits(s.limits, limits), sqliteSummaryAccess(ctx, conn, snapshot))
}

func (s *SQLiteHistoryStore) CommitSummary(ctx context.Context, request SummaryCommit) (SummaryCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return SummaryCheckpoint{}, err
	}
	if !s.validSnapshot(request.Snapshot) {
		return SummaryCheckpoint{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	if err := validateSummaryCommit(request, s.limits); err != nil {
		return SummaryCheckpoint{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.limits.WriteTimeoutMS)*time.Millisecond)
	defer cancel()
	conn, closeConn, err := s.openHistoryConn(ctx, request.Snapshot.Owner)
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	defer closeConn()
	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	defer tx.Rollback()
	owner := request.Snapshot.Owner
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM context_summaries WHERE game_id=? AND world_id=? AND entity_id=?`, owner.GameID, owner.WorldID, owner.EntityID).Scan(&revision)
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	// Publication follows the current owner revision; rollback may select an older parent.
	if revision != request.ExpectedRevision || revision != request.Snapshot.SummaryRevision {
		return SummaryCheckpoint{}, ErrSummaryConflict
	}
	checkpoint, err := prepareSummaryCheckpoint(ctx, request, s.limits, sqliteSummaryAccess(ctx, tx, request.Snapshot))
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	checkpoint.ID = idgen.New("summary")
	checkpoint.CreatedAt = s.base.options.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO context_summaries(summary_id,game_id,world_id,entity_id,parent_id,generation_version,summary_text,source_count,coverage_fingerprint,coverage_times_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, checkpoint.ID, owner.GameID, owner.WorldID, owner.EntityID, checkpoint.ParentID, checkpoint.Version, checkpoint.Text, checkpoint.sourceCount, checkpoint.CoverageFingerprint, checkpoint.coverageTimesJSON, checkpoint.CreatedAt.UnixNano())
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	checkpoint.Revision, err = result.LastInsertId()
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	for _, ref := range checkpoint.Sources {
		if _, err = tx.ExecContext(ctx, `INSERT INTO summary_sources(summary_id,source_id,source_sequence,content_fingerprint) VALUES(?,?,?,?)`, checkpoint.ID, ref.ID, ref.Sequence, ref.Fingerprint); err != nil {
			return SummaryCheckpoint{}, err
		}
	}
	if !s.validSnapshot(request.Snapshot) {
		return SummaryCheckpoint{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	if err := commitSQLiteTransaction(ctx, tx); err != nil {
		return SummaryCheckpoint{}, err
	}
	return checkpoint, nil
}

func sqliteSummaryAccess(ctx context.Context, db sqliteConnector, snapshot HistorySnapshot) summaryAccess {
	return summaryAccess{
		checkpoint: func(id string, before int64, budget *summaryScanBudget) (*SummaryCheckpoint, error) {
			return readSQLiteSummaryHeader(ctx, db, snapshot, id, before, budget)
		},
		references: func(id string, budget *summaryScanBudget) ([]HistorySourceRef, error) {
			return readSQLiteSummaryReferences(ctx, db, id, budget)
		},
		sources: func(ctx context.Context, ids []string, budget *summaryScanBudget) ([]HistorySource, error) {
			sources := make([]HistorySource, 0, len(ids))
			for start := 0; start < len(ids); start += 256 {
				heads, err := readSQLiteSummaryHeadBatch(ctx, db, snapshot, ids[start:min(start+256, len(ids))], budget)
				if err != nil {
					return nil, err
				}
				sources = append(sources, heads...)
			}
			return sources, nil
		},
	}
}

func readSQLiteSummaryHeadBatch(ctx context.Context, db sqliteConnector, snapshot HistorySnapshot, ids []string, budget *summaryScanBudget) ([]HistorySource, error) {
	owner := snapshot.Owner
	args := make([]any, 0, len(ids)+3)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, owner.GameID, owner.WorldID, owner.EntityID)
	// The bounded ID table is the outer loop, keeping each head a unique lookup
	// even when SQLite has no statistics for a large owner history.
	prefix := `WITH requested(source_id) AS (VALUES ` + strings.TrimSuffix(strings.Repeat("(?),", len(ids)), ",") + `) `
	from := ` FROM requested CROSS JOIN session_history AS h WHERE h.source_id=requested.source_id AND h.game_id=? AND h.world_id=? AND h.entity_id=?`
	var size, count int
	err := db.QueryRowContext(ctx, prefix+`SELECT COUNT(*),COALESCE(SUM(length(CAST(h.source_id AS BLOB))+length(CAST(h.content_fingerprint AS BLOB))+length(CAST(h.times_json AS BLOB))+length(CAST(h.availability AS BLOB))+8),0)`+from, args...).Scan(&count, &size)
	if err != nil {
		return nil, err
	}
	if count != len(ids) {
		return nil, ErrHistoryNotFound
	}
	if err := budget.takeBytes(size); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, prefix+`SELECT h.source_id,h.sequence,h.content_fingerprint,h.times_json,h.availability`+from, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]HistorySource, len(ids))
	loadedBytes := 0
	for rows.Next() {
		head := HistorySource{Owner: owner}
		var timesJSON string
		if err := rows.Scan(&head.ID, &head.Sequence, &head.Fingerprint, &timesJSON, &head.Availability); err != nil {
			return nil, err
		}
		loadedBytes += summarySourceHeadBytes(head, len(timesJSON))
		if loadedBytes > size || len(byID) >= len(ids) {
			return nil, fmt.Errorf("%w: summary source heads changed during read", ErrInvalidHistory)
		}
		if err := json.Unmarshal([]byte(timesJSON), &head.Times); err != nil {
			return nil, fmt.Errorf("%w: summary source head times", ErrInvalidHistory)
		}
		byID[head.ID] = head
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// SQL row order is independent of the canonical coverage order.
	heads := make([]HistorySource, len(ids))
	for i, id := range ids {
		head, ok := byID[id]
		if !ok {
			return nil, ErrHistoryNotFound
		}
		heads[i] = head
	}
	return heads, nil
}

func readSQLiteSummaryHeader(ctx context.Context, db sqliteConnector, snapshot HistorySnapshot, id string, before int64, budget *summaryScanBudget) (*SummaryCheckpoint, error) {
	owner := snapshot.Owner
	checkpoint := &SummaryCheckpoint{Owner: owner}
	var size int
	err := db.QueryRowContext(ctx, `SELECT summary_id,revision,CASE WHEN typeof(source_count)='integer' THEN source_count ELSE 0 END,length(CAST(summary_id AS BLOB))+length(CAST(parent_id AS BLOB))+length(CAST(generation_version AS BLOB))+length(CAST(summary_text AS BLOB))+length(CAST(game_id AS BLOB))+length(CAST(world_id AS BLOB))+length(CAST(entity_id AS BLOB))+length(CAST(coverage_fingerprint AS BLOB))+length(CAST(coverage_times_json AS BLOB))+24 FROM context_summaries WHERE game_id=? AND world_id=? AND entity_id=? AND revision<=? AND (?='' OR summary_id=?) AND (?=0 OR revision<?) ORDER BY revision DESC LIMIT 1`, owner.GameID, owner.WorldID, owner.EntityID, snapshot.SummaryRevision, id, id, before, before).Scan(&checkpoint.ID, &checkpoint.Revision, &checkpoint.sourceCount, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := budget.takeBytes(size); err != nil {
		return checkpoint, err
	}
	var created int64
	err = db.QueryRowContext(ctx, `SELECT parent_id,generation_version,summary_text,coverage_fingerprint,coverage_times_json,created_at FROM context_summaries WHERE summary_id=? AND game_id=? AND world_id=? AND entity_id=?`, checkpoint.ID, owner.GameID, owner.WorldID, owner.EntityID).Scan(&checkpoint.ParentID, &checkpoint.Version, &checkpoint.Text, &checkpoint.CoverageFingerprint, &checkpoint.coverageTimesJSON, &created)
	if err != nil {
		return checkpoint, err
	}
	checkpoint.CreatedAt = time.Unix(0, created).UTC()
	return checkpoint, decodeSummaryCoverageTimes(checkpoint)
}

func readSQLiteSummaryReferences(ctx context.Context, db sqliteConnector, id string, budget *summaryScanBudget) ([]HistorySourceRef, error) {
	var count, size int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(source_id AS BLOB))+length(CAST(content_fingerprint AS BLOB))+8),0) FROM (SELECT source_id,content_fingerprint FROM summary_sources WHERE summary_id=? LIMIT ?)`, id, budget.sources+1).Scan(&count, &size)
	if err != nil {
		return nil, err
	}
	if count > budget.sources {
		return nil, fmt.Errorf("%w: summary coverage scan", ErrHistoryCapacity)
	}
	if err := budget.takeBytes(size); err != nil {
		return nil, err
	}
	budget.sources -= count
	rows, err := db.QueryContext(ctx, `SELECT source_id,source_sequence,content_fingerprint FROM summary_sources WHERE summary_id=? ORDER BY source_sequence,source_id LIMIT ?`, id, count+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]HistorySourceRef, 0, count)
	for rows.Next() {
		var ref HistorySourceRef
		if err := rows.Scan(&ref.ID, &ref.Sequence, &ref.Fingerprint); err != nil {
			return nil, err
		}
		if len(refs) == count {
			return nil, fmt.Errorf("%w: summary coverage changed during read", ErrInvalidHistory)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(refs) != count {
		return nil, fmt.Errorf("%w: incomplete summary coverage", ErrInvalidHistory)
	}
	return refs, nil
}
