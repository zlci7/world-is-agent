package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/session"
)

// These preimages freeze the Phase8.1 fingerprint wire format at
// 006bf845e334312961e53fd0ee9da593bfaa9256, independently of the current serializer.
const (
	phase81V1Preimage       = `{"projection_kind":"settled_turn","projection_version":1,"projection_batch_key":"[\"fixture-game\",\"fixture-world\",\"agent-v1\",\"turn-v1\",\"event-v1\",\"settled_turn\",1]","game_id":"fixture-game","world_id":"fixture-world","entity_id":"agent-v1","source_turn_id":"turn-v1","source_event_id":"event-v1","source_event_sequence":9007199254740993,"event_type":"user.message","game_time":{"Year":1,"Season":1,"Day":1,"Hour":10,"Minute":0,"Tick":42},"source_context_facts_json":[{"Kind":"user_statement","ActorEntityID":"player","TargetEntityID":"agent-v1","ScopeID":"local","Text":"I do not like rain. The code is 7319.","Label":"statement","Attributes":{"code":9007199254740993,"decimal":1.2500,"nested":{"enabled":false}}}],"outcomes_json":[{"ToolName":"speak","ToolArguments":{"message":"I will not water today.","steps":9007199254740993},"ActionStatus":"succeeded"}]}`
	phase81V2Preimage       = `{"projection_kind":"prior_successful_actions","projection_version":2,"projection_batch_key":"[\"fixture-game\",\"fixture-world\",\"agent-v2\",\"turn-v2\",\"event-v2\",\"prior_successful_actions\",2]","game_id":"fixture-game","world_id":"fixture-world","entity_id":"agent-v2","source_turn_id":"turn-v2","source_event_id":"event-v2","source_event_sequence":0,"event_type":"tick","game_time":{"Year":0,"Season":0,"Day":0,"Hour":0,"Minute":0,"Tick":0,"PresentFields":31},"source_context_facts_json":[],"outcomes_json":[{"ToolName":"move","ToolArguments":{"direction":"north"},"ActionStatus":"succeeded"}]}`
	phase81RetainedPreimage = `{"projection_kind":"settled_turn","projection_version":1,"projection_batch_key":"[\"fixture-game\",\"fixture-world\",\"agent-retained\",\"turn-pruned\",\"event-pruned\",\"settled_turn\",1]","game_id":"fixture-game","world_id":"fixture-world","entity_id":"agent-retained","source_turn_id":"turn-pruned","source_event_id":"event-pruned","source_event_sequence":7,"event_type":"user.message","game_time":null,"source_context_facts_json":null,"outcomes_json":null}`
)

type phase81FixtureRecord struct {
	memoryID  string
	createdAt int64
	lastSeen  int64
	preimage  string
}

func phase81FixtureRecords() []phase81FixtureRecord {
	return []phase81FixtureRecord{
		{"phase81-v1", 1700000000123456789, 1700000000987654321, phase81V1Preimage},
		{"phase81-v2", 1700000000123456790, 1700000000987654322, phase81V2Preimage},
		{"", 1700000000000000001, 1700000001000000000, phase81RetainedPreimage},
	}
}

type phase81FixturePayload struct {
	ProjectionKind         string          `json:"projection_kind"`
	ProjectionVersion      int             `json:"projection_version"`
	ProjectionBatchKey     string          `json:"projection_batch_key"`
	GameID                 string          `json:"game_id"`
	WorldID                string          `json:"world_id"`
	EntityID               string          `json:"entity_id"`
	SourceTurnID           string          `json:"source_turn_id"`
	SourceEventID          string          `json:"source_event_id"`
	SourceEventSequence    uint64          `json:"source_event_sequence"`
	EventType              string          `json:"event_type"`
	GameTime               json.RawMessage `json:"game_time"`
	SourceContextFactsJSON json.RawMessage `json:"source_context_facts_json"`
	OutcomesJSON           json.RawMessage `json:"outcomes_json"`
}

