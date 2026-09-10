package memory

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gameagent/runtime/internal/idgen"
	"gameagent/runtime/internal/session"
)

type SQLiteHistoryStore struct {
	base        *SQLiteMemoryStore
	limits      HistoryLimits
	indexLimits HistoryIndexLimits
	mu          historyLeaseMutex
	leases      map[string]HistorySnapshot
}

type HistoryStoreOption func(*SQLiteHistoryStore)

func WithHistoryIndexLimits(limits HistoryIndexLimits) HistoryStoreOption {
	return func(store *SQLiteHistoryStore) { store.indexLimits = limits.WithDefaults() }
}

func NewSQLiteHistoryStore(options SQLiteStoreOptions, limits HistoryLimits, opts ...HistoryStoreOption) *SQLiteHistoryStore {
	s := &SQLiteHistoryStore{base: NewSQLiteMemoryStore(options), limits: limits.WithDefaults(), indexLimits: DefaultHistoryIndexLimits(), mu: newHistoryLeaseMutex(), leases: make(map[string]HistorySnapshot)}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *SQLiteHistoryStore) AppendHistory(ctx context.Context, batch HistoryBatch) (HistorySource, error) {
	data, key, fp, err := CanonicalHistoryBatch(batch, s.limits.MaxBatchBytes)
	if err != nil {
		return HistorySource{}, err
	}
	if batch.Kind != HistoryKindTerminal {
		return HistorySource{}, fmt.Errorf("%w: legacy sources are imported by migration", ErrInvalidHistory)
	}
	conn, closeConn, err := s.openHistoryConn(ctx, batch.Owner)
	if err != nil {
		return HistorySource{}, err
	}
	defer closeConn()
	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return HistorySource{}, err
	}
	defer tx.Rollback()
	var id, oldFP string
	err = tx.QueryRowContext(ctx, "SELECT source_id,content_fingerprint FROM session_history WHERE batch_key = ?", key).Scan(&id, &oldFP)
	if err == nil {
		if oldFP != fp {
			return HistorySource{}, ErrHistoryConflict
		}
		return readHistorySource(ctx, tx, batch.Owner, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return HistorySource{}, err
	}
	source, err := insertHistorySource(ctx, tx, batch, data, key, fp, s.base.options.Now().UTC(), "")
	if err != nil {
		return HistorySource{}, err
	}
	if err = s.indexHistorySource(ctx, tx, source); err != nil {
		return HistorySource{}, err
	}
	if err = commitSQLiteTransaction(ctx, tx); err != nil {
		return HistorySource{}, err
	}
	return source, nil
}

