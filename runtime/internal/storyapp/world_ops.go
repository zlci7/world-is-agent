package storyapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) ActivateWorld(ctx context.Context, worldID string, expectedRevision int64) (Status, error) {
	if err := a.activate(ctx, worldID, expectedRevision); err != nil {
		return Status{}, err
	}
	return a.Status(ctx)
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
	var operation SaveOperation
	var found bool
	row := a.appDB.QueryRowContext(ctx, `SELECT operation_id,request_key,source_world_id,target_world_id,target_name,status,error,created_at,updated_at FROM copy_operations WHERE request_key=?`, requestKey)
	operation, found, err = scanSaveOperation(row)
	if err != nil {
		return SaveOperation{}, err
	}
	if found {
		return operation, nil
	}
	sourcePath, status, err := a.worldRecord(ctx, sourceWorldID)
	if err != nil {
		return SaveOperation{}, err
	}
	if status != "ready" {
		return SaveOperation{}, ErrWorldNotReady
	}
	targetID := newID("world")
	operation = SaveOperation{OperationID: newID("copy"), RequestKey: requestKey, SourceWorldID: sourceWorldID, TargetWorldID: targetID, TargetName: name, Status: "copying", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	targetPath := a.worldPath(targetID)
	if _, err := a.appDB.ExecContext(ctx, `INSERT INTO copy_operations(operation_id,request_key,user_id,game_id,source_world_id,target_world_id,target_name,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, operation.OperationID, operation.RequestKey, a.userID, GameID, sourceWorldID, targetID, name, operation.Status, operation.CreatedAt.Format(time.RFC3339Nano), operation.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return SaveOperation{}, err
	}
	if _, err := a.appDB.ExecContext(ctx, `INSERT INTO worlds(user_id,game_id,world_id,name,path,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, a.userID, GameID, targetID, name, targetPath, "copying", operation.CreatedAt.Format(time.RFC3339Nano), operation.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return SaveOperation{}, err
	}
	world := a.worldRuntimeFor(sourceWorldID)
	world.mu.Lock()
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
		go a.waitAndCopy(operation, runID)
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

func (a *App) waitAndCopy(operation SaveOperation, runID string) {
	for i := 0; i < 600; i++ {
		ctx := context.Background()
		path, _, err := a.worldRecord(ctx, operation.SourceWorldID)
		if err != nil {
			return
		}
		store, err := openWorldDB(path)
		if err != nil {
			return
		}
		run, found, _ := readRun(ctx, store.db, runID)
		store.db.Close()
		if !found {
			return
		}
		if run.Status != "accepted" && run.Status != "running" {
			world := a.worldRuntimeFor(operation.SourceWorldID)
			world.mu.Lock()
			sourcePath, _, err := a.worldRecord(ctx, operation.SourceWorldID)
			if err == nil {
				source, openErr := openWorldDB(sourcePath)
				if openErr == nil {
					if run.Status == "completed" {
						err = a.performCopyLocked(ctx, operation, source)
					} else {
						err = ErrSaveFailed
					}
					source.db.Close()
				}
			}
			world.savePending = false
			world.pendingOperation = ""
			world.mu.Unlock()
			if err != nil {
				_, _ = a.failCopy(ctx, operation, err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (a *App) performCopyLocked(ctx context.Context, operation SaveOperation, source *worldStore) error {
	targetPath := a.worldPath(operation.TargetWorldID)
	if err := cloneWorld(ctx, source, targetPath, operation.TargetWorldID); err != nil {
		return err
	}
	now := nowText()
	if _, err := a.appDB.ExecContext(ctx, `UPDATE worlds SET status='ready',updated_at=? WHERE user_id=? AND world_id=?`, now, a.userID, operation.TargetWorldID); err != nil {
		return err
	}
	if _, err := a.appDB.ExecContext(ctx, `UPDATE copy_operations SET status='ready',error='',updated_at=? WHERE operation_id=?`, now, operation.OperationID); err != nil {
		return err
	}
	return nil
}

func (a *App) failCopy(ctx context.Context, operation SaveOperation, err error) (SaveOperation, error) {
	message := err.Error()
	if _, e := a.appDB.ExecContext(ctx, `UPDATE copy_operations SET status='failed',error=?,updated_at=? WHERE operation_id=?`, message, nowText(), operation.OperationID); e != nil {
		return SaveOperation{}, e
	}
	if _, e := a.appDB.ExecContext(ctx, `UPDATE worlds SET status='failed',updated_at=? WHERE user_id=? AND world_id=?`, nowText(), a.userID, operation.TargetWorldID); e != nil {
		return SaveOperation{}, e
	}
	operation.Status = "failed"
	operation.Error = message
	operation.UpdatedAt = time.Now().UTC()
	return operation, ErrSaveFailed
}

func (a *App) CopyOperation(ctx context.Context, operationID string) (SaveOperation, error) {
	operation, found, err := scanSaveOperation(a.appDB.QueryRowContext(ctx, `SELECT operation_id,request_key,source_world_id,target_world_id,target_name,status,error,created_at,updated_at FROM copy_operations WHERE operation_id=?`, operationID))
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

func (a *App) DeleteWorld(ctx context.Context, worldID string) error {
	active, _, err := a.activeWorldState(ctx)
	if err != nil {
		return err
	}
	if active == worldID {
		return ErrWorldBusy
	}
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return err
	}
	if status == "copying" {
		return ErrWorldBusy
	}
	if _, err := a.appDB.ExecContext(ctx, `DELETE FROM worlds WHERE user_id=? AND world_id=?`, a.userID, worldID); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Dir(path))
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

var _ = fmt.Sprintf
