package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gameagent/runtime/internal/session"
)

var historySchema = []struct{ name, statement string }{
	{"session_history", `CREATE TABLE session_history (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		source_id TEXT NOT NULL UNIQUE,
		batch_key TEXT NOT NULL UNIQUE,
		game_id TEXT NOT NULL,
		world_id TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		version INTEGER NOT NULL,
		content_fingerprint TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		times_json TEXT NOT NULL,
		payload_json TEXT,
		payload_bytes INTEGER NOT NULL CHECK(payload_bytes >= 0),
		availability TEXT NOT NULL CHECK(availability IN ('available','pruned')),
		legacy_memory_id TEXT NOT NULL
	)`},
	{"idx_history_scope_sequence", `CREATE INDEX idx_history_scope_sequence ON session_history(game_id,world_id,entity_id,sequence)`},
	{"idx_history_legacy", `CREATE UNIQUE INDEX idx_history_legacy ON session_history(legacy_memory_id) WHERE legacy_memory_id <> ''`},
	{"context_summaries", `CREATE TABLE context_summaries (
		revision INTEGER PRIMARY KEY AUTOINCREMENT,
		summary_id TEXT NOT NULL UNIQUE,
		game_id TEXT NOT NULL,
		world_id TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		parent_id TEXT NOT NULL,
		generation_version TEXT NOT NULL,
		summary_text TEXT NOT NULL,
		source_count INTEGER NOT NULL,
		coverage_fingerprint TEXT NOT NULL,
		coverage_times_json TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`},
	{"idx_summaries_scope_revision", `CREATE INDEX idx_summaries_scope_revision ON context_summaries(game_id,world_id,entity_id,revision)`},
	{"summary_sources", `CREATE TABLE summary_sources (
		summary_id TEXT NOT NULL REFERENCES context_summaries(summary_id),
		source_id TEXT NOT NULL REFERENCES session_history(source_id),
		source_sequence INTEGER NOT NULL,
		content_fingerprint TEXT NOT NULL,
		PRIMARY KEY(summary_id,source_id)
	)`},
	{"idx_summary_sources_source", `CREATE INDEX idx_summary_sources_source ON summary_sources(source_id,summary_id)`},
}

func (s *SQLiteHistoryStore) openHistoryConn(ctx context.Context, key session.AgentSessionKey) (*sql.Conn, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if _, err := session.Resolve(key.GameID, key.WorldID, key.EntityID); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidHistory, err)
	}
	if err := s.limits.Validate(); err != nil {
		return nil, nil, err
	}
	if err := s.indexLimits.Validate(); err != nil {
		return nil, nil, err
	}
	conn, closeConn, err := s.base.openRawConn(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	if err := s.prepareHistoryDatabase(ctx, conn, key); err != nil {
		closeConn()
		return nil, nil, err
	}
	return conn, closeConn, nil
}

func (s *SQLiteHistoryStore) prepareHistoryDatabase(ctx context.Context, conn *sql.Conn, key session.AgentSessionKey) error {
	if err := s.prepareHistoryFoundation(ctx, conn, key); err != nil {
		return err
	}
	return prepareRetrievalDatabase(ctx, conn, key)
}

