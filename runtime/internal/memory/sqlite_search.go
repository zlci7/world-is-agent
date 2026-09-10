package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gameagent/runtime/internal/session"
)

var _ HistorySearchStore = (*SQLiteHistoryStore)(nil)

func (s *SQLiteHistoryStore) SearchHistory(ctx context.Context, request HistorySearchRequest) (page HistorySearchPage, err error) {
	if err := ctx.Err(); err != nil {
		return page, err
	}
	limits := request.Limits.WithDefaults()
	if err := limits.Validate(); err != nil {
		return page, err
	}
	if request.Offset < 0 {
		return page, fmt.Errorf("%w: negative search offset", ErrInvalidHistory)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.TimeoutMS)*time.Millisecond)
	defer cancel()
	defer func() {
		if err != nil {
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "read_failed")
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
		}
	}()
	valid, err := s.validSnapshotContext(ctx, request.Snapshot)
	if err != nil {
		return page, err
	}
	if !valid {
		return page, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	page.NextOffset = min(request.Offset, limits.ScanCandidates)
	request.Offset = page.NextOffset
	terms := HistoryQueryTerms(request.Query, limits.QueryChars, limits.QueryTerms)
	if len(terms) == 0 {
		page.Diagnostics = []string{"no_valid_query"}
		return page, nil
	}
	budget := &historySearchReadBudget{limit: limits.Bytes}
	defer func() { page.Bytes = budget.used }()
	conn, closeConn, err := s.openHistoryConn(ctx, request.Snapshot.Owner)
	if err != nil {
		if errors.Is(err, ErrSchemaMismatch) {
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "index_not_ready")
		}
		return page, err
	}
	defer closeConn()
	// A deferred read transaction keeps preflight byte lengths and raw verification
	// on one SQLite snapshot without taking the immediate writer lock from the DSN.
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		return page, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	if err = s.historyIndexCoverage(ctx, conn, request.Snapshot, &page); err != nil {
		return page, err
	}
	candidates, truncated, err := s.historySearchCandidates(ctx, conn, request, limits, terms, budget)
	if err != nil {
		return page, err
	}
	type verifiedSource struct {
		source    HistorySource
		root      map[string]any
		headBytes int
		visible   bool
	}
	verified := make(map[int64]verifiedSource)
	page.More = truncated
	remaining := limits.ScanCandidates - request.Offset
	for i, candidate := range candidates {
		if i == remaining {
			page.More = true
			break
		}
		if err := ctx.Err(); err != nil {
			return page, err
		}
		value, loaded := verified[candidate.sequence]
		if !loaded {
			if !budget.reserve(candidate.idBytes) {
				page.More = true
				break
			}
			var id string
			err = conn.QueryRowContext(ctx, `SELECT source_id FROM session_history WHERE sequence=?`, candidate.sequence).Scan(&id)
			if err != nil {
				return page, err
			}
			size, err := historySourceReadSize(ctx, conn, request.Snapshot.Owner, id)
			if err != nil {
				return page, err
			}
			if size <= budget.limit && !budget.reserve(size) {
				page.More = true
				break
			}
			switch {
			case size > budget.limit:
				page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
			default:
				source, readErr := readHistorySource(ctx, conn, request.Snapshot.Owner, id)
				if readErr != nil {
					var syntax *json.SyntaxError
					var valueType *json.UnmarshalTypeError
					if !errors.Is(readErr, ErrInvalidHistory) && !errors.Is(readErr, ErrHistoryNotFound) && !errors.As(readErr, &syntax) && !errors.As(readErr, &valueType) {
						return page, readErr
					}
					page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "read_failed")
					page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
				} else if source.Batch == nil || source.Sequence > request.Snapshot.Watermark || source.Owner != request.Snapshot.Owner || source.Availability != HistoryAvailable {
					page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
				} else {
					data, err := json.Marshal(source.Batch)
					if err != nil {
						return page, err
					}
					decoder := json.NewDecoder(bytes.NewReader(data))
					decoder.UseNumber()
					if err := decoder.Decode(&value.root); err != nil {
						return page, err
					}
					var unknown bool
					value.visible, unknown = HistoryVisibility(source.Times, request.CurrentTime)
					if unknown {
						page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "time_unknown")
					}
					if !value.visible {
						page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "time_hidden")
					}
					source.Batch = nil
					value.source = source
					value.headBytes = size - source.Bytes
				}
			}
			verified[candidate.sequence] = value
		}
		if value.root == nil || !value.visible {
			page.Scanned++
			page.NextOffset++
			continue
		}
		if !budget.reserve(candidate.pathBytes) {
			page.More = true
			break
		}
		var path string
		if err := conn.QueryRowContext(ctx, `SELECT field_path FROM history_terms WHERE rowid=?`, candidate.termRowID).Scan(&path); err != nil {
			return page, err
		}
		field, found := historySearchTextField(value.root, path)
		var valid bool
		if found {
			err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM history_fragments WHERE source_id=? AND field_path=? AND actor_id=? AND field_kind=? AND original_text=? AND start_rune=? AND end_rune=?)`, value.source.ID, field.Path, field.ActorID, field.Kind, field.Text, 0, utf8.RuneCountInString(field.Text)).Scan(&valid)
			if err != nil {
				return page, err
			}
		}
		matched := historyMatchedQueryTerms(field.Text, terms)
		if !valid || len(matched) == 0 || len(matched) != candidate.score {
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "index_not_ready")
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
			page.Scanned++
			page.NextOffset++
			continue
		}
		outputBytes := value.headBytes + len(field.Path) + len(field.ActorID) + len(field.Kind) + len(field.Text) + 24
		for _, term := range matched {
			outputBytes += len(term)
		}
		if !budget.reserve(outputBytes) {
			page.More = true
			break
		}
		page.Scanned++
		page.NextOffset++
		page.Matches = append(page.Matches, HistoryMatch{Source: value.source, Field: field, Span: HistoryTextSpan{Path: field.Path, End: utf8.RuneCountInString(field.Text)}, MatchedTerms: matched, Score: len(matched)})
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	valid, err = s.validSnapshotContext(ctx, request.Snapshot)
	if err != nil || !valid {
		page.Matches = nil
		if err != nil {
			return page, err
		}
		return page, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	if page.More {
		page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
	}
	if len(page.Matches) == 0 {
		page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "no_match")
	}
	return page, nil
}

type historySearchCandidate struct {
	sequence  int64
	termRowID int64
	pathBytes int
	idBytes   int
	score     int
}

type historySearchReadBudget struct{ limit, used int }

func (b *historySearchReadBudget) reserve(size int) bool {
	if size < 0 || size > b.limit-b.used {
		return false
	}
	b.used += size
	return true
}

func (s *SQLiteHistoryStore) historySearchCandidates(ctx context.Context, db sqliteConnector, request HistorySearchRequest, limits HistorySearchLimits, terms []string, budget *historySearchReadBudget) ([]historySearchCandidate, bool, error) {
	owner := request.Snapshot.Owner
	args := []any{owner.GameID, owner.WorldID, owner.EntityID, owner.GameID, owner.WorldID, owner.EntityID, request.Snapshot.Watermark, HistoryAvailable, "ready", s.indexLimits.signature()}
	for _, term := range terms {
		args = append(args, term)
	}
	args = append(args, limits.ScanCandidates-request.Offset+1, request.Offset)
	query := `SELECT h.sequence,MIN(t.rowid),length(CAST(t.field_path AS BLOB)),length(CAST(h.source_id AS BLOB)),COUNT(*)
		FROM history_terms AS t JOIN session_history AS h ON h.source_id=t.source_id
		JOIN history_index_sources AS i ON i.source_id=h.source_id
		WHERE t.game_id=? AND t.world_id=? AND t.entity_id=?
		AND h.game_id=? AND h.world_id=? AND h.entity_id=? AND h.sequence<=? AND h.availability=?
		AND i.status=? AND i.index_signature=? AND i.content_fingerprint=h.content_fingerprint
		AND t.term IN (` + strings.TrimSuffix(strings.Repeat("?,", len(terms)), ",") + `)
		GROUP BY t.source_id,t.field_path ORDER BY COUNT(*) DESC,h.sequence DESC,t.field_path ASC LIMIT ? OFFSET ?`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var candidates []historySearchCandidate
	for rows.Next() {
		if !budget.reserve(5 * 8) {
			return candidates, true, nil
		}
		var candidate historySearchCandidate
		if err := rows.Scan(&candidate.sequence, &candidate.termRowID, &candidate.pathBytes, &candidate.idBytes, &candidate.score); err != nil {
			return nil, false, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, false, rows.Err()
}

// Resolve only a requested canonical JSON pointer. Shared object keys are never
// expanded into a path string for every leaf in the source.
func historySearchTextField(root map[string]any, path string) (HistoryTextField, bool) {
	if !strings.HasPrefix(path, "/") {
		return HistoryTextField{}, false
	}
	parts := strings.Split(path[1:], "/")
	for i, part := range parts {
		for j := 0; j < len(part); j++ {
			if part[j] == '~' {
				j++
				if j == len(part) || (part[j] != '0' && part[j] != '1') {
					return HistoryTextField{}, false
				}
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	field := HistoryTextField{Path: path}
	fact := len(parts) == 4 && parts[3] == "Text" && ((parts[0] == "event" && parts[1] == "facts") || (parts[0] == "legacy" && parts[1] == "SourceContextFacts"))
	if !fact {
		if len(parts) < 6 || parts[0] != "steps" {
			return HistoryTextField{}, false
		}
		switch {
		case parts[2] == "decision" && parts[3] == "ToolCalls" && parts[5] == "Arguments":
			field.Kind = "tool_argument"
		case parts[2] == "executions" && parts[4] == "call" && parts[5] == "Arguments":
			field.Kind = "tool_argument"
		case parts[2] == "executions" && parts[4] == "action_result" && parts[5] == "output":
			field.Kind = "action_output"
		case parts[2] == "executions" && parts[4] == "action_result" && parts[5] == "error":
			field.Kind = "action_error"
		default:
			return HistoryTextField{}, false
		}
	}
	var value any = root
	for i, part := range parts {
		switch node := value.(type) {
		case map[string]any:
			if fact && i == len(parts)-1 {
				field.ActorID, _ = node["ActorEntityID"].(string)
				field.Kind, _ = node["Kind"].(string)
			}
			value = node[part]
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != part {
				return HistoryTextField{}, false
			}
			value = node[index]
		default:
			return HistoryTextField{}, false
		}
	}
	field.Text, _ = value.(string)
	return field, field.Text != ""
}

func (s *SQLiteHistoryStore) historyIndexCoverage(ctx context.Context, db sqliteConnector, snapshot HistorySnapshot, page *HistorySearchPage) error {
	owner := snapshot.Owner
	for _, check := range []struct {
		condition, diagnostic string
		withSignature         bool
	}{
		{`(i.source_id IS NULL OR i.index_signature<>? OR i.content_fingerprint<>h.content_fingerprint)`, "index_not_ready", true},
		{`(i.index_signature=? AND i.content_fingerprint=h.content_fingerprint AND i.status='capacity_exceeded')`, "index_capacity_exceeded", true},
		{`(i.content_fingerprint=h.content_fingerprint AND i.status='maintenance_exceeded')`, "index_not_ready", false},
	} {
		var incomplete bool
		args := []any{owner.GameID, owner.WorldID, owner.EntityID, snapshot.Watermark, HistoryAvailable}
		if check.withSignature {
			args = append(args, s.indexLimits.signature())
		}
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_history AS h LEFT JOIN history_index_sources AS i ON i.source_id=h.source_id WHERE h.game_id=? AND h.world_id=? AND h.entity_id=? AND h.sequence<=? AND h.availability=? AND `+check.condition+`)`, args...).Scan(&incomplete)
		if err != nil {
			return err
		}
		if incomplete {
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, check.diagnostic)
			page.Diagnostics = appendUniqueDiagnostic(page.Diagnostics, "scan_incomplete")
		}
	}
	return nil
}

