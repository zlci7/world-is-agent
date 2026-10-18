package agent_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRecentExperiencesReachProviderAfterChatSummaryRestartAndRollback(t *testing.T) {
	for _, game := range []string{"fake-garden", "fake-space"} {
		t.Run(game, func(t *testing.T) {
			ctx := context.Background()
			cfg := defaultLoopConfig(t)
			cfg.Compaction.Enabled = boolPtr(false)
			cfg.History.ScanRecords = 8
			cfg.History.PageRecords = 4
			key := session.AgentSessionKey{GameID: game, WorldID: "save-a", EntityID: "agent-1"}
			store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: cfg.MemoryStore.Root}, cfg.History)
			gift, err := store.AppendHistory(ctx, memory.HistoryBatch{Owner: key, Kind: memory.HistoryKindExperience, Version: memory.HistoryVersion,
				Experience: &experience.Record{ClockID: game + ".clock", OccurredTick: 100, FocusUntilTick: 400, Priority: 50},
				Event:      memory.HistoryEvent{ID: "gift-receipt", Type: game + ".different_event", GameTime: memory.TickGameTime(100), Facts: []memory.SourceContextFact{{Kind: "interaction", ActorEntityID: "player:local", TargetEntityID: key.EntityID, Text: "玩家昨天赠送了一个蓝莓，游戏已确认。"}}}})
			if err != nil {
				t.Fatal(err)
			}
			var refs []memory.HistorySourceRef
			for i := 0; i < 40; i++ {
				match := appendRetrievalFixture(t, store, key, fmt.Sprintf("chat-%d", i), "普通闲聊，与礼物无关。", 110)
				refs = append(refs, memory.HistorySourceRef{ID: match.Source.ID, Sequence: match.Source.Sequence, Fingerprint: match.Source.Fingerprint})
			}
			snap, err := store.BeginHistorySnapshot(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.CommitSummary(ctx, memory.SummaryCommit{Snapshot: snap, Version: "test", Text: "玩家和角色聊了很多日常。", MaxOutputTokens: 100, Sources: refs})
			store.ReleaseHistorySnapshot(snap)
			if err != nil {
				t.Fatal(err)
			}
			// A new store and Loop model a Runtime restart; the record lies beyond the ordinary scan window.
			store = memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: cfg.MemoryStore.Root}, cfg.History)
			for i, tc := range []struct {
				name, text    string
				now           int64
				valid         bool
				focus, recall bool
			}{
				{"next-day-greeting", "早上好", 200, true, true, false},
				{"expired-greeting", "早上好", 400, true, false, false},
				{"long-term-question", "之前送你的蓝莓呢", 400, true, false, true},
				{"rollback-greeting", "早上好", 200, false, false, false},
				{"rollback-search", "之前送你的蓝莓呢", 400, false, false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ids := []any{}
					if tc.valid {
						ids = append(ids, "gift-receipt")
					}
					ext, _ := structpb.NewStruct(map[string]any{experience.Extension: map[string]any{"version": 1, "clock_id": game + ".clock", "now_tick": strconv.FormatInt(tc.now, 10), "valid_fact_ids": ids}})
					obs := &protocol.Observation{WorldId: key.WorldID, EntityId: key.EntityID, Extensions: ext, GameTime: &protocol.GameTime{Tick: &tc.now}}
					event := playerUtteranceEvent(fmt.Sprintf("current-%d", i), key, uint64(i+1), tc.text)
					event.GameTime = &protocol.GameTime{Tick: &tc.now}
					provider := &recordingProvider{response: model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}}}
					loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
					if err := loop.HandleEvent(ctx, &fakeEnvironment{observations: []*protocol.Observation{obs}}, agent.ConnectionContext{GameID: game}, key, entityTarget(key), newSpeakRegistry(), event); err != nil {
						t.Fatal(err)
					}
					if len(provider.requests) != 1 {
						t.Fatalf("requests=%d", len(provider.requests))
					}
					text := provider.requests[0].Messages[0].Content
					if strings.Contains(text, "[Recent Experiences]") != tc.focus {
						t.Fatalf("focus expected=%v: %s", tc.focus, text)
					}
					if strings.Contains(text, gift.ID) != (tc.focus || tc.recall) {
						t.Fatalf("receipt visibility mismatch: %s", text)
					}
					if tc.focus && (!strings.Contains(text, "蓝莓") || strings.Count(text, gift.ID) != 1) {
						t.Fatalf("fact missing or duplicated: %s", text)
					}
					if strings.Contains(text, "valid_fact_ids") || strings.Contains(text, experience.Extension) {
						t.Fatal("internal receipt membership leaked into model Observation")
					}
				})
			}
		})
	}
}
