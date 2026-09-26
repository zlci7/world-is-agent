package storyapp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) ActivateWorld(ctx context.Context, worldID string, expectedRevision int64, requestKeys ...string) (Status, error) {
	requestKey := ""
	if len(requestKeys) > 0 {
		requestKey = strings.TrimSpace(requestKeys[0])
	}
	a.activationMu.Lock()
	defer a.activationMu.Unlock()
	if requestKey == "" {
		requestKey = newID("activate")
	}
	requestHash := a.activationHash(worldID, expectedRevision)
	var existingHash, existingStatus string
	err := a.appDB.QueryRowContext(ctx, `SELECT request_hash,status FROM activation_operations WHERE user_id=? AND game_id=? AND request_key=?`, a.userID, GameID, requestKey).Scan(&existingHash, &existingStatus)
	if err == nil {
		if existingHash != requestHash {
			return Status{}, ErrIdempotencyConflict
		}
		if existingStatus == "completed" {
			return a.Status(ctx)
		}
		_, _ = a.appDB.ExecContext(ctx, `DELETE FROM activation_operations WHERE user_id=? AND game_id=? AND request_key=?`, a.userID, GameID, requestKey)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Status{}, err
	}
	now := nowText()
	if _, err := a.appDB.ExecContext(ctx, `INSERT INTO activation_operations(operation_id,request_key,user_id,game_id,target_world_id,expected_revision,request_hash,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, newID("activation"), requestKey, a.userID, GameID, worldID, expectedRevision, requestHash, "accepted", now, now); err != nil {
		return Status{}, err
	}
	if err := a.activate(ctx, worldID, expectedRevision); err != nil {
		_, _ = a.appDB.ExecContext(ctx, `DELETE FROM activation_operations WHERE user_id=? AND game_id=? AND request_key=?`, a.userID, GameID, requestKey)
		return Status{}, err
	}
	if _, err := a.appDB.ExecContext(ctx, `UPDATE activation_operations SET status='completed',updated_at=? WHERE user_id=? AND game_id=? AND request_key=?`, nowText(), a.userID, GameID, requestKey); err != nil {
		return Status{}, err
	}
	return a.Status(ctx)
}

func (a *App) activationHash(worldID string, expectedRevision int64) string {
	data, _ := json.Marshal(struct {
		WorldID         string `json:"world_id"`
		ExpectedVersion int64  `json:"expected_revision"`
	}{worldID, expectedRevision})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (a *App) activate(ctx context.Context, worldID string, expectedRevision int64) error {
	if _, status, err := a.worldRecord(ctx, worldID); err != nil {
		return err
	} else if status != "ready" {
		return ErrWorldNotReady
	}
	currentID, currentRevision, err := a.activeWorldStateExact(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		currentID = ""
		currentRevision = 0
	} else if err != nil {
		return err
	}
	if expectedRevision > 0 && expectedRevision != currentRevision {
		return ErrVersionConflict
	}
	if currentID == worldID {
		return nil
	}
	if currentID != "" {
		a.cancelWorldRuns(ctx, currentID)
	}
	_, err = a.appDB.ExecContext(ctx, `INSERT INTO user_play_state(user_id,active_world_id,active_revision) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET active_world_id=excluded.active_world_id,active_revision=excluded.active_revision`, a.userID, worldID, currentRevision+1)
	return err
}

func (a *App) cancelWorldRuns(ctx context.Context, worldID string) {
	path, _, err := a.worldRecord(ctx, worldID)
	if err == nil {
		if store, e := openWorldDB(path); e == nil {
			_, _ = store.db.ExecContext(ctx, `UPDATE runs SET cancel_requested=1,updated_at=? WHERE status IN ('accepted','running')`, nowText())
			_ = store.db.Close()
		}
	}
	a.runsMu.Lock()
	var active []*runRuntime
	for _, runtime := range a.runs {
		if runtime.WorldID == worldID {
			runtime.Cancel()
			active = append(active, runtime)
		}
	}
	a.runsMu.Unlock()
	deadline := time.After(5 * time.Second)
	for _, runtime := range active {
		select {
		case <-runtime.Done:
		case <-deadline:
			return
		}
	}
}

func (a *App) SaveAs(ctx context.Context, sourceWorldID, name, requestKey string, expectedRevision int64) (SaveOperation, error) {
	name = cleanText(name)
	if name == "" {
		name = "另一个暮灯镇存档"
	}
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		return SaveOperation{}, ErrInvalidRequest
	}
	a.copyMu.Lock()
	if a.closing {
		a.copyMu.Unlock()
		return SaveOperation{}, ErrWorldBusy
	}
	a.copyWG.Add(1)
	a.copyMu.Unlock()
	defer a.copyWG.Done()
	var operation SaveOperation
	var found bool
	var err error
	row := a.appDB.QueryRowContext(ctx, `SELECT operation_id,request_key,source_world_id,target_world_id,target_name,status,error,created_at,updated_at FROM copy_operations WHERE user_id=? AND game_id=? AND request_key=?`, a.userID, GameID, requestKey)
	operation, found, err = scanSaveOperation(row)
	if err != nil {
		return SaveOperation{}, err
	}
	if found {
		if operation.SourceWorldID != sourceWorldID || operation.TargetName != name {
			return SaveOperation{}, ErrIdempotencyConflict
		}
		return operation, nil
	}
	activeID, currentRevision, err := a.activeWorldStateExact(ctx)
	if err != nil {
		return SaveOperation{}, ErrVersionConflict
	}
	if activeID != sourceWorldID {
		return SaveOperation{}, ErrVersionConflict
	}
	if expectedRevision > 0 && expectedRevision != currentRevision {
		return SaveOperation{}, ErrVersionConflict
	}
	sourcePath, status, err := a.worldRecord(ctx, sourceWorldID)
	if err != nil {
		return SaveOperation{}, err
	}
	if status != "ready" {
		return SaveOperation{}, ErrWorldNotReady
	}
	world := a.worldRuntimeFor(sourceWorldID)
	world.mu.Lock()
	if world.savePending {
		world.mu.Unlock()
		return SaveOperation{}, ErrWorldBusy
	}
	targetID := newID("world")
	operation = SaveOperation{OperationID: newID("copy"), RequestKey: requestKey, SourceWorldID: sourceWorldID, TargetWorldID: targetID, TargetName: name, Status: "copying", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	targetPath := a.worldPath(targetID)
	tx, err := a.appDB.BeginTx(ctx, nil)
	if err != nil {
		world.mu.Unlock()
		return SaveOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO copy_operations(operation_id,request_key,user_id,game_id,source_world_id,target_world_id,target_name,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, operation.OperationID, operation.RequestKey, a.userID, GameID, sourceWorldID, targetID, name, operation.Status, operation.CreatedAt.Format(time.RFC3339Nano), operation.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		world.mu.Unlock()
		return SaveOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO worlds(user_id,game_id,world_id,name,path,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, a.userID, GameID, targetID, name, targetPath, "copying", operation.CreatedAt.Format(time.RFC3339Nano), operation.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		world.mu.Unlock()
		return SaveOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		world.mu.Unlock()
		return SaveOperation{}, err
	}
	store, openErr := openWorldDB(sourcePath)
	if openErr != nil {
		world.mu.Unlock()
		return a.failCopy(ctx, operation, openErr)
	}
	activeCount, countErr := countActiveRuns(ctx, store.db)
	if countErr != nil {
		store.db.Close()
		world.mu.Unlock()
		return a.failCopy(ctx, operation, countErr)
	}
	if activeCount > 0 {
		world.savePending = true
		world.pendingOperation = operation.OperationID
		var runID string
		_ = store.db.QueryRowContext(ctx, `SELECT run_id FROM runs WHERE status IN ('accepted','running') ORDER BY created_at LIMIT 1`).Scan(&runID)
		store.db.Close()
		world.mu.Unlock()
		a.copyWG.Add(1)
		go func() {
			defer a.copyWG.Done()
			a.waitAndCopy(a.copyCtx, operation, runID)
		}()
		return operation, nil
	}
	err = a.performCopyLocked(ctx, operation, store)
	_ = store.db.Close()
	world.mu.Unlock()
	if err != nil {
		return a.failCopy(ctx, operation, err)
	}
	return a.CopyOperation(ctx, operation.OperationID)
}

func (a *App) waitAndCopy(ctx context.Context, operation SaveOperation, runID string) {
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			a.clearSavePending(operation.SourceWorldID, operation.OperationID)
			_, _ = a.failCopy(context.Background(), operation, err)
			return
		}
		path, _, err := a.worldRecord(ctx, operation.SourceWorldID)
		if err != nil {
			a.clearSavePending(operation.SourceWorldID, operation.OperationID)
			_, _ = a.failCopy(ctx, operation, err)
			return
		}
		store, err := openWorldDB(path)
		if err != nil {
			a.clearSavePending(operation.SourceWorldID, operation.OperationID)
			_, _ = a.failCopy(ctx, operation, err)
			return
		}
		run, found, readErr := readRun(ctx, store.db, runID)
		store.db.Close()
		if readErr != nil || !found {
			a.clearSavePending(operation.SourceWorldID, operation.OperationID)
			if readErr == nil {
				readErr = ErrRunNotFound
			}
			_, _ = a.failCopy(ctx, operation, readErr)
			return
		}
		if run.Status != "accepted" && run.Status != "running" {
			world := a.worldRuntimeFor(operation.SourceWorldID)
			world.mu.Lock()
			if !world.savePending || world.pendingOperation != operation.OperationID {
				world.mu.Unlock()
				_, _ = a.failCopy(context.Background(), operation, ErrSaveFailed)
				return
			}
			sourcePath, _, err := a.worldRecord(ctx, operation.SourceWorldID)
			if err == nil {
				source, openErr := openWorldDB(sourcePath)
				if openErr == nil {
					if run.Status == "completed" {
						var messageHead int64
						messageHead, err = metaInt(ctx, source.db, "message_head")
						if err == nil && messageHead == run.MessageSeq {
							err = a.performCopyLocked(ctx, operation, source)
						} else if err == nil {
							err = ErrVersionConflict
						}
					} else {
						err = ErrSaveFailed
					}
					source.db.Close()
				}
			}
			if world.pendingOperation == operation.OperationID {
				world.savePending = false
				world.pendingOperation = ""
			}
			world.mu.Unlock()
			if err != nil {
				_, _ = a.failCopy(ctx, operation, err)
			}
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	a.clearSavePending(operation.SourceWorldID, operation.OperationID)
	_, _ = a.failCopy(context.Background(), operation, context.DeadlineExceeded)
}

func (a *App) clearSavePending(worldID, operationID string) {
	world := a.worldRuntimeFor(worldID)
	world.mu.Lock()
	if world.pendingOperation == operationID {
		world.savePending = false
		world.pendingOperation = ""
	}
	world.mu.Unlock()
}

func (a *App) performCopyLocked(ctx context.Context, operation SaveOperation, source *worldStore) error {
	targetPath := a.worldPath(operation.TargetWorldID)
	if err := cloneWorld(ctx, source, targetPath, operation.TargetWorldID, operation.TargetName); err != nil {
		return err
	}
	now := nowText()
	tx, err := a.appDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	worldResult, err := tx.ExecContext(ctx, `UPDATE worlds SET status='ready',updated_at=? WHERE user_id=? AND game_id=? AND world_id=? AND status='copying'`, now, a.userID, GameID, operation.TargetWorldID)
	if err != nil {
		return err
	}
	if affected, err := worldResult.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrSaveFailed
	}
	copyResult, err := tx.ExecContext(ctx, `UPDATE copy_operations SET status='ready',error='',updated_at=? WHERE operation_id=? AND status='copying'`, now, operation.OperationID)
	if err != nil {
		return err
	}
	if affected, err := copyResult.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrSaveFailed
	}
	return tx.Commit()
}

func (a *App) failCopy(ctx context.Context, operation SaveOperation, err error) (SaveOperation, error) {
	message := err.Error()
	if _, e := a.appDB.ExecContext(ctx, `UPDATE copy_operations SET status='failed',error=?,updated_at=? WHERE user_id=? AND game_id=? AND operation_id=?`, message, nowText(), a.userID, GameID, operation.OperationID); e != nil {
		return SaveOperation{}, e
	}
	if _, e := a.appDB.ExecContext(ctx, `UPDATE worlds SET status='failed',updated_at=? WHERE user_id=? AND game_id=? AND world_id=?`, nowText(), a.userID, GameID, operation.TargetWorldID); e != nil {
		return SaveOperation{}, e
	}
	operation.Status = "failed"
	operation.Error = message
	operation.UpdatedAt = time.Now().UTC()
	return operation, ErrSaveFailed
}

func (a *App) CopyOperation(ctx context.Context, operationID string) (SaveOperation, error) {
	operation, found, err := scanSaveOperation(a.appDB.QueryRowContext(ctx, `SELECT operation_id,request_key,source_world_id,target_world_id,target_name,status,error,created_at,updated_at FROM copy_operations WHERE user_id=? AND game_id=? AND operation_id=?`, a.userID, GameID, operationID))
	if err != nil {
		return SaveOperation{}, err
	}
	if !found {
		return SaveOperation{}, ErrWorldNotFound
	}
	return operation, nil
}

func scanSaveOperation(row interface{ Scan(...any) error }) (SaveOperation, bool, error) {
	var op SaveOperation
	var created, updated string
	err := row.Scan(&op.OperationID, &op.RequestKey, &op.SourceWorldID, &op.TargetWorldID, &op.TargetName, &op.Status, &op.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SaveOperation{}, false, nil
	}
	if err != nil {
		return SaveOperation{}, false, err
	}
	op.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	op.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return op, true, nil
}

func (a *App) DeleteWorld(ctx context.Context, worldID string, expectedRevision int64) error {
	a.activationMu.Lock()
	defer a.activationMu.Unlock()

	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return err
	}
	if status != "ready" {
		if status == "copying" || status == "deleting" {
			return ErrWorldBusy
		}
		return ErrWorldNotReady
	}
	world := a.worldRuntimeFor(worldID)
	world.mu.Lock()
	defer world.mu.Unlock()
	if world.savePending {
		return ErrWorldBusy
	}
	store, err := openWorldDB(path)
	if err != nil {
		return err
	}
	activeRuns, err := countActiveRuns(ctx, store.db)
	_ = store.db.Close()
	if err != nil {
		return err
	}
	if activeRuns > 0 {
		return ErrWorldBusy
	}

	activeID, activeRevision, err := a.activeWorldState(ctx)
	if err != nil {
		return err
	}
	if expectedRevision != activeRevision {
		return ErrVersionConflict
	}
	tx, err := a.appDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE worlds SET status='deleting',updated_at=? WHERE user_id=? AND game_id=? AND world_id=? AND status='ready'`, nowText(), a.userID, GameID, worldID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrWorldBusy
	}
	if activeID == worldID {
		result, err = tx.ExecContext(ctx, `UPDATE user_play_state SET active_world_id='',active_revision=? WHERE user_id=? AND active_world_id=? AND active_revision=?`, activeRevision+1, a.userID, worldID, activeRevision)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return err
			}
			return ErrVersionConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		_, _ = a.appDB.ExecContext(context.Background(), `UPDATE worlds SET status='ready',updated_at=? WHERE user_id=? AND game_id=? AND world_id=? AND status='deleting'`, nowText(), a.userID, GameID, worldID)
		return err
	}
	if _, err := a.appDB.ExecContext(ctx, `DELETE FROM worlds WHERE user_id=? AND game_id=? AND world_id=? AND status='deleting'`, a.userID, GameID, worldID); err != nil {
		return err
	}
	a.worldMu.Lock()
	delete(a.worlds, worldID)
	a.worldMu.Unlock()
	return nil
}

func (a *App) CurrentWorld(ctx context.Context) (WorldSummary, error) {
	id, _, err := a.activeWorldStateExact(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorldSummary{}, ErrWorldNotFound
		}
		return WorldSummary{}, err
	}
	return a.worldSummary(ctx, id)
}
