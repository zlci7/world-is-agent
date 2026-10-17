package task

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type consoleCursor struct {
	Version                                  int `json:"v"`
	Instance, Game, World, Collection, State string
	WorkingSet                               ConsoleWorkingSet
	Entity, Task                             string
}

func (q ConsoleQuery) Validate(kind string) error {
	if strings.TrimSpace(q.InstanceID) == "" || strings.TrimSpace(q.GameID) == "" || q.Limit < 0 || q.Limit > 50 || len(q.Cursor) > 4096 {
		return ErrConsoleQuery
	}
	for _, value := range []string{q.InstanceID, q.GameID, q.WorldID, q.EntityID, q.TaskID} {
		if len(value) > 1024 {
			return ErrConsoleQuery
		}
	}
	if kind == "worlds" {
		if q.WorldID != "" || q.EntityID != "" || q.TaskID != "" || q.Collection != "" || q.State != "" {
			return ErrConsoleQuery
		}
		return nil
	}
	if strings.TrimSpace(q.WorldID) == "" {
		return ErrConsoleQuery
	}
	if kind == "detail" {
		if strings.TrimSpace(q.EntityID) == "" || strings.TrimSpace(q.TaskID) == "" || q.Cursor != "" || q.Collection != "" || q.State != "" {
			return ErrConsoleQuery
		}
		return nil
	}
	if q.Collection != "active" && q.Collection != "terminal" {
		return ErrConsoleQuery
	}
	if q.State != "" && (!State(q.State).Valid() || taskStateTerminal(State(q.State)) != (q.Collection == "terminal")) {
		return ErrConsoleQuery
	}
	if q.EntityID != "" || q.TaskID != "" {
		return ErrConsoleQuery
	}
	return nil
}

