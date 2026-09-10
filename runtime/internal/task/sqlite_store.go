package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gameagent/runtime/internal/session"

	_ "modernc.org/sqlite"
)

const taskSQLiteSchemaVersion = 1

type SQLiteStore struct {
	path    string
	options StoreOptions
	db      *sql.DB
	lock    *storeProcessLock

	closeOnce sync.Once
	closeErr  error

	// testAfterTaskInsert is the narrow fault seam required to prove that the
	// task and its first wake are one atomic write. Production never sets it.
	testAfterTaskInsert func(context.Context) error
}

type worldHeadRow struct {
	Head          Head   `json:"head"`
	SaveRequestID string `json:"save_request_id"`
	BarrierStatus string `json:"barrier_status"`
}

type checkpointRow struct {
	ID            string
	World         WorldKey
	SaveRequestID string
	SchemaVersion int
	Clock         Clock
	Checksum      string
	Snapshot      json.RawMessage
}

type taskRowColumns struct {
	gameID             string
	worldID            string
	entityID           string
	taskID             string
	state              string
	revision           int64
	clockID            string
	nextWakeAt         sql.NullInt64
	createEventID      string
	createTurnID       string
	createCallID       string
	createFingerprint  string
	equivalenceKey     string
	recordJSON         []byte
	createResponseJSON []byte
	createResponseHash string
}

type preparedTaskCreate struct {
	record             Record
	wake               Wake
	fingerprint        string
	recordJSON         []byte
	wakeJSON           []byte
	createResponseJSON []byte
	createResponseHash string
}

type rowScanner interface {
	Scan(...any) error
}

var taskSQLiteSchema = []struct {
	name      string
	statement string
}{
	{
		name: "task_world_heads",
		statement: `CREATE TABLE task_world_heads (
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK (generation BETWEEN 1 AND 9223372036854775807),
			clock_id TEXT NOT NULL,
			clock_tick INTEGER NOT NULL CHECK (clock_tick >= 0),
			clock_sequence INTEGER NOT NULL CHECK (clock_sequence BETWEEN 0 AND 9223372036854775807),
			checkpoint_id TEXT NOT NULL,
			save_request_id TEXT NOT NULL,
			barrier_status TEXT NOT NULL,
			status TEXT NOT NULL,
			reason TEXT NOT NULL,
			head_json BLOB NOT NULL,
			PRIMARY KEY (game_id, world_id)
		)`,
	},
	{
		name: "tasks",
		statement: `CREATE TABLE tasks (
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			state TEXT NOT NULL,
			revision INTEGER NOT NULL CHECK (revision BETWEEN 1 AND 9223372036854775807),
			clock_id TEXT NOT NULL,
			next_wake_at INTEGER CHECK (next_wake_at IS NULL OR next_wake_at >= 0),
			create_event_id TEXT NOT NULL,
			create_turn_id TEXT NOT NULL,
			create_call_id TEXT NOT NULL,
			create_fingerprint TEXT NOT NULL,
			equivalence_key TEXT NOT NULL,
			record_json BLOB NOT NULL,
			create_response_json BLOB NOT NULL,
			create_response_hash TEXT NOT NULL,
			PRIMARY KEY (game_id, world_id, entity_id, task_id)
		)`,
	},
	{
		name: "task_wakeups",
		statement: `CREATE TABLE task_wakeups (
			wake_id TEXT PRIMARY KEY,
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			clock_id TEXT NOT NULL,
			expected_revision INTEGER NOT NULL CHECK (expected_revision BETWEEN 1 AND 9223372036854775807),
			due_tick INTEGER NOT NULL CHECK (due_tick >= 0),
			reason TEXT NOT NULL,
			status TEXT NOT NULL,
			claim_id TEXT NOT NULL,
			claimed_by TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK (generation BETWEEN 1 AND 9223372036854775807),
			attempt INTEGER NOT NULL CHECK (attempt >= 0),
			retry_after_unix_ms INTEGER NOT NULL CHECK (retry_after_unix_ms >= 0),
			wake_json BLOB NOT NULL,
			UNIQUE (game_id, world_id, entity_id, task_id, expected_revision, reason),
			FOREIGN KEY (game_id, world_id, entity_id, task_id)
				REFERENCES tasks (game_id, world_id, entity_id, task_id)
				ON DELETE CASCADE
		)`,
	},
	{
		name: "task_checkpoints",
		statement: `CREATE TABLE task_checkpoints (
			checkpoint_id TEXT PRIMARY KEY,
			game_id TEXT NOT NULL,
			world_id TEXT NOT NULL,
			save_request_id TEXT NOT NULL,
			schema_version INTEGER NOT NULL CHECK (schema_version > 0),
			clock_id TEXT NOT NULL,
			clock_tick INTEGER NOT NULL CHECK (clock_tick >= 0),
			clock_sequence INTEGER NOT NULL CHECK (clock_sequence BETWEEN 0 AND 9223372036854775807),
			checksum TEXT NOT NULL,
			snapshot_json BLOB NOT NULL,
			UNIQUE (game_id, world_id, save_request_id)
		)`,
	},
	{
		name:      "idx_tasks_owner_state",
		statement: `CREATE INDEX idx_tasks_owner_state ON tasks (game_id, world_id, entity_id, state, task_id)`,
	},
	{
		name:      "idx_tasks_create_call",
		statement: `CREATE UNIQUE INDEX idx_tasks_create_call ON tasks (game_id, world_id, entity_id, create_event_id, create_turn_id, create_call_id)`,
	},
	{
		name: "idx_tasks_active_equivalence",
		statement: `CREATE UNIQUE INDEX idx_tasks_active_equivalence
			ON tasks (game_id, world_id, entity_id, equivalence_key)
			WHERE equivalence_key <> '' AND state IN ('waiting', 'running', 'paused')`,
	},
	{
		name:      "idx_task_wakeups_due",
		statement: `CREATE INDEX idx_task_wakeups_due ON task_wakeups (game_id, world_id, clock_id, due_tick, status, wake_id)`,
	},
}

