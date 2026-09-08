package memory_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
)

func TestSQLiteMemoryStoreRestoresGameTimePresence(t *testing.T) {
	cases := []struct {
		name     string
		gameTime *protocolv1alpha2.GameTime
		want     memory.GameTimeBasis
	}{
		{name: "empty", gameTime: &protocolv1alpha2.GameTime{}, want: memory.GameTimeUnknown},
		{name: "zero-tick", gameTime: &protocolv1alpha2.GameTime{Tick: ptrInt64(0)}, want: memory.GameTimeTick},
		{name: "zero-calendar", gameTime: &protocolv1alpha2.GameTime{Year: ptrInt32(0), Season: ptrInt32(0), Day: ptrInt32(0), Hour: ptrInt32(0), Minute: ptrInt32(0)}, want: memory.GameTimeCalendar},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world", EntityID: "entity"}
			record, err := memory.NewProjector(time.Now).Project(memory.ProjectInput{
				SessionKey: key, TurnID: "turn", ProjectionKind: memory.ProjectionKindSettledTurn,
				Event: &protocolv1alpha2.GameEvent{EventId: "event", GameTime: tc.gameTime},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root}).Append(ctx, record); err != nil {
				t.Fatal(err)
			}
			reopened := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
			restored, err := reopened.Recent(ctx, key, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(restored) != 1 {
				t.Fatalf("restored record count=%d, want 1", len(restored))
			}
			if got := memory.SharedGameTimeBasis(restored[0].GameTime); got != tc.want {
				t.Fatalf("restored time basis=%v, want %v; snapshot=%+v", got, tc.want, restored[0].GameTime)
			}
		})
	}
}

func TestSQLiteMemoryStoreRetriesExistingV1Fingerprint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world", EntityID: "entity"}
	const batchKey = `["fake-game","world","entity","turn","event","settled_turn",1]`
	record := memory.Record{
		MemoryID: "original", SessionKey: key,
		ProjectionKind:    memory.ProjectionKindSettledTurn,
		ProjectionVersion: memory.ProjectionVersionRecentV1, ProjectionBatchKey: batchKey,
		SourceTurnID: "turn", SourceEventID: "event",
		GameTime:  &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 1, Hour: 10, Tick: 42},
		CreatedAt: time.Unix(100, 0),
	}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
	if err := store.Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	path, err := memory.SQLiteDatabasePath(root, key.GameID, key.WorldID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const v1FingerprintInput = `{"projection_kind":"settled_turn","projection_version":1,"projection_batch_key":"[\"fake-game\",\"world\",\"entity\",\"turn\",\"event\",\"settled_turn\",1]","game_id":"fake-game","world_id":"world","entity_id":"entity","source_turn_id":"turn","source_event_id":"event","source_event_sequence":0,"event_type":"","game_time":{"Year":1,"Season":1,"Day":1,"Hour":10,"Minute":0,"Tick":42},"source_context_facts_json":null,"outcomes_json":null}`
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(v1FingerprintInput)))
	if _, err := db.Exec("UPDATE recent_projection_batches SET content_fingerprint = ? WHERE projection_batch_key = ?", fingerprint, batchKey); err != nil {
		t.Fatal(err)
	}
	retry := record
	retry.MemoryID = "retry"
	retry.CreatedAt = time.Unix(200, 0)
	if err := store.Append(ctx, retry); err != nil {
		t.Fatalf("v1 retry returned error: %v", err)
	}
	restored, err := store.Recent(ctx, key, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].MemoryID != "original" || !restored[0].CreatedAt.Equal(record.CreatedAt) {
		t.Fatalf("v1 retry changed the original record: %+v", restored)
	}
}
