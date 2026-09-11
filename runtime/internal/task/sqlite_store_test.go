package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gameagent/runtime/internal/session"
)

func TestSQLiteStoreSchemaAndPragmas(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{
		Path:        filepath.Join(t.TempDir(), "tasks.sqlite"),
		BusyTimeout: 137 * time.Millisecond,
	})

	var foreignKeys, synchronous, busyTimeout int
	var journalMode string
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || strings.ToLower(journalMode) != "wal" || synchronous != 2 || busyTimeout != 137 {
		t.Fatalf("PRAGMAs = foreign_keys:%d journal_mode:%q synchronous:%d busy_timeout:%d", foreignKeys, journalMode, synchronous, busyTimeout)
	}
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	tables := sqliteObjectNames(t, store.db, "table")
	wantTables := []string{"task_checkpoints", "task_wakeups", "task_world_heads", "tasks"}
	if !reflect.DeepEqual(tables, wantTables) {
		t.Fatalf("tables = %v, want %v", tables, wantTables)
	}
	indexes := sqliteObjectNames(t, store.db, "index")
	for _, name := range []string{
		"idx_task_wakeups_claim_due",
		"idx_task_wakeups_due",
		"idx_task_wakeups_unconsumed_task",
		"idx_tasks_active_equivalence",
		"idx_tasks_create_call",
		"idx_tasks_owner_state",
	} {
		if !containsString(indexes, name) {
			t.Errorf("indexes %v missing %q", indexes, name)
		}
	}
	columns := sqliteTableColumnNames(t, store.db, "tasks")
	for _, name := range []string{"create_response_hash", "create_response_json", "intent_history_hash", "intent_history_json"} {
		if !containsString(columns, name) {
			t.Errorf("tasks columns %v missing %q", columns, name)
		}
	}
	headColumns := sqliteTableColumnNames(t, store.db, "task_world_heads")
	if !containsString(headColumns, "runtime_instance_id") {
		t.Errorf("task_world_heads columns %v missing runtime_instance_id", headColumns)
	}
	var equivalenceIndexSQL string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_tasks_active_equivalence'`).Scan(&equivalenceIndexSQL); err != nil {
		t.Fatal(err)
	}
	normalizedIndexSQL := strings.Join(strings.Fields(equivalenceIndexSQL), " ")
	for _, clause := range []string{
		"UNIQUE INDEX",
		"equivalence_key <> ''",
		"state IN ('waiting', 'running', 'paused')",
	} {
		if !strings.Contains(normalizedIndexSQL, clause) {
			t.Errorf("active-equivalence index %q missing %q", normalizedIndexSQL, clause)
		}
	}

	rows, err := store.db.Query("PRAGMA foreign_key_list(task_wakeups)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gotForeignKey := map[string]string{}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "tasks" {
			gotForeignKey[from] = to
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantForeignKey := map[string]string{"game_id": "game_id", "world_id": "world_id", "entity_id": "entity_id", "task_id": "task_id"}
	if !reflect.DeepEqual(gotForeignKey, wantForeignKey) {
		t.Fatalf("task_wakeups foreign key = %v, want %v", gotForeignKey, wantForeignKey)
	}
}

func TestSQLiteStoreReplacementConnectionsRetainConfiguration(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{
		Path:        filepath.Join(t.TempDir(), "tasks.sqlite"),
		BusyTimeout: 137 * time.Millisecond,
	})
	store.db.SetMaxIdleConns(0)

	var foreignKeys int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("replacement connection foreign_keys = %d, want 1", foreignKeys)
	}
	var synchronous int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("replacement connection synchronous = %d, want FULL (2)", synchronous)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 137 {
		t.Fatalf("replacement connection busy_timeout = %d, want 137", busyTimeout)
	}
}

