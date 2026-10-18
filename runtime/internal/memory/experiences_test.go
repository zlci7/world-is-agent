package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/session"
)

func experienceBatch(owner session.AgentSessionKey, id string, tick int64) HistoryBatch {
	return HistoryBatch{Owner: owner, Kind: HistoryKindExperience, Version: HistoryVersion, Experience: &experience.Record{ClockID: "generic.clock", OccurredTick: tick, FocusUntilTick: tick + 300, Priority: 50},
		Event: HistoryEvent{ID: id, Type: "game.private.event", GameTime: TickGameTime(tick), Facts: []SourceContextFact{{Kind: "interaction", ActorEntityID: "player", TargetEntityID: owner.EntityID, Text: "玩家送给角色一个蓝莓，结果已确认。"}}}}
}

func TestExperiencesPersistDeduplicateExpireAndRespectSave(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	owner := session.AgentSessionKey{GameID: "fake-garden", WorldID: "save", EntityID: "villager"}
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	batch := experienceBatch(owner, "receipt-a", 100)
	first, err := store.AppendHistory(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.AppendHistory(ctx, batch)
	if err != nil || first.ID != again.ID || first.Sequence != again.Sequence {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	batch.Event.Facts[0].Text = "other"
	if _, err = store.AppendHistory(ctx, batch); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("conflict: %v", err)
	}
	store = NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	snap, err := store.BeginHistorySnapshot(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snap)
	view := &experience.View{ClockID: "generic.clock", NowTick: 200, ValidFactIDs: map[string]struct{}{"receipt-a": {}}}
	read := func(want int) {
		t.Helper()
		sources, err := store.ReadExperiences(ctx, ExperienceQuery{Snapshot: snap, View: view, Limit: 64})
		if err != nil || len(sources) != want {
			t.Fatalf("sources=%+v err=%v", sources, err)
		}
	}
	read(1)
	view.ValidFactIDs = map[string]struct{}{}
	read(0)
	view.ValidFactIDs["receipt-a"] = struct{}{}
	view.NowTick = 400
	read(0)
	matches, err := store.SearchHistory(ctx, HistorySearchRequest{Snapshot: snap, Experiences: view, CurrentTime: TickGameTime(400), Query: "蓝莓"})
	if err != nil || len(matches.Matches) != 1 || matches.Matches[0].Source.Experience == nil {
		t.Fatalf("long recall=%+v %v", matches, err)
	}
	view.ValidFactIDs = map[string]struct{}{}
	matches, err = store.SearchHistory(ctx, HistorySearchRequest{Snapshot: snap, Experiences: view, CurrentTime: TickGameTime(400), Query: "蓝莓"})
	if err != nil || len(matches.Matches) != 0 {
		t.Fatalf("rollback recall=%+v %v", matches, err)
	}
	for _, other := range []session.AgentSessionKey{{GameID: "other", WorldID: owner.WorldID, EntityID: owner.EntityID}, {GameID: owner.GameID, WorldID: "other", EntityID: owner.EntityID}, {GameID: owner.GameID, WorldID: owner.WorldID, EntityID: "other"}} {
		isolated, err := store.BeginHistorySnapshot(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		sources, err := store.ReadExperiences(ctx, ExperienceQuery{Snapshot: isolated, View: &experience.View{ClockID: "generic.clock", NowTick: 200, ValidFactIDs: map[string]struct{}{"receipt-a": {}}}, Limit: 64})
		store.ReleaseHistorySnapshot(isolated)
		if err != nil || len(sources) != 0 {
			t.Fatalf("scope=%+v sources=%+v %v", other, sources, err)
		}
	}
}

func TestExperienceRawAndIndexesCommitAtomically(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	owner := session.AgentSessionKey{GameID: "fake", WorldID: "save", EntityID: "agent"}
	batch := experienceBatch(owner, "receipt", 100)
	snap, err := store.BeginHistorySnapshot(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	store.ReleaseHistorySnapshot(snap)
	db := historyTestDB(t, root, batch)
	if _, err = db.Exec(`CREATE TRIGGER reject_experience BEFORE INSERT ON experience_index BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AppendHistory(ctx, batch); err == nil {
		t.Fatal("index failure accepted")
	}
	for _, table := range []string{"session_history", "history_terms", "experience_index"} {
		var count int
		if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows=%d err=%v", table, count, err)
		}
	}
	if _, err = db.Exec("DROP TRIGGER reject_experience"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AppendHistory(ctx, batch); err != nil {
		t.Fatal(err)
	}
}

func TestExperienceIndexRebuildAndInvalidBranchesDoNotConsumeSearchLimit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	owner := session.AgentSessionKey{GameID: "fake", WorldID: "save", EntityID: "agent"}
	batch := experienceBatch(owner, "valid", 100)
	first, err := store.AppendHistory(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err = store.AppendHistory(ctx, experienceBatch(owner, fmt.Sprintf("abandoned-%d", i), 100)); err != nil {
			t.Fatal(err)
		}
	}
	db := historyTestDB(t, root, batch)
	if _, err = db.Exec("DROP TABLE experience_index"); err != nil {
		t.Fatal(err)
	}
	store = NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	snap, err := store.BeginHistorySnapshot(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snap)
	view := &experience.View{ClockID: "generic.clock", NowTick: 200, ValidFactIDs: map[string]struct{}{"valid": {}}}
	sources, err := store.ReadExperiences(ctx, ExperienceQuery{Snapshot: snap, View: view, Limit: 8})
	if err != nil || len(sources) != 1 || sources[0].ID != first.ID {
		t.Fatalf("rebuilt focus=%+v %v", sources, err)
	}
	limits := DefaultHistorySearchLimits()
	limits.ScanCandidates = 1
	page, err := store.SearchHistory(ctx, HistorySearchRequest{Snapshot: snap, Experiences: view, CurrentTime: TickGameTime(200), Query: "蓝莓", Limits: limits})
	if err != nil || len(page.Matches) != 1 || page.Matches[0].Source.ID != first.ID {
		t.Fatalf("valid source displaced by abandoned branches: %+v %v", page, err)
	}
}
