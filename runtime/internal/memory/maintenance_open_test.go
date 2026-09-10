package memory

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceWaitsForNormalInitialization(t *testing.T) {
	for _, stage := range []string{"fresh", "phase81", "phase82"} {
		for _, operation := range []string{"rebuild", "prune"} {
			t.Run(stage+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				batch := testHistoryBatch("maintenance-open")
				root := t.TempDir()
				var db *sql.DB
				var original HistorySource
				if stage == "phase82" {
					root, db, original = phase82Fixture(t)
				}
				store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
				if stage == "phase81" {
					if _, err := store.base.Recent(ctx, batch.Owner, 1); err != nil {
						t.Fatal(err)
					}
				}
				if db == nil {
					conn, closeConn, err := store.base.openRawConn(ctx, batch.Owner)
					if err != nil {
						t.Fatal(err)
					}
					_ = conn
					closeConn()
					db = historyTestDB(t, root, batch)
				}
				beforeSchema, err := readSQLiteSchema(ctx, db)
				if err != nil {
					t.Fatal(err)
				}
				var beforeMetadata map[string]string
				if stage != "fresh" {
					beforeMetadata, err = readSQLiteMetadata(ctx, db)
					if err != nil {
						t.Fatal(err)
					}
				}
				var beforeRows [][]any
				if stage == "phase82" {
					beforeRows = retrievalMigrationRows(t, db)
				}
				run := func() (HistoryMaintenanceResult, error) {
					if operation == "rebuild" {
						return store.RebuildHistoryIndex(ctx, HistoryRebuildRequest{Owner: batch.Owner})
					}
					return store.PruneHistory(ctx, HistoryPruneRequest{Owner: batch.Owner, RetentionDays: 1, CurrentTime: summaryTestTime(12)})
				}
				for attempt := 0; attempt < 2; attempt++ {
					result, err := run()
					if err == nil || !strings.Contains(err.Error(), "not_ready") {
						t.Errorf("uninitialized maintenance must return not_ready: %+v %v", result, err)
					}
					if result.Processed != 0 || result.Bytes != 0 || result.ReadSources != 0 || len(result.SourceIDs) != 0 {
						t.Errorf("uninitialized maintenance processed originals: %+v", result)
					}
				}
				afterSchema, err := readSQLiteSchema(ctx, db)
				if err != nil || !reflect.DeepEqual(afterSchema, beforeSchema) {
					t.Errorf("maintenance migrated schema: %v", err)
				}
				if stage != "fresh" {
					afterMetadata, err := readSQLiteMetadata(ctx, db)
					if err != nil || !reflect.DeepEqual(afterMetadata, beforeMetadata) {
						t.Errorf("maintenance changed metadata: %+v %v", afterMetadata, err)
					}
				}
				if stage == "phase82" && !reflect.DeepEqual(retrievalMigrationRows(t, db), beforeRows) {
					t.Error("maintenance changed phase82 originals")
				}
				snapshot, err := store.BeginHistorySnapshot(ctx, batch.Owner)
				if err != nil {
					t.Fatal(err)
				}
				store.ReleaseHistorySnapshot(snapshot)
				if _, err := run(); err != nil {
					t.Fatalf("maintenance did not recover after normal initialization: %v", err)
				}
				if original.ID != "" {
					got, err := store.ReadHistorySource(ctx, original.Owner, original.ID)
					if err != nil || !reflect.DeepEqual(got, original) {
						t.Fatalf("normal initialization changed original: %v", err)
					}
				}
				conn, closeConn, err := store.openMaintenanceConn(ctx, batch.Owner, DefaultHistoryMaintenanceLimits())
				if err != nil {
					t.Fatal(err)
				}
				defer closeConn()
				var busy int
				if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 100 {
					t.Fatalf("maintenance busy timeout=%d, want 100ms: %v", busy, err)
				}
			})
		}
	}
}

