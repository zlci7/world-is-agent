package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetrievalMigrationRejectsCorruptOriginalsAtomically(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"equal_length_payload", `UPDATE session_history SET payload_json=replace(payload_json,'7319','7320')`},
		{"fingerprint", `UPDATE session_history SET content_fingerprint=printf('%064d',0)`},
		{"malformed_fingerprint", `UPDATE session_history SET content_fingerprint=replace(content_fingerprint,substr(content_fingerprint,1,1),'z')`},
		{"game_header", `UPDATE session_history SET game_id='other-game'`},
		{"world_header", `UPDATE session_history SET world_id='other-world'`},
		{"entity_header", `UPDATE session_history SET entity_id='agent-2'`},
		{"time_header", `UPDATE session_history SET times_json='[]'`},
		{"malformed_time_header", `UPDATE session_history SET times_json='['`},
		{"kind_header", `UPDATE session_history SET kind='unknown'`},
		{"version_header", `UPDATE session_history SET version=2`},
		{"source_id_header", `UPDATE session_history SET source_id='history_wrong'`},
		{"batch_key_header", `UPDATE session_history SET batch_key='wrong'`},
		{"sequence_header", `UPDATE session_history SET sequence=0`},
		{"legacy_header", `UPDATE session_history SET legacy_memory_id='unexpected-copy'`},
		{"payload_bytes", `UPDATE session_history SET payload_bytes=payload_bytes+1`},
		{"available_without_original", `UPDATE session_history SET payload_json=NULL`},
		{"pruned_with_original", `UPDATE session_history SET availability='pruned'`},
		{"pruned_invalid_fingerprint", `UPDATE session_history SET availability='pruned',payload_json=NULL,content_fingerprint=replace(content_fingerprint,substr(content_fingerprint,1,1),'z')`},
		{"pruned_entity_header", `UPDATE session_history SET availability='pruned',payload_json=NULL,entity_id='agent-2'`},
		{"pruned_empty_times", `UPDATE session_history SET availability='pruned',payload_json=NULL,times_json='[]'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, db, source := phase82Fixture(t)
			if _, err := db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			assertCorruptRetrievalMigrationRollsBack(t, root, db, source)
		})
	}
}

func TestRetrievalMigrationRejectsFingerprintedInvalidPayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string) string
	}{
		{"malformed_json", func(raw string) string { return "[" + raw[1:] }},
		{"trailing_json", func(raw string) string { return raw + " true" }},
		{"unknown_field", func(raw string) string { return `{"unknown":true,` + raw[1:] }},
		{"invalid_terminal", func(raw string) string { return strings.Replace(raw, `"completed"`, `"invalid"`, 1) }},
		{"payload_owner", func(raw string) string { return strings.Replace(raw, `"agent-1"`, `"agent-2"`, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, db, source := phase82Fixture(t)
			var raw string
			if err := db.QueryRow("SELECT payload_json FROM session_history").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			changed := tc.change(raw)
			if changed == raw {
				t.Fatal("fixture mutation did not change the original")
			}
			if _, err := db.Exec("UPDATE session_history SET payload_json=?,payload_bytes=?,content_fingerprint=?", changed, len(changed), sha256LowerHex(changed)); err != nil {
				t.Fatal(err)
			}
			assertCorruptRetrievalMigrationRollsBack(t, root, db, source)
		})
	}
}

func assertCorruptRetrievalMigrationRollsBack(t *testing.T, root string, db *sql.DB, source HistorySource) {
	t.Helper()
	ctx := context.Background()
	beforeSchema, err := readSQLiteSchema(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	beforeMetadata, err := readSQLiteMetadata(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	beforeRows := retrievalMigrationRows(t, db)
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	snapshot, err := store.BeginHistorySnapshot(ctx, source.Owner)
	if err == nil {
		store.ReleaseHistorySnapshot(snapshot)
	}
	if !errors.Is(err, ErrInvalidHistory) {
		t.Errorf("corrupt original must reject migration: %v", err)
	}
	afterSchema, err := readSQLiteSchema(ctx, db)
	if err != nil || !reflect.DeepEqual(afterSchema, beforeSchema) {
		t.Errorf("migration changed schema: %v", err)
	}
	afterMetadata, err := readSQLiteMetadata(ctx, db)
	if err != nil || !reflect.DeepEqual(afterMetadata, beforeMetadata) || afterMetadata["schema_version"] != HistorySchemaVersion {
		t.Errorf("migration changed phase82 metadata: %+v %v", afterMetadata, err)
	}
	if !reflect.DeepEqual(retrievalMigrationRows(t, db), beforeRows) {
		t.Error("migration changed the stored original or headers")
	}
}

func retrievalMigrationRows(t *testing.T, db *sql.DB) [][]any {
	t.Helper()
	rows, err := db.Query("SELECT * FROM session_history ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		args := make([]any, len(columns))
		for i := range values {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRetrievalMigrationPreservesLargeOriginalAcrossConfiguredLimits(t *testing.T) {
	root, db, source := phase82Fixture(t)
	batch := testHistoryBatch("large-original")
	batch.Event.Facts[0].Text = strings.Repeat("original 7319 ", (8<<20)/14)
	data, key, fp, err := CanonicalHistoryBatch(batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	large, err := insertHistorySource(context.Background(), db, batch, data, key, fp, time.Unix(200, 0).UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{MaxBatchBytes: 1024}, WithHistoryIndexLimits(HistoryIndexLimits{TextBytes: 128}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := store.BeginHistorySnapshot(ctx, source.Owner)
	if err != nil {
		t.Fatal(err)
	}
	store.ReleaseHistorySnapshot(snapshot)
	got, err := store.ReadHistorySource(ctx, large.Owner, large.ID)
	if err != nil || !reflect.DeepEqual(got, large) {
		t.Fatalf("valid large original changed: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM history_index_sources").Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration eagerly indexed originals: %d %v", count, err)
	}
}

func TestRetrievalMigrationStreamsAllOwners(t *testing.T) {
	_, db, source := phase82Fixture(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 1; i < 16384; i++ {
		batch := testHistoryBatch(fmt.Sprintf("migration-%d", i))
		batch.Owner.EntityID = fmt.Sprintf("agent-%d", i%4)
		data, key, fp, err := CanonicalHistoryBatch(batch, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := insertHistorySource(context.Background(), tx, batch, data, key, fp, time.Unix(100, 0).UTC(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema, err := readSQLiteSchema(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &summarySQLRecorder{sqliteConnector: tx}
	if err := migrateRetrievalDatabase(ctx, recorder, schema, source.Owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var sourceQueries int
	for _, call := range recorder.calls {
		if strings.Contains(call.query, "FROM session_history") {
			sourceQueries++
		}
	}
	if sourceQueries != 1 {
		t.Fatalf("migration source SQL queries=%d, want one streamed scan", sourceQueries)
	}
	t.Logf("validated 16384 sources across four owners in %s", time.Since(started))
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_history WHERE availability='available'").Scan(&count); err != nil || count != 16384 {
		t.Fatalf("source preservation: %d %v", count, err)
	}
}

func TestRetrievalMigrationPreservesPrunedHeaders(t *testing.T) {
	root, db, source := phase82Fixture(t)
	if _, err := db.Exec("UPDATE session_history SET payload_json=NULL,availability='pruned'"); err != nil {
		t.Fatal(err)
	}
	before := retrievalMigrationRows(t, db)
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	got, err := store.ReadHistorySource(context.Background(), source.Owner, source.ID)
	if err != nil || got.Availability != HistoryPruned || got.Batch != nil || got.Fingerprint != source.Fingerprint || got.Sequence != source.Sequence || !got.CreatedAt.Equal(source.CreatedAt) {
		t.Fatalf("pruned source header changed: %+v %v", got, err)
	}
	if !reflect.DeepEqual(retrievalMigrationRows(t, db), before) {
		t.Fatal("migration changed the retained source header")
	}
}
