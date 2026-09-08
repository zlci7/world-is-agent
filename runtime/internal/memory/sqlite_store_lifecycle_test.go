package memory_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"

	sqlite "modernc.org/sqlite"
)

func TestSQLiteRecentReadsWhileWorldWriterHoldsLock(t *testing.T) {
	store, key, _, path := sqliteLifecycleSetup(t, 10, 20)
	if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
		t.Fatal(err)
	}
	db := sqliteLifecycleDB(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE recent_projection_batches SET last_seen_at = last_seen_at + 1"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM recent_records").Scan(&count); err != nil {
		t.Fatalf("WAL SELECT control failed: %v", err)
	}
	got, err := store.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("WAL SELECT read %d rows, but Recent failed: %v", count, err)
	}
	assertMemoryIDs(t, got, []string{"lifecycle-1"})
}

func TestSQLiteRejectsExistingInvalidDatabaseWithoutChanges(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want error
	}{
		{"missing schema version", "DELETE FROM memory_schema_metadata WHERE key = 'schema_version'", memory.ErrSchemaMismatch},
		{"missing game binding", "DELETE FROM memory_schema_metadata WHERE key = 'game_id'", memory.ErrWorldBindingMismatch},
		{"missing world binding", "DELETE FROM memory_schema_metadata WHERE key = 'world_id'", memory.ErrWorldBindingMismatch},
		{"missing created at", "DELETE FROM memory_schema_metadata WHERE key = 'created_at'", memory.ErrSchemaMismatch},
		{"incompatible version", "UPDATE memory_schema_metadata SET value = 'future' WHERE key = 'schema_version'", memory.ErrSchemaMismatch},
		{"wrong game binding", "UPDATE memory_schema_metadata SET value = 'other' WHERE key = 'game_id'", memory.ErrWorldBindingMismatch},
		{"wrong world binding", "UPDATE memory_schema_metadata SET value = 'other' WHERE key = 'world_id'", memory.ErrWorldBindingMismatch},
		{"missing metadata table", "DROP TABLE memory_schema_metadata", memory.ErrSchemaMismatch},
		{"missing records table", "DROP TABLE recent_records", memory.ErrSchemaMismatch},
		{"missing batches table", "DROP TABLE recent_projection_batches", memory.ErrSchemaMismatch},
		{"missing records index", "DROP INDEX idx_recent_records_scope_created", memory.ErrSchemaMismatch},
		{"missing batches index", "DROP INDEX idx_recent_projection_batches_scope_seen", memory.ErrSchemaMismatch},
		{"missing column", "ALTER TABLE recent_records DROP COLUMN outcomes_json", memory.ErrSchemaMismatch},
	}
	for _, tc := range cases {
		for _, operation := range []string{"Recent", "Append"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				store, key, _, path := sqliteLifecycleSetup(t, 10, 20)
				if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
					t.Fatal(err)
				}
				db := sqliteLifecycleDB(t, path)
				sqliteLifecycleExec(t, db, "PRAGMA journal_mode = DELETE")
				sqliteLifecycleExec(t, db, tc.sql)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if operation == "Recent" {
					_, err = store.Recent(context.Background(), key, 10)
				} else {
					err = store.Append(context.Background(), sqliteLifecycleRecord(t, key, 2))
				}
				if !errors.Is(err, tc.want) {
					t.Errorf("%s error = %v, want %v", operation, err, tc.want)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Error("rejected database was modified")
				}
			})
		}
	}
}

func TestSQLiteInitializesEmptyDatabaseFiles(t *testing.T) {
	for _, materialized := range []bool{false, true} {
		t.Run(fmt.Sprintf("sqlite-header=%t", materialized), func(t *testing.T) {
			store, key, _, path := sqliteLifecycleSetup(t, 10, 20)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if materialized {
				db := sqliteLifecycleDB(t, path)
				sqliteLifecycleExec(t, db, "VACUUM")
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
				t.Fatal(err)
			}
			sqliteLifecycleAssertCounts(t, sqliteLifecycleDB(t, path), 1, 1)
		})
	}
}