func (f phase81FixtureRecord) payload(t *testing.T) phase81FixturePayload {
	t.Helper()
	var payload phase81FixturePayload
	if err := json.Unmarshal([]byte(f.preimage), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func (p phase81FixturePayload) owner() session.AgentSessionKey {
	return session.AgentSessionKey{GameID: p.GameID, WorldID: p.WorldID, EntityID: p.EntityID}
}

func phase81FixtureSHA(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func openPhase81Fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("testdata", "phase8_1_recent_v1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, phase81FixtureSHA("fixture-game"), phase81FixtureSHA("fixture-world"), "memory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range phase81FixtureRecords() {
		payload := fixture.payload(t)
		var fingerprint string
		var createdAt, lastSeen int64
		if err := db.QueryRow(`SELECT content_fingerprint,created_at,last_seen_at
FROM recent_projection_batches WHERE projection_batch_key=?`, payload.ProjectionBatchKey).Scan(&fingerprint, &createdAt, &lastSeen); err != nil {
			t.Fatal(err)
		}
		if fingerprint != phase81FixtureSHA(fixture.preimage) || createdAt != fixture.createdAt || lastSeen != fixture.lastSeen {
			t.Fatalf("frozen credential differs from its independent preimage or age: %s", payload.EntityID)
		}
	}
	return root, db
}

func phase81FixtureState(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	queries := map[string]string{
		"schema":      "SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name",
		"version":     "SELECT value FROM memory_schema_metadata WHERE key='schema_version'",
		"metadata":    "SELECT key,value FROM memory_schema_metadata WHERE key<>'schema_version' ORDER BY key",
		"records":     "SELECT * FROM recent_records ORDER BY memory_id",
		"credentials": "SELECT * FROM recent_projection_batches ORDER BY projection_batch_key",
	}
	state := make(map[string][][]any, len(queries))
	for name, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			state[name] = append(state[name], values)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read fixture state %s: %v %v", name, err, closeErr)
		}
	}
	return state
}

func assertPhase81FixtureState(t *testing.T, db *sql.DB, before map[string][][]any, migrated bool) {
	t.Helper()
	after := phase81FixtureState(t, db)
	for _, name := range []string{"schema", "version", "metadata", "records", "credentials"} {
		want := before[name]
		if migrated {
			if name == "schema" {
				continue
			}
			if name == "version" {
				want = [][]any{{RetrievalSchemaVersion}}
			}
		}
		if !reflect.DeepEqual(after[name], want) {
			t.Fatalf("legacy %s changed unexpectedly (migrated=%t)", name, migrated)
		}
	}
}

func phase81FixtureSnapshot(t *testing.T, store *SQLiteHistoryStore, owner session.AgentSessionKey) HistorySnapshot {
	t.Helper()
	snapshot, err := store.BeginHistorySnapshot(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.ReleaseHistorySnapshot(snapshot) })
	return snapshot
}