func OpenSQLiteStore(ctx context.Context, options StoreOptions) (*SQLiteStore, error) {
	if ctx == nil {
		return nil, ErrInvalidTaskSpec
	}
	resolved, err := options.Resolve()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, WrapError(CodeTaskConflict, err)
	}
	canonicalPath, err := resolveSQLiteStorePath(resolved.Path)
	if err != nil {
		return nil, WrapError(CodeTaskConflict, err)
	}
	resolved.Path = canonicalPath
	lock, err := acquireStoreProcessLock(canonicalPath)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", taskSQLiteDSN(canonicalPath, resolved.BusyTimeout))
	if err != nil {
		_ = lock.release()
		return nil, WrapError(CodeTaskConflict, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)

	store := &SQLiteStore{path: canonicalPath, options: resolved, db: db, lock: lock}
	if err := store.prepare(ctx); err != nil {
		_ = db.Close()
		_ = lock.release()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		var dbErr, lockErr error
		if s.db != nil {
			dbErr = s.db.Close()
		}
		if s.lock != nil {
			lockErr = s.lock.release()
		}
		if err := errors.Join(dbErr, lockErr); err != nil {
			s.closeErr = WrapError(CodeTaskConflict, err)
		}
	})
	return s.closeErr
}

func (s *SQLiteStore) prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return WrapError(CodeTaskConflict, err)
	}
	busyMS := durationMillisecondsCeiling(s.options.BusyTimeout)
	for _, statement := range []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyMS),
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return WrapError(CodeTaskConflict, err)
		}
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return WrapError(CodeTaskConflict, err)
	}
	if strings.ToLower(journalMode) != "wal" {
		return ErrTaskConflict
	}
	return s.initializeSchema(ctx)
}

func (s *SQLiteStore) initializeSchema(ctx context.Context) error {
	return s.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		existing, err := schemaObjectSQL(ctx, tx)
		if err != nil {
			return err
		}
		if version == 0 && len(existing) == 0 {
			for _, object := range taskSQLiteSchema {
				if _, err := tx.ExecContext(ctx, object.statement); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", taskSQLiteSchemaVersion)); err != nil {
				return err
			}
			return nil
		}
		if version != taskSQLiteSchemaVersion {
			return ErrTaskConflict
		}
		if !schemaMatches(existing) {
			return ErrTaskConflict
		}
		return nil
	})
}