func TestSQLiteInitializationPublishesSchemaAndMetadataAtomically(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			_, key, root, path := sqliteLifecycleSetup(t, 10, 20)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root, Now: func() time.Time {
				close(entered)
				<-release
				return time.Unix(100, 0)
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := store.Recent(ctx, key, 1)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("initialization never reached metadata")
			}
			db := sqliteLifecycleDB(t, path)
			var tables int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table'").Scan(&tables); err != nil {
				t.Fatal(err)
			}
			if tables != 0 {
				t.Errorf("uncommitted initialization exposed %d tables", tables)
			}
			if canceled {
				cancel()
			}
			unblock()
			if err := <-done; canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
				t.Fatalf("initialization error = %v, canceled=%t", err, canceled)
			}
			if canceled {
				if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table'").Scan(&tables); err != nil {
					t.Fatal(err)
				}
				if tables != 0 {
					t.Errorf("canceled initialization left %d tables", tables)
				}
			}
			store = memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
			if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
				t.Fatalf("Append after initialization: %v", err)
			}
			sqliteLifecycleAssertCounts(t, db, 1, 1)
		})
	}
}

func TestSQLiteDatabasePathPreservesURICharacters(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory # % & = space")
	key := session.AgentSessionKey{GameID: "game", WorldID: "world", EntityID: "entity"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
	if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
		t.Fatal(err)
	}
	path, err := memory.SQLiteDatabasePath(root, key.GameID, key.WorldID)
	if err != nil {
		t.Fatal(err)
	}
	sqliteLifecycleAssertCounts(t, sqliteLifecycleDB(t, path), 1, 1)
}

func TestSQLiteConcurrentFirstOpenAndAppends(t *testing.T) {
	for _, firstOpen := range []bool{true, false} {
		for _, equivalent := range []bool{true, false} {
			t.Run(fmt.Sprintf("first=%t/equivalent=%t", firstOpen, equivalent), func(t *testing.T) {
				_, key, root, path := sqliteLifecycleSetup(t, 10, 20)
				if !firstOpen {
					store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
					if _, err := store.Recent(context.Background(), key, 1); err != nil {
						t.Fatal(err)
					}
				}
				const workers = 8
				start := make(chan struct{})
				results := make(chan error, workers)
				for i := 0; i < workers; i++ {
					entityKey := key
					if !equivalent {
						entityKey.EntityID = fmt.Sprintf("entity-%d", i)
					}
					record := sqliteLifecycleRecord(t, entityKey, 1)
					record.MemoryID = fmt.Sprintf("worker-%d", i)
					go func() {
						<-start
						store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
						results <- store.Append(context.Background(), record)
					}()
				}
				close(start)
				for i := 0; i < workers; i++ {
					if err := <-results; err != nil {
						t.Error(err)
					}
				}
				want := workers
				if equivalent {
					want = 1
				}
				db := sqliteLifecycleDB(t, path)
				sqliteLifecycleAssertCounts(t, db, want, want)
				var metadataCount int
				if err := db.QueryRow("SELECT COUNT(*) FROM memory_schema_metadata").Scan(&metadataCount); err != nil {
					t.Fatal(err)
				}
				if metadataCount != 4 {
					t.Fatalf("metadata count = %d, want 4", metadataCount)
				}
			})
		}
	}
}

func TestSQLiteAppendWaitsForWriterWithBoundedTimeout(t *testing.T) {
	store, key, _, path := sqliteLifecycleSetup(t, 10, 20)
	if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
		t.Fatal(err)
	}
	db := sqliteLifecycleDB(t, path)
	sqliteLifecycleExec(t, db, "BEGIN IMMEDIATE")
	defer sqliteLifecycleExec(t, db, "ROLLBACK")
	start := time.Now()
	err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 2))
	elapsed := time.Since(start)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 5 {
		t.Fatalf("Append error = %v, want SQLITE_BUSY", err)
	}
	if elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("busy wait = %s, want bounded wait near 100ms", elapsed)
	}
	sqliteLifecycleAssertCounts(t, db, 1, 1)
}

