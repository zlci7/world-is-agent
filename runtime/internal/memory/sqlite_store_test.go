package memory_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
)

func TestSQLiteMemoryStoreUsesHashedWorldDatabasePath(t *testing.T) {
	root := t.TempDir()

	got, err := memory.SQLiteDatabasePath(root, "con", "world.")
	if err != nil {
		t.Fatalf("SQLiteDatabasePath returned error: %v", err)
	}

	gameHash := sha256Hex("con")
	worldHash := sha256Hex("world.")
	want := filepath.Join(root, gameHash, worldHash, "memory.db")
	if got != want {
		t.Fatalf("SQLiteDatabasePath = %q, want %q", got, want)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(got))) != gameHash {
		t.Fatalf("game directory = %q, want hash %q", filepath.Base(filepath.Dir(filepath.Dir(got))), gameHash)
	}
	if filepath.Base(filepath.Dir(got)) != worldHash {
		t.Fatalf("world directory = %q, want hash %q", filepath.Base(filepath.Dir(got)), worldHash)
	}
}

func TestSQLiteMemoryStorePersistsRecentAcrossStoreInstances(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"}
	record := sqliteTestRecord(t, key, "mem-1", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())

	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           10,
		MaxProjectionBatchesPerEntity: 20,
	})
	if err := store.Append(ctx, record); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	reopened := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           10,
		MaxProjectionBatchesPerEntity: 20,
	})
	got, err := reopened.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got=%+v", len(got), got)
	}
	assertSQLiteRecordRoundTrip(t, got[0], record)
}

func TestSQLiteMemoryStoreRecentUsesChronologicalOrderWithinSameSecond(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           10,
		MaxProjectionBatchesPerEntity: 20,
	})
	integerSecond := sqliteTestRecord(t, key, "mem-integer-second", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())
	nanosecondLater := sqliteTestRecord(t, key, "mem-nanosecond-later", "turn-2", "event-2", memory.ProjectionKindSettledTurn, time.Unix(100, int64(100*time.Millisecond)).UTC())

	if err := store.Append(ctx, integerSecond); err != nil {
		t.Fatalf("integer-second Append returned error: %v", err)
	}
	if err := store.Append(ctx, nanosecondLater); err != nil {
		t.Fatalf("nanosecond-later Append returned error: %v", err)
	}
	got, err := store.Recent(ctx, key, 1)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	assertMemoryIDs(t, got, []string{"mem-nanosecond-later"})
}

func TestSQLiteMemoryStoreRetentionUsesChronologicalOrderWithinSameSecond(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           1,
		MaxProjectionBatchesPerEntity: 20,
	})
	integerSecond := sqliteTestRecord(t, key, "mem-integer-second", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())
	nanosecondLater := sqliteTestRecord(t, key, "mem-nanosecond-later", "turn-2", "event-2", memory.ProjectionKindSettledTurn, time.Unix(100, int64(100*time.Millisecond)).UTC())

	if err := store.Append(ctx, integerSecond); err != nil {
		t.Fatalf("integer-second Append returned error: %v", err)
	}
	if err := store.Append(ctx, nanosecondLater); err != nil {
		t.Fatalf("nanosecond-later Append returned error: %v", err)
	}
	got, err := store.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	assertMemoryIDs(t, got, []string{"mem-nanosecond-later"})
}

func TestSQLiteMemoryStoreIsolatesWorldAndEntity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           10,
		MaxProjectionBatchesPerEntity: 20,
	})
	abigail := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"}
	linus := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Linus"}
	otherWorldAbigail := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-b", EntityID: "npc:Abigail"}

	for _, record := range []memory.Record{
		sqliteTestRecord(t, abigail, "abigail-world-a", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC()),
		sqliteTestRecord(t, linus, "linus-world-a", "turn-2", "event-2", memory.ProjectionKindSettledTurn, time.Unix(200, 0).UTC()),
		sqliteTestRecord(t, otherWorldAbigail, "abigail-world-b", "turn-3", "event-3", memory.ProjectionKindSettledTurn, time.Unix(300, 0).UTC()),
	} {
		if err := store.Append(ctx, record); err != nil {
			t.Fatalf("Append(%s) returned error: %v", record.MemoryID, err)
		}
	}

	got, err := store.Recent(ctx, abigail, 10)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	assertMemoryIDs(t, got, []string{"abigail-world-a"})
}

func TestSQLiteMemoryStoreValidatesDatabaseBinding(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})

	if err := store.Append(ctx, sqliteTestRecord(t, key, "mem-1", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	dbPath, err := memory.SQLiteDatabasePath(root, key.GameID, key.WorldID)
	if err != nil {
		t.Fatalf("SQLiteDatabasePath returned error: %v", err)
	}
	overwriteMetadataValue(t, dbPath, "game_id", "other-game")

	if _, err := store.Recent(ctx, key, 10); !errors.Is(err, memory.ErrWorldBindingMismatch) {
		t.Fatalf("Recent error = %v, want ErrWorldBindingMismatch", err)
	}
}

func TestSQLiteMemoryStoreAppendEquivalentProjectionBatchIsNoop(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           10,
		MaxProjectionBatchesPerEntity: 20,
	})
	original := sqliteTestRecord(t, key, "mem-original", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())
	retry := original
	retry.MemoryID = "mem-retry"
	retry.CreatedAt = time.Unix(900, 0).UTC()

	if err := store.Append(ctx, original); err != nil {
		t.Fatalf("original Append returned error: %v", err)
	}
	if err := store.Append(ctx, retry); err != nil {
		t.Fatalf("retry Append returned error: %v", err)
	}
	got, err := store.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got=%+v", len(got), got)
	}
	if got[0].MemoryID != original.MemoryID {
		t.Fatalf("MemoryID = %q, want original %q", got[0].MemoryID, original.MemoryID)
	}
	if !got[0].CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want original %v", got[0].CreatedAt, original.CreatedAt)
	}
}