func (q ConsoleQuery) limit() int {
	if q.Limit == 0 {
		return 20
	}
	return q.Limit
}
func (q ConsoleQuery) cursor() (consoleCursor, error) {
	c := consoleCursor{Version: 1, Instance: q.InstanceID, Game: q.GameID, World: q.WorldID, Collection: q.Collection, State: q.State}
	if q.Cursor == "" {
		return c, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil {
		return c, ErrConsoleQuery
	}
	var stored consoleCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || stored.Version != 1 || stored.Instance != c.Instance || stored.Game != c.Game || stored.World != c.World || stored.Collection != c.Collection || stored.State != c.State || len(stored.Entity) > 1024 || len(stored.Task) > 1024 {
		return c, ErrConsoleQuery
	}
	return stored, nil
}
func encodeConsoleCursor(c consoleCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}
func consoleWorkingSet(head Head) ConsoleWorkingSet {
	return ConsoleWorkingSet{RunID: head.Binding.RunID, Generation: strconv.FormatUint(head.Binding.Generation, 10), CheckpointID: head.CheckpointID}
}
func consoleWorld(row worldHeadRow, barrierTimeout time.Duration) ConsoleWorld {
	w := ConsoleWorld{WorldID: row.Head.Binding.World.WorldID, WorkingSet: consoleWorkingSet(row.Head), Head: row.Head, Availability: "waiting_for_world"}
	if row.RecoveryRunID != "" || row.RecoveryError != "" || row.Head.Status == worldHeadStatusPaused && (row.Head.Reason == string(CodeClockMismatch) || row.Head.Reason == string(CodeClockRewound)) {
		w.Availability = "recovery_blocked"
		w.ReasonCode = row.Head.Reason
	}
	age := time.Now().UnixMilli() - row.BarrierPreparedAtUnixMS
	w.SaveBarrierActive = row.BarrierStatus == checkpointBarrierPrepared && row.BarrierPreparedAtUnixMS > 0 && age >= 0 && age < barrierTimeout.Milliseconds()
	return w
}

func (s *SQLiteStore) ConsoleWorlds(ctx context.Context, q ConsoleQuery) (ConsoleWorlds, error) {
	result := ConsoleWorlds{InstanceID: q.InstanceID, GameID: q.GameID, Supported: true, ObservedAt: time.Now().UTC(), Worlds: []ConsoleWorld{}}
	if err := q.Validate("worlds"); err != nil {
		return result, err
	}
	cursor, err := q.cursor()
	if err != nil {
		return result, err
	}
	query := strings.Replace(worldHeadSelectSQL, " WHERE game_id = ? AND world_id = ?", " WHERE game_id = ? AND world_id > ? ORDER BY world_id LIMIT ?", 1)
	query = strings.Replace(query, "head_json", "CASE WHEN length(head_json) <= 262144 THEN head_json END", 1)
	rows, err := s.db.QueryContext(ctx, query, q.GameID, cursor.Task, q.limit()+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanWorldHeadRow(rows)
		if err != nil {
			return result, err
		}
		if len(result.Worlds) == q.limit() {
			cursor.Task = result.Worlds[len(result.Worlds)-1].WorldID
			result.NextCursor = encodeConsoleCursor(cursor)
			break
		}
		result.Worlds = append(result.Worlds, consoleWorld(row, s.options.CheckpointBarrierTimeout))
	}
	return result, rows.Err()
}

// ConsoleTasks reads head, counts and the bounded page from one deferred read transaction.
// It uses the existing store connection and never invokes lifecycle or scheduler mutations.
func (s *SQLiteStore) ConsoleTasks(ctx context.Context, q ConsoleQuery, detail bool) (ConsolePage, error) {
	result := ConsolePage{InstanceID: q.InstanceID, GameID: q.GameID, WorldID: q.WorldID, Supported: true, ObservedAt: time.Now().UTC(), Items: []ConsoleTask{}, Counts: map[State]int{StateWaiting: 0, StateRunning: 0, StatePaused: 0, StateSucceeded: 0, StateFailed: 0, StateCancelled: 0}}
	kind := "tasks"
	if detail {
		kind = "detail"
	}
	if err := q.Validate(kind); err != nil {
		return result, err
	}
	cursor, err := q.cursor()
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	head, found, err := s.loadWorldHeadTx(ctx, tx, WorldKey{GameID: q.GameID, WorldID: q.WorldID})
	if err != nil {
		return result, err
	}
	if !found {
		return result, ErrTaskNotFound
	}
	result.World = consoleWorld(head, s.options.CheckpointBarrierTimeout)
	result.WorkingSet = result.World.WorkingSet
	if q.Cursor != "" && cursor.WorkingSet != result.WorkingSet {
		return result, ErrConsoleSnapshotChanged
	}
	cursor.WorkingSet = result.WorkingSet
	counts, err := tx.QueryContext(ctx, `SELECT state, COUNT(*) FROM tasks WHERE game_id=? AND world_id=? GROUP BY state`, q.GameID, q.WorldID)
	if err != nil {
		return result, err
	}
	for counts.Next() {
		var state State
		var n int
		if err = counts.Scan(&state, &n); err != nil {
			break
		}
		if !state.Valid() {
			err = ErrInvalidTaskSpec
			break
		}
		result.Counts[state] = n
	}
	scanErr := counts.Err()
	counts.Close()
	if err != nil {
		return result, err
	}
	if scanErr != nil {
		return result, scanErr
	}
	where := ` WHERE game_id=? AND world_id=?`
	args := []any{q.GameID, q.WorldID}
	if detail {
		where += ` AND entity_id=? AND task_id=?`
		args = append(args, q.EntityID, q.TaskID)
	} else {
		if q.Collection == "active" {
			where += ` AND state IN ('waiting','running','paused')`
		} else {
			where += ` AND state IN ('succeeded','failed','cancelled')`
		}
		if q.State != "" {
			where += ` AND state=?`
			args = append(args, q.State)
		}
		where += ` AND (entity_id,task_id) > (?,?)`
		args = append(args, cursor.Entity, cursor.Task)
	}
	where += ` ORDER BY entity_id,task_id LIMIT ?`
	limit := q.limit()
	if detail {
		limit = 1
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT entity_id,task_id,length(record_json)+length(create_response_json)+length(intent_history_json) FROM tasks`+where, args...)
	if err != nil {
		return result, err
	}
	type key struct{ entity, id string }
	keys := []key{}
	total := int64(0)
	for rows.Next() {
		var k key
		var size int64
		if err = rows.Scan(&k.entity, &k.id, &size); err != nil {
			break
		}
		if len(keys) == limit {
			last := keys[len(keys)-1]
			cursor.Entity, cursor.Task = last.entity, last.id
			result.NextCursor = encodeConsoleCursor(cursor)
			break
		}
		total += size
		if size < 0 || total > consoleReadBytes {
			err = ErrConsoleBudget
			break
		}
		keys = append(keys, k)
	}
	scanErr = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if scanErr != nil {
		return result, scanErr
	}
	if detail && len(keys) == 0 {
		return result, ErrTaskNotFound
	}
	for _, k := range keys {
		record, readErr := scanTaskRow(tx.QueryRowContext(ctx, taskSelectSQL+` WHERE game_id=? AND world_id=? AND entity_id=? AND task_id=?`, q.GameID, q.WorldID, k.entity, k.id))
		if readErr != nil {
			return result, readErr
		}
		result.Items = append(result.Items, projectConsoleTask(record, detail))
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
