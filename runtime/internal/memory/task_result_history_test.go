package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gameagent/runtime/internal/session"
)

func taskResultOwner() session.AgentSessionKey {
	return session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
}

// A task result source carries the committed result plus the fact and the game
// time the result belongs to. It never claims a model turn or a spoken line.
func taskResultBatch(resultID, state, reason string, hour int32, text string) HistoryBatch {
	return HistoryBatch{
		Owner:   taskResultOwner(),
		Kind:    HistoryKindTaskResult,
		Version: HistoryVersion,
		Event: HistoryEvent{
			GameTime: &GameTimeSnapshot{Year: 1, Season: 1, Day: 1, Hour: hour, PresentFields: gameTimeCalendarFields},
			Facts:    []SourceContextFact{{Kind: "task_result", TargetEntityID: "task-1", Text: text}},
		},
		TaskResult: &HistoryTaskResult{
			ResultID: resultID, TaskID: "task-1", Revision: 3, State: state, Reason: reason,
			OccurredAt: 520, EvidenceRefs: []string{"fact-1"},
		},
	}
}

func TestTaskResultHistoryPublishesWithoutATurnOrSteps(t *testing.T) {
	batch := taskResultBatch("result-1", "succeeded", "satisfied", 9, "the agreed result was confirmed")
	data, key, fingerprint, err := CanonicalHistoryBatch(batch, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if batch.TurnID != "" || len(batch.Steps) != 0 {
		t.Fatalf("fixture carries a model turn: %+v", batch)
	}
	var parts []any
	if err := json.Unmarshal([]byte(key), &parts); err != nil || len(parts) != 6 {
		t.Fatalf("task result key = %s (%v), want six segments", key, err)
	}
	if parts[3] != "result-1" || parts[4] != HistoryKindTaskResult || parts[5] != float64(HistoryVersion) {
		t.Fatalf("task result key identity = %v", parts)
	}
	if !strings.Contains(string(data), "the agreed result was confirmed") {
		t.Fatalf("fact text missing from canonical batch: %s", data)
	}

	for name, store := range map[string]HistoryStore{
		"sqlite":    NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{}),
		"in-memory": NewInMemoryHistoryStore(HistoryLimits{}),
	} {
		t.Run(name, func(t *testing.T) {
			source, err := store.AppendHistory(context.Background(), batch)
			if err != nil {
				t.Fatal(err)
			}
			if source.ID != "history_"+sha256LowerHex(key) || source.Fingerprint != fingerprint || source.Batch == nil || source.Batch.TaskResult == nil {
				t.Fatalf("stored source = %+v", source)
			}
			if !reflect.DeepEqual(*source.Batch.TaskResult, *batch.TaskResult) {
				t.Fatalf("stored result = %+v, want %+v", *source.Batch.TaskResult, *batch.TaskResult)
			}
		})
	}
}

func TestTaskResultHistoryRejectsIncompleteResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HistoryBatch)
	}{
		{name: "missing result", mutate: func(b *HistoryBatch) { b.TaskResult = nil }},
		{name: "missing result id", mutate: func(b *HistoryBatch) { b.TaskResult.ResultID = "" }},
		{name: "missing task id", mutate: func(b *HistoryBatch) { b.TaskResult.TaskID = "" }},
		{name: "zero revision", mutate: func(b *HistoryBatch) { b.TaskResult.Revision = 0 }},
		{name: "nonterminal state", mutate: func(b *HistoryBatch) { b.TaskResult.State = "waiting" }},
		{name: "unknown state", mutate: func(b *HistoryBatch) { b.TaskResult.State = "archived" }},
		{name: "missing occurrence", mutate: func(b *HistoryBatch) { b.TaskResult.OccurredAt = 0 }},
		{name: "missing game time", mutate: func(b *HistoryBatch) { b.Event.GameTime = nil }},
		{name: "empty evidence ref", mutate: func(b *HistoryBatch) { b.TaskResult.EvidenceRefs = []string{""} }},
		{name: "model steps", mutate: func(b *HistoryBatch) { b.Steps = []HistoryStep{{Index: 1}} }},
		{name: "observations", mutate: func(b *HistoryBatch) { b.Observations = []HistoryObservation{{Step: 1}} }},
		{name: "legacy payload", mutate: func(b *HistoryBatch) { b.Legacy = &Record{} }},
		{name: "terminal section", mutate: func(b *HistoryBatch) { b.Terminal = HistoryTerminal{Status: "completed"} }},
		{name: "wrong version", mutate: func(b *HistoryBatch) { b.Version = 2 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := taskResultBatch("result-1", "succeeded", "satisfied", 9, "the agreed result was confirmed")
			tt.mutate(&batch)
			if _, _, _, err := CanonicalHistoryBatch(batch, 8<<20); !errors.Is(err, ErrInvalidHistory) {
				t.Fatalf("CanonicalHistoryBatch() = %v, want ErrInvalidHistory", err)
			}
		})
	}
}