func (s *SQLiteStore) insertTaskAndWake(ctx context.Context, record Record, wake Wake) error {
	prepared, err := s.prepareTaskCreate(record, wake)
	if err != nil {
		return err
	}
	return s.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		return s.insertTaskAndWakeTx(ctx, tx, prepared)
	})
}

func (s *SQLiteStore) prepareTaskCreate(record Record, wake Wake) (preparedTaskCreate, error) {
	if s == nil || s.db == nil {
		return preparedTaskCreate{}, ErrTaskConflict
	}
	if err := validateTaskWakePair(record, wake); err != nil {
		return preparedTaskCreate{}, err
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return preparedTaskCreate{}, ErrInvalidTaskSpec
	}
	wakeJSON, err := json.Marshal(wake)
	if err != nil {
		return preparedTaskCreate{}, ErrInvalidTaskSpec
	}
	fingerprint, err := taskSpecFingerprint(record.Spec)
	if err != nil {
		return preparedTaskCreate{}, ErrInvalidTaskSpec
	}
	responseJSON, err := json.Marshal(initialCreateResult(record))
	if err != nil || len(recordJSON) > s.options.MaxTaskBytes || len(responseJSON) > s.options.MaxTaskBytes-len(recordJSON) {
		return preparedTaskCreate{}, ErrInvalidTaskSpec
	}
	return preparedTaskCreate{
		record:             record,
		wake:               wake,
		fingerprint:        fingerprint,
		recordJSON:         recordJSON,
		wakeJSON:           wakeJSON,
		createResponseJSON: responseJSON,
		createResponseHash: sha256Hex(responseJSON),
	}, nil
}

func initialCreateResult(record Record) CreateResult {
	wakeAt := record.Spec.WakeAt
	initial := Record{
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
	}
	return CreateResult{Task: initial, Created: true}
}

func (s *SQLiteStore) insertTaskAndWakeTx(ctx context.Context, tx *sql.Tx, prepared preparedTaskCreate) error {
	record, wake := prepared.record, prepared.wake
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE game_id = ? AND world_id = ?`, record.Owner.GameID, record.Owner.WorldID).Scan(&count); err != nil {
		return err
	}
	if count >= s.options.MaxTasksPerWorld {
		return ErrTaskCapacityExceeded
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (
		game_id, world_id, entity_id, task_id, state, revision, clock_id, next_wake_at,
		create_event_id, create_turn_id, create_call_id, create_fingerprint, equivalence_key,
		record_json, create_response_json, create_response_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID,
		string(record.State), int64(record.Revision), record.Spec.ClockID, nullableTick(record.NextWakeAt),
		record.Spec.Source.EventID, record.Spec.Source.TurnID, record.Spec.Source.CallID,
		prepared.fingerprint, record.Spec.EquivalenceKey, prepared.recordJSON,
		prepared.createResponseJSON, prepared.createResponseHash,
	); err != nil {
		return err
	}
	if s.testAfterTaskInsert != nil {
		if err := s.testAfterTaskInsert(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		record.Spec.ClockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
		wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS, prepared.wakeJSON,
	)
	return err
}

func (s *SQLiteStore) loadTask(ctx context.Context, owner session.AgentSessionKey, taskID string) (Record, error) {
	if err := validateTaskLookup(owner, taskID); err != nil {
		return Record{}, err
	}
	row := s.db.QueryRowContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID)
	record, err := scanTaskRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrTaskNotFound
	}
	if err != nil {
		return Record{}, classifyStoreError(err)
	}
	return record, nil
}

func (s *SQLiteStore) loadExactCreateTx(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, source SourceRef, fingerprint string) (CreateResult, bool, error) {
	var storedFingerprint string
	err := tx.QueryRowContext(ctx, `SELECT create_fingerprint FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ?
			AND create_event_id = ? AND create_turn_id = ? AND create_call_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, source.EventID, source.TurnID, source.CallID,
	).Scan(&storedFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateResult{}, false, nil
	}
	if err != nil {
		return CreateResult{}, false, err
	}
	if storedFingerprint != fingerprint {
		return CreateResult{}, true, ErrIdempotencyConflict
	}
	row := tx.QueryRowContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND entity_id = ?
		AND create_event_id = ? AND create_turn_id = ? AND create_call_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, source.EventID, source.TurnID, source.CallID)
	_, result, decodedFingerprint, err := scanTaskRowWithCreate(row)
	if err != nil {
		return CreateResult{}, true, err
	}
	if decodedFingerprint != fingerprint {
		return CreateResult{}, true, ErrInvalidTaskSpec
	}
	return result, true, nil
}