func TestSQLiteInitializationWaitsForJournalReaderWithBoundedTimeout(t *testing.T) {
	store, key, _, path := sqliteLifecycleSetup(t, 10, 20)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db := sqliteLifecycleDB(t, path)
	sqliteLifecycleExec(t, db, "VACUUM")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var tables int
	if err := tx.QueryRow("SELECT COUNT(*) FROM sqlite_schema").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = store.Recent(context.Background(), key, 1)
	elapsed := time.Since(start)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 5 {
		t.Fatalf("Recent error = %v, want SQLITE_BUSY", err)
	}
	if elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("journal wait = %s, want bounded wait near 100ms", elapsed)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("failed initialization left %d schema objects", tables)
	}
	if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
		t.Fatalf("Append after reader released: %v", err)
	}
	sqliteLifecycleAssertCounts(t, db, 1, 1)
}

func TestSQLiteRetentionShrinksTotalCredentialsOnPrunedRetry(t *testing.T) {
	store, key, root, path := sqliteLifecycleSetup(t, 2, 3)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if err := store.Append(ctx, sqliteLifecycleRecord(t, key, i)); err != nil {
			t.Fatal(err)
		}
	}
	retry := sqliteLifecycleRecord(t, key, 1)
	retry.MemoryID = "retry"
	retry.CreatedAt = time.Unix(999, 0)
	if err := store.Append(ctx, retry); err != nil {
		t.Fatal(err)
	}
	store = memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root, MaxRecordsPerEntity: 2, MaxProjectionBatchesPerEntity: 2})
	if err := store.Append(ctx, retry); err != nil {
		t.Fatal(err)
	}
	sqliteLifecycleAssertCounts(t, sqliteLifecycleDB(t, path), 2, 2)
	got, err := store.Recent(ctx, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, got, []string{"lifecycle-2", "lifecycle-3"})
}

func TestSQLiteRetentionEvictsOldestUnreferencedCredentialsPerEntity(t *testing.T) {
	store, key, root, path := sqliteLifecycleSetup(t, 2, 5)
	ctx := context.Background()
	for _, entity := range []string{key.EntityID, "other-entity"} {
		entityKey := key
		entityKey.EntityID = entity
		for i := 1; i <= 5; i++ {
			record := sqliteLifecycleRecord(t, entityKey, i)
			record.MemoryID = entity + record.MemoryID
			if err := store.Append(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
	}
	db := sqliteLifecycleDB(t, path)
	for i := 1; i <= 5; i++ {
		record := sqliteLifecycleRecord(t, key, i)
		sqliteLifecycleExec(t, db, "UPDATE recent_projection_batches SET last_seen_at = ? WHERE projection_batch_key = ?", i, record.ProjectionBatchKey)
	}
	store = memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root, MaxRecordsPerEntity: 1, MaxProjectionBatchesPerEntity: 3})
	retry := sqliteLifecycleRecord(t, key, 1)
	retry.CreatedAt = time.Unix(999, 0)
	if err := store.Append(ctx, retry); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 0, 0, 1, 1} {
		var count int
		record := sqliteLifecycleRecord(t, key, i+1)
		if err := db.QueryRow("SELECT COUNT(*) FROM recent_projection_batches WHERE projection_batch_key = ?", record.ProjectionBatchKey).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("credential %d count = %d, want %d", i+1, count, want)
		}
	}
	sqliteLifecycleAssertCounts(t, db, 3, 8)
	got, err := store.Recent(ctx, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, got, []string{key.EntityID + "lifecycle-5"})
}

func TestSQLiteAppendRollsBackBothTablesOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
	}{
		{"batch insert", "BEFORE INSERT ON recent_projection_batches BEGIN SELECT RAISE(ABORT, 'injected batch failure'); END"},
		{"recent retention", "BEFORE DELETE ON recent_records BEGIN SELECT RAISE(ABORT, 'injected recent failure'); END"},
		{"credential retention", "BEFORE DELETE ON recent_projection_batches BEGIN SELECT RAISE(ABORT, 'injected credential failure'); END"},
		{"deferred foreign key commit", "AFTER INSERT ON recent_projection_batches BEGIN DELETE FROM recent_projection_batches WHERE projection_batch_key = NEW.projection_batch_key; END"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, key, _, path := sqliteLifecycleSetup(t, 1, 1)
			if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1)); err != nil {
				t.Fatal(err)
			}
			db := sqliteLifecycleDB(t, path)
			sqliteLifecycleExec(t, db, "CREATE TRIGGER fail_append "+tc.trigger)
			if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 2)); err == nil {
				t.Fatal("Append succeeded, want injected failure")
			}
			sqliteLifecycleAssertCounts(t, db, 1, 1)
			sqliteLifecycleExec(t, db, "BEGIN IMMEDIATE")
			sqliteLifecycleExec(t, db, "ROLLBACK")
			sqliteLifecycleExec(t, db, "DROP TRIGGER fail_append")
			got, err := store.Recent(context.Background(), key, 10)
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryIDs(t, got, []string{"lifecycle-1"})
			if err := store.Append(context.Background(), sqliteLifecycleRecord(t, key, 2)); err != nil {
				t.Fatalf("Append after rollback: %v", err)
			}
		})
	}
}

