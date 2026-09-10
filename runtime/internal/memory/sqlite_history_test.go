package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func historyTestDB(t *testing.T, root string, batch HistoryBatch) *sql.DB {
	t.Helper()
	path, err := SQLiteDatabasePath(root, batch.Owner.GameID, batch.Owner.WorldID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteHistoryPersistsIsolatesAndUsesStableSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	b := testHistoryBatch("first")
	first, err := s.AppendHistory(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.BeginHistorySnapshot(ctx, b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseHistorySnapshot(snap)
	b2 := testHistoryBatch("second")
	if _, err = s.AppendHistory(ctx, b2); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadHistorySnapshot(ctx, snap, 0, HistoryReadLimits{Records: 10, Bytes: 8 << 20})
	if err != nil || len(page.Sources) != 1 || page.Sources[0].ID != first.ID {
		t.Fatalf("snapshot: %+v %v", page, err)
	}
	restarted := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	got, err := restarted.ReadHistorySource(ctx, b.Owner, first.ID)
	if err != nil || got.Fingerprint != first.Fingerprint || got.Batch.Event.Facts[0].Text != b.Event.Facts[0].Text {
		t.Fatalf("restart: %+v %v", got, err)
	}
	for _, field := range []string{"game", "world", "entity"} {
		other := b.Owner
		switch field {
		case "game":
			other.GameID = "other"
		case "world":
			other.WorldID = "other"
		case "entity":
			other.EntityID = "other"
		}
		if _, err := restarted.ReadHistorySource(ctx, other, first.ID); !errors.Is(err, ErrHistoryNotFound) {
			t.Fatalf("%s isolation: %v", field, err)
		}
	}
}

func TestSQLiteHistoryRetriesDoNotRefreshAgeOrOverwrite(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir(), Now: func() time.Time { return now }}, HistoryLimits{})
	b := testHistoryBatch("t")
	first, err := s.AppendHistory(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	retry, err := s.AppendHistory(ctx, b)
	if err != nil || retry.ID != first.ID || retry.Sequence != first.Sequence || !retry.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	b.Event.Facts[0].Text = "changed"
	if _, err = s.AppendHistory(ctx, b); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestSQLiteHistoryMigratesLegacyPresenceAndAge(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := testHistoryBatch("legacy")
	legacy := Record{MemoryID: "memory-old", SessionKey: b.Owner, ProjectionKind: ProjectionKindSettledTurn, ProjectionVersion: ProjectionVersionRecentV2, SourceTurnID: b.TurnID, SourceEventID: b.Event.ID, EventType: b.Event.Type, CreatedAt: time.Unix(100, 0).UTC(), GameTime: &GameTimeSnapshot{PresentFields: gameTimeCalendarFields}, SourceContextFacts: b.Event.Facts}
	legacy.ProjectionBatchKey, _ = BuildProjectionBatchKey(b.Owner, b.TurnID, b.Event.ID, legacy.ProjectionKind, legacy.ProjectionVersion)
	old := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root, Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err := old.Append(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	snap, err := s.BeginHistorySnapshot(ctx, b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseHistorySnapshot(snap)
	p, err := s.ReadHistorySnapshot(ctx, snap, 0, HistoryReadLimits{Records: 10, Bytes: 8 << 20})
	if err != nil || len(p.Sources) != 1 {
		t.Fatalf("migration: %+v %v", p, err)
	}
	got := p.Sources[0]
	if got.LegacyMemoryID != legacy.MemoryID || !reflect.DeepEqual(got.Batch.Legacy, &legacy) || !got.CreatedAt.Equal(legacy.CreatedAt) {
		t.Fatalf("legacy mismatch: %+v", got)
	}
	db := historyTestDB(t, root, b)
	for _, table := range []string{"recent_records", "recent_projection_batches", "session_history"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: %d %v", table, count, err)
		}
	}
}

func TestSQLiteHistoryMigrationRollsBackSchemaAndData(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := testHistoryBatch("old")
	old := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root})
	if _, err := old.Recent(ctx, b.Owner, 1); err != nil {
		t.Fatal(err)
	}
	db := historyTestDB(t, root, b)
	if _, err := db.Exec("CREATE TRIGGER fail_upgrade BEFORE UPDATE ON memory_schema_metadata BEGIN SELECT RAISE(ABORT,'test migration failure'); END"); err != nil {
		t.Fatal(err)
	}
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	if _, err := s.BeginHistorySnapshot(ctx, b.Owner); err == nil {
		t.Fatal("migration succeeded")
	}
	var version string
	if err := db.QueryRow("SELECT value FROM memory_schema_metadata WHERE key='schema_version'").Scan(&version); err != nil || version != SQLiteSchemaVersion {
		t.Fatalf("version: %s %v", version, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='session_history'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial schema: %d %v", count, err)
	}
}

func TestSQLiteHistoryRetainsMoreThanLegacyCapacity(t *testing.T) {
	ctx := context.Background()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir(), MaxRecordsPerEntity: 1, MaxProjectionBatchesPerEntity: 1}, HistoryLimits{})
	var first HistorySource
	for i := 0; i < 101; i++ {
		r, err := s.AppendHistory(ctx, testHistoryBatch(fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = r
		}
	}
	if _, err := s.ReadHistorySource(ctx, first.Owner, first.ID); err != nil {
		t.Fatal(err)
	}
}