func (s *SQLiteStore) loadEquivalentTaskTx(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, equivalenceKey string) (Record, bool, error) {
	if equivalenceKey == "" {
		return Record{}, false, nil
	}
	row := tx.QueryRowContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND entity_id = ?
		AND equivalence_key = ? AND state IN (?, ?, ?)`,
		owner.GameID, owner.WorldID, owner.EntityID, equivalenceKey,
		string(StateWaiting), string(StateRunning), string(StatePaused))
	record, err := scanTaskRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (s *SQLiteStore) listTasks(ctx context.Context, owner session.AgentSessionKey) ([]Record, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND entity_id = ? ORDER BY task_id`,
		owner.GameID, owner.WorldID, owner.EntityID)
	if err != nil {
		return nil, classifyStoreError(err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		record, err := scanTaskRow(rows)
		if err != nil {
			return nil, classifyStoreError(err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyStoreError(err)
	}
	return records, nil
}

func (s *SQLiteStore) loadWake(ctx context.Context, owner session.AgentSessionKey, wakeID string) (Wake, error) {
	if err := validateOwner(owner); err != nil || !requiredIdentity(wakeID) {
		return Wake{}, ErrInvalidTaskSpec
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		w.wake_id, w.game_id, w.world_id, w.entity_id, w.task_id, w.clock_id,
		w.expected_revision, w.due_tick, w.reason, w.status, w.claim_id, w.claimed_by,
		w.generation, w.attempt, w.retry_after_unix_ms, w.wake_json, t.clock_id
		FROM task_wakeups w
		JOIN tasks t ON t.game_id = w.game_id AND t.world_id = w.world_id
			AND t.entity_id = w.entity_id AND t.task_id = w.task_id
		WHERE w.game_id = ? AND w.world_id = ? AND w.entity_id = ? AND w.wake_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, wakeID)
	var (
		indexed                      Wake
		clockID, taskClockID         string
		expectedRevision, generation int64
		wakeJSON                     []byte
	)
	indexed.Owner = owner
	if err := row.Scan(
		&indexed.ID, &indexed.Owner.GameID, &indexed.Owner.WorldID, &indexed.Owner.EntityID,
		&indexed.TaskID, &clockID, &expectedRevision, &indexed.DueTick, &indexed.Reason,
		&indexed.Status, &indexed.ClaimID, &indexed.ClaimedBy, &generation, &indexed.Attempt,
		&indexed.RetryAfterUnixMS, &wakeJSON, &taskClockID,
	); errors.Is(err, sql.ErrNoRows) {
		return Wake{}, ErrTaskNotFound
	} else if err != nil {
		return Wake{}, classifyStoreError(err)
	}
	if expectedRevision <= 0 || generation <= 0 {
		return Wake{}, ErrInvalidTaskSpec
	}
	indexed.ExpectedRevision = uint64(expectedRevision)
	indexed.Generation = uint64(generation)
	var stored Wake
	if err := json.Unmarshal(wakeJSON, &stored); err != nil || stored.Validate() != nil ||
		!wakeIndexedValuesEqual(stored, indexed) || clockID != taskClockID {
		return Wake{}, ErrInvalidTaskSpec
	}
	return stored, nil
}

func (s *SQLiteStore) putWorldHead(ctx context.Context, row worldHeadRow) error {
	if err := row.validate(); err != nil {
		return err
	}
	headJSON, err := json.Marshal(row)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	return s.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_world_heads (
			game_id, world_id, run_id, generation, clock_id, clock_tick, clock_sequence,
			checkpoint_id, save_request_id, barrier_status, status, reason, head_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (game_id, world_id) DO UPDATE SET
			run_id = excluded.run_id,
			generation = excluded.generation,
			clock_id = excluded.clock_id,
			clock_tick = excluded.clock_tick,
			clock_sequence = excluded.clock_sequence,
			checkpoint_id = excluded.checkpoint_id,
			save_request_id = excluded.save_request_id,
			barrier_status = excluded.barrier_status,
			status = excluded.status,
			reason = excluded.reason,
			head_json = excluded.head_json`,
			row.Head.Binding.World.GameID, row.Head.Binding.World.WorldID, row.Head.Binding.RunID,
			int64(row.Head.Binding.Generation), row.Head.Clock.ID, row.Head.Clock.Tick,
			int64(row.Head.Clock.Sequence), row.Head.CheckpointID, row.SaveRequestID,
			row.BarrierStatus, row.Head.Status, row.Head.Reason, headJSON,
		)
		return err
	})
}

func (s *SQLiteStore) loadWorldHead(ctx context.Context, world WorldKey) (worldHeadRow, error) {
	if err := world.Validate(); err != nil {
		return worldHeadRow{}, err
	}
	row, err := scanWorldHeadRow(s.db.QueryRowContext(ctx, worldHeadSelectSQL, world.GameID, world.WorldID))
	if errors.Is(err, sql.ErrNoRows) {
		return worldHeadRow{}, ErrTaskNotFound
	}
	if err != nil {
		return worldHeadRow{}, classifyStoreError(err)
	}
	return row, nil
}

func (s *SQLiteStore) loadWorldHeadTx(ctx context.Context, tx *sql.Tx, world WorldKey) (worldHeadRow, bool, error) {
	row, err := scanWorldHeadRow(tx.QueryRowContext(ctx, worldHeadSelectSQL, world.GameID, world.WorldID))
	if errors.Is(err, sql.ErrNoRows) {
		return worldHeadRow{}, false, nil
	}
	if err != nil {
		return worldHeadRow{}, false, err
	}
	return row, true, nil
}

func (s *SQLiteStore) worldHasDurableStateTx(ctx context.Context, tx *sql.Tx, world WorldKey) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN
		EXISTS (SELECT 1 FROM tasks WHERE game_id = ? AND world_id = ?) OR
		EXISTS (SELECT 1 FROM task_wakeups WHERE game_id = ? AND world_id = ?) OR
		EXISTS (SELECT 1 FROM task_checkpoints WHERE game_id = ? AND world_id = ?)
		THEN 1 ELSE 0 END`,
		world.GameID, world.WorldID,
		world.GameID, world.WorldID,
		world.GameID, world.WorldID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

const worldHeadSelectSQL = `SELECT game_id, world_id, run_id, generation,
	clock_id, clock_tick, clock_sequence, checkpoint_id, save_request_id,
	barrier_status, status, reason, head_json
	FROM task_world_heads WHERE game_id = ? AND world_id = ?`

func scanWorldHeadRow(scanner rowScanner) (worldHeadRow, error) {
	var (
		row                                                           worldHeadRow
		gameID, worldID, runID, clockID, checkpointID, status, reason string
		generation, clockSequence                                     int64
		clockTick                                                     int64
		headJSON                                                      []byte
	)
	err := scanner.Scan(
		&gameID, &worldID, &runID, &generation, &clockID, &clockTick, &clockSequence,
		&checkpointID, &row.SaveRequestID, &row.BarrierStatus, &status, &reason, &headJSON,
	)
	if err != nil {
		return worldHeadRow{}, err
	}
	if generation <= 0 || clockSequence < 0 {
		return worldHeadRow{}, ErrInvalidTaskSpec
	}
	var stored worldHeadRow
	if err := json.Unmarshal(headJSON, &stored); err != nil {
		return worldHeadRow{}, ErrInvalidTaskSpec
	}
	indexed := worldHeadRow{
		Head: Head{
			Binding:      Binding{World: WorldKey{GameID: gameID, WorldID: worldID}, RunID: runID, Generation: uint64(generation)},
			Clock:        Clock{ID: clockID, Tick: clockTick, Sequence: uint64(clockSequence)},
			CheckpointID: checkpointID,
			Status:       status,
			Reason:       reason,
		},
		SaveRequestID: row.SaveRequestID,
		BarrierStatus: row.BarrierStatus,
	}
	if stored != indexed || stored.validate() != nil {
		return worldHeadRow{}, ErrInvalidTaskSpec
	}
	return stored, nil
}

func (s *SQLiteStore) insertWorldHeadTx(ctx context.Context, tx *sql.Tx, row worldHeadRow, headJSON []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_world_heads (
		game_id, world_id, run_id, generation, clock_id, clock_tick, clock_sequence,
		checkpoint_id, save_request_id, barrier_status, status, reason, head_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.Head.Binding.World.GameID, row.Head.Binding.World.WorldID, row.Head.Binding.RunID,
		int64(row.Head.Binding.Generation), row.Head.Clock.ID, row.Head.Clock.Tick,
		int64(row.Head.Clock.Sequence), row.Head.CheckpointID, row.SaveRequestID,
		row.BarrierStatus, row.Head.Status, row.Head.Reason, headJSON,
	)
	return err
}

func (s *SQLiteStore) insertCheckpoint(ctx context.Context, row checkpointRow) error {
	if err := row.validate(); err != nil {
		return err
	}
	if len(row.Snapshot) > s.options.MaxSnapshotBytes {
		return ErrInvalidTaskSpec
	}
	snapshot := append([]byte(nil), row.Snapshot...)
	return s.withImmediateTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_checkpoints (
			checkpoint_id, game_id, world_id, save_request_id, schema_version,
			clock_id, clock_tick, clock_sequence, checksum, snapshot_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, row.World.GameID, row.World.WorldID, row.SaveRequestID, row.SchemaVersion,
			row.Clock.ID, row.Clock.Tick, int64(row.Clock.Sequence), row.Checksum, snapshot,
		)
		return err
	})
}

func (s *SQLiteStore) loadCheckpoint(ctx context.Context, world WorldKey, checkpointID string) (checkpointRow, error) {
	if err := world.Validate(); err != nil || !requiredIdentity(checkpointID) {
		return checkpointRow{}, ErrInvalidTaskSpec
	}
	var row checkpointRow
	var clockSequence int64
	row.World = world
	err := s.db.QueryRowContext(ctx, `SELECT checkpoint_id, game_id, world_id, save_request_id,
		schema_version, clock_id, clock_tick, clock_sequence, checksum, snapshot_json
		FROM task_checkpoints WHERE game_id = ? AND world_id = ? AND checkpoint_id = ?`,
		world.GameID, world.WorldID, checkpointID).Scan(
		&row.ID, &row.World.GameID, &row.World.WorldID, &row.SaveRequestID, &row.SchemaVersion,
		&row.Clock.ID, &row.Clock.Tick, &clockSequence, &row.Checksum, &row.Snapshot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpointRow{}, ErrTaskNotFound
	}
	if err != nil {
		return checkpointRow{}, classifyStoreError(err)
	}
	if clockSequence < 0 {
		return checkpointRow{}, ErrCheckpointInvalid
	}
	row.Clock.Sequence = uint64(clockSequence)
	if err := row.validate(); err != nil {
		return checkpointRow{}, ErrCheckpointInvalid
	}
	return row, nil
}

func (s *SQLiteStore) withImmediateTransaction(ctx context.Context, callback func(*sql.Tx) error) error {
	if ctx == nil || callback == nil {
		return ErrInvalidTaskSpec
	}
	if err := ctx.Err(); err != nil {
		return WrapError(CodeTaskConflict, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifyStoreError(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback()
		}
	}()
	if err := callback(tx); err != nil {
		_ = tx.Rollback()
		finished = true
		return classifyStoreError(err)
	}
	if err := ctx.Err(); err != nil {
		_ = tx.Rollback()
		finished = true
		return WrapError(CodeTaskConflict, err)
	}
	if err := tx.Commit(); err != nil {
		finished = true
		return classifyStoreError(err)
	}
	finished = true
	return nil
}

const taskSelectSQL = `SELECT game_id, world_id, entity_id, task_id, state, revision,
	clock_id, next_wake_at, create_event_id, create_turn_id, create_call_id,
	create_fingerprint, equivalence_key, record_json, create_response_json,
	create_response_hash FROM tasks`

func scanTaskRow(row rowScanner) (Record, error) {
	record, _, _, err := scanTaskRowWithCreate(row)
	return record, err
}

func scanTaskRowWithCreate(row rowScanner) (Record, CreateResult, string, error) {
	var columns taskRowColumns
	if err := row.Scan(
		&columns.gameID, &columns.worldID, &columns.entityID, &columns.taskID,
		&columns.state, &columns.revision, &columns.clockID, &columns.nextWakeAt,
		&columns.createEventID, &columns.createTurnID, &columns.createCallID,
		&columns.createFingerprint, &columns.equivalenceKey, &columns.recordJSON,
		&columns.createResponseJSON, &columns.createResponseHash,
	); err != nil {
		return Record{}, CreateResult{}, "", err
	}
	if columns.revision <= 0 {
		return Record{}, CreateResult{}, "", ErrInvalidTaskSpec
	}
	var record Record
	if err := json.Unmarshal(columns.recordJSON, &record); err != nil {
		return Record{}, CreateResult{}, "", ErrInvalidTaskSpec
	}
	if err := record.Validate(); err != nil {
		return Record{}, CreateResult{}, "", err
	}
	fingerprint, err := taskSpecFingerprint(record.Spec)
	if err != nil {
		return Record{}, CreateResult{}, "", ErrInvalidTaskSpec
	}
	if record.Owner != (session.AgentSessionKey{GameID: columns.gameID, WorldID: columns.worldID, EntityID: columns.entityID}) ||
		record.ID != columns.taskID || string(record.State) != columns.state ||
		record.Revision != uint64(columns.revision) || record.Spec.ClockID != columns.clockID ||
		!sameOptionalTick(record.NextWakeAt, columns.nextWakeAt) ||
		record.Spec.Source.EventID != columns.createEventID ||
		record.Spec.Source.TurnID != columns.createTurnID ||
		record.Spec.Source.CallID != columns.createCallID ||
		fingerprint != columns.createFingerprint || record.Spec.EquivalenceKey != columns.equivalenceKey {
		return Record{}, CreateResult{}, "", ErrInvalidTaskSpec
	}
	response, err := decodeCreateResponse(columns, record, columns.createFingerprint)
	if err != nil {
		return Record{}, CreateResult{}, "", err
	}
	return record, response, columns.createFingerprint, nil
}

func decodeCreateResponse(columns taskRowColumns, record Record, wantFingerprint string) (CreateResult, error) {
	if columns.createResponseHash != sha256Hex(columns.createResponseJSON) {
		return CreateResult{}, ErrInvalidTaskSpec
	}
	canonical := initialCreateResult(record)
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil || !bytes.Equal(columns.createResponseJSON, canonicalJSON) {
		return CreateResult{}, ErrInvalidTaskSpec
	}
	if err := canonical.Task.Validate(); err != nil {
		return CreateResult{}, ErrInvalidTaskSpec
	}
	fingerprint, err := taskSpecFingerprint(canonical.Task.Spec)
	if err != nil || fingerprint != wantFingerprint {
		return CreateResult{}, ErrInvalidTaskSpec
	}
	return canonical, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateTaskWakePair(record Record, wake Wake) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if err := wake.Validate(); err != nil {
		return err
	}
	if record.Owner != wake.Owner || record.ID != wake.TaskID || record.Revision != wake.ExpectedRevision {
		return ErrInvalidTaskSpec
	}
	return nil
}

func validateTaskLookup(owner session.AgentSessionKey, taskID string) error {
	if err := validateOwner(owner); err != nil || !requiredIdentity(taskID) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (r worldHeadRow) validate() error {
	if err := r.Head.Binding.Validate(); err != nil {
		return err
	}
	if err := r.Head.Clock.Validate(); err != nil {
		return err
	}
	if r.Head.Clock.Sequence > uint64(math.MaxInt64) ||
		!optionalIdentity(r.Head.CheckpointID) || !requiredIdentity(r.Head.Status) ||
		!optionalIdentity(r.Head.Reason) || !optionalIdentity(r.SaveRequestID) ||
		!optionalIdentity(r.BarrierStatus) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (r checkpointRow) validate() error {
	if !requiredIdentity(r.ID) || !requiredIdentity(r.SaveRequestID) || r.SchemaVersion <= 0 ||
		!requiredIdentity(r.Checksum) || len(r.Snapshot) == 0 || !json.Valid(r.Snapshot) {
		return ErrInvalidTaskSpec
	}
	if err := r.World.Validate(); err != nil {
		return err
	}
	if err := r.Clock.Validate(); err != nil {
		return err
	}
	if r.Clock.Sequence > uint64(math.MaxInt64) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func taskSpecFingerprint(spec TaskSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func nullableTick(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameOptionalTick(value *int64, stored sql.NullInt64) bool {
	if value == nil {
		return !stored.Valid
	}
	return stored.Valid && *value == stored.Int64
}

func wakeIndexedValuesEqual(stored, indexed Wake) bool {
	return stored.ID == indexed.ID && stored.TaskID == indexed.TaskID && stored.Owner == indexed.Owner &&
		stored.ExpectedRevision == indexed.ExpectedRevision && stored.DueTick == indexed.DueTick &&
		stored.Reason == indexed.Reason && stored.Status == indexed.Status && stored.ClaimID == indexed.ClaimID &&
		stored.ClaimedBy == indexed.ClaimedBy && stored.Generation == indexed.Generation &&
		stored.Attempt == indexed.Attempt && stored.RetryAfterUnixMS == indexed.RetryAfterUnixMS
}

func taskSQLiteDSN(path string, busyTimeout time.Duration) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{
		"_busy_timeout": {strconv.FormatInt(durationMillisecondsCeiling(busyTimeout), 10)},
		"_foreign_keys": {"on"},
		"_journal_mode": {"wal"},
		"_synchronous":  {"full"},
		"_txlock":       {"immediate"},
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String()
}

func durationMillisecondsCeiling(value time.Duration) int64 {
	milliseconds := value / time.Millisecond
	if value%time.Millisecond != 0 {
		milliseconds++
	}
	return int64(milliseconds)
}

func schemaObjectSQL(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema
		WHERE name NOT GLOB 'sqlite_*' AND sql IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make(map[string]string)
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			return nil, err
		}
		objects[name] = statement
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

func schemaMatches(existing map[string]string) bool {
	if len(existing) != len(taskSQLiteSchema) {
		return false
	}
	for _, object := range taskSQLiteSchema {
		if normalizeSQL(existing[object.name]) != normalizeSQL(object.statement) {
			return false
		}
	}
	return true
}

func normalizeSQL(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

func resolveSQLiteStorePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolutePath = filepath.Clean(absolutePath)
	if _, err := os.Lstat(absolutePath); err == nil {
		resolvedPath, err := filepath.EvalSymlinks(absolutePath)
		if err != nil {
			return "", err
		}
		resolvedPath, err = filepath.Abs(resolvedPath)
		if err != nil {
			return "", err
		}
		return filepath.Clean(resolvedPath), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	parent := filepath.Dir(absolutePath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	resolvedParent, err = filepath.Abs(resolvedParent)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(resolvedParent), filepath.Base(absolutePath)), nil
}

func classifyStoreError(err error) error {
	if err == nil {
		return nil
	}
	var taskErr *Error
	if errors.As(err, &taskErr) && taskErr != nil && taskErr.Code.Valid() {
		return WrapError(taskErr.Code, err)
	}
	return WrapError(CodeTaskConflict, err)
}
