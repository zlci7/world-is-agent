package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gameagent/runtime/internal/session"
)

// Opening maintenance connections with the short busy timeout also bounds migration lock waits.
func (s *SQLiteHistoryStore) openMaintenanceConn(ctx context.Context, owner session.AgentSessionKey, limits HistoryMaintenanceLimits) (*sql.Conn, func(), error) {
	if _, err := session.Resolve(owner.GameID, owner.WorldID, owner.EntityID); err != nil {
		return nil, nil, err
	}
	if err := s.limits.Validate(); err != nil {
		return nil, nil, err
	}
	if err := s.indexLimits.Validate(); err != nil {
		return nil, nil, err
	}
	conn, closeConn, err := s.base.openRawConn(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", limits.LockTimeoutMS)); err == nil {
		var schema map[string]string
		schema, err = readSQLiteSchema(ctx, conn)
		if err == nil && len(schema) == 0 {
			err = ErrHistoryMaintenanceNotReady
		}
		if err == nil {
			var metadata map[string]string
			metadata, err = readSQLiteMetadata(ctx, conn)
			if err == nil && metadata["schema_version"] != RetrievalSchemaVersion {
				switch metadata["schema_version"] {
				case SQLiteSchemaVersion, HistorySchemaVersion:
					err = ErrHistoryMaintenanceNotReady
				default:
					err = fmt.Errorf("%w: unsupported maintenance schema %q", ErrSchemaMismatch, metadata["schema_version"])
				}
			}
			if err == nil {
				err = validateRetrievalDatabase(ctx, conn, schema, owner)
			}
		}
	}
	if err != nil {
		closeConn()
		return nil, nil, err
	}
	return conn, closeConn, nil
}