func insertHistorySource(ctx context.Context, db sqliteConnector, batch HistoryBatch, data []byte, key, fp string, created time.Time, legacyID string) (HistorySource, error) {
	times := HistoryTimes(batch)
	timesJSON, err := json.Marshal(times)
	if err != nil {
		return HistorySource{}, err
	}
	id := "history_" + sha256LowerHex(key)
	result, err := db.ExecContext(ctx, `INSERT INTO session_history(source_id,batch_key,game_id,world_id,entity_id,kind,version,content_fingerprint,created_at,times_json,payload_json,payload_bytes,availability,legacy_memory_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, key, batch.Owner.GameID, batch.Owner.WorldID, batch.Owner.EntityID, batch.Kind, batch.Version, fp, created.UnixNano(), string(timesJSON), string(data), len(data), HistoryAvailable, legacyID)
	if err != nil {
		return HistorySource{}, err
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return HistorySource{}, err
	}
	stored, err := decodeHistoryBatch(data)
	if err != nil {
		return HistorySource{}, err
	}
	return HistorySource{ID: id, Sequence: seq, Owner: batch.Owner, BatchKey: key, Fingerprint: fp, CreatedAt: created, Times: HistoryTimes(stored), Batch: &stored, Bytes: len(data), Availability: HistoryAvailable, LegacyMemoryID: legacyID}, nil
}

func (s *SQLiteHistoryStore) BeginHistorySnapshot(ctx context.Context, key session.AgentSessionKey) (HistorySnapshot, error) {
	conn, closeConn, err := s.openHistoryConn(ctx, key)
	if err != nil {
		return HistorySnapshot{}, err
	}
	defer closeConn()
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := HistorySnapshot{Owner: key, LeaseID: idgen.New("history_snapshot")}
	err = conn.QueryRowContext(ctx, `SELECT (SELECT COALESCE(MAX(sequence),0) FROM session_history WHERE game_id=? AND world_id=? AND entity_id=?), (SELECT COALESCE(MAX(revision),0) FROM context_summaries WHERE game_id=? AND world_id=? AND entity_id=?)`, key.GameID, key.WorldID, key.EntityID, key.GameID, key.WorldID, key.EntityID).Scan(&snapshot.Watermark, &snapshot.SummaryRevision)
	if err != nil {
		return HistorySnapshot{}, err
	}
	s.leases[snapshot.LeaseID] = snapshot
	return snapshot, nil
}

func (s *SQLiteHistoryStore) ReleaseHistorySnapshot(snapshot HistorySnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if saved, ok := s.leases[snapshot.LeaseID]; ok && saved == snapshot {
		delete(s.leases, snapshot.LeaseID)
	}
}

func (s *SQLiteHistoryStore) validSnapshot(snapshot HistorySnapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, ok := s.leases[snapshot.LeaseID]
	return ok && saved == snapshot
}

func (s *SQLiteHistoryStore) ReadHistorySnapshot(ctx context.Context, snapshot HistorySnapshot, before int64, limits HistoryReadLimits) (HistoryPage, error) {
	if !s.validSnapshot(snapshot) {
		return HistoryPage{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	if limits.Records <= 0 {
		limits.Records = s.limits.PageRecords
	}
	if limits.Bytes <= 0 {
		limits.Bytes = s.limits.PageBytes
	}
	limits.Records = min(limits.Records, s.limits.PageRecords)
	limits.Bytes = min(limits.Bytes, s.limits.PageBytes)
	conn, closeConn, err := s.openHistoryConn(ctx, snapshot.Owner)
	if err != nil {
		return HistoryPage{}, err
	}
	defer closeConn()
	key := snapshot.Owner
	rows, err := conn.QueryContext(ctx, `SELECT source_id,sequence,payload_bytes,availability FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND sequence<=? AND (?=0 OR sequence<?) ORDER BY sequence DESC LIMIT ?`, key.GameID, key.WorldID, key.EntityID, snapshot.Watermark, before, before, limits.Records+1)
	if err != nil {
		return HistoryPage{}, err
	}
	type header struct {
		id           string
		seq          int64
		bytes        int
		availability string
	}
	var headers []header
	for rows.Next() {
		var h header
		if err = rows.Scan(&h.id, &h.seq, &h.bytes, &h.availability); err != nil {
			_ = rows.Close()
			return HistoryPage{}, err
		}
		headers = append(headers, h)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return HistoryPage{}, err
	}
	if err = rows.Close(); err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{}
	for i, h := range headers {
		if i >= limits.Records {
			page.More = true
			break
		}
		if h.bytes < 0 {
			return HistoryPage{}, ErrInvalidHistory
		}
		if h.availability == HistoryAvailable && h.bytes <= limits.Bytes && page.Bytes+h.bytes > limits.Bytes {
			page.More = true
			break
		}
		page.Scanned++
		page.NextBefore = h.seq
		if h.availability == HistoryPruned {
			page.Diagnostics = append(page.Diagnostics, "source_pruned")
			continue
		}
		if h.bytes > limits.Bytes {
			page.Diagnostics = append(page.Diagnostics, "history_source_exceeds_page_bytes")
			continue
		}
		source, readErr := readHistorySource(ctx, conn, key, h.id)
		if readErr != nil {
			return HistoryPage{}, readErr
		}
		page.Sources = append(page.Sources, source)
		page.Bytes += source.Bytes
	}
	return page, nil
}

func (s *SQLiteHistoryStore) ReadHistorySource(ctx context.Context, key session.AgentSessionKey, id string) (HistorySource, error) {
	conn, closeConn, err := s.openHistoryConn(ctx, key)
	if err != nil {
		return HistorySource{}, err
	}
	defer closeConn()
	return readHistorySource(ctx, conn, key, id)
}

const historySourceColumns = `source_id,sequence,batch_key,game_id,world_id,entity_id,kind,version,content_fingerprint,created_at,times_json,payload_json,payload_bytes,availability,legacy_memory_id`

func readHistorySource(ctx context.Context, db sqliteConnector, key session.AgentSessionKey, id string) (HistorySource, error) {
	return scanHistorySource(db.QueryRowContext(ctx, `SELECT `+historySourceColumns+` FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND source_id=?`, key.GameID, key.WorldID, key.EntityID, id))
}

func scanHistorySource(row interface{ Scan(...any) error }) (HistorySource, error) {
	var source HistorySource
	var payload sql.NullString
	var timesJSON, kind string
	var version int
	var created int64
	err := row.Scan(&source.ID, &source.Sequence, &source.BatchKey, &source.Owner.GameID, &source.Owner.WorldID, &source.Owner.EntityID, &kind, &version, &source.Fingerprint, &created, &timesJSON, &payload, &source.Bytes, &source.Availability, &source.LegacyMemoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return HistorySource{}, ErrHistoryNotFound
	}
	if err != nil {
		return HistorySource{}, err
	}
	if _, err := session.Resolve(source.Owner.GameID, source.Owner.WorldID, source.Owner.EntityID); err != nil {
		return HistorySource{}, fmt.Errorf("%w: source owner", ErrInvalidHistory)
	}
	if source.Sequence <= 0 || source.Bytes < 0 || source.BatchKey == "" || source.ID != "history_"+sha256LowerHex(source.BatchKey) || version != HistoryVersion {
		return HistorySource{}, fmt.Errorf("%w: source header", ErrInvalidHistory)
	}
	if (kind != HistoryKindTerminal && kind != HistoryKindLegacy) || (kind == HistoryKindTerminal && source.LegacyMemoryID != "") || (kind == HistoryKindLegacy && source.LegacyMemoryID == "") {
		return HistorySource{}, fmt.Errorf("%w: source kind or legacy identity", ErrInvalidHistory)
	}
	fingerprint, err := hex.DecodeString(source.Fingerprint)
	if err != nil || len(fingerprint) != 32 || hex.EncodeToString(fingerprint) != source.Fingerprint {
		return HistorySource{}, fmt.Errorf("%w: source fingerprint header", ErrInvalidHistory)
	}
	source.CreatedAt = time.Unix(0, created).UTC()
	if err = json.Unmarshal([]byte(timesJSON), &source.Times); err != nil {
		return HistorySource{}, fmt.Errorf("%w: source times", ErrInvalidHistory)
	}
	if source.Availability == HistoryPruned {
		if payload.Valid || len(source.Times) == 0 {
			return HistorySource{}, ErrInvalidHistory
		}
		var parts []any
		count := 7
		if kind == HistoryKindLegacy {
			count = 6
		}
		if err := decodeHistoryJSON([]byte(source.BatchKey), &parts); err != nil || len(parts) != count {
			return HistorySource{}, fmt.Errorf("%w: pruned source key", ErrInvalidHistory)
		}
		if parts[0] != source.Owner.GameID || parts[1] != source.Owner.WorldID || parts[2] != source.Owner.EntityID || parts[count-2] != kind || parts[count-1] != json.Number(fmt.Sprint(version)) {
			return HistorySource{}, fmt.Errorf("%w: pruned source identity", ErrInvalidHistory)
		}
		for i := 3; i < count-2; i++ {
			if value, ok := parts[i].(string); !ok || (i == 3 && value == "") {
				return HistorySource{}, fmt.Errorf("%w: pruned source key fields", ErrInvalidHistory)
			}
		}
		canonicalKey, err := json.Marshal(parts)
		if err != nil || string(canonicalKey) != source.BatchKey {
			return HistorySource{}, fmt.Errorf("%w: pruned source canonical key", ErrInvalidHistory)
		}
		return source, nil
	}
	if source.Availability != HistoryAvailable || !payload.Valid || len(payload.String) != source.Bytes || sha256LowerHex(payload.String) != source.Fingerprint {
		return HistorySource{}, fmt.Errorf("%w: source fingerprint or availability", ErrInvalidHistory)
	}
	batch, err := decodeHistoryBatch([]byte(payload.String))
	if err != nil {
		return HistorySource{}, fmt.Errorf("%w: source payload: %w", ErrInvalidHistory, err)
	}
	_, batchKey, fp, err := CanonicalHistoryBatch(batch, 0)
	if err != nil || batch.Owner != source.Owner || batch.Kind != kind || batch.Version != version || batchKey != source.BatchKey || fp != source.Fingerprint {
		return HistorySource{}, fmt.Errorf("%w: source identity", ErrInvalidHistory)
	}
	if batch.Legacy != nil && (batch.Legacy.MemoryID != source.LegacyMemoryID || !batch.Legacy.CreatedAt.Equal(source.CreatedAt)) {
		return HistorySource{}, fmt.Errorf("%w: legacy source header", ErrInvalidHistory)
	}
	expectedTimes, _ := json.Marshal(HistoryTimes(batch))
	if string(expectedTimes) != timesJSON {
		return HistorySource{}, fmt.Errorf("%w: source time metadata", ErrInvalidHistory)
	}
	source.Batch = &batch
	return source, nil
}