func TestTaskResultHistoryKeepsLegacySourceRequirements(t *testing.T) {
	turn := testHistoryBatch("turn-1")
	turn.TurnID = ""
	if _, _, _, err := CanonicalHistoryBatch(turn, 8<<20); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("terminal turn without turn_id = %v, want ErrInvalidHistory", err)
	}
	legacy := testHistoryBatch("turn-1")
	legacy.Kind = HistoryKindLegacy
	legacy.TurnID = ""
	legacy.Event.GameTime = nil
	legacy.Legacy = &Record{}
	if _, _, _, err := CanonicalHistoryBatch(legacy, 8<<20); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("legacy source without payload = %v, want ErrInvalidHistory", err)
	}
	data, _, _, err := CanonicalHistoryBatch(testHistoryBatch("turn-1"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "task_result") {
		t.Fatalf("absent task result changed legacy serialization: %s", data)
	}
}

func TestTaskResultHistoryIsIdempotentPerResult(t *testing.T) {
	ctx := context.Background()
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	batch := taskResultBatch("result-1", "succeeded", "satisfied", 9, "the agreed result was confirmed")
	first, err := store.AppendHistory(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.AppendHistory(ctx, batch)
	if err != nil || again.ID != first.ID || again.Fingerprint != first.Fingerprint {
		t.Fatalf("repeat publish = %+v (%v), want the original source", again, err)
	}
	var count int
	db := historyTestDB(t, store.base.options.Root, batch)
	if err := db.QueryRow("SELECT COUNT(*) FROM session_history").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows after repeat publish = %d (%v)", count, err)
	}

	changed := batch
	changedResult := *batch.TaskResult
	changedResult.State, changedResult.Reason = "failed", "unsatisfied"
	changed.TaskResult = &changedResult
	if _, err := store.AppendHistory(ctx, changed); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("same result id with a new state = %v, want ErrHistoryConflict", err)
	}
	other := taskResultBatch("result-2", "failed", "unsatisfied", 10, "the agreed window ended without the result")
	second, err := store.AppendHistory(ctx, other)
	if err != nil || second.ID == first.ID || second.Sequence <= first.Sequence {
		t.Fatalf("distinct result = %+v (%v)", second, err)
	}
}

func TestTaskResultHistoryTextFieldsAndPrunedKey(t *testing.T) {
	ctx := context.Background()
	batch := taskResultBatch("result-1", "failed", "unsatisfied", 13, "the agreed window ended without the result")
	fields := HistoryTextFields(batch)
	if len(fields) != 2 {
		t.Fatalf("text fields = %+v, want the fact and the reason", fields)
	}
	byPath := map[string]HistoryTextField{}
	for _, field := range fields {
		byPath[field.Path] = field
	}
	if byPath["/event/facts/0/Text"].Kind != "task_result" || byPath["/event/facts/0/Text"].ActorID != "" {
		t.Fatalf("fact field = %+v, want a task_result fact without a speaker", byPath["/event/facts/0/Text"])
	}
	if byPath["/task_result/reason"].Text != "unsatisfied" || byPath["/task_result/reason"].Kind != "task_result" {
		t.Fatalf("reason field = %+v", byPath["/task_result/reason"])
	}

	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	source, err := store.AppendHistory(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	db := historyTestDB(t, store.base.options.Root, batch)
	if _, err := db.Exec("UPDATE session_history SET availability=?,payload_json=NULL WHERE source_id=?", HistoryPruned, source.ID); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.ReadHistorySource(ctx, batch.Owner, source.ID)
	if err != nil {
		t.Fatalf("pruned task result source = %v", err)
	}
	if pruned.Availability != HistoryPruned || pruned.Batch != nil || len(pruned.Times) == 0 {
		t.Fatalf("pruned source = %+v", pruned)
	}
}

func TestTaskResultHistoryJoinsRetrievalAndSummary(t *testing.T) {
	ctx := context.Background()
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	dialogue := testHistoryBatch("turn-1")
	dialogue.Event.GameTime = summaryTestTime(8)
	dialogue.Event.Facts = []SourceContextFact{{Kind: "utterance", ActorEntityID: "player", Text: "我们八点见。"}}
	turn, err := store.AppendHistory(ctx, dialogue)
	if err != nil {
		t.Fatal(err)
	}
	result := taskResultBatch("result-1", "succeeded", "satisfied", 8, "约定结果已确认")
	outcome, err := store.AppendHistory(ctx, result)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Sequence <= turn.Sequence {
		t.Fatalf("result sequence = %d, want after %d", outcome.Sequence, turn.Sequence)
	}

	snapshot, err := store.BeginHistorySnapshot(ctx, result.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.SearchHistory(ctx, HistorySearchRequest{Snapshot: snapshot, CurrentTime: summaryTestTime(12), Query: "约定结果"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Matches) != 1 {
		t.Fatalf("retrieval matches = %+v, want the task result fact", page)
	}
	if page.Matches[0].Source.ID != outcome.ID || page.Matches[0].Field.Kind != "task_result" || page.Matches[0].Field.Path != "/event/facts/0/Text" {
		t.Fatalf("retrieval match = %+v", page.Matches[0])
	}

	checkpoint := summaryTestCommit(t, store, "", turn, outcome)
	if len(checkpoint.Sources) != 2 {
		t.Fatalf("summary sources = %+v", checkpoint.Sources)
	}
	read, err := store.ReadSummary(ctx, summaryTestSnapshot(t, store, result.Owner), summaryTestTime(12), SummaryReadLimits{})
	if err != nil || read.Checkpoint == nil || read.Checkpoint.ID != checkpoint.ID || len(read.Checkpoint.Sources) != 2 {
		t.Fatalf("summary read = %+v (%v)", read, err)
	}
}