func (s *SQLiteHistoryStore) prepareHistoryFoundation(ctx context.Context, conn *sql.Conn, key session.AgentSessionKey) error {
	schema, err := readSQLiteSchema(ctx, conn)
	if err != nil {
		return err
	}
	if len(schema) == 0 {
		var busyMS int64
		if err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyMS); err != nil {
			return err
		}
		if err = configureSQLiteJournal(ctx, conn, time.Duration(busyMS)*time.Millisecond); err != nil {
			return err
		}
	} else {
		metadata, readErr := readSQLiteMetadata(ctx, conn)
		if readErr != nil {
			return readErr
		}
		if metadata["schema_version"] == RetrievalSchemaVersion {
			// Read schema after observing publication; an earlier schema read may predate migration.
			schema, err = readSQLiteSchema(ctx, conn)
			if err != nil {
				return err
			}
			return validateRetrievalDatabase(ctx, conn, schema, key)
		}
		if metadata["schema_version"] == HistorySchemaVersion {
			schema, err = readSQLiteSchema(ctx, conn)
			if err != nil {
				return err
			}
			return validateHistoryDatabase(ctx, conn, schema, key)
		}
		if metadata["schema_version"] != SQLiteSchemaVersion && metadata["schema_version"] != HistorySchemaVersion {
			return fmt.Errorf("%w: unsupported history schema %q", ErrSchemaMismatch, metadata["schema_version"])
		}
	}
	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema, err = readSQLiteSchema(ctx, tx)
	if err != nil {
		return err
	}
	if len(schema) == 0 {
		for _, object := range sqliteSchema {
			if _, err = tx.ExecContext(ctx, object.statement); err != nil {
				return fmt.Errorf("create history base %s: %w", object.name, err)
			}
		}
		if err = initializeSQLiteMetadata(ctx, tx, key, s.base.options.Now); err != nil {
			return err
		}
		schema, err = readSQLiteSchema(ctx, tx)
		if err != nil {
			return err
		}
	}
	metadata, err := readSQLiteMetadata(ctx, tx)
	if err != nil {
		return err
	}
	if metadata["schema_version"] == RetrievalSchemaVersion {
		return validateRetrievalDatabase(ctx, tx, schema, key)
	}
	if metadata["schema_version"] == HistorySchemaVersion {
		return validateHistoryDatabase(ctx, tx, schema, key)
	}
	if metadata["schema_version"] != SQLiteSchemaVersion {
		return fmt.Errorf("%w: unsupported history schema %q", ErrSchemaMismatch, metadata["schema_version"])
	}
	if err = validateSQLiteDatabase(ctx, tx, schema, key); err != nil {
		return err
	}
	for _, object := range historySchema {
		if _, err = tx.ExecContext(ctx, object.statement); err != nil {
			return fmt.Errorf("migrate history %s: %w", object.name, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT memory_id,projection_kind,projection_version,projection_batch_key,game_id,world_id,entity_id,source_turn_id,source_event_id,source_event_sequence,event_type,game_time,source_context_facts_json,outcomes_json,created_at FROM recent_records ORDER BY created_at,memory_id`)
	if err != nil {
		return err
	}
	// Read one source at a time. The transaction owns both import and schema publication.
	count := 0
	for rows.Next() {
		record, readErr := scanSQLiteRecordWithDecoder(rows, decodeHistoryJSON)
		if readErr != nil {
			_ = rows.Close()
			return readErr
		}
		if record.SessionKey.GameID != key.GameID || record.SessionKey.WorldID != key.WorldID {
			_ = rows.Close()
			return ErrWorldBindingMismatch
		}
		if err := validateLegacyHistoryCredential(ctx, tx, record); err != nil {
			_ = rows.Close()
			return err
		}
		batch := HistoryBatch{Owner: record.SessionKey, Kind: HistoryKindLegacy, Version: HistoryVersion, TurnID: record.SourceTurnID, Event: HistoryEvent{ID: record.SourceEventID, Type: record.EventType, Sequence: record.SourceEventSequence, GameTime: record.GameTime, Facts: record.SourceContextFacts}, Terminal: HistoryTerminal{Status: "legacy"}, Legacy: &record}
		data, batchKey, fp, importErr := CanonicalHistoryBatch(batch, 0)
		if importErr == nil {
			_, importErr = insertHistorySource(ctx, tx, batch, data, batchKey, fp, record.CreatedAt, record.MemoryID)
		}
		if importErr != nil {
			_ = rows.Close()
			return importErr
		}
		count++
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	var imported int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_history WHERE kind = ?", HistoryKindLegacy).Scan(&imported); err != nil {
		return err
	}
	if imported != count {
		return fmt.Errorf("history migration count mismatch: %d != %d", imported, count)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE memory_schema_metadata SET value = ? WHERE key = 'schema_version'", HistorySchemaVersion); err != nil {
		return err
	}
	schema, err = readSQLiteSchema(ctx, tx)
	if err != nil {
		return err
	}
	if err = validateHistoryDatabase(ctx, tx, schema, key); err != nil {
		return err
	}
	return commitSQLiteTransaction(ctx, tx)
}

func validateLegacyHistoryCredential(ctx context.Context, db sqliteConnector, record Record) error {
	var kind, game, world, entity, fingerprint string
	var version int
	err := db.QueryRowContext(ctx, `SELECT projection_kind,projection_version,game_id,world_id,entity_id,content_fingerprint FROM recent_projection_batches WHERE projection_batch_key=?`, record.ProjectionBatchKey).Scan(&kind, &version, &game, &world, &entity, &fingerprint)
	if err != nil {
		return fmt.Errorf("%w: legacy credential unavailable: %w", ErrInvalidHistory, err)
	}
	payload, err := sqliteRecordPayloadFromRecord(record)
	if err != nil {
		return err
	}
	if kind != string(record.ProjectionKind) || version != record.ProjectionVersion || game != record.SessionKey.GameID || world != record.SessionKey.WorldID || entity != record.SessionKey.EntityID || fingerprint != payload.ContentFingerprint {
		return fmt.Errorf("%w: legacy credential does not match original", ErrInvalidHistory)
	}
	return nil
}

func validateHistoryDatabase(ctx context.Context, db sqliteConnector, schema map[string]string, key session.AgentSessionKey) error {
	return validateHistoryDatabaseVersion(ctx, db, schema, key, HistorySchemaVersion)
}

func validateHistoryDatabaseVersion(ctx context.Context, db sqliteConnector, schema map[string]string, key session.AgentSessionKey, version string) error {
	for _, objects := range [][]struct{ name, statement string }{sqliteSchema, historySchema} {
		for _, object := range objects {
			if strings.Join(strings.Fields(schema[object.name]), " ") != strings.Join(strings.Fields(object.statement), " ") {
				return fmt.Errorf("%w: incompatible %s", ErrSchemaMismatch, object.name)
			}
		}
	}
	m, err := readSQLiteMetadata(ctx, db)
	if err != nil {
		return err
	}
	if m["schema_version"] != version || m["created_at"] == "" {
		return ErrSchemaMismatch
	}
	if m["game_id"] != key.GameID || m["world_id"] != key.WorldID {
		return ErrWorldBindingMismatch
	}
	return nil
}