func (s *SQLiteHistoryStore) PruneHistory(ctx context.Context, request HistoryPruneRequest) (result HistoryMaintenanceResult, err error) {
	result.NextAfter = request.AfterSequence
	defer func() {
		if err != nil {
			result.Processed = 0
			result.Bytes = 0
			result.SourceIDs = nil
			result.NextAfter = request.AfterSequence
		}
	}()
	if request.RetentionDays == 0 {
		return result, nil
	}
	if request.RetentionDays < 0 || request.RetentionDays > 106751 || request.AfterSequence < 0 {
		return result, fmt.Errorf("invalid history retention age or cursor")
	}
	limits := request.Limits.WithDefaults()
	if err := limits.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.TimeoutMS)*time.Millisecond)
	defer cancel()
	conn, closeConn, err := s.openMaintenanceConn(ctx, request.Owner, limits)
	if err != nil {
		if errors.Is(err, ErrHistoryMaintenanceNotReady) {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "not_ready")
		}
		return result, err
	}
	defer closeConn()
	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// Publication takes the writer before the lease lock as well. New snapshots
	// cannot acquire a watermark between eligibility checks and the commit.
	if err := s.mu.LockContext(ctx, time.Duration(limits.LockTimeoutMS)*time.Millisecond); err != nil {
		return result, err
	}
	defer s.mu.Unlock()
	owner := request.Owner
	snapshot := HistorySnapshot{Owner: owner}
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COALESCE(MAX(sequence),0) FROM session_history WHERE game_id=? AND world_id=? AND entity_id=?),(SELECT COALESCE(MAX(revision),0) FROM context_summaries WHERE game_id=? AND world_id=? AND entity_id=?)`, owner.GameID, owner.WorldID, owner.EntityID, owner.GameID, owner.WorldID, owner.EntityID).Scan(&snapshot.Watermark, &snapshot.SummaryRevision); err != nil {
		return result, err
	}
	read, err := readSummaryWithAccess(ctx, snapshot, request.CurrentTime, boundedSummaryReadLimits(s.limits, SummaryReadLimits{Bytes: limits.Bytes}), sqliteSummaryAccess(ctx, tx, snapshot))
	result.ReadBytes = read.ReadBytes
	if err != nil {
		return result, err
	}
	result.Diagnostics = append(result.Diagnostics, read.Diagnostics...)
	for _, diagnostic := range read.Diagnostics {
		if diagnostic == "summary_scan_incomplete" || diagnostic == "summary_coverage_invalid" {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_summary_unproven")
			return result, nil
		}
	}
	if read.Checkpoint == nil {
		result.Diagnostics = append(result.Diagnostics, "prune_summary_unavailable")
		return result, nil
	}
	visible, unknown := HistoryVisibility(read.Checkpoint.Times, request.CurrentTime)
	if !visible || unknown {
		result.Diagnostics = append(result.Diagnostics, "prune_time_unproven")
		return result, nil
	}
	covered := map[string]HistorySourceRef{}
	for _, ref := range read.Checkpoint.Sources {
		covered[ref.ID] = ref
	}
	tail, tailBytes, proven, err := s.retentionTail(ctx, tx, snapshot, request.CurrentTime, request.KeepRecentTokens, limits.Bytes-result.ReadBytes)
	result.ReadBytes += tailBytes
	if err != nil {
		return result, err
	}
	if !proven {
		result.Diagnostics = append(result.Diagnostics, "prune_tail_unproven")
		return result, nil
	}
	now := request.Now
	if now.IsZero() {
		now = s.base.options.Now()
	}
	cutoff := now.Add(-time.Duration(request.RetentionDays) * 24 * time.Hour).UnixNano()
	candidateFrom := ` FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND availability='available' AND created_at<? AND sequence>? ORDER BY sequence LIMIT ?`
	args := []any{owner.GameID, owner.WorldID, owner.EntityID, cutoff, request.AfterSequence, limits.Sources + 1}
	var headerBytes int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(source_id AS BLOB))+16),0) FROM (SELECT source_id`+candidateFrom+`)`, args...).Scan(&headerBytes); err != nil {
		return result, err
	}
	if headerBytes > limits.Bytes-result.ReadBytes {
		result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_metadata_incomplete")
		return result, nil
	}
	result.ReadBytes += headerBytes
	rows, err := tx.QueryContext(ctx, `SELECT source_id,sequence,payload_bytes FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND availability='available' AND created_at<? AND sequence>? ORDER BY sequence LIMIT ?`, owner.GameID, owner.WorldID, owner.EntityID, cutoff, request.AfterSequence, limits.Sources+1)
	if err != nil {
		return result, err
	}
	type candidate struct {
		id       string
		sequence int64
		bytes    int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.sequence, &c.bytes); err != nil {
			_ = rows.Close()
			return result, err
		}
		candidates = append(candidates, c)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	for i, c := range candidates {
		if i >= limits.Sources {
			result.More = true
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Processed++
		result.NextAfter = c.sequence
		ref, ok := covered[c.id]
		if !ok {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_uncovered")
			continue
		}
		if tail[c.id] || c.sequence == snapshot.Watermark {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_recent_tail")
			continue
		}
		protected := false
		for _, lease := range s.leases {
			if lease.Owner == owner && c.sequence <= lease.Watermark {
				protected = true
				break
			}
		}
		if protected {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_snapshot_protected")
			continue
		}
		if c.bytes < 0 {
			return result, ErrInvalidHistory
		}
		size, err := historySourceReadSize(ctx, tx, owner, c.id)
		if err != nil {
			return result, err
		}
		if size > limits.Bytes-result.ReadBytes {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_bytes_incomplete")
			continue
		}
		result.ReadBytes += size
		result.ReadSources++
		source, err := readHistorySource(ctx, tx, owner, c.id)
		if err != nil {
			return result, err
		}
		if ref.Sequence != source.Sequence || ref.Fingerprint != source.Fingerprint {
			return result, ErrSummaryConflict
		}
		visible, unknown := HistoryVisibility(source.Times, request.CurrentTime)
		if !visible || unknown {
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "prune_time_unproven")
			continue
		}
		if source.LegacyMemoryID != "" {
			if source.Batch.Legacy == nil || source.Batch.Legacy.MemoryID != source.LegacyMemoryID {
				return result, ErrInvalidHistory
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM recent_records WHERE memory_id=? AND game_id=? AND world_id=? AND entity_id=?`, source.LegacyMemoryID, owner.GameID, owner.WorldID, owner.EntityID); err != nil {
				return result, err
			}
		}
		for _, statement := range []string{`DELETE FROM history_terms WHERE source_id=?`, `DELETE FROM history_fragments WHERE source_id=?`, `DELETE FROM history_index_sources WHERE source_id=?`, `UPDATE session_history SET payload_json=NULL,availability='pruned' WHERE source_id=?`} {
			if _, err = tx.ExecContext(ctx, statement, source.ID); err != nil {
				return result, err
			}
		}
		result.SourceIDs = append(result.SourceIDs, source.ID)
		result.Bytes += source.Bytes
	}
	if err = commitSQLiteTransaction(ctx, tx); err != nil {
		return result, err
	}
	return result, nil
}

// UTF-8 bytes/4 is a lower bound on the shared estimator's framed token cost.
// Header-only selection therefore protects at least the corresponding 8.2 tail.
func (s *SQLiteHistoryStore) retentionTail(ctx context.Context, db sqliteConnector, snapshot HistorySnapshot, current *GameTimeSnapshot, target, byteLimit int) (map[string]bool, int, bool, error) {
	if target <= 0 {
		target = 20000
	}
	owner := snapshot.Owner
	maxHeads := min(s.limits.ScanRecords, DefaultHistoryLimits().ScanRecords)
	from := ` FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND availability='available' ORDER BY sequence DESC LIMIT ?`
	args := []any{owner.GameID, owner.WorldID, owner.EntityID, maxHeads + 1}
	var size int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(source_id AS BLOB))+length(CAST(times_json AS BLOB))+8),0) FROM (SELECT source_id,times_json`+from+`)`, args...).Scan(&size)
	if err != nil {
		return nil, 0, false, err
	}
	if size > byteLimit {
		return nil, 0, false, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT source_id,payload_bytes,times_json`+from, args...)
	if err != nil {
		return nil, 0, false, err
	}
	type head struct {
		id    string
		bytes int
		times string
	}
	var heads []head
	for rows.Next() {
		var h head
		if err = rows.Scan(&h.id, &h.bytes, &h.times); err != nil {
			_ = rows.Close()
			return nil, 0, false, err
		}
		heads = append(heads, h)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, false, err
	}
	if err = rows.Close(); err != nil {
		return nil, 0, false, err
	}
	protected := map[string]bool{}
	tokens := 0
	for i, h := range heads {
		if err := ctx.Err(); err != nil {
			return nil, size, false, err
		}
		if i >= maxHeads || h.bytes < 0 {
			return protected, size, false, nil
		}
		var times []HistoryTime
		if err := json.Unmarshal([]byte(h.times), &times); err != nil {
			return nil, size, false, err
		}
		visible, unknown := HistoryVisibility(times, current)
		if unknown {
			return protected, size, false, nil
		}
		if !visible {
			continue
		}
		protected[h.id] = true
		tokens += h.bytes / 4
		if tokens >= target {
			return protected, size, true, nil
		}
	}
	return protected, size, true, nil
}

func historySourceReadSize(ctx context.Context, db sqliteConnector, owner session.AgentSessionKey, id string) (int, error) {
	var size int
	err := db.QueryRowContext(ctx, `SELECT length(CAST(source_id AS BLOB))+length(CAST(batch_key AS BLOB))+length(CAST(game_id AS BLOB))+length(CAST(world_id AS BLOB))+length(CAST(entity_id AS BLOB))+length(CAST(kind AS BLOB))+length(CAST(content_fingerprint AS BLOB))+length(CAST(times_json AS BLOB))+COALESCE(length(CAST(payload_json AS BLOB)),0)+length(CAST(availability AS BLOB))+length(CAST(legacy_memory_id AS BLOB))+32 FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND source_id=?`, owner.GameID, owner.WorldID, owner.EntityID, id).Scan(&size)
	return size, err
}

func appendUniqueDiagnostic(diagnostics []string, code string) []string {
	for _, existing := range diagnostics {
		if existing == code {
			return diagnostics
		}
	}
	return append(diagnostics, code)
}
