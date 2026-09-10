package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gameagent/runtime/internal/session"
)

func prepareRetrievalDatabase(ctx context.Context, conn *sql.Conn, key session.AgentSessionKey) error {
	metadata, err := readSQLiteMetadata(ctx, conn)
	if err != nil {
		return err
	}
	if metadata["schema_version"] == RetrievalSchemaVersion {
		return nil
	}
	tx, err := beginSQLiteWriteTransaction(ctx, conn)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema, err := readSQLiteSchema(ctx, tx)
	if err != nil {
		return err
	}
	metadata, err = readSQLiteMetadata(ctx, tx)
	if err != nil {
		return err
	}
	if metadata["schema_version"] == RetrievalSchemaVersion {
		return validateRetrievalDatabase(ctx, tx, schema, key)
	}
	if err = migrateRetrievalDatabase(ctx, tx, schema, key); err != nil {
		return err
	}
	return commitSQLiteTransaction(ctx, tx)
}

var retrievalSchema = []struct{ name, statement string }{
	{"history_index_sources", `CREATE TABLE history_index_sources (
  source_id TEXT PRIMARY KEY REFERENCES session_history(source_id),
  content_fingerprint TEXT NOT NULL,
  index_signature TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('ready','capacity_exceeded','maintenance_exceeded'))
 )`},
	{"history_fragments", `CREATE TABLE history_fragments (
  source_id TEXT NOT NULL REFERENCES session_history(source_id),
  field_path TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  field_kind TEXT NOT NULL,
  original_text TEXT NOT NULL,
  start_rune INTEGER NOT NULL,
  end_rune INTEGER NOT NULL,
  PRIMARY KEY(source_id,field_path)
 )`},
	{"history_terms", `CREATE TABLE history_terms (
  game_id TEXT NOT NULL,
  world_id TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  term TEXT NOT NULL,
  source_id TEXT NOT NULL,
  field_path TEXT NOT NULL,
  PRIMARY KEY(game_id,world_id,entity_id,term,source_id,field_path),
  FOREIGN KEY(source_id,field_path) REFERENCES history_fragments(source_id,field_path)
 )`},
	{"idx_history_terms_source", `CREATE INDEX idx_history_terms_source ON history_terms(source_id,field_path)`},
}

func migrateRetrievalDatabase(ctx context.Context, db sqliteConnector, schema map[string]string, key session.AgentSessionKey) error {
	if err := validateHistoryDatabase(ctx, db, schema, key); err != nil {
		return err
	}
	if err := validateRetrievalMigrationSources(ctx, db, key); err != nil {
		return err
	}
	for _, object := range retrievalSchema {
		if _, err := db.ExecContext(ctx, object.statement); err != nil {
			return fmt.Errorf("migrate retrieval %s: %w", object.name, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE memory_schema_metadata SET value=? WHERE key='schema_version'`, RetrievalSchemaVersion); err != nil {
		return err
	}
	schema, err := readSQLiteSchema(ctx, db)
	if err != nil {
		return err
	}
	return validateRetrievalDatabase(ctx, db, schema, key)
}

func validateRetrievalMigrationSources(ctx context.Context, db sqliteConnector, key session.AgentSessionKey) error {
	// Stream originals across all owners without per-source queries or indexing limits.
	rows, err := db.QueryContext(ctx, `SELECT `+historySourceColumns+` FROM session_history ORDER BY sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, err := scanHistorySource(rows)
		if err != nil {
			return fmt.Errorf("validate history migration source: %w", err)
		}
		if source.Owner.GameID != key.GameID || source.Owner.WorldID != key.WorldID {
			return fmt.Errorf("%w: history migration source world", ErrInvalidHistory)
		}
	}
	return rows.Err()
}

func validateRetrievalDatabase(ctx context.Context, db sqliteConnector, schema map[string]string, key session.AgentSessionKey) error {
	if err := validateHistoryDatabaseVersion(ctx, db, schema, key, RetrievalSchemaVersion); err != nil {
		return err
	}
	for _, object := range retrievalSchema {
		if strings.Join(strings.Fields(schema[object.name]), " ") != strings.Join(strings.Fields(object.statement), " ") {
			return fmt.Errorf("%w: incompatible %s", ErrSchemaMismatch, object.name)
		}
	}
	return nil
}