func assertPhase81Imported(t *testing.T, db *sql.DB, store *SQLiteHistoryStore, fixtures []phase81FixtureRecord) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_history").Scan(&count); err != nil {
		t.Fatal(err)
	}
	wantCount := 0
	for _, fixture := range fixtures {
		if fixture.memoryID == "" {
			continue
		}
		wantCount++
		payload := fixture.payload(t)
		batchKey, err := json.Marshal([]any{payload.GameID, payload.WorldID, payload.EntityID, payload.ProjectionBatchKey, "legacy_recent", 1})
		if err != nil {
			t.Fatal(err)
		}
		id := "history_" + phase81FixtureSHA(string(batchKey))
		source, err := store.ReadHistorySource(context.Background(), payload.owner(), id)
		if err != nil {
			t.Fatal(err)
		}
		want := Record{
			MemoryID: fixture.memoryID, ProjectionKind: ProjectionKind(payload.ProjectionKind),
			ProjectionVersion: payload.ProjectionVersion, ProjectionBatchKey: payload.ProjectionBatchKey,
			SessionKey: payload.owner(), SourceTurnID: payload.SourceTurnID, SourceEventID: payload.SourceEventID,
			SourceEventSequence: payload.SourceEventSequence, EventType: payload.EventType,
			CreatedAt: time.Unix(0, fixture.createdAt).UTC(),
		}
		for _, field := range []struct {
			raw    json.RawMessage
			target any
		}{{payload.GameTime, &want.GameTime}, {payload.SourceContextFactsJSON, &want.SourceContextFacts}, {payload.OutcomesJSON, &want.Outcomes}} {
			decoder := json.NewDecoder(strings.NewReader(string(field.raw)))
			decoder.UseNumber()
			if err := decoder.Decode(field.target); err != nil {
				t.Fatal(err)
			}
		}
		if source.ID != id || source.Sequence != int64(wantCount) || source.Owner != want.SessionKey ||
			source.BatchKey != string(batchKey) || source.LegacyMemoryID != fixture.memoryID ||
			source.CreatedAt.UnixNano() != fixture.createdAt || source.Availability != "available" {
			t.Fatalf("legacy source identity or age changed: %s", fixture.memoryID)
		}
		if source.Batch == nil || !reflect.DeepEqual(source.Batch.Legacy, &want) {
			t.Fatalf("legacy fields differ from frozen raw JSON: %s", fixture.memoryID)
		}
		wantEvent := HistoryEvent{ID: want.SourceEventID, Type: want.EventType, Sequence: want.SourceEventSequence, GameTime: want.GameTime, Facts: want.SourceContextFacts}
		if source.Batch.Owner != want.SessionKey || source.Batch.Kind != "legacy_recent" || source.Batch.Version != 1 ||
			source.Batch.TurnID != want.SourceTurnID || !reflect.DeepEqual(source.Batch.Event, wantEvent) ||
			source.Batch.Terminal != (HistoryTerminal{Status: "legacy"}) || len(source.Batch.Steps) != 0 || len(source.Batch.Observations) != 0 {
			t.Fatalf("legacy provenance was changed or fabricated: %s", fixture.memoryID)
		}
		if !reflect.DeepEqual(source.Times, []HistoryTime{{Source: "legacy_event", Value: want.GameTime}}) {
			t.Fatalf("legacy time or presence changed: %s", fixture.memoryID)
		}
		var raw string
		var storedBytes int
		if err := db.QueryRow("SELECT payload_json,payload_bytes FROM session_history WHERE source_id=?", id).Scan(&raw, &storedBytes); err != nil {
			t.Fatal(err)
		}
		if source.Fingerprint != phase81FixtureSHA(raw) || source.Bytes != len(raw) || storedBytes != len(raw) {
			t.Fatalf("migrated source fingerprint or byte count differs: %s", fixture.memoryID)
		}
	}
	if count != wantCount {
		t.Fatalf("imported %d sources for %d legacy originals", count, wantCount)
	}
}

func TestHistoryPhase81FixtureMigration(t *testing.T) {
	root, db := openPhase81Fixture(t)
	fixtures := phase81FixtureRecords()
	before := phase81FixtureState(t, db)
	for attempt := 0; attempt < 2; attempt++ {
		store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
		for _, fixture := range fixtures {
			snapshot := phase81FixtureSnapshot(t, store, fixture.payload(t).owner())
			page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, HistoryReadLimits{Records: 10, Bytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.memoryID == "" {
				if snapshot.Watermark != 0 || len(page.Sources) != 0 {
					t.Fatal("retained credential fabricated an owner-scoped source")
				}
			} else if len(page.Sources) != 1 || page.Sources[0].LegacyMemoryID != fixture.memoryID {
				t.Fatalf("owner-scoped legacy source missing or mixed: %s", fixture.memoryID)
			}
			store.ReleaseHistorySnapshot(snapshot)
		}
		assertPhase81Imported(t, db, store, fixtures)
		assertPhase81FixtureState(t, db, before, true)
	}
}

func TestHistoryPhase81FixtureCredentialOnly(t *testing.T) {
	root, db := openPhase81Fixture(t)
	if _, err := db.Exec("DELETE FROM recent_records"); err != nil {
		t.Fatal(err)
	}
	before := phase81FixtureState(t, db)
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	snapshot := phase81FixtureSnapshot(t, store, phase81FixtureRecords()[2].payload(t).owner())
	page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, HistoryReadLimits{Records: 10, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Watermark != 0 || len(page.Sources) != 0 || page.Scanned != 0 || page.More {
		t.Fatal("credential-only database fabricated history")
	}
	assertPhase81Imported(t, db, store, nil)
	assertPhase81FixtureState(t, db, before, true)
}

func TestHistoryPhase81FixtureOversizedOriginal(t *testing.T) {
	root, db := openPhase81Fixture(t)
	fixtures := phase81FixtureRecords()
	payload := fixtures[0].payload(t)
	const sentence = "I do not like rain. "
	original := strings.Repeat(sentence, (8<<20)/len(sentence)+1) + "The final code is 7319."
	quoted, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	payload.SourceContextFactsJSON = json.RawMessage(fmt.Sprintf(`[{"Kind":"user_statement","ActorEntityID":"player","TargetEntityID":"agent-v1","ScopeID":"local","Text":%s,"Label":"statement","Attributes":{"code":9007199254740993,"decimal":1.2500,"nested":{"enabled":false}}}]`, quoted))
	preimage, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	fixtures[0].preimage = string(preimage)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE recent_records SET source_context_facts_json=? WHERE memory_id=?", string(payload.SourceContextFactsJSON), fixtures[0].memoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE recent_projection_batches SET content_fingerprint=? WHERE projection_batch_key=?", phase81FixtureSHA(string(preimage)), payload.ProjectionBatchKey); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := phase81FixtureState(t, db)
	// Size fidelity is independent of the deadline contract; the default 8 MiB
	// new-batch limit remains in force while this larger legacy source migrates.
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{ReadTimeoutMS: 30000, WriteTimeoutMS: 30000})
	phase81FixtureSnapshot(t, store, payload.owner())
	assertPhase81Imported(t, db, store, fixtures)
	assertPhase81FixtureState(t, db, before, true)
	var size int
	if err := db.QueryRow("SELECT payload_bytes FROM session_history WHERE legacy_memory_id=?", fixtures[0].memoryID).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if len(original) <= 8<<20 || size <= 8<<20 {
		t.Fatal("fixture did not exercise an original larger than 8 MiB")
	}
}

