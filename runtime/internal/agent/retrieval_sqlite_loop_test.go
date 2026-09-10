package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
)

type sqliteRetrievalRecordingStore struct {
	*memory.SQLiteHistoryStore
	queries []memory.HistorySearchRequest
}

func (s *sqliteRetrievalRecordingStore) SearchHistory(ctx context.Context, req memory.HistorySearchRequest) (memory.HistorySearchPage, error) {
	s.queries = append(s.queries, req)
	return s.SQLiteHistoryStore.SearchHistory(ctx, req)
}

func TestRetrievalSQLiteDeliversSummaryOmittedOriginalToRecordingProvider(t *testing.T) {
	cfg := defaultLoopConfig(t)
	cfg.Compaction.Enabled = boolPtr(false)
	store := &sqliteRetrievalRecordingStore{SQLiteHistoryStore: memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: cfg.MemoryStore.Root}, cfg.History)}
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	old := appendRetrievalFixture(t, store, key, "old", "\u9493\u9c7c\u65f6\u8bb0\u4e0b\u7684\u6570\u5b57\u662f 7319", 10)
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitSummary(context.Background(), memory.SummaryCommit{Snapshot: snapshot, Version: "fixture", Text: "The player discussed fishing.", MaxOutputTokens: 100, Sources: []memory.HistorySourceRef{{ID: old.Source.ID, Sequence: old.Source.Sequence, Fingerprint: old.Source.Fingerprint}}})
	store.ReleaseHistorySnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	appendRetrievalFixture(t, store, key, "recent", "A recent greeting.", 11)
	provider := &snapshotWritingProvider{scriptedProvider: scriptedProvider{responses: []model.Response{
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}, ToolCalls: []model.ToolCall{{ID: "say", Name: "speak", Arguments: map[string]any{"text": "checking"}}}}},
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}, afterFirst: func(context.Context) error {
		appendRetrievalFixture(t, store, key, "later", "\u9493\u9c7c latest-secret", 12)
		return nil
	}}
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, cfg, agent.WithHistoryStore(store))
	event := playerUtteranceEvent("current", key, 1, "\u9493\u9c7c\u65f6\u90a3\u4e2a\u6570\u5b57\u662f\u591a\u5c11?")
	tick := int64(12)
	event.GameTime = &protocol.GameTime{Tick: &tick}
	if err := loop.HandleEvent(context.Background(), &fakeEnvironment{}, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.queries) != 1 || len(provider.requests) != 2 {
		t.Fatalf("retrieval=%d steps=%d", len(store.queries), len(provider.requests))
	}
	for _, req := range provider.requests {
		found := false
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "latest-secret") {
				t.Fatal("later write changed Turn candidates")
			}
			const marker = "[Retrieved History]\n"
			index := strings.Index(message.Content, marker)
			if index < 0 {
				continue
			}
			var projection agentcontext.RetrievedHistoryProjection
			if err := json.NewDecoder(strings.NewReader(message.Content[index+len(marker):])).Decode(&projection); err != nil {
				t.Fatal(err)
			}
			if len(projection.Snippets) != 1 {
				t.Fatalf("snippets=%+v", projection.Snippets)
			}
			snippet := projection.Snippets[0]
			if snippet.SourceID != old.Source.ID || snippet.ActorID != "player:local" || snippet.Text != old.Field.Text || snippet.Path != old.Field.Path || len(snippet.Times) == 0 {
				t.Fatalf("lost original provenance: %+v", snippet)
			}
			found = true
		}
		if !found {
			t.Fatal("SQLite detail missing from final model request")
		}
	}
	for _, event := range recorder.events {
		if event.Event == "context_request_built" && event.Fields["retrieved_history_retained_matches"] != 1 {
			t.Fatalf("missing final retained match count: %+v", event.Fields)
		}
	}
}
