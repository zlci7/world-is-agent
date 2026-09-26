package storyapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type worldSnapshot struct {
	Summary       WorldSummary
	PlayerName    string
	PlayerProfile string
	Bystanders    []string
	SceneVersion  int64
	Characters    []Character
	Messages      []Message
	Events        []Event
	Perceptions   map[string][]Perception
	Memories      map[string][]Memory
}

type worldStore struct {
	path string
	db   *sql.DB
}

func openAppDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create app database directory: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA synchronous = FULL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(appSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openWorldDB(path string) (*worldStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create world database directory: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA synchronous = FULL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(worldSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureWorldSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &worldStore{path: path, db: db}, nil
}

func sqliteDSN(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: url.Values{
		"_busy_timeout": []string{"5000"},
		"_foreign_keys": []string{"on"},
		"_journal_mode": []string{"wal"},
		"_synchronous":  []string{"full"},
		"_txlock":       []string{"immediate"},
	}.Encode()}).String()
}

const appSchema = `
CREATE TABLE IF NOT EXISTS app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS worlds (
  user_id TEXT NOT NULL, game_id TEXT NOT NULL, world_id TEXT NOT NULL,
  name TEXT NOT NULL, path TEXT NOT NULL, status TEXT NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, game_id, world_id)
);
CREATE TABLE IF NOT EXISTS user_play_state (
  user_id TEXT PRIMARY KEY, active_world_id TEXT NOT NULL, active_revision INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS activation_operations (
  operation_id TEXT PRIMARY KEY, request_key TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL, game_id TEXT NOT NULL, target_world_id TEXT NOT NULL,
  expected_revision INTEGER NOT NULL, request_hash TEXT NOT NULL,
  status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS copy_operations (
  operation_id TEXT PRIMARY KEY, request_key TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL, game_id TEXT NOT NULL, source_world_id TEXT NOT NULL,
  target_world_id TEXT NOT NULL, target_name TEXT NOT NULL, status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_worlds_owner_updated ON worlds(user_id, updated_at DESC);
`