func TestHistoryPhase81FixtureMigrationFailureRollbackAndRetry(t *testing.T) {
	for _, stage := range []string{"second_original", "metadata_after_import"} {
		t.Run(stage, func(t *testing.T) {
			root, db := openPhase81Fixture(t)
			fixtures := phase81FixtureRecords()
			original := phase81FixtureState(t, db)
			var wantError string
			switch stage {
			case "second_original":
				// The older valid v1 row is imported before the v2 row is decoded.
				if _, err := db.Exec("UPDATE recent_records SET outcomes_json='[' WHERE memory_id=?", fixtures[1].memoryID); err != nil {
					t.Fatal(err)
				}
				wantError = "unmarshal outcomes"
			case "metadata_after_import":
				if _, err := db.Exec(`CREATE TRIGGER phase81_fail_publish
BEFORE UPDATE OF value ON memory_schema_metadata
WHEN OLD.key='schema_version' AND NEW.value='phase8_2_history_v1'
BEGIN
	SELECT CASE WHEN (SELECT COUNT(*) FROM session_history WHERE kind='legacy_recent')=2
		THEN RAISE(ABORT,'phase81 fixture: originals imported before metadata failure')
		ELSE RAISE(ABORT,'phase81 fixture: unexpected import count') END;
END`); err != nil {
					t.Fatal(err)
				}
				wantError = "phase81 fixture: originals imported before metadata failure"
			}
			injected := phase81FixtureState(t, db)
			store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
			snapshot, err := store.BeginHistorySnapshot(context.Background(), fixtures[0].payload(t).owner())
			if err == nil {
				store.ReleaseHistorySnapshot(snapshot)
				t.Fatal("migration ignored the injected failure")
			}
			if !strings.Contains(err.Error(), wantError) {
				t.Fatalf("migration failed before the targeted stage: %v", err)
			}
			assertPhase81FixtureState(t, db, injected, false)
			if stage == "second_original" {
				if _, err := db.Exec("UPDATE recent_records SET outcomes_json=? WHERE memory_id=?", string(fixtures[1].payload(t).OutcomesJSON), fixtures[1].memoryID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec("DROP TRIGGER phase81_fail_publish"); err != nil {
				t.Fatal(err)
			}
			assertPhase81FixtureState(t, db, original, false)
			phase81FixtureSnapshot(t, store, fixtures[0].payload(t).owner())
			assertPhase81Imported(t, db, store, fixtures)
			assertPhase81FixtureState(t, db, original, true)
		})
	}
}
