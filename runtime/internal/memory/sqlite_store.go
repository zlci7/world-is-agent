package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gameagent/runtime/internal/session"

	"modernc.org/sqlite"
)

const (
	SQLiteSchemaVersion                        = "phase8_1_recent_v1"
	DefaultSQLiteMemoryRoot                    = "runtime/.local/memory"
	DefaultSQLiteBusyTimeout                   = 5 * time.Second
	DefaultSQLiteMaxRecordsPerEntity           = 100
	DefaultSQLiteMaxProjectionBatchesPerEntity = DefaultSQLiteMaxRecordsPerEntity * 4
	defaultProjectionBatchCap                  = 100
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
	options       SQLiteStoreOptions
	validationErr error
}

func NewSQLiteMemoryStore(options SQLiteStoreOptions) *SQLiteMemoryStore {
	options = options.withDefaults()
	var validationErr error
	if options.MaxProjectionBatchesPerEntity < options.MaxRecordsPerEntity {
		validationErr = fmt.Errorf("invalid sqlite memory options: max_projection_batches_per_entity (%d) must be at least max_records_per_entity (%d)",
			options.MaxProjectionBatchesPerEntity, options.MaxRecordsPerEntity)
	}
	return &SQLiteMemoryStore{options: options, validationErr: validationErr}
}

func (o SQLiteStoreOptions) withDefaults() SQLiteStoreOptions {
	if o.Root == "" {
		o.Root = DefaultSQLiteMemoryRoot
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = DefaultSQLiteBusyTimeout
	}
	if o.MaxRecordsPerEntity <= 0 {
		o.MaxRecordsPerEntity = DefaultSQLiteMaxRecordsPerEntity
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
	if s.validationErr != nil {
		return s.validationErr
	}
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

	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return err
	}
	defer tx.Rollback()

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
		return commitSQLiteTransaction(ctx, tx)
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
	return commitSQLiteTransaction(ctx, tx)
}

func (s *SQLiteMemoryStore) Recent(ctx context.Context, key session.AgentSessionKey, limit int) ([]Record, error) {
	if s.validationErr != nil {
		return nil, s.validationErr
	}
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
	conn, closeConn, err := s.openRawConn(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	if err := s.prepareSQLiteDatabase(ctx, conn, key); err != nil {
		closeConn()
		return nil, nil, err
	}
	return conn, closeConn, nil
}

func (s *SQLiteMemoryStore) openRawConn(ctx context.Context, key session.AgentSessionKey) (*sql.Conn, func(), error) {
	dbPath, err := SQLiteDatabasePath(s.options.Root, key.GameID, key.WorldID)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create memory database directory: %w", err)
	}

	absolutePath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve memory database path: %w", err)
	}
	uriPath := filepath.ToSlash(absolutePath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := url.URL{Scheme: "file", Path: uriPath, RawQuery: url.Values{"_txlock": {"immediate"}}.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
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
	return conn, closeConn, nil
}

func configureSQLiteConn(ctx context.Context, conn *sql.Conn, busyTimeout time.Duration) error {
	busyTimeoutMS := int(busyTimeout / time.Millisecond)
	for _, statement := range []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS),
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite connection: %w", err)
		}
	}
	return nil
}

var sqliteSchema = []struct {
	name      string
	statement string
}{
	{"memory_schema_metadata", `CREATE TABLE memory_schema_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`},
	{"recent_projection_batches", `CREATE TABLE recent_projection_batches (
			projection_batch_key TEXT PRIMARY KEY,
			projection_kind TEXT NOT NULL,
			projection_version INTEGER NOT NULL,
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			content_fingerprint TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL
		)`},
	{"recent_records", `CREATE TABLE recent_records (
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
		)`},
	{"idx_recent_records_scope_created", `CREATE INDEX idx_recent_records_scope_created
			ON recent_records(game_id, world_id, entity_id, created_at, memory_id)`},
	{"idx_recent_projection_batches_scope_seen", `CREATE INDEX idx_recent_projection_batches_scope_seen
			ON recent_projection_batches(game_id, world_id, entity_id, last_seen_at, projection_batch_key)`},
}

func (s *SQLiteMemoryStore) prepareSQLiteDatabase(ctx context.Context, conn *sql.Conn, key session.AgentSessionKey) error {
	schema, err := readSQLiteSchema(ctx, conn)
	if err != nil {
		return err
	}
	if len(schema) != 0 {
		return validateSQLiteDatabase(ctx, conn, schema, key)
	}

	if err := configureSQLiteJournal(ctx, conn, s.options.BusyTimeout); err != nil {
		return err
	}
	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Another opener may have initialized this world while we waited for its writer.
	schema, err = readSQLiteSchema(ctx, tx)
	if err != nil {
		return err
	}
	if len(schema) != 0 {
		return validateSQLiteDatabase(ctx, tx, schema, key)
	}
	for _, object := range sqliteSchema {
		if _, err := tx.ExecContext(ctx, object.statement); err != nil {
			return fmt.Errorf("create sqlite memory schema: %w", err)
		}
	}
	if err := initializeSQLiteMetadata(ctx, tx, key, s.options.Now); err != nil {
		return err
	}
	return commitSQLiteTransaction(ctx, tx)
}