const worldSchema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS characters (
  entity_id TEXT PRIMARY KEY, definition_id TEXT NOT NULL, name TEXT NOT NULL,
  role TEXT NOT NULL, profile TEXT NOT NULL, knowledge TEXT NOT NULL, in_scene INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  seq INTEGER PRIMARY KEY, message_id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
  content TEXT NOT NULL, run_id TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY, event_id TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
  actor_id TEXT NOT NULL, target_id TEXT NOT NULL, content TEXT NOT NULL,
  run_id TEXT NOT NULL, stage INTEGER NOT NULL, scene_version INTEGER NOT NULL,
  source_type TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS perceptions (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, recipient_id TEXT NOT NULL,
  source_event_id TEXT NOT NULL, source_type TEXT NOT NULL, content TEXT NOT NULL,
  stage INTEGER NOT NULL, scene_version INTEGER NOT NULL, created_at TEXT NOT NULL,
  UNIQUE(recipient_id, source_event_id, content)
);
CREATE TABLE IF NOT EXISTS memories (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, recipient_id TEXT NOT NULL,
  kind TEXT NOT NULL, content TEXT NOT NULL, source_event_id TEXT NOT NULL,
  created_at TEXT NOT NULL, UNIQUE(recipient_id, kind, content, source_event_id)
);
CREATE TABLE IF NOT EXISTS runs (
  run_id TEXT PRIMARY KEY, request_key TEXT NOT NULL UNIQUE, request_hash TEXT NOT NULL,
  input TEXT NOT NULL, addressee_id TEXT NOT NULL, attempt INTEGER NOT NULL,
  status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
  message_seq INTEGER NOT NULL DEFAULT 0, cancel_requested INTEGER NOT NULL DEFAULT 0,
  base_turn_seq INTEGER NOT NULL DEFAULT 0, base_message_head INTEGER NOT NULL DEFAULT 0,
  base_event_head INTEGER NOT NULL DEFAULT 0, base_context_epoch INTEGER NOT NULL DEFAULT 0,
  base_scene_version INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_seq ON messages(seq);
CREATE INDEX IF NOT EXISTS idx_events_seq ON events(seq);
CREATE INDEX IF NOT EXISTS idx_perceptions_recipient_seq ON perceptions(recipient_id, seq);
CREATE INDEX IF NOT EXISTS idx_memories_recipient_seq ON memories(recipient_id, seq);
`

func ensureWorldSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for name := range map[string]struct{}{
		"base_turn_seq": {}, "base_message_head": {}, "base_event_head": {},
		"base_context_epoch": {}, "base_scene_version": {},
	} {
		if columns[name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE runs ADD COLUMN ` + name + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func metaGet(ctx context.Context, db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	return value, err
}

func metaGetTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	return value, err
}

func metaSetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func metaInt(ctx context.Context, db *sql.DB, key string) (int64, error) {
	value, err := metaGet(ctx, db, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid meta %s: %w", key, err)
	}
	return n, nil
}

func initializeWorld(ctx context.Context, store *worldStore, userID, worldID string, def gameDefinition, mode, playerName, playerProfile string) error {
	if mode != "open" && mode != "guided" {
		return ErrInvalidRequest
	}
	now := nowText()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	values := map[string]string{
		"schema_version": strconv.Itoa(SchemaVersion), "user_id": userID, "game_id": GameID,
		"world_id": worldID, "game_revision": "lantern-dusk.v1", "mode": mode,
		"turn_seq": "0", "message_head": "1", "event_head": "0", "context_epoch": "1",
		"scene_version": "1", "scene": def.Scene, "clock": def.Clock,
		"player_name": playerName, "player_profile": playerProfile, "status": "ready",
		"generation": "1", "plot_status": "active", "bystanders": marshalJSON(def.Bystanders),
	}
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, key, value); err != nil {
			return err
		}
	}
	for _, c := range def.Characters {
		if _, err := tx.ExecContext(ctx, `INSERT INTO characters(entity_id,definition_id,name,role,profile,knowledge,in_scene) VALUES(?,?,?,?,?,?,?)`, c.EntityID, c.DefinitionID, c.Name, c.Role, c.Profile, c.Knowledge, boolInt(c.InScene)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(seq,message_id,kind,content,run_id,created_at) VALUES(1,?,?,?,?,?)`, "opening", "narrative", def.Opening, "system", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(seq,event_id,event_type,actor_id,target_id,content,run_id,stage,scene_version,source_type,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, 1, "opening", "scene_opened", "system", "", def.Opening, "system", 0, 1, "definition", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value='1' WHERE key='event_head'`); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func loadWorldSnapshot(ctx context.Context, store *worldStore, limit int) (worldSnapshot, error) {
	var out worldSnapshot
	get := func(key string) (string, error) { return metaGet(ctx, store.db, key) }
	var err error
	out.Summary.GameID, err = get("game_id")
	if err != nil {
		return out, err
	}
	out.Summary.WorldID, err = get("world_id")
	if err != nil {
		return out, err
	}
	out.Summary.Mode, err = get("mode")
	if err != nil {
		return out, err
	}
	out.Summary.Scene, err = get("scene")
	if err != nil {
		return out, err
	}
	out.Summary.Clock, err = get("clock")
	if err != nil {
		return out, err
	}
	out.Summary.Status, err = get("status")
	if err != nil {
		return out, err
	}
	out.Summary.Name, err = get("name")
	if errors.Is(err, sql.ErrNoRows) {
		out.Summary.Name = "暮灯镇 · 新存档"
	} else if err != nil {
		return out, err
	}
	out.PlayerName, err = get("player_name")
	if err != nil {
		return out, err
	}
	out.PlayerProfile, err = get("player_profile")
	if err != nil {
		return out, err
	}
	for key, target := range map[string]*int64{"turn_seq": &out.Summary.TurnSeq, "message_head": &out.Summary.MessageHead, "event_head": &out.Summary.EventHead, "context_epoch": &out.Summary.ContextEpoch, "scene_version": &out.SceneVersion} {
		*target, err = metaInt(ctx, store.db, key)
		if err != nil {
			return out, err
		}
	}
	if value, e := get("updated_at"); e == nil {
		out.Summary.UpdatedAt, _ = time.Parse(time.RFC3339Nano, value)
	}
	if value, e := get("bystanders"); e == nil {
		_ = json.Unmarshal([]byte(value), &out.Bystanders)
	}
	if len(out.Bystanders) == 0 {
		out.Bystanders = append([]string(nil), lanternDefinition().Bystanders...)
	}
	out.Characters, err = loadCharacters(ctx, store.db)
	if err != nil {
		return out, err
	}
	out.Messages, err = loadMessages(ctx, store.db, limit)
	if err != nil {
		return out, err
	}
	out.Events, err = loadEvents(ctx, store.db, limit)
	if err != nil {
		return out, err
	}
	out.Perceptions = make(map[string][]Perception)
	out.Memories = make(map[string][]Memory)
	for _, c := range out.Characters {
		out.Perceptions[c.EntityID], err = loadPerceptions(ctx, store.db, c.EntityID, 20)
		if err != nil {
			return out, err
		}
		out.Memories[c.EntityID], err = loadMemories(ctx, store.db, c.EntityID, 20)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func loadCharacters(ctx context.Context, db *sql.DB) ([]Character, error) {
	rows, err := db.QueryContext(ctx, `SELECT entity_id,definition_id,name,role,profile,knowledge,in_scene FROM characters ORDER BY entity_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Character
	for rows.Next() {
		var c Character
		var in int
		if err := rows.Scan(&c.EntityID, &c.DefinitionID, &c.Name, &c.Role, &c.Profile, &c.Knowledge, &in); err != nil {
			return nil, err
		}
		c.InScene = in != 0
		result = append(result, c)
	}
	return result, rows.Err()
}

func loadMessages(ctx context.Context, db *sql.DB, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT seq,message_id,kind,content,run_id,created_at FROM messages ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Message
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.Seq, &m.MessageID, &m.Kind, &m.Content, &m.RunID, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, m)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, rows.Err()
}

func loadEvents(ctx context.Context, db *sql.DB, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT seq,event_id,event_type,actor_id,target_id,content,run_id,stage,scene_version,source_type,created_at FROM events ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Event
	for rows.Next() {
		var e Event
		var created string
		if err := rows.Scan(&e.Seq, &e.EventID, &e.EventType, &e.ActorID, &e.TargetID, &e.Content, &e.RunID, &e.Stage, &e.SceneVersion, &e.SourceType, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, e)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, rows.Err()
}

func loadPerceptions(ctx context.Context, db *sql.DB, recipient string, limit int) ([]Perception, error) {
	rows, err := db.QueryContext(ctx, `SELECT seq,recipient_id,source_event_id,source_type,content,stage,scene_version,created_at FROM perceptions WHERE recipient_id=? ORDER BY seq DESC LIMIT ?`, recipient, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Perception
	for rows.Next() {
		var p Perception
		var created string
		if err := rows.Scan(&p.Seq, &p.RecipientID, &p.SourceEventID, &p.SourceType, &p.Content, &p.Stage, &p.SceneVersion, &created); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, p)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, rows.Err()
}

func loadMemories(ctx context.Context, db *sql.DB, recipient string, limit int) ([]Memory, error) {
	rows, err := db.QueryContext(ctx, `SELECT seq,recipient_id,kind,content,source_event_id,created_at FROM memories WHERE recipient_id=? ORDER BY seq DESC LIMIT ?`, recipient, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Memory
	for rows.Next() {
		var m Memory
		var created string
		if err := rows.Scan(&m.Seq, &m.RecipientID, &m.Kind, &m.Content, &m.SourceEventID, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, m)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, rows.Err()
}

func scanRun(row interface{ Scan(...any) error }) (Run, bool, error) {
	var r Run
	var created, updated string
	var foundErr error
	foundErr = row.Scan(&r.RunID, &r.RequestKey, &r.RequestHash, &r.Input, &r.AddresseeID, &r.Attempt, &r.Status, &r.Reason, &r.Error, &r.MessageSeq, &r.BaseTurnSeq, &r.BaseMessageHead, &r.BaseEventHead, &r.BaseContextEpoch, &r.BaseSceneVersion, &created, &updated)
	if errors.Is(foundErr, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if foundErr != nil {
		return Run{}, false, foundErr
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return r, true, nil
}

func readRun(ctx context.Context, db *sql.DB, runID string) (Run, bool, error) {
	return scanRun(db.QueryRowContext(ctx, `SELECT run_id,request_key,request_hash,input,addressee_id,attempt,status,reason,error,message_seq,base_turn_seq,base_message_head,base_event_head,base_context_epoch,base_scene_version,created_at,updated_at FROM runs WHERE run_id=?`, runID))
}

func readRunByRequest(ctx context.Context, db *sql.DB, key string) (Run, bool, error) {
	return scanRun(db.QueryRowContext(ctx, `SELECT run_id,request_key,request_hash,input,addressee_id,attempt,status,reason,error,message_seq,base_turn_seq,base_message_head,base_event_head,base_context_epoch,base_scene_version,created_at,updated_at FROM runs WHERE request_key=?`, key))
}

func updateRunStatus(ctx context.Context, db *sql.DB, runID, status, reason, errorText string) error {
	_, err := db.ExecContext(ctx, `UPDATE runs SET status=?,reason=?,error=?,updated_at=? WHERE run_id=?`, status, reason, errorText, nowText(), runID)
	return err
}

func runCancelRequested(ctx context.Context, db *sql.DB, runID string) (bool, error) {
	var value int
	err := db.QueryRowContext(ctx, `SELECT cancel_requested FROM runs WHERE run_id=?`, runID).Scan(&value)
	return value != 0, err
}

func countActiveRuns(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE status IN ('accepted','running')`).Scan(&count)
	return count, err
}

func commitTurn(ctx context.Context, store *worldStore, run Run, narrative string, events []Event, perceptions []Perception, memories []Memory, clock, scene string, sceneVersion int64, sceneCharacters []string) (int64, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE run_id=?`, run.RunID).Scan(&status); err != nil {
		return 0, err
	}
	if status != "running" {
		return 0, ErrWorldBusy
	}
	var cancelled int
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runs WHERE run_id=?`, run.RunID).Scan(&cancelled); err != nil {
		return 0, err
	}
	if cancelled != 0 {
		return 0, context.Canceled
	}
	var messageHead, eventHead, turnSeq, currentSceneVersion, contextEpoch int64
	for key, target := range map[string]*int64{"message_head": &messageHead, "event_head": &eventHead, "turn_seq": &turnSeq, "scene_version": &currentSceneVersion, "context_epoch": &contextEpoch} {
		value, err := metaGetTx(ctx, tx, key)
		if err != nil {
			return 0, err
		}
		*target, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	if run.BaseMessageHead > 0 && (run.BaseTurnSeq != turnSeq || run.BaseMessageHead != messageHead || run.BaseEventHead != eventHead || run.BaseContextEpoch != contextEpoch || run.BaseSceneVersion != currentSceneVersion) {
		return 0, ErrVersionConflict
	}
	if sceneVersion < currentSceneVersion {
		return 0, ErrVersionConflict
	}
	for _, e := range events {
		eventHead++
		e.Seq = eventHead
		if _, err := tx.ExecContext(ctx, `INSERT INTO events(seq,event_id,event_type,actor_id,target_id,content,run_id,stage,scene_version,source_type,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, e.Seq, e.EventID, e.EventType, e.ActorID, e.TargetID, e.Content, e.RunID, e.Stage, e.SceneVersion, e.SourceType, e.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return 0, err
		}
	}
	for _, p := range perceptions {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO perceptions(recipient_id,source_event_id,source_type,content,stage,scene_version,created_at) VALUES(?,?,?,?,?,?,?)`, p.RecipientID, p.SourceEventID, p.SourceType, p.Content, p.Stage, p.SceneVersion, p.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return 0, err
		}
	}
	for _, m := range memories {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memories(recipient_id,kind,content,source_event_id,created_at) VALUES(?,?,?,?,?)`, m.RecipientID, m.Kind, m.Content, m.SourceEventID, m.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return 0, err
		}
	}
	messageHead++
	inputID := run.RunID + ":input"
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(seq,message_id,kind,content,run_id,created_at) VALUES(?,?,?,?,?,?)`, messageHead, inputID, "player", run.Input, run.RunID, nowText()); err != nil {
		return 0, err
	}
	messageHead++
	messageID := run.RunID + ":narrative"
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(seq,message_id,kind,content,run_id,created_at) VALUES(?,?,?,?,?,?)`, messageHead, messageID, "narrative", narrative, run.RunID, nowText()); err != nil {
		return 0, err
	}
	turnSeq++
	if err := metaSetTx(ctx, tx, "message_head", strconv.FormatInt(messageHead, 10)); err != nil {
		return 0, err
	}
	if err := metaSetTx(ctx, tx, "event_head", strconv.FormatInt(eventHead, 10)); err != nil {
		return 0, err
	}
	if err := metaSetTx(ctx, tx, "turn_seq", strconv.FormatInt(turnSeq, 10)); err != nil {
		return 0, err
	}
	if err := metaSetTx(ctx, tx, "clock", clock); err != nil {
		return 0, err
	}
	if scene == "" {
		scene, _ = metaGetTx(ctx, tx, "scene")
	}
	if err := metaSetTx(ctx, tx, "scene", scene); err != nil {
		return 0, err
	}
	if err := metaSetTx(ctx, tx, "scene_version", strconv.FormatInt(sceneVersion, 10)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE characters SET in_scene=0`); err != nil {
		return 0, err
	}
	for _, entityID := range sceneCharacters {
		result, err := tx.ExecContext(ctx, `UPDATE characters SET in_scene=1 WHERE entity_id=?`, entityID)
		if err != nil {
			return 0, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return 0, err
			}
			return 0, ErrGenerationFailed
		}
	}
	if err := metaSetTx(ctx, tx, "updated_at", nowText()); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='completed',reason='',error='',message_seq=?,updated_at=? WHERE run_id=? AND status='running' AND cancel_requested=0`, messageHead, nowText(), run.RunID)
	if err != nil {
		return 0, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return 0, err
		}
		return 0, ErrWorldBusy
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return messageHead, nil
}

func markRunInterrupted(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `UPDATE runs SET status='interrupted',reason='process_restarted',error='the previous process stopped before completion',updated_at=? WHERE status IN ('accepted','running')`, nowText())
	return err
}

func removeDir(path string) error { return os.RemoveAll(path) }

func cloneWorld(ctx context.Context, source *worldStore, targetPath, targetWorldID, targetName string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(targetPath)
	if _, err := source.db.ExecContext(ctx, `VACUUM INTO ?`, targetPath); err != nil {
		return err
	}
	target, err := openWorldDB(targetPath)
	if err != nil {
		return err
	}
	defer target.db.Close()
	tx, err := target.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := metaSetTx(ctx, tx, "world_id", targetWorldID); err != nil {
		return err
	}
	if err := metaSetTx(ctx, tx, "name", targetName); err != nil {
		return err
	}
	if err := metaSetTx(ctx, tx, "generation", "1"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func marshalJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