var sqliteLifecycleFunctionID atomic.Uint64

func TestSQLiteCancelMidTransactionRollsBackBeforeReturning(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	function := fmt.Sprintf("sqlite_cancel_gate_%d", sqliteLifecycleFunctionID.Add(1))
	if err := sqlite.RegisterScalarFunction(function, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	store, key, _, path := sqliteLifecycleSetup(t, 10, 20)
	if _, err := store.Recent(context.Background(), key, 1); err != nil {
		t.Fatal(err)
	}
	db := sqliteLifecycleDB(t, path)
	sqliteLifecycleExec(t, db, "CREATE TRIGGER cancel_gate BEFORE INSERT ON recent_projection_batches BEGIN SELECT "+function+"(); END")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	record := sqliteLifecycleRecord(t, key, 1)
	go func() { done <- store.Append(ctx, record) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transaction never reached gate")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Append returned while transaction was blocked: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Append error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Append did not finish rollback")
	}
	sqliteLifecycleAssertCounts(t, db, 0, 0)
	sqliteLifecycleExec(t, db, "BEGIN IMMEDIATE")
	sqliteLifecycleExec(t, db, "ROLLBACK")
}

func TestSQLiteInvalidCapacityFailsBeforeDiskAccess(t *testing.T) {
	for _, recordCap := range []int{0, 3} {
		t.Run(fmt.Sprintf("records=%d", recordCap), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "untouched")
			store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root, MaxRecordsPerEntity: recordCap, MaxProjectionBatchesPerEntity: 2})
			key := session.AgentSessionKey{GameID: "game", WorldID: "world", EntityID: "entity"}
			errs := []error{store.Append(context.Background(), sqliteLifecycleRecord(t, key, 1))}
			for _, limit := range []int{0, 1} {
				_, err := store.Recent(context.Background(), key, limit)
				errs = append(errs, err)
			}
			for _, err := range errs {
				if err == nil || !strings.Contains(err.Error(), "max_projection_batches_per_entity") || !strings.Contains(err.Error(), "max_records_per_entity") {
					t.Errorf("error = %v, want capacity validation error", err)
				}
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid options touched disk: %v", err)
			}
		})
	}
}

func sqliteLifecycleSetup(t *testing.T, recordCap, batchCap int) (*memory.SQLiteMemoryStore, session.AgentSessionKey, string, string) {
	t.Helper()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "game", WorldID: "world", EntityID: "entity"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root, BusyTimeout: 100 * time.Millisecond, MaxRecordsPerEntity: recordCap, MaxProjectionBatchesPerEntity: batchCap})
	path, err := memory.SQLiteDatabasePath(root, key.GameID, key.WorldID)
	if err != nil {
		t.Fatal(err)
	}
	return store, key, root, path
}

func sqliteLifecycleRecord(t *testing.T, key session.AgentSessionKey, index int) memory.Record {
	t.Helper()
	id := fmt.Sprintf("lifecycle-%d", index)
	return sqliteTestRecord(t, key, id, "turn-"+id, "event-"+id, memory.ProjectionKindSettledTurn, time.Unix(int64(index+100), 0))
}

func sqliteLifecycleDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqliteLifecycleExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func sqliteLifecycleAssertCounts(t *testing.T, db *sql.DB, wantRecords, wantBatches int) {
	t.Helper()
	var records, batches int
	if err := db.QueryRow("SELECT COUNT(*) FROM recent_records").Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM recent_projection_batches").Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if records != wantRecords || batches != wantBatches {
		t.Fatalf("records=%d batches=%d, want records=%d batches=%d", records, batches, wantRecords, wantBatches)
	}
}