func configureSQLiteJournal(ctx context.Context, conn *sql.Conn, busyTimeout time.Duration) (err error) {
	// Concurrent WAL transitions can return BUSY without invoking SQLite's busy
	// handler. This loop owns the transition's wait budget; normal writes use it again.
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return fmt.Errorf("configure sqlite journal timeout: %w", err)
	}
	defer func() {
		_, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout/time.Millisecond))
		err = errors.Join(err, restoreErr)
	}()
	deadline := time.Now().Add(busyTimeout)
	for {
		var mode string
		err = conn.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
		if err == nil {
			if mode != "wal" {
				return fmt.Errorf("configure sqlite journal: got %q, want wal", mode)
			}
			return nil
		}
		var sqliteErr *sqlite.Error
		remaining := time.Until(deadline)
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 || remaining <= 0 {
			return fmt.Errorf("configure sqlite journal: %w", err)
		}
		timer := time.NewTimer(min(10*time.Millisecond, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func beginSQLiteWriteTransaction(ctx context.Context, conn *sql.Conn) (*sql.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		var busyMS int64
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyMS); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline).Milliseconds()
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < busyMS {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", remaining)); err != nil {
				return nil, err
			}
		}
	}
	// Statement contexts remain cancelable. The owner synchronously ends the
	// transaction, including rollback, before releasing the connection.
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func commitSQLiteTransaction(ctx context.Context, tx *sql.Tx) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func readSQLiteSchema(ctx context.Context, db sqliteConnector) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("%w: read sqlite schema: %w", ErrSchemaMismatch, err)
	}
	defer rows.Close()
	schema := make(map[string]string)
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			return nil, fmt.Errorf("%w: scan sqlite schema: %w", ErrSchemaMismatch, err)
		}
		schema[name] = statement
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sqlite schema: %w", err)
	}
	return schema, nil
}

func initializeSQLiteMetadata(ctx context.Context, db sqliteConnector, key session.AgentSessionKey, now func() time.Time) error {
	createdAt := now().UTC().Format(time.RFC3339Nano)
	required := map[string]string{
		"schema_version": SQLiteSchemaVersion,
		"game_id":        key.GameID,
		"world_id":       key.WorldID,
		"created_at":     createdAt,
	}
	for metadataKey, value := range required {
		if _, err := db.ExecContext(ctx, `
INSERT INTO memory_schema_metadata (key, value)
VALUES (?, ?)`, metadataKey, value); err != nil {
			return fmt.Errorf("initialize sqlite memory metadata: %w", err)
		}
	}
	return nil
}

func validateSQLiteDatabase(ctx context.Context, db sqliteConnector, schema map[string]string, key session.AgentSessionKey) error {
	// This schema version fixes columns, constraints and indexes as one contract.
	for _, object := range sqliteSchema {
		got := strings.Join(strings.Fields(schema[object.name]), " ")
		want := strings.Join(strings.Fields(object.statement), " ")
		if got != want {
			return fmt.Errorf("%w: missing or incompatible %s", ErrSchemaMismatch, object.name)
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
		return fmt.Errorf("%w: created_at is required", ErrSchemaMismatch)
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
WHERE projection_batch_key IN (
		SELECT projection_batch_key
		FROM recent_projection_batches
		WHERE game_id = ? AND world_id = ? AND entity_id = ?
			AND NOT EXISTS (
				SELECT 1 FROM recent_records
				WHERE recent_records.projection_batch_key = recent_projection_batches.projection_batch_key
			)
		ORDER BY last_seen_at ASC, projection_batch_key ASC
		LIMIT (
			SELECT max(0, COUNT(*) - ?)
			FROM recent_projection_batches
			WHERE game_id = ? AND world_id = ? AND entity_id = ?
		)
	)`,
		key.GameID, key.WorldID, key.EntityID,
		s.options.MaxProjectionBatchesPerEntity,
		key.GameID, key.WorldID, key.EntityID,
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
	return scanSQLiteRecordWithDecoder(rows, json.Unmarshal)
}

func scanSQLiteRecordWithDecoder(rows *sql.Rows, decode func([]byte, any) error) (Record, error) {
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
	if err := decode([]byte(gameTimeJSON), &record.GameTime); err != nil {
		return Record{}, fmt.Errorf("unmarshal game_time: %w", err)
	}
	if err := decode([]byte(sourceContextFactsJSON), &record.SourceContextFacts); err != nil {
		return Record{}, fmt.Errorf("unmarshal source_context_facts: %w", err)
	}
	if err := decode([]byte(outcomesJSON), &record.Outcomes); err != nil {
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