func TestSQLiteStorePreCancelledOpenDoesNotCreateDirectoryOrHoldLock(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-created")
	path := filepath.Join(parent, "tasks.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store, err := OpenSQLiteStore(ctx, StoreOptions{Path: path})
	if store != nil {
		t.Fatalf("pre-cancelled OpenSQLiteStore() store = %v, want nil", store)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled OpenSQLiteStore() error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(parent); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("pre-cancelled OpenSQLiteStore() touched parent: %v", statErr)
	}

	reopened := openTaskTestStore(t, StoreOptions{Path: path})
	if reopened == nil {
		t.Fatal("OpenSQLiteStore() after pre-cancel returned nil")
	}
}

func TestTaskIsolationRoundTripsSameIDsAcrossWorlds(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	ownerA := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "actor"}
	ownerB := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-b", EntityID: "actor"}
	recordA, wakeA := taskStoreFixture(ownerA, "same-task", "wake-a")
	recordB, wakeB := taskStoreFixture(ownerB, "same-task", "wake-b")
	recordA.Progress = json.RawMessage(`{"world":"a"}`)
	recordB.Progress = json.RawMessage(`{"world":"b"}`)

	if err := store.insertTaskAndWake(context.Background(), recordA, wakeA); err != nil {
		t.Fatal(err)
	}
	if err := store.insertTaskAndWake(context.Background(), recordB, wakeB); err != nil {
		t.Fatal(err)
	}
	gotA, err := store.loadTask(context.Background(), ownerA, "same-task")
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := store.loadTask(context.Background(), ownerB, "same-task")
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA.Progress) != `{"world":"a"}` || string(gotB.Progress) != `{"world":"b"}` {
		t.Fatalf("isolated progress = %s / %s", gotA.Progress, gotB.Progress)
	}

	recordA2, wakeA2 := taskStoreFixture(ownerA, "task-2", "wake-a-2")
	if err := store.insertTaskAndWake(context.Background(), recordA2, wakeA2); err != nil {
		t.Fatal(err)
	}
	gotList, err := store.listTasks(context.Background(), ownerA)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{gotList[0].ID, gotList[1].ID}; !reflect.DeepEqual(got, []string{"same-task", "task-2"}) {
		t.Fatalf("list order = %v, want task_id order", got)
	}
	for _, task := range gotList {
		if task.Owner != ownerA {
			t.Fatalf("list leaked owner %+v", task.Owner)
		}
	}
}

func TestTaskIsolationWrongOwnerIsIndistinguishableAndSanitized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secret-database-directory")
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(dir, "secret-tasks.sqlite")})
	owner := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "actor"}
	record, wake := taskStoreFixture(owner, "secret-task", "wake-a")
	record.Progress = json.RawMessage(`{"opaque_payload":"secret-payload"}`)
	if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
		t.Fatal(err)
	}

	owners := []session.AgentSessionKey{
		{GameID: "other-game", WorldID: owner.WorldID, EntityID: owner.EntityID},
		{GameID: owner.GameID, WorldID: "other-world", EntityID: owner.EntityID},
		{GameID: owner.GameID, WorldID: owner.WorldID, EntityID: "other-entity"},
	}
	for _, wrong := range owners {
		_, err := store.loadTask(context.Background(), wrong, record.ID)
		if !errors.Is(err, ErrTaskNotFound) {
			t.Fatalf("loadTask(%+v) error = %v, want ErrTaskNotFound", wrong, err)
		}
		if got := err.Error(); got != "task_not_found" {
			t.Fatalf("wrong-owner error = %q, want stable classification", got)
		}
		assertTaskErrorSanitized(t, err, dir, "secret-task", "secret-payload", "SELECT", "record_json")
	}
}

func TestStoreTransactionCommitsTaskAndWakeTogether(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	owner := testOwner()
	record, wake := taskStoreFixture(owner, "task-atomic", "wake-atomic")
	if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
		t.Fatal(err)
	}
	if _, err := store.loadTask(context.Background(), owner, record.ID); err != nil {
		t.Fatal(err)
	}
	gotWake, err := store.loadWake(context.Background(), owner, wake.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotWake, wake) {
		t.Fatalf("wake = %+v, want %+v", gotWake, wake)
	}
}

func TestStoreTransactionRollsBackInjectedFailureAndCancellation(t *testing.T) {
	for _, tt := range []struct {
		name string
		hook func(context.CancelFunc) func(context.Context) error
	}{
		{name: "injected failure", hook: func(context.CancelFunc) func(context.Context) error {
			return func(context.Context) error { return errors.New("injected between task and wake") }
		}},
		{name: "context cancellation", hook: func(cancel context.CancelFunc) func(context.Context) error {
			return func(context.Context) error { cancel(); return nil }
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.testAfterTaskInsert = tt.hook(cancel)
			record, wake := taskStoreFixture(testOwner(), "task-rollback", "wake-rollback")
			if err := store.insertTaskAndWake(ctx, record, wake); err == nil {
				t.Fatal("insertTaskAndWake() succeeded, want rollback error")
			}
			store.testAfterTaskInsert = nil
			if got := scopedRowCount(t, store.db, "tasks", record.Owner); got != 0 {
				t.Fatalf("tasks count = %d, want 0", got)
			}
			if got := scopedRowCount(t, store.db, "task_wakeups", record.Owner); got != 0 {
				t.Fatalf("task_wakeups count = %d, want 0", got)
			}
		})
	}
}

