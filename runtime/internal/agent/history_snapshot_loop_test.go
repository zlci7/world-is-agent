package agent_test

import (
	"context"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
)

type snapshotCountingStore struct {
	memory.HistoryStore
	begins, pages, releases int
}

func (s *snapshotCountingStore) BeginHistorySnapshot(ctx context.Context, key session.AgentSessionKey) (memory.HistorySnapshot, error) {
	s.begins++
	return s.HistoryStore.BeginHistorySnapshot(ctx, key)
}

func (s *snapshotCountingStore) ReadHistorySnapshot(ctx context.Context, snapshot memory.HistorySnapshot, before int64, limits memory.HistoryReadLimits) (memory.HistoryPage, error) {
	s.pages++
	return s.HistoryStore.ReadHistorySnapshot(ctx, snapshot, before, limits)
}

func (s *snapshotCountingStore) ReleaseHistorySnapshot(snapshot memory.HistorySnapshot) {
	s.releases++
	s.HistoryStore.ReleaseHistorySnapshot(snapshot)
}

type snapshotWritingProvider struct {
	scriptedProvider
	afterFirst func(context.Context) error
}

func (p *snapshotWritingProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	response, err := p.scriptedProvider.Generate(ctx, req)
	if err == nil && len(p.requests) == 1 {
		err = p.afterFirst(ctx)
	}
	return response, err
}

func TestHistoryLoopKeepsOneSnapshotAcrossModelRequests(t *testing.T) {
	cfg := defaultLoopConfig(t)
	cfg.Compaction.Enabled = boolPtr(false)
	store := &snapshotCountingStore{HistoryStore: memory.NewInMemoryHistoryStore(cfg.History)}
	key := session.AgentSessionKey{GameID: "snapshot-game", WorldID: "snapshot-world", EntityID: "speaker"}
	tick := int64(10)
	appendSource := func(ctx context.Context, id string) error {
		_, err := store.AppendHistory(ctx, memory.HistoryBatch{
			Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: id,
			Event: memory.HistoryEvent{ID: id, GameTime: memory.SnapshotGameTime(&protocol.GameTime{Tick: &tick}),
				Facts: []memory.SourceContextFact{{Kind: "statement", Text: id}}},
			Terminal: memory.HistoryTerminal{Status: "completed"},
		})
		return err
	}
	if err := appendSource(context.Background(), "before-turn"); err != nil {
		t.Fatal(err)
	}
	provider := &snapshotWritingProvider{scriptedProvider: scriptedProvider{responses: []model.Response{
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue},
			ToolCalls: []model.ToolCall{{ID: "say", Name: "speak", Arguments: map[string]any{"text": "current turn action"}}}}},
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}, afterFirst: func(ctx context.Context) error { return appendSource(ctx, "after-snapshot") }}
	loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
	event := gameEvent("current", key)
	event.GameTime = &protocol.GameTime{Tick: &tick}
	env := &fakeEnvironment{observations: []*protocol.Observation{{WorldId: key.WorldID, EntityId: key.EntityID, GameTime: &protocol.GameTime{Tick: &tick}}}}
	if err := loop.HandleEvent(context.Background(), env, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), event); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 || store.begins != 1 || store.pages != 1 || store.releases != 1 {
		t.Fatalf("history snapshot lifecycle: requests=%d begin=%d pages=%d release=%d", len(provider.requests), store.begins, store.pages, store.releases)
	}
	for i, request := range provider.requests {
		sources := requestHistorySources(t, request)
		if len(sources) != 1 || sources[0].Event.ID != "before-turn" {
			t.Fatalf("request %d changed frozen history: %+v", i, sources)
		}
	}
	toolResults := 0
	for _, message := range provider.requests[1].Messages {
		toolResults += len(message.ToolResults)
	}
	if toolResults != 1 {
		t.Fatalf("second request lost current-turn causal result: %d", toolResults)
	}
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	if snapshot.Watermark != 3 {
		t.Fatalf("later source or completed turn not persisted: watermark=%d", snapshot.Watermark)
	}
}
