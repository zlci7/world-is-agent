package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"gameagent/runtime/internal/session"

	_ "modernc.org/sqlite"
)

const (
	SQLiteSchemaVersion       = "phase8_1_recent_v1"
	DefaultSQLiteMemoryRoot   = "runtime/.local/memory"
	DefaultSQLiteBusyTimeout  = 5 * time.Second
	defaultProjectionBatchCap = 100
)

var (
	ErrWorldBindingMismatch    = errors.New("memory world binding mismatch")
	ErrSchemaMismatch          = errors.New("memory schema mismatch")
	ErrProjectionBatchConflict = errors.New("memory projection batch conflict")
)

type SQLiteStoreOptions struct {
	Root                          string
	BusyTimeout                   time.Duration
	MaxRecordsPerEntity           int
	MaxProjectionBatchesPerEntity int

	Now func() time.Time
}

type SQLiteMemoryStore struct {
	options SQLiteStoreOptions
}

func NewSQLiteMemoryStore(options SQLiteStoreOptions) *SQLiteMemoryStore {
	return &SQLiteMemoryStore{options: options.withDefaults()}
}

func (o SQLiteStoreOptions) withDefaults() SQLiteStoreOptions {
	if o.Root == "" {
		o.Root = DefaultSQLiteMemoryRoot
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = DefaultSQLiteBusyTimeout
	}
	if o.MaxRecordsPerEntity <= 0 {
		o.MaxRecordsPerEntity = DefaultMaxRecordsPerSession
	}
	if o.MaxProjectionBatchesPerEntity <= 0 {
		o.MaxProjectionBatchesPerEntity = max(defaultProjectionBatchCap, o.MaxRecordsPerEntity*4)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func SQLiteDatabasePath(root string, gameID string, worldID string) (string, error) {
	if root == "" {
		root = DefaultSQLiteMemoryRoot
	}
	if gameID == "" || worldID == "" {
		return "", fmt.Errorf("%w: game_id and world_id are required", ErrInvalidRecord)
	}
	return filepath.Join(root, sha256LowerHex(gameID), sha256LowerHex(worldID), "memory.db"), nil
}

func (s *SQLiteMemoryStore) Append(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSQLiteRecord(record); err != nil {
		return err
	}

	conn, closeConn, err := s.openConn(ctx, record.SessionKey)
	if err != nil {
		return err
	}
	defer closeConn()

	payload, err := sqliteRecordPayloadFromRecord(record)
	if err != nil {
		return err
	}
	now := s.options.Now().UTC().UnixNano()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := ensureSQLiteMetadata(ctx, tx, record.SessionKey, s.options.Now); err != nil {
		return err
	}

	existingFingerprint, found, err := projectionBatchFingerprint(ctx, tx, record.ProjectionBatchKey)
	if err != nil {
		return err
	}
	if found {
		if existingFingerprint != payload.ContentFingerprint {
			return fmt.Errorf("%w: %s", ErrProjectionBatchConflict, record.ProjectionBatchKey)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE recent_projection_batches
SET last_seen_at = ?
WHERE projection_batch_key = ?`, now, record.ProjectionBatchKey); err != nil {
			return fmt.Errorf("update projection batch last_seen_at: %w", err)
		}
		if err := s.enforceRetention(ctx, tx, record.SessionKey); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO recent_records (
	memory_id,
	projection_kind,
	projection_version,
	projection_batch_key,
	game_id,
	world_id,
	entity_id,
	source_turn_id,
	source_event_id,
	source_event_sequence,
	event_type,
	game_time,
	source_context_facts_json,
	outcomes_json,
	created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.MemoryID,
		string(record.ProjectionKind),
		record.ProjectionVersion,
		record.ProjectionBatchKey,
		record.SessionKey.GameID,
		record.SessionKey.WorldID,
		record.SessionKey.EntityID,
		record.SourceTurnID,
		record.SourceEventID,
		int64(record.SourceEventSequence),
		record.EventType,
		payload.GameTimeJSON,
		payload.SourceContextFactsJSON,
		payload.OutcomesJSON,
		record.CreatedAt.UTC().UnixNano(),
	); err != nil {
		return fmt.Errorf("insert recent record: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO recent_projection_batches (
	projection_batch_key,
	projection_kind,
	projection_version,
	game_id,
	world_id,
	entity_id,
	content_fingerprint,
	created_at,
	last_seen_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ProjectionBatchKey,
		string(record.ProjectionKind),
		record.ProjectionVersion,
		record.SessionKey.GameID,
		record.SessionKey.WorldID,
		record.SessionKey.EntityID,
		payload.ContentFingerprint,
		record.CreatedAt.UTC().UnixNano(),
		now,
	); err != nil {
		return fmt.Errorf("insert projection batch: %w", err)
	}

	if err := s.enforceRetention(ctx, tx, record.SessionKey); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *SQLiteMemoryStore) Recent(ctx context.Context, key session.AgentSessionKey, limit int) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	if key.GameID == "" || key.WorldID == "" || key.EntityID == "" {
		return nil, fmt.Errorf("%w: session key is required", ErrInvalidRecord)
	}

	conn, closeConn, err := s.openConn(ctx, key)
	if err != nil {
		return nil, err
	}
	defer closeConn()

	if err := ensureSQLiteMetadata(ctx, conn, key, s.options.Now); err != nil {
		return nil, err
	}

	rows, err := conn.QueryContext(ctx, `
SELECT
	memory_id,
	projection_kind,
	projection_version,
	projection_batch_key,
	game_id,
	world_id,
	entity_id,
	source_turn_id,
	source_event_id,
	source_event_sequence,
	event_type,
	game_time,
	source_context_facts_json,
	outcomes_json,
	created_at
FROM recent_records
WHERE game_id = ? AND world_id = ? AND entity_id = ?
ORDER BY created_at DESC, memory_id DESC
LIMIT ?`,
		key.GameID,
		key.WorldID,
		key.EntityID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent records: %w", err)
	}
	defer rows.Close()

	var newestFirst []Record
	for rows.Next() {
		record, err := scanSQLiteRecord(rows)
		if err != nil {
			return nil, err
		}
		newestFirst = append(newestFirst, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent records: %w", err)
	}

	for left, right := 0, len(newestFirst)-1; left < right; left, right = left+1, right-1 {
		newestFirst[left], newestFirst[right] = newestFirst[right], newestFirst[left]
	}
	return newestFirst, nil
}

type sqliteConnector interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteMemoryStore) openConn(ctx context.Context, key session.AgentSessionKey) (*sql.Conn, func(), error) {
	dbPath, err := SQLiteDatabasePath(s.options.Root, key.GameID, key.WorldID)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create memory database directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open memory database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("open memory database connection: %w", err)
	}
	closeConn := func() {
		_ = conn.Close()
		_ = db.Close()
	}

	if err := configureSQLiteConn(ctx, conn, s.options.BusyTimeout); err != nil {
		closeConn()
		return nil, nil, err
	}
	if err := createSQLiteSchema(ctx, conn); err != nil {
		closeConn()
		return nil, nil, err
	}
	return conn, closeConn, nil
}

func configureSQLiteConn(ctx context.Context, conn *sql.Conn, busyTimeout time.Duration) error {
	busyTimeoutMS := int(busyTimeout / time.Millisecond)
	for _, statement := range []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS),
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite connection: %w", err)
		}
	}
	return nil
}

func createSQLiteSchema(ctx context.Context, conn *sql.Conn) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS memory_schema_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS recent_projection_batches (
			projection_batch_key TEXT PRIMARY KEY,
			projection_kind TEXT NOT NULL,
			projection_version INTEGER NOT NULL,
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			content_fingerprint TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS recent_records (
			memory_id TEXT PRIMARY KEY,
			projection_kind TEXT NOT NULL,
			projection_version INTEGER NOT NULL,
			projection_batch_key TEXT NOT NULL UNIQUE,
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			source_turn_id TEXT NOT NULL,
			source_event_id TEXT NOT NULL,
			source_event_sequence INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			game_time TEXT NOT NULL,
			source_context_facts_json TEXT NOT NULL,
			outcomes_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (projection_batch_key)
				REFERENCES recent_projection_batches(projection_batch_key)
				DEFERRABLE INITIALLY DEFERRED
		)`,
		`CREATE INDEX IF NOT EXISTS idx_recent_records_scope_created
			ON recent_records(game_id, world_id, entity_id, created_at, memory_id)`,
		`CREATE INDEX IF NOT EXISTS idx_recent_projection_batches_scope_seen
			ON recent_projection_batches(game_id, world_id, entity_id, last_seen_at, projection_batch_key)`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create sqlite memory schema: %w", err)
		}
	}
	return nil
}

func ensureSQLiteMetadata(ctx context.Context, db sqliteConnector, key session.AgentSessionKey, now func() time.Time) error {
	createdAt := now().UTC().Format(time.RFC3339Nano)
	required := map[string]string{
		"schema_version": SQLiteSchemaVersion,
		"game_id":        key.GameID,
		"world_id":       key.WorldID,
		"created_at":     createdAt,
	}
	for metadataKey, value := range required {
		if _, err := db.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_schema_metadata (key, value)
VALUES (?, ?)`, metadataKey, value); err != nil {
			return fmt.Errorf("initialize sqlite memory metadata: %w", err)
		}
	}

	metadata, err := readSQLiteMetadata(ctx, db)
	if err != nil {
		return err
	}
	if metadata["schema_version"] != SQLiteSchemaVersion {
		return fmt.Errorf("%w: got %q want %q", ErrSchemaMismatch, metadata["schema_version"], SQLiteSchemaVersion)
	}
	if metadata["game_id"] != key.GameID || metadata["world_id"] != key.WorldID {
		return fmt.Errorf("%w: got game_id=%q world_id=%q want game_id=%q world_id=%q",
			ErrWorldBindingMismatch,
			metadata["game_id"],
			metadata["world_id"],
			key.GameID,
			key.WorldID,
		)
	}
	if metadata["created_at"] == "" {
		return fmt.Errorf("%w: created_at is required", ErrWorldBindingMismatch)
	}
	return nil
}

func readSQLiteMetadata(ctx context.Context, db sqliteConnector) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM memory_schema_metadata`)
	if err != nil {
		return nil, fmt.Errorf("read sqlite memory metadata: %w", err)
	}
	defer rows.Close()

	metadata := make(map[string]string)
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan sqlite memory metadata: %w", err)
		}
		metadata[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite memory metadata: %w", err)
	}
	return metadata, nil
}

func projectionBatchFingerprint(ctx context.Context, tx *sql.Tx, projectionBatchKey string) (string, bool, error) {
	var fingerprint string
	err := tx.QueryRowContext(ctx, `
SELECT content_fingerprint
FROM recent_projection_batches
WHERE projection_batch_key = ?`, projectionBatchKey).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read projection batch: %w", err)
	}
	return fingerprint, true, nil
}

