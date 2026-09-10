package memory

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func phase82Fixture(t *testing.T) (string, *sql.DB, HistorySource) {
	t.Helper()
	root := t.TempDir()
	batch := testHistoryBatch("phase82")
	base := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root})
	if _, err := base.Recent(context.Background(), batch.Owner, 1); err != nil {
		t.Fatal(err)
	}
	db := historyTestDB(t, root, batch)
	for _, object := range historySchema {
		if _, err := db.Exec(object.statement); err != nil {
			t.Fatal(err)
		}
	}
	data, key, fp, err := CanonicalHistoryBatch(batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := insertHistorySource(context.Background(), db, batch, data, key, fp, time.Unix(100, 0).UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE memory_schema_metadata SET value=? WHERE key='schema_version'", HistorySchemaVersion); err != nil {
		t.Fatal(err)
	}
	return root, db, source
}

func TestRetrievalMigrationPreservesHistoryAndDefersIndex(t *testing.T) {
	root, db, source := phase82Fixture(t)
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	got, err := s.ReadHistorySource(context.Background(), source.Owner, source.ID)
	if err != nil || !reflect.DeepEqual(got, source) {
		t.Fatalf("history changed: %v %+v", err, got)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM memory_schema_metadata WHERE key='schema_version'").Scan(&version); err != nil || version != RetrievalSchemaVersion {
		t.Fatalf("schema: %s %v", version, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM history_index_sources").Scan(&count); err != nil || count != 0 {
		t.Fatalf("index must be pending: %d %v", count, err)
	}
}

func TestRetrievalMigrationRollback(t *testing.T) {
	root, db, source := phase82Fixture(t)
	if _, err := db.Exec("CREATE TRIGGER fail_retrieval BEFORE UPDATE ON memory_schema_metadata BEGIN SELECT RAISE(ABORT,'test migration'); END"); err != nil {
		t.Fatal(err)
	}
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	if _, err := s.ReadHistorySource(context.Background(), source.Owner, source.ID); err == nil {
		t.Fatal("migration must fail")
	}
	var version string
	if err := db.QueryRow("SELECT value FROM memory_schema_metadata WHERE key='schema_version'").Scan(&version); err != nil || version != HistorySchemaVersion {
		t.Fatalf("schema not rolled back: %s %v", version, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='history_terms'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("DDL not rolled back: %d %v", count, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_retrieval"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadHistorySource(context.Background(), source.Owner, source.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSequentialMigrationFailureLeavesCompletePhase82(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	batch := testHistoryBatch("chain")
	old := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root})
	record := Record{MemoryID: "chain-old", SessionKey: batch.Owner, ProjectionKind: ProjectionKindSettledTurn, ProjectionVersion: ProjectionVersionRecentV2, SourceTurnID: batch.TurnID, SourceEventID: batch.Event.ID, EventType: batch.Event.Type, CreatedAt: time.Unix(100, 0).UTC(), SourceContextFacts: batch.Event.Facts}
	record.ProjectionBatchKey, _ = BuildProjectionBatchKey(record.SessionKey, record.SourceTurnID, record.SourceEventID, record.ProjectionKind, record.ProjectionVersion)
	if err := old.Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	db := historyTestDB(t, root, batch)
	if _, err := db.Exec(`CREATE TRIGGER fail_second BEFORE UPDATE ON memory_schema_metadata WHEN NEW.value='phase8_3_history_v1' BEGIN SELECT RAISE(ABORT,'second migration'); END`); err != nil {
		t.Fatal(err)
	}
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	if _, err := s.BeginHistorySnapshot(ctx, batch.Owner); err == nil {
		t.Fatal("second migration must fail")
	}
	var version string
	if err := db.QueryRow("SELECT value FROM memory_schema_metadata WHERE key='schema_version'").Scan(&version); err != nil || version != HistorySchemaVersion {
		t.Fatalf("intermediate schema: %s %v", version, err)
	}
	var raw string
	if err := db.QueryRow("SELECT payload_json FROM session_history WHERE legacy_memory_id=?", record.MemoryID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	restored, err := decodeHistoryBatch([]byte(raw))
	if err != nil || !reflect.DeepEqual(restored.Legacy, &record) {
		t.Fatalf("phase82 incomplete: %+v %v", restored, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_second"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.BeginHistorySnapshot(ctx, batch.Owner)
	if err != nil {
		t.Fatal(err)
	}
	s.ReleaseHistorySnapshot(snap)
}