func TestMaintenanceOpenRejectsInvalidSchemaAndBinding(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  error
	}{
		{`UPDATE memory_schema_metadata SET value='other-world' WHERE key='world_id'`, ErrWorldBindingMismatch},
		{`UPDATE memory_schema_metadata SET value='other-game' WHERE key='game_id'`, ErrWorldBindingMismatch},
		{`UPDATE memory_schema_metadata SET value='future-version' WHERE key='schema_version'`, ErrSchemaMismatch},
		{`DROP TABLE history_terms`, ErrSchemaMismatch},
	} {
		t.Run(tc.query, func(t *testing.T) {
			store, sources, _ := pruneFixture(t)
			db := historyTestDB(t, store.base.options.Root, *sources[0].Batch)
			if _, err := db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			conn, closeConn, err := store.openMaintenanceConn(context.Background(), sources[0].Owner, DefaultHistoryMaintenanceLimits())
			if closeConn != nil {
				closeConn()
			}
			if !errors.Is(err, tc.want) || conn != nil {
				t.Fatalf("invalid maintenance connection: %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHistorySourceReadSizeCountsSelectedColumns(t *testing.T) {
	for _, pruned := range []bool{false, true} {
		name := "available"
		if pruned {
			name = "pruned"
		}
		t.Run(name, func(t *testing.T) {
			batch := testHistoryBatch("read-size")
			batch.Owner.GameID += "-\u4e2d"
			batch.Owner.WorldID += "-\u6587"
			batch.Owner.EntityID = strings.Repeat("agent", 8) + "-\u5b9e\u4f53"
			store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
			source, err := store.AppendHistory(context.Background(), batch)
			if err != nil {
				t.Fatal(err)
			}
			db := historyTestDB(t, store.base.options.Root, batch)
			if pruned {
				if _, err := db.Exec("UPDATE session_history SET payload_json=NULL,availability='pruned'"); err != nil {
					t.Fatal(err)
				}
			}
			want := maintenanceSelectedSourceBytes(t, db, source.ID)
			got, err := historySourceReadSize(context.Background(), db, source.Owner, source.ID)
			if err != nil || got != want {
				t.Fatalf("source read bytes=%d, selected columns=%d: %v", got, want, err)
			}
		})
	}
}

func maintenanceSelectedSourceBytes(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	rows, err := db.Query("SELECT "+historySourceColumns+" FROM session_history WHERE source_id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || !rows.Next() {
		t.Fatalf("source row unavailable: %v", err)
	}
	values := make([]any, len(columns))
	args := make([]any, len(columns))
	for i := range values {
		args[i] = &values[i]
	}
	if err := rows.Scan(args...); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, value := range values {
		switch v := value.(type) {
		case string:
			total += len(v)
		case int64:
			total += 8
		case nil:
		default:
			t.Fatalf("unexpected source column type %T", value)
		}
	}
	return total
}

func TestHistoryPruneRequiresFullSourceReadBudget(t *testing.T) {
	store, sources, created := pruneFixture(t)
	ctx := context.Background()
	req := pruneRequest(sources, created.Add(48*time.Hour))
	req.Limits.Sources = 1
	snapshot, err := store.BeginHistorySnapshot(ctx, sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := store.PruneHistory(ctx, req)
	store.ReleaseHistorySnapshot(snapshot)
	if err != nil || proof.ReadSources != 0 || proof.ReadBytes == 0 {
		t.Fatalf("protected proof read: %+v %v", proof, err)
	}
	db := historyTestDB(t, store.base.options.Root, *sources[0].Batch)
	req.Limits.Bytes = proof.ReadBytes + maintenanceSelectedSourceBytes(t, db, sources[0].ID) - 1
	result, err := store.PruneHistory(ctx, req)
	if err != nil || result.ReadSources != 0 || len(result.SourceIDs) != 0 || result.ReadBytes > req.Limits.Bytes {
		t.Fatalf("read an original beyond the remaining budget: %+v %v", result, err)
	}
	source, err := store.ReadHistorySource(ctx, sources[0].Owner, sources[0].ID)
	if err != nil || source.Availability != HistoryAvailable {
		t.Fatalf("over-budget original was deleted: %+v %v", source, err)
	}
}