func historyMatchedQueryTerms(text string, query []string) []string {
	available := make(map[string]bool)
	for _, term := range HistoryQueryTerms(text, len(text), len(text)) {
		available[term] = true
	}
	var matched []string
	for _, term := range query {
		if available[term] {
			matched = append(matched, term)
		}
	}
	return matched
}

func (s *SQLiteHistoryStore) RebuildHistoryIndex(ctx context.Context, request HistoryRebuildRequest) (result HistoryMaintenanceResult, err error) {
	result.NextAfter = request.AfterSequence
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.AfterSequence < 0 {
		return result, fmt.Errorf("%w: negative rebuild cursor", ErrInvalidHistory)
	}
	limits := request.Limits.WithDefaults()
	if err := limits.Validate(); err != nil {
		return result, err
	}
	budget := &historySearchReadBudget{limit: limits.Bytes}
	defer func() {
		result.ReadBytes = budget.used
		if err != nil {
			result.Processed = 0
			result.Bytes = 0
			result.SourceIDs = nil
			result.NextAfter = request.AfterSequence
		}
	}()
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
	owner := request.Owner
	maintenanceSignature := historyMaintenanceIndexSignature(s.indexLimits, limits.Bytes)
	rows, err := tx.QueryContext(ctx, `SELECT h.sequence,h.payload_bytes,COALESCE(length(CAST(h.payload_json AS BLOB)),0),length(CAST(h.source_id AS BLOB)),length(CAST(h.content_fingerprint AS BLOB))
		FROM session_history AS h LEFT JOIN history_index_sources AS i ON i.source_id=h.source_id
		WHERE h.game_id=? AND h.world_id=? AND h.entity_id=? AND h.availability=? AND h.sequence>?
		AND (i.source_id IS NULL OR i.content_fingerprint<>h.content_fingerprint
		OR (i.status IN ('ready','capacity_exceeded') AND i.index_signature<>?)
		OR (i.status='maintenance_exceeded' AND i.index_signature<>?)
		OR (? AND i.status<>'capacity_exceeded'))
		ORDER BY h.sequence LIMIT ?`, owner.GameID, owner.WorldID, owner.EntityID, HistoryAvailable, request.AfterSequence, s.indexLimits.signature(), maintenanceSignature, request.Force, limits.Sources+1)
	if err != nil {
		return result, err
	}
	type header struct {
		sequence         int64
		declaredBytes    int
		bytes            int
		idBytes          int
		fingerprintBytes int
	}
	var headers []header
	for rows.Next() {
		if !budget.reserve(5 * 8) {
			result.More = true
			break
		}
		var h header
		if err := rows.Scan(&h.sequence, &h.declaredBytes, &h.bytes, &h.idBytes, &h.fingerprintBytes); err != nil {
			_ = rows.Close()
			return result, err
		}
		headers = append(headers, h)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for i, h := range headers {
		if i == limits.Sources {
			result.More = true
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if h.bytes <= 0 || h.bytes != h.declaredBytes {
			return result, fmt.Errorf("%w: rebuild source bytes", ErrInvalidHistory)
		}
		if !budget.reserve(h.idBytes) {
			result.More = true
			break
		}
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT source_id FROM session_history WHERE sequence=?`, h.sequence).Scan(&id); err != nil {
			return result, err
		}
		size, err := historySourceReadSize(ctx, tx, owner, id)
		if err != nil {
			return result, err
		}
		markIncomplete := func() error {
			if !budget.reserve(h.fingerprintBytes) {
				return ErrHistoryCapacity
			}
			return markHistoryMaintenanceExceeded(ctx, tx, owner, id, maintenanceSignature)
		}
		if size > limits.Bytes {
			if err := markIncomplete(); err != nil {
				if errors.Is(err, ErrHistoryCapacity) {
					result.More = true
					break
				}
				return result, err
			}
			result.Processed++
			result.NextAfter = h.sequence
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "rebuild_bytes_incomplete")
			result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "scan_incomplete")
			continue
		}
		if !budget.reserve(size) {
			if result.Processed == 0 {
				// The same page metadata will be read on every retry. Advance past a
				// source that cannot fit even as the first candidate under this limit.
				if err := markIncomplete(); err != nil {
					if errors.Is(err, ErrHistoryCapacity) {
						result.More = true
						break
					}
					return result, err
				}
				result.Processed++
				result.NextAfter = h.sequence
				result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "rebuild_bytes_incomplete")
				result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "scan_incomplete")
				continue
			}
			result.More = true
			break
		}
		result.ReadSources++
		source, err := readHistorySource(ctx, tx, owner, id)
		if err != nil {
			return result, err
		}
		if err := s.indexHistorySource(ctx, tx, source); err != nil {
			return result, err
		}
		result.Processed++
		result.NextAfter = h.sequence
		result.Bytes += h.bytes
		result.SourceIDs = append(result.SourceIDs, source.ID)
	}
	if result.More {
		result.Diagnostics = appendUniqueDiagnostic(result.Diagnostics, "scan_incomplete")
	}
	if err := commitSQLiteTransaction(ctx, tx); err != nil {
		return result, err
	}
	return result, nil
}

func historyMaintenanceIndexSignature(limits HistoryIndexLimits, bytes int) string {
	return fmt.Sprintf("%s:maintenance:%d", limits.signature(), bytes)
}

func markHistoryMaintenanceExceeded(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, id, signature string) error {
	for _, statement := range []string{
		`DELETE FROM history_terms WHERE source_id=?`,
		`DELETE FROM history_fragments WHERE source_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO history_index_sources(source_id,content_fingerprint,index_signature,status)
		SELECT source_id,content_fingerprint,?,'maintenance_exceeded' FROM session_history
		WHERE source_id=? AND game_id=? AND world_id=? AND entity_id=? AND availability=?
		ON CONFLICT(source_id) DO UPDATE SET content_fingerprint=excluded.content_fingerprint,index_signature=excluded.index_signature,status=excluded.status`,
		signature, id, owner.GameID, owner.WorldID, owner.EntityID, HistoryAvailable)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrHistoryNotFound
	}
	return nil
}

// The caller owns the transaction containing both the raw source and its index.
func (s *SQLiteHistoryStore) indexHistorySource(ctx context.Context, tx *sql.Tx, source HistorySource) error {
	limits := s.indexLimits.WithDefaults()
	if err := limits.Validate(); err != nil {
		return err
	}
	var fingerprint, availability string
	err := tx.QueryRowContext(ctx, `SELECT content_fingerprint,availability FROM session_history WHERE source_id=? AND game_id=? AND world_id=? AND entity_id=?`, source.ID, source.Owner.GameID, source.Owner.WorldID, source.Owner.EntityID).Scan(&fingerprint, &availability)
	if err != nil {
		return err
	}
	if availability == HistoryPruned {
		return nil
	}
	if availability != HistoryAvailable || source.Batch == nil || source.Fingerprint != fingerprint || source.Batch.Owner != source.Owner {
		return fmt.Errorf("%w: index source identity", ErrInvalidHistory)
	}
	var knownImpossible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM history_index_sources WHERE source_id=? AND content_fingerprint=? AND index_signature=? AND status=?)`, source.ID, fingerprint, limits.signature(), "capacity_exceeded").Scan(&knownImpossible)
	if err != nil || knownImpossible {
		return err
	}
	fields, status := historyIndexFields(*source.Batch, limits)
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_terms WHERE source_id=?`, source.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_fragments WHERE source_id=?`, source.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_sources(source_id,content_fingerprint,index_signature,status) VALUES(?,?,?,?) ON CONFLICT(source_id) DO UPDATE SET content_fingerprint=excluded.content_fingerprint,index_signature=excluded.index_signature,status=excluded.status`, source.ID, fingerprint, limits.signature(), status); err != nil {
		return err
	}
	if status == "capacity_exceeded" {
		return nil
	}
	fragment, err := tx.PrepareContext(ctx, `INSERT INTO history_fragments(source_id,field_path,actor_id,field_kind,original_text,start_rune,end_rune) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer fragment.Close()
	term, err := tx.PrepareContext(ctx, `INSERT INTO history_terms(game_id,world_id,entity_id,term,source_id,field_path) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer term.Close()
	for _, field := range fields {
		if _, err := fragment.ExecContext(ctx, source.ID, field.Path, field.ActorID, field.Kind, field.Text, 0, utf8.RuneCountInString(field.Text)); err != nil {
			return err
		}
		for _, value := range HistoryQueryTerms(field.Text, len(field.Text), limits.TermAssociations) {
			if _, err := term.ExecContext(ctx, source.Owner.GameID, source.Owner.WorldID, source.Owner.EntityID, value, source.ID, field.Path); err != nil {
				return err
			}
		}
	}
	return nil
}