func (s *SQLiteMemoryStore) enforceRetention(ctx context.Context, tx *sql.Tx, key session.AgentSessionKey) error {
	if _, err := tx.ExecContext(ctx, `
DELETE FROM recent_records
WHERE game_id = ? AND world_id = ? AND entity_id = ?
	AND rowid NOT IN (
		SELECT rowid
		FROM recent_records
		WHERE game_id = ? AND world_id = ? AND entity_id = ?
		ORDER BY created_at DESC, memory_id DESC
		LIMIT ?
	)`,
		key.GameID, key.WorldID, key.EntityID,
		key.GameID, key.WorldID, key.EntityID,
		s.options.MaxRecordsPerEntity,
	); err != nil {
		return fmt.Errorf("prune recent records: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
DELETE FROM recent_projection_batches
WHERE game_id = ? AND world_id = ? AND entity_id = ?
	AND NOT EXISTS (
		SELECT 1
		FROM recent_records
		WHERE recent_records.projection_batch_key = recent_projection_batches.projection_batch_key
	)
	AND projection_batch_key NOT IN (
		SELECT projection_batch_key
		FROM recent_projection_batches
		WHERE game_id = ? AND world_id = ? AND entity_id = ?
		ORDER BY last_seen_at DESC, projection_batch_key DESC
		LIMIT ?
	)`,
		key.GameID, key.WorldID, key.EntityID,
		key.GameID, key.WorldID, key.EntityID,
		s.options.MaxProjectionBatchesPerEntity,
	); err != nil {
		return fmt.Errorf("prune projection batches: %w", err)
	}
	return nil
}

type sqliteRecordPayload struct {
	GameTimeJSON           string
	SourceContextFactsJSON string
	OutcomesJSON           string
	ContentFingerprint     string
}

func sqliteRecordPayloadFromRecord(record Record) (sqliteRecordPayload, error) {
	gameTimeJSON, err := stableJSON(record.GameTime)
	if err != nil {
		return sqliteRecordPayload{}, fmt.Errorf("marshal game_time: %w", err)
	}
	sourceContextFactsJSON, err := stableJSON(record.SourceContextFacts)
	if err != nil {
		return sqliteRecordPayload{}, fmt.Errorf("marshal source_context_facts: %w", err)
	}
	outcomesJSON, err := stableJSON(record.Outcomes)
	if err != nil {
		return sqliteRecordPayload{}, fmt.Errorf("marshal outcomes: %w", err)
	}

	fingerprintInput := struct {
		ProjectionKind         string          `json:"projection_kind"`
		ProjectionVersion      int             `json:"projection_version"`
		ProjectionBatchKey     string          `json:"projection_batch_key"`
		GameID                 string          `json:"game_id"`
		WorldID                string          `json:"world_id"`
		EntityID               string          `json:"entity_id"`
		SourceTurnID           string          `json:"source_turn_id"`
		SourceEventID          string          `json:"source_event_id"`
		SourceEventSequence    uint64          `json:"source_event_sequence"`
		EventType              string          `json:"event_type"`
		GameTime               json.RawMessage `json:"game_time"`
		SourceContextFactsJSON json.RawMessage `json:"source_context_facts_json"`
		OutcomesJSON           json.RawMessage `json:"outcomes_json"`
	}{
		ProjectionKind:         string(record.ProjectionKind),
		ProjectionVersion:      record.ProjectionVersion,
		ProjectionBatchKey:     record.ProjectionBatchKey,
		GameID:                 record.SessionKey.GameID,
		WorldID:                record.SessionKey.WorldID,
		EntityID:               record.SessionKey.EntityID,
		SourceTurnID:           record.SourceTurnID,
		SourceEventID:          record.SourceEventID,
		SourceEventSequence:    record.SourceEventSequence,
		EventType:              record.EventType,
		GameTime:               json.RawMessage(gameTimeJSON),
		SourceContextFactsJSON: json.RawMessage(sourceContextFactsJSON),
		OutcomesJSON:           json.RawMessage(outcomesJSON),
	}
	fingerprintJSON, err := json.Marshal(fingerprintInput)
	if err != nil {
		return sqliteRecordPayload{}, fmt.Errorf("marshal projection fingerprint: %w", err)
	}

	return sqliteRecordPayload{
		GameTimeJSON:           gameTimeJSON,
		SourceContextFactsJSON: sourceContextFactsJSON,
		OutcomesJSON:           outcomesJSON,
		ContentFingerprint:     sha256LowerHex(string(fingerprintJSON)),
	}, nil
}

func scanSQLiteRecord(rows *sql.Rows) (Record, error) {
	var record Record
	var projectionKind string
	var gameTimeJSON string
	var sourceContextFactsJSON string
	var outcomesJSON string
	var createdAt int64
	var sourceEventSequence int64

	if err := rows.Scan(
		&record.MemoryID,
		&projectionKind,
		&record.ProjectionVersion,
		&record.ProjectionBatchKey,
		&record.SessionKey.GameID,
		&record.SessionKey.WorldID,
		&record.SessionKey.EntityID,
		&record.SourceTurnID,
		&record.SourceEventID,
		&sourceEventSequence,
		&record.EventType,
		&gameTimeJSON,
		&sourceContextFactsJSON,
		&outcomesJSON,
		&createdAt,
	); err != nil {
		return Record{}, fmt.Errorf("scan recent record: %w", err)
	}

	record.ProjectionKind = ProjectionKind(projectionKind)
	record.SourceEventSequence = uint64(sourceEventSequence)
	if err := json.Unmarshal([]byte(gameTimeJSON), &record.GameTime); err != nil {
		return Record{}, fmt.Errorf("unmarshal game_time: %w", err)
	}
	if err := json.Unmarshal([]byte(sourceContextFactsJSON), &record.SourceContextFacts); err != nil {
		return Record{}, fmt.Errorf("unmarshal source_context_facts: %w", err)
	}
	if err := json.Unmarshal([]byte(outcomesJSON), &record.Outcomes); err != nil {
		return Record{}, fmt.Errorf("unmarshal outcomes: %w", err)
	}
	record.CreatedAt = time.Unix(0, createdAt).UTC()
	return record, nil
}

func validateSQLiteRecord(record Record) error {
	if record.MemoryID == "" {
		return ErrInvalidRecord
	}
	if record.SessionKey.GameID == "" || record.SessionKey.WorldID == "" || record.SessionKey.EntityID == "" {
		return ErrInvalidRecord
	}
	if !validProjectionKind(record.ProjectionKind) || record.ProjectionVersion <= 0 || record.ProjectionBatchKey == "" {
		return ErrInvalidRecord
	}
	if record.SourceTurnID == "" {
		return ErrInvalidRecord
	}
	if record.SourceEventSequence > math.MaxInt64 {
		return ErrInvalidRecord
	}
	if record.CreatedAt.IsZero() {
		return ErrInvalidRecord
	}
	expectedBatchKey, err := BuildProjectionBatchKey(record.SessionKey, record.SourceTurnID, record.SourceEventID, record.ProjectionKind, record.ProjectionVersion)
	if err != nil {
		return err
	}
	if record.ProjectionBatchKey != expectedBatchKey {
		return ErrInvalidRecord
	}
	return nil
}

func stableJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func sha256LowerHex(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
