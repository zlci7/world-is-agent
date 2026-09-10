package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHistoryMigrationPreservesLegacyLargeNumbers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	batch := testHistoryBatch("legacy-precision")
	record := Record{MemoryID: "legacy-big-number", SessionKey: batch.Owner, ProjectionKind: ProjectionKindSettledTurn, ProjectionVersion: ProjectionVersionRecentV2, SourceTurnID: batch.TurnID, SourceEventID: batch.Event.ID, SourceContextFacts: []SourceContextFact{{Kind: "fact", Text: "original", Attributes: map[string]any{"number": json.Number("9007199254740993")}}}}
	record.ProjectionBatchKey, _ = BuildProjectionBatchKey(record.SessionKey, record.SourceTurnID, record.SourceEventID, record.ProjectionKind, record.ProjectionVersion)
	record.CreatedAt = time.Unix(100, 0).UTC()
	old := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root})
	if err := old.Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	snapshot, err := store.BeginHistorySnapshot(ctx, batch.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(ctx, snapshot, 0, HistoryReadLimits{Records: 1, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sources) != 1 {
		t.Fatalf("got %d migrated sources", len(page.Sources))
	}
	data, err := json.Marshal(page.Sources[0].Batch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "9007199254740993") || strings.Contains(string(data), "9007199254740992") {
		t.Fatalf("legacy numeric value changed: %s", data)
	}
}

func TestHistoryDefaultRawCapacityBoundary(t *testing.T) {
	ctx := context.Background()
	batch := testHistoryBatch("raw-cap")
	batch.Event.Facts[0].Text = ""
	base, _, _, err := CanonicalHistoryBatch(batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	cap := DefaultHistoryLimits().MaxBatchBytes
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			batch.Event.Facts[0].Text = strings.Repeat("a", cap-len(base)+delta)
			store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			source, err := store.AppendHistory(writeCtx, batch)
			if delta > 0 {
				if !errors.Is(err, ErrHistoryCapacity) {
					t.Fatalf("over capacity: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if source.Bytes != cap+delta {
				t.Fatalf("bytes=%d want=%d", source.Bytes, cap+delta)
			}
			readCtx, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			stored, err := store.ReadHistorySource(readCtx, batch.Owner, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Batch.Event.Facts[0].Text != batch.Event.Facts[0].Text {
				t.Fatal("original was truncated")
			}
		})
	}
}

func TestHistoryConcurrentFreshAndLegacyMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			batch := testHistoryBatch("concurrent")
			if legacy {
				if _, err := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root}).Recent(ctx, batch.Owner, 1); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 6)
			var group sync.WaitGroup
			for i := 0; i < 6; i++ {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					<-start
					store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
					b := testHistoryBatch(fmt.Sprintf("concurrent-%d", i))
					_, err := store.AppendHistory(ctx, b)
					results <- err
				}(i)
			}
			close(start)
			group.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestHistoryWriteLockWaitHonorsDeadline(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	batch := testHistoryBatch("lock")
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root, BusyTimeout: 5 * time.Second}, HistoryLimits{})
	if _, err := store.AppendHistory(ctx, batch); err != nil {
		t.Fatal(err)
	}
	db := historyTestDB(t, root, batch)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	writeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = store.AppendHistory(writeCtx, testHistoryBatch("blocked"))
	if err == nil {
		t.Fatal("write with occupied writer succeeded")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("write deadline ignored: %s", elapsed)
	}
}

func TestHistoryJSONRejectsTrailingValues(t *testing.T) {
	for _, input := range []string{`{"a":1} true`, `{"a":1} invalid`} {
		var value map[string]any
		if err := decodeHistoryJSON([]byte(input), &value); err == nil {
			t.Fatalf("invalid original accepted: %s", input)
		}
	}
}

func TestHistoryMigrationRejectsInconsistentLegacyCredential(t *testing.T) {
	for _, query := range []string{
		"UPDATE recent_projection_batches SET content_fingerprint='wrong'",
		"UPDATE recent_projection_batches SET entity_id='someone-else'",
		"UPDATE recent_projection_batches SET projection_version=999",
		"DELETE FROM recent_projection_batches",
		`UPDATE recent_records SET source_context_facts_json='[]'`,
	} {
		t.Run(query, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			batch := testHistoryBatch("legacy-invalid")
			record := Record{MemoryID: "original", SessionKey: batch.Owner, ProjectionKind: ProjectionKindSettledTurn, ProjectionVersion: ProjectionVersionRecentV2, SourceTurnID: batch.TurnID, SourceEventID: batch.Event.ID, SourceContextFacts: batch.Event.Facts, CreatedAt: time.Unix(100, 0).UTC()}
			record.ProjectionBatchKey, _ = BuildProjectionBatchKey(record.SessionKey, record.SourceTurnID, record.SourceEventID, record.ProjectionKind, record.ProjectionVersion)
			if err := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root}).Append(ctx, record); err != nil {
				t.Fatal(err)
			}
			db := historyTestDB(t, root, batch)
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
			if snapshot, err := store.BeginHistorySnapshot(ctx, batch.Owner); err == nil {
				store.ReleaseHistorySnapshot(snapshot)
				t.Fatal("inconsistent legacy provenance was published")
			}
			var version string
			if err := db.QueryRow("SELECT value FROM memory_schema_metadata WHERE key='schema_version'").Scan(&version); err != nil || version != SQLiteSchemaVersion {
				t.Fatalf("migration was partially published: %s %v", version, err)
			}
		})
	}
}