func TestSQLiteMemoryStoreRejectsProjectionBatchConflict(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{Root: root})
	original := sqliteTestRecord(t, key, "mem-original", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())
	conflict := original
	conflict.MemoryID = "mem-conflict"
	conflict.EventType = "changed_event_type"
	conflict.CreatedAt = time.Unix(900, 0).UTC()

	if err := store.Append(ctx, original); err != nil {
		t.Fatalf("original Append returned error: %v", err)
	}
	if err := store.Append(ctx, conflict); !errors.Is(err, memory.ErrProjectionBatchConflict) {
		t.Fatalf("conflict Append error = %v, want ErrProjectionBatchConflict", err)
	}
	got, err := store.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	assertMemoryIDs(t, got, []string{"mem-original"})
	if got[0].EventType != original.EventType {
		t.Fatalf("EventType = %q, want original %q", got[0].EventType, original.EventType)
	}
}

func TestSQLiteMemoryStoreKeepsProjectionBatchAfterRecentPrune(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	store := memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
		Root:                          root,
		MaxRecordsPerEntity:           1,
		MaxProjectionBatchesPerEntity: 2,
	})
	pruned := sqliteTestRecord(t, key, "mem-pruned", "turn-1", "event-1", memory.ProjectionKindSettledTurn, time.Unix(100, 0).UTC())
	kept := sqliteTestRecord(t, key, "mem-kept", "turn-2", "event-2", memory.ProjectionKindSettledTurn, time.Unix(200, 0).UTC())
	retry := pruned
	retry.MemoryID = "mem-pruned-retry"
	retry.CreatedAt = time.Unix(900, 0).UTC()

	if err := store.Append(ctx, pruned); err != nil {
		t.Fatalf("pruned Append returned error: %v", err)
	}
	if err := store.Append(ctx, kept); err != nil {
		t.Fatalf("kept Append returned error: %v", err)
	}
	if err := store.Append(ctx, retry); err != nil {
		t.Fatalf("retry Append returned error: %v", err)
	}
	got, err := store.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent returned error: %v", err)
	}

	assertMemoryIDs(t, got, []string{"mem-kept"})
}

func sqliteTestRecord(t *testing.T, key session.AgentSessionKey, memoryID string, turnID string, eventID string, kind memory.ProjectionKind, createdAt time.Time) memory.Record {
	t.Helper()

	batchKey, err := memory.BuildProjectionBatchKey(key, turnID, eventID, kind, memory.ProjectionVersionRecentV1)
	if err != nil {
		t.Fatalf("BuildProjectionBatchKey returned error: %v", err)
	}
	return memory.Record{
		MemoryID:            memoryID,
		ProjectionKind:      kind,
		ProjectionVersion:   memory.ProjectionVersionRecentV1,
		ProjectionBatchKey:  batchKey,
		SessionKey:          key,
		SourceTurnID:        turnID,
		SourceEventID:       eventID,
		SourceEventSequence: 42,
		EventType:           "player_interacted_with_agent",
		GameTime:            &memory.GameTimeSnapshot{Year: 1, Season: 2, Day: 3, Hour: 6, Minute: 20, Tick: 9001},
		SourceContextFacts: []memory.SourceContextFact{{
			Kind:           "utterance",
			ActorEntityID:  "player:local",
			TargetEntityID: key.EntityID,
			ScopeID:        "conversation-1",
			Text:           "I prefer quiet evenings.",
			Label:          "preference",
			Attributes:     map[string]any{"topic": "evenings"},
		}},
		Outcomes: []memory.TurnOutcome{{
			ToolName:      "speak",
			ToolArguments: map[string]any{"text": "I'll remember that."},
			ActionStatus:  "ACTION_STATUS_SUCCEEDED",
		}},
		CreatedAt: createdAt,
	}
}

func assertSQLiteRecordRoundTrip(t *testing.T, got memory.Record, want memory.Record) {
	t.Helper()

	if got.MemoryID != want.MemoryID ||
		got.ProjectionKind != want.ProjectionKind ||
		got.ProjectionVersion != want.ProjectionVersion ||
		got.ProjectionBatchKey != want.ProjectionBatchKey ||
		got.SessionKey != want.SessionKey ||
		got.SourceTurnID != want.SourceTurnID ||
		got.SourceEventID != want.SourceEventID ||
		got.SourceEventSequence != want.SourceEventSequence ||
		got.EventType != want.EventType {
		t.Fatalf("record identity mismatch:\ngot  %+v\nwant %+v", got, want)
	}
	if got.GameTime == nil || *got.GameTime != *want.GameTime {
		t.Fatalf("GameTime = %+v, want %+v", got.GameTime, want.GameTime)
	}
	if len(got.SourceContextFacts) != 1 || got.SourceContextFacts[0].Text != want.SourceContextFacts[0].Text || got.SourceContextFacts[0].Attributes["topic"] != "evenings" {
		t.Fatalf("SourceContextFacts = %+v, want %+v", got.SourceContextFacts, want.SourceContextFacts)
	}
	if len(got.Outcomes) != 1 || got.Outcomes[0].ToolName != want.Outcomes[0].ToolName || got.Outcomes[0].ToolArguments["text"] != "I'll remember that." {
		t.Fatalf("Outcomes = %+v, want %+v", got.Outcomes, want.Outcomes)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
}

func overwriteMetadataValue(t *testing.T, dbPath string, key string, value string) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`UPDATE memory_schema_metadata SET value = ? WHERE key = ?`, value, key); err != nil {
		t.Fatalf("metadata update returned error: %v", err)
	}
}

func sha256Hex(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
