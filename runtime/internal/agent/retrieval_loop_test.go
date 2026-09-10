package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
)

type retrievalWiringStore struct {
	*memory.InMemoryHistoryStore
	matches []memory.HistoryMatch
	queries []memory.HistorySearchRequest
	err     error
}

func (s *retrievalWiringStore) SearchHistory(_ context.Context, req memory.HistorySearchRequest) (memory.HistorySearchPage, error) {
	s.queries = append(s.queries, req)
	if s.err != nil {
		return memory.HistorySearchPage{}, s.err
	}
	end := min(len(s.matches), req.Offset+5, req.Offset+req.Limits.ScanCandidates)
	return memory.HistorySearchPage{Matches: s.matches[req.Offset:end], Scanned: end - req.Offset, Bytes: (end - req.Offset) * 100, NextOffset: end, More: end < len(s.matches)}, nil
}

func appendRetrievalFixture(t *testing.T, store memory.HistoryStore, key session.AgentSessionKey, id, text string, tick int64) memory.HistoryMatch {
	t.Helper()
	source, err := store.AppendHistory(context.Background(), memory.HistoryBatch{
		Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: id,
		Event:    memory.HistoryEvent{ID: id, GameTime: memory.SnapshotGameTime(&protocol.GameTime{Tick: &tick}), Facts: []memory.SourceContextFact{{Kind: "utterance", ActorEntityID: "player:local", Text: text}}},
		Terminal: memory.HistoryTerminal{Status: "completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	field := memory.HistoryTextFields(*source.Batch)[0]
	source.Batch = nil
	return memory.HistoryMatch{Source: source, Field: field, Span: memory.HistoryTextSpan{Path: field.Path, End: utf8.RuneCountInString(field.Text)}, MatchedTerms: []string{"fishing"}, Score: 1}
}

func retrievalFixture(t *testing.T) (*retrievalWiringStore, session.AgentSessionKey, memory.HistoryMatch) {
	t.Helper()
	store := &retrievalWiringStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	old := appendRetrievalFixture(t, store, key, "old", "\u9493\u9c7c\u65f6\u8bb0\u4e0b\u7684\u6570\u5b57\u662f 7319", 10)
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitSummary(context.Background(), memory.SummaryCommit{Snapshot: snapshot, Version: "test", Text: "The player discussed fishing.", MaxOutputTokens: 100,
		Sources: []memory.HistorySourceRef{{ID: old.Source.ID, Sequence: old.Source.Sequence, Fingerprint: old.Source.Fingerprint}}})
	store.ReleaseHistorySnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		store.matches = append(store.matches, appendRetrievalFixture(t, store, key, fmt.Sprintf("recent-%d", i), fmt.Sprintf("fishing recent detail %d", i), 11))
	}
	store.matches = append(store.matches, old)
	return store, key, old
}

func TestRetrievalLoopReusesCandidatesAndRecallsSixthSummaryOmittedDetail(t *testing.T) {
	store, key, old := retrievalFixture(t)
	provider := &snapshotWritingProvider{scriptedProvider: scriptedProvider{responses: []model.Response{
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}, ToolCalls: []model.ToolCall{{ID: "say", Name: "speak", Arguments: map[string]any{"text": "checking"}}}}},
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}, afterFirst: func(context.Context) error {
		store.matches = append(store.matches, appendRetrievalFixture(t, store, key, "later", "fishing newly written detail 99999", 12))
		return nil
	}}
	cfg := defaultLoopConfig(t)
	cfg.Compaction.Enabled = boolPtr(false)
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, cfg, agent.WithHistoryStore(store))
	event := playerUtteranceEvent("current", key, 1, "\u9493\u9c7c\u65f6\u90a3\u4e2a\u6570\u5b57\u662f\u591a\u5c11?")
	tick := int64(12)
	event.GameTime = &protocol.GameTime{Tick: &tick}
	if err := loop.HandleEvent(context.Background(), &fakeEnvironment{}, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.queries) != 2 || len(provider.requests) != 2 {
		t.Fatalf("queries=%d model requests=%d; expected one two-page query for the turn", len(store.queries), len(provider.requests))
	}
	for _, req := range provider.requests {
		var text strings.Builder
		for _, message := range req.Messages {
			text.WriteString(message.Content)
		}
		if !strings.Contains(text.String(), old.Field.Text) || !strings.Contains(text.String(), old.Source.ID) || !strings.Contains(text.String(), "player:local") || strings.Count(text.String(), "7319") != 1 || strings.Contains(text.String(), "99999") {
			t.Fatalf("missing frozen retrieval provenance or leaked later write: %s", text.String())
		}
		for _, batch := range requestHistorySources(t, req) {
			if batch.Event.ID == "old" {
				t.Fatal("old source was still in recent history")
			}
		}
		if !strings.Contains(text.String(), "The player discussed fishing.") {
			t.Fatal("summary disappeared")
		}
	}
	for _, req := range store.queries {
		if req.Snapshot.Watermark != 6 || req.Query != event.ContextFacts[0].Text {
			t.Fatalf("changed retrieval snapshot/query: %+v", req)
		}
	}
}

func TestRetrievalLoopFiltersFutureAcrossPages(t *testing.T) {
	store, key, old := retrievalFixture(t)
	store.matches = nil
	for i := 0; i < 7; i++ {
		store.matches = append(store.matches, appendRetrievalFixture(t, store, key, fmt.Sprintf("future-%d", i), "future-secret", 20))
	}
	store.matches = append(store.matches, old)
	cfg := defaultLoopConfig(t)
	cfg.Compaction.Enabled = boolPtr(false)
	cfg.Retrieval.Limit = 1
	provider := &recordingProvider{response: model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}}}
	loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
	event := playerUtteranceEvent("current", key, 1, "fishing")
	tick := int64(12)
	event.GameTime = &protocol.GameTime{Tick: &tick}
	if err := loop.HandleEvent(context.Background(), &fakeEnvironment{}, agent.ConnectionContext{}, key, entityTarget(key), newSpeakRegistry(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.queries) != 2 {
		t.Fatalf("did not continue past future candidates: %d", len(store.queries))
	}
	text := fmt.Sprint(provider.requests)
	if !strings.Contains(text, "7319") || strings.Contains(text, "future-secret") {
		t.Fatalf("time filtering lost past detail or leaked future: %s", text)
	}
}

func TestRetrievalLoopReadFailureContinuesModelAndTerminalHistory(t *testing.T) {
	store, key, _ := retrievalFixture(t)
	store.err = errors.New("search unavailable")
	cfg := defaultLoopConfig(t)
	cfg.Compaction.Enabled = boolPtr(false)
	provider := &recordingProvider{response: model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}}}
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, cfg, agent.WithHistoryStore(store))
	event := playerUtteranceEvent("current", key, 1, "fishing")
	if err := loop.HandleEvent(context.Background(), &fakeEnvironment{}, agent.ConnectionContext{}, key, entityTarget(key), newSpeakRegistry(), event); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 || len(store.queries) != 1 {
		t.Fatalf("fail-open requests=%d searches=%d", len(provider.requests), len(store.queries))
	}
	if !strings.Contains(fmt.Sprint(recorder.events), "search_read_failed") {
		t.Fatal("missing search failure trace")
	}
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	if snapshot.Watermark != 7 {
		t.Fatalf("terminal history missing: %+v", snapshot)
	}
}