func TestSQLiteStoreCloseThenReopenPreservesRowsAndReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	store := openTaskTestStoreWithoutCleanup(t, StoreOptions{Path: path})
	record, wake := taskStoreFixture(testOwner(), "task-durable", "wake-durable")
	if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
		t.Fatal(err)
	}

	const closers = 16
	var wg sync.WaitGroup
	errs := make(chan error, closers)
	for i := 0; i < closers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	}

	reopened := openTaskTestStore(t, StoreOptions{Path: path})
	got, err := reopened.loadTask(context.Background(), record.Owner, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("reopened task = %+v, want %+v", got, record)
	}
}

func TestStoreLimitsKeepOldRowsAtExactTaskAndWorldBoundaries(t *testing.T) {
	t.Run("task bytes", func(t *testing.T) {
		record, wake := taskStoreFixture(testOwner(), "task-exact", "wake-exact")
		exact := taskCreateStorageBytes(t, record)
		store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite"), MaxTaskBytes: exact})
		if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
			t.Fatalf("exact-boundary insert error = %v", err)
		}
		over, overWake := taskStoreFixture(testOwner(), "task-over", "wake-over")
		over.Progress = json.RawMessage(`{"padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)
		if taskCreateStorageBytes(t, over) <= exact {
			t.Fatal("over-limit fixture did not exceed exact boundary")
		}
		if err := store.insertTaskAndWake(context.Background(), over, overWake); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("over-limit insert error = %v, want ErrInvalidTaskSpec", err)
		}
		if got := scopedRowCount(t, store.db, "tasks", record.Owner); got != 1 {
			t.Fatalf("retained tasks = %d, want 1", got)
		}
	})

	t.Run("tasks per world", func(t *testing.T) {
		store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite"), MaxTasksPerWorld: 1})
		first, firstWake := taskStoreFixture(testOwner(), "task-first", "wake-first")
		if err := store.insertTaskAndWake(context.Background(), first, firstWake); err != nil {
			t.Fatal(err)
		}
		otherOwner := session.AgentSessionKey{GameID: first.Owner.GameID, WorldID: first.Owner.WorldID, EntityID: "other-entity"}
		second, secondWake := taskStoreFixture(otherOwner, "task-second", "wake-second")
		if err := store.insertTaskAndWake(context.Background(), second, secondWake); !errors.Is(err, ErrTaskCapacityExceeded) {
			t.Fatalf("capacity insert error = %v, want ErrTaskCapacityExceeded", err)
		}
		if got := worldTaskCount(t, store.db, WorldKey{GameID: first.Owner.GameID, WorldID: first.Owner.WorldID}); got != 1 {
			t.Fatalf("retained world tasks = %d, want 1", got)
		}
	})
}

func TestStoreLimitsKeepOldSnapshotsAtExactBoundary(t *testing.T) {
	snapshot := json.RawMessage(`{"large_integer":9007199254740993,"lexical_decimal":1.2300}`)
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite"), MaxSnapshotBytes: len(snapshot)})
	row := checkpointStoreFixture("checkpoint-exact", "save-exact", snapshot)
	if err := store.insertCheckpoint(context.Background(), row); err != nil {
		t.Fatalf("exact-boundary checkpoint error = %v", err)
	}
	over := checkpointStoreFixture("checkpoint-over", "save-over", append(append(json.RawMessage(nil), snapshot...), ' '))
	if err := store.insertCheckpoint(context.Background(), over); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("over-limit checkpoint error = %v, want ErrInvalidTaskSpec", err)
	}
	got, err := store.loadCheckpoint(context.Background(), row.World, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Snapshot) != string(snapshot) {
		t.Fatalf("snapshot = %q, want exact %q", got.Snapshot, snapshot)
	}
}

func TestSQLiteStorePreservesOpaqueJSONNumberLexemes(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	record, wake := taskStoreFixture(testOwner(), "task-json", "wake-json")
	record.Progress = json.RawMessage(`{"large_integer":9007199254740993,"lexical_decimal":1.2300}`)
	if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
		t.Fatal(err)
	}
	got, err := store.loadTask(context.Background(), record.Owner, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Progress) != string(record.Progress) {
		t.Fatalf("task opaque JSON = %s, want %s", got.Progress, record.Progress)
	}

	snapshot := json.RawMessage(`{"large_integer":9007199254740993,"lexical_decimal":1.2300}`)
	checkpoint := checkpointStoreFixture("checkpoint-json", "save-json", snapshot)
	if err := store.insertCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	gotCheckpoint, err := store.loadCheckpoint(context.Background(), checkpoint.World, checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCheckpoint.Snapshot) != string(snapshot) {
		t.Fatalf("checkpoint opaque JSON = %s, want %s", gotCheckpoint.Snapshot, snapshot)
	}
}

func TestSQLiteStoreRejectsMalformedJSONAndIndexDisagreement(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	owner := testOwner()
	malformed, malformedWake := taskStoreFixture(owner, "task-malformed", "wake-malformed")
	mismatch, mismatchWake := taskStoreFixture(owner, "task-mismatch", "wake-mismatch")
	if err := store.insertTaskAndWake(context.Background(), malformed, malformedWake); err != nil {
		t.Fatal(err)
	}
	if err := store.insertTaskAndWake(context.Background(), mismatch, mismatchWake); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET record_json = ? WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		`{"owner":`, owner.GameID, owner.WorldID, owner.EntityID, malformed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET state = ? WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		string(StatePaused), owner.GameID, owner.WorldID, owner.EntityID, mismatch.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{malformed.ID, mismatch.ID} {
		if _, err := store.loadTask(context.Background(), owner, id); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("load corrupted %q error = %v, want ErrInvalidTaskSpec", id, err)
		}
	}
}

func TestSQLiteStoreRejectsUnexpectedPersistentSchemaObjects(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		secret    string
	}{
		{
			name:      "trigger",
			statement: `CREATE TRIGGER secret_task_trigger AFTER INSERT ON tasks BEGIN SELECT 1; END`,
			secret:    "secret_task_trigger",
		},
		{
			name:      "view",
			statement: `CREATE VIEW secret_task_view AS SELECT record_json FROM tasks`,
			secret:    "secret_task_view",
		},
		{
			name:      "trigger resembling internal prefix",
			statement: `CREATE TRIGGER sqliteXsecret_task_trigger AFTER INSERT ON tasks BEGIN SELECT 1; END`,
			secret:    "sqliteXsecret_task_trigger",
		},
		{
			name:      "view resembling internal prefix",
			statement: `CREATE VIEW sqliteXsecret_task_view AS SELECT record_json FROM tasks`,
			secret:    "sqliteXsecret_task_view",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-schema.sqlite")
			seed := openTaskTestStoreWithoutCleanup(t, StoreOptions{Path: path})
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}

			raw, err := sql.Open("sqlite", taskSQLiteDSN(path, time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(tt.statement); err != nil {
				_ = raw.Close()
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: path})
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrTaskConflict) {
				t.Fatalf("OpenSQLiteStore() error = %v, want ErrTaskConflict", err)
			}
			assertTaskErrorSanitized(t, err, path, tt.statement, tt.secret, "record_json")
		})
	}
}

func TestSQLiteStoreRejectsPreIntentSchemaOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-intent-schema.sqlite")
	seed := openTaskTestStoreWithoutCleanup(t, StoreOptions{Path: path})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", taskSQLiteDSN(path, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`ALTER TABLE tasks RENAME COLUMN intent_history_hash TO legacy_intent_history_hash`); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: path})
	if got != nil {
		_ = got.Close()
	}
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrTaskConflict", err)
	}
	assertTaskErrorSanitized(t, err, path, "legacy_intent_history_hash", "ALTER TABLE")
}

func TestSQLiteStoreWorldHeadAndCheckpointRoundTripByWorld(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	head := worldHeadRow{
		Head: Head{
			Binding:      Binding{World: testWorld(), RunID: "run-a", Generation: 1},
			Clock:        Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 7},
			CheckpointID: "checkpoint-a",
			Status:       "ready",
			Reason:       "activated",
		},
		SaveRequestID:     "save-a",
		BarrierStatus:     "confirmed",
		RuntimeInstanceID: "runtime-test",
	}
	if err := store.putWorldHead(context.Background(), head); err != nil {
		t.Fatal(err)
	}
	gotHead, err := store.loadWorldHead(context.Background(), testWorld())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotHead, head) {
		t.Fatalf("world head = %+v, want %+v", gotHead, head)
	}
	if _, err := store.loadWorldHead(context.Background(), WorldKey{GameID: "fake-game", WorldID: "world-b"}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("wrong-world head error = %v, want ErrTaskNotFound", err)
	}

	checkpoint := checkpointStoreFixture("checkpoint-a", "save-a", json.RawMessage(`{"tasks":[]}`))
	if err := store.insertCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	gotCheckpoint, err := store.loadCheckpoint(context.Background(), checkpoint.World, checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCheckpoint, checkpoint) {
		t.Fatalf("checkpoint = %+v, want %+v", gotCheckpoint, checkpoint)
	}
	if _, err := store.loadCheckpoint(context.Background(), WorldKey{GameID: "fake-game", WorldID: "world-b"}, checkpoint.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("wrong-world checkpoint error = %v, want ErrTaskNotFound", err)
	}
}

func TestSQLiteStoreErrorsNeverExposeSensitiveInputs(t *testing.T) {
	secretDir := filepath.Join(t.TempDir(), "private-database-path")
	if err := os.WriteFile(secretDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: filepath.Join(secretDir, "tasks.sqlite")})
	if err == nil {
		t.Fatal("OpenSQLiteStore() succeeded with file as parent directory")
	}
	assertTaskErrorSanitized(t, err, secretDir, "CREATE TABLE", "PRAGMA", "tasks.sqlite")

	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	record, wake := taskStoreFixture(testOwner(), "task-secret", "wake-secret")
	record.Progress = json.RawMessage(`{"secret_payload":`)
	err = store.insertTaskAndWake(context.Background(), record, wake)
	if !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("invalid payload error = %v, want ErrInvalidTaskSpec", err)
	}
	assertTaskErrorSanitized(t, err, "secret_payload", "INSERT INTO", "record_json")
}

func openTaskTestStore(t *testing.T, options StoreOptions) *SQLiteStore {
	t.Helper()
	store := openTaskTestStoreWithoutCleanup(t, options)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func openTaskTestStoreWithoutCleanup(t *testing.T, options StoreOptions) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLiteStore(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	return store
}

func taskStoreFixture(owner session.AgentSessionKey, taskID, wakeID string) (Record, Wake) {
	record := testRecord()
	record.ID = taskID
	record.Owner = owner
	record.Spec.Source.EventID = "event-" + taskID
	record.Spec.Source.TurnID = "turn-" + taskID
	record.Spec.Source.CallID = "call-" + taskID
	record.Spec.EquivalenceKey = "equivalence-" + taskID
	record.Progress = json.RawMessage(`{"status":"scheduled"}`)
	wake := testWake()
	wake.ID = wakeID
	wake.TaskID = taskID
	wake.Owner = owner
	wake.Status = "pending"
	wake.ClaimID = ""
	wake.ClaimedBy = ""
	wake.Attempt = 0
	wake.RetryAfterUnixMS = 0
	return record, wake
}

func checkpointStoreFixture(id, saveRequestID string, snapshot json.RawMessage) checkpointRow {
	return checkpointRow{
		ID:            id,
		World:         testWorld(),
		SaveRequestID: saveRequestID,
		SchemaVersion: 1,
		Clock:         Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 7},
		Checksum:      "checksum-" + id,
		Snapshot:      snapshot,
	}
}

func sqliteObjectNames(t *testing.T, db *sql.DB, kind string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = ? AND name NOT LIKE 'sqlite_%' ORDER BY name`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func sqliteTableColumnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")") // table is a test-owned literal.
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func scopedRowCount(t *testing.T, db *sql.DB, table string, owner session.AgentSessionKey) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM " + table + " WHERE game_id = ? AND world_id = ? AND entity_id = ?" // table is a test-controlled literal.
	var count int
	if err := db.QueryRow(query, owner.GameID, owner.WorldID, owner.EntityID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func worldTaskCount(t *testing.T, db *sql.DB, world WorldKey) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE game_id = ? AND world_id = ?`, world.GameID, world.WorldID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mustTaskJSON(t *testing.T, record Record) []byte {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func taskCreateStorageBytes(t *testing.T, record Record) int {
	t.Helper()
	wakeAt := record.Spec.WakeAt
	response, err := json.Marshal(CreateResult{Task: Record{
		ID:                record.ID,
		Owner:             record.Owner,
		Spec:              record.Spec,
		State:             StateWaiting,
		Revision:          1,
		CreatedAtGameTick: record.CreatedAtGameTick,
		CreatedAtUnixMS:   record.CreatedAtUnixMS,
		NextWakeAt:        &wakeAt,
		Operations:        []Operation{},
		Evidence:          []Evidence{},
		Cleanup:           []Cleanup{},
	}, Created: true})
	if err != nil {
		t.Fatal(err)
	}
	recordJSON := mustTaskJSON(t, record)
	return len(recordJSON) + len(response) + len([]byte("[]")) + len(recordJSON) + intentTerminalStructuralReserve
}

func assertTaskErrorSanitized(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for current, depth := err, 0; current != nil; current, depth = errors.Unwrap(current), depth+1 {
		if depth > 16 {
			t.Fatal("error chain exceeded 16 entries")
		}
		for _, value := range forbidden {
			if strings.Contains(current.Error(), value) {
				t.Fatalf("error chain %q exposed %q", current.Error(), value)
			}
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
