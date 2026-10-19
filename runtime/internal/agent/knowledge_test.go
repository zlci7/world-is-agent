package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/knowledge"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tool"
)

func knowledgeLoopFixture(t *testing.T, game string) (*knowledge.Service, definition.Catalog) {
	t.Helper()
	defs, err := definition.NewCatalog([]definition.GameDefinition{{SchemaVersion: "v1alpha1", GameID: game, Title: game}}, []definition.AgentDefinition{{SchemaVersion: "v1alpha1", GameID: game, DefinitionID: "builder"}, {SchemaVersion: "v1alpha1", GameID: game, DefinitionID: "fisher"}})
	if err != nil {
		t.Fatal(err)
	}
	c := knowledge.Catalog{SchemaVersion: "v1", GameID: game, SourceVersion: "test-v1", Entries: []knowledge.Entry{
		{ID: "coop", Title: "鸡舍", Aliases: []string{"鸡舍", "coop"}, Visibility: knowledge.Visibility{Kind: "public"}, Text: "鸡舍可以饲养动物。当前是否建好须根据观察。"},
		{ID: "letter", Title: "旧信", Aliases: []string{"旧信"}, Visibility: knowledge.Visibility{Kind: "definitions", DefinitionIDs: []string{"builder"}}, Text: "仅木匠可知的旧信秘密内容。"},
	}}
	data, _ := json.Marshal(c)
	s, err := knowledge.Open(context.Background(), data, game, defs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, defs
}

func TestKnowledgeFirstRequestAndToolContinuationWithoutTask(t *testing.T) {
	for _, game := range []string{"forest", "station"} {
		for _, memoryOn := range []bool{false, true} {
			t.Run(game+"/memory="+map[bool]string{true: "on", false: "off"}[memoryOn], func(t *testing.T) {
				s, defs := knowledgeLoopFixture(t, game)
				cfg := DefaultConfig()
				cfg.Knowledge.Enabled = true
				cfg.MemoryEnabled = boolPtr(memoryOn)
				cfg.MemoryStore.Root = t.TempDir()
				provider := &mixedLoopProvider{decisions: []model.ModelDecision{
					{ToolCalls: []model.ToolCall{{ID: "lookup", Name: tool.KnowledgeToolName, Arguments: map[string]any{"query": "鸡舍"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}},
					{ToolCalls: []model.ToolCall{{ID: "reply", Name: "environment_check", Arguments: map[string]any{}}}, Control: model.ControlDirective{Kind: model.ControlSettle}},
				}}
				catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{schedulerCapability("environment_check", tool.ConcurrencySequential)}})
				if err != nil {
					t.Fatal(err)
				}
				loop := NewLoop(provider, nil, cfg, WithKnowledge(s), WithDefinitionCatalog(defs), WithHistoryStore(memory.NewInMemoryHistoryStore(cfg.History)))
				key := session.AgentSessionKey{GameID: game, WorldID: "save", EntityID: "npc"}
				env := &mixedLoopEnvironment{}
				event := &protocol.GameEvent{EventId: "event", EventType: "utterance", WorldId: key.WorldID, TargetEntityId: key.EntityID, ContextFacts: []*protocol.ContextFact{{Kind: "speech", Text: "鸡舍有什么用途"}}}
				err = loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID, DefinitionId: "builder", DisplayName: "木匠"}, catalog, event)
				if err != nil {
					t.Fatal(err)
				}
				if len(provider.requests) != 2 {
					t.Fatalf("requests=%d", len(provider.requests))
				}
				first := provider.requests[0]
				if !strings.Contains(first.Messages[0].Content, "[Retrieved Knowledge]") || !strings.Contains(first.Messages[0].Content, "鸡舍可以饲养动物") {
					t.Fatal("automatic knowledge missing from provider request")
				}
				for _, def := range first.Tools {
					if def.Name == "create_task" || def.Name == "update_task" {
						t.Fatal("task authority appeared")
					}
				}
				if strings.Contains(first.Messages[0].Content, "[Task Context]") {
					t.Fatal("phantom task context")
				}
				var found bool
				for _, msg := range provider.requests[1].Messages {
					for _, result := range msg.ToolResults {
						if result.Name == tool.KnowledgeToolName {
							found = result.Status == "succeeded" && result.Output["status"] == "ok" && result.ToolCallID == "lookup"
						}
					}
				}
				if !found || len(env.callOrder()) != 1 || env.callOrder()[0] != "environment_check" {
					t.Fatalf("tool continuation or read-only routing failed: found=%v calls=%v", found, env.callOrder())
				}
			})
		}
	}
}

func TestKnowledgeToolCorrectionAndRestrictedScope(t *testing.T) {
	s, defs := knowledgeLoopFixture(t, "forest")
	cfg := DefaultConfig()
	cfg.Knowledge.Enabled = true
	cfg.MemoryEnabled = boolPtr(false)
	cfg.MemoryStore.Root = t.TempDir()
	provider := &mixedLoopProvider{decisions: []model.ModelDecision{
		{ToolCalls: []model.ToolCall{{ID: "bad", Name: tool.KnowledgeToolName, Arguments: map[string]any{"query": "旧信", "world_id": "other"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}},
		{ToolCalls: []model.ToolCall{{ID: "fixed", Name: tool.KnowledgeToolName, Arguments: map[string]any{"query": "旧信"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}},
		{Control: model.ControlDirective{Kind: model.ControlSettle}},
	}}
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{})
	if err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(provider, nil, cfg, WithKnowledge(s), WithDefinitionCatalog(defs))
	key := session.AgentSessionKey{GameID: "forest", WorldID: "save", EntityID: "npc"}
	err = loop.HandleEvent(context.Background(), &mixedLoopEnvironment{}, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID, DefinitionId: "fisher"}, catalog, &protocol.GameEvent{EventId: "input", WorldId: key.WorldID, TargetEntityId: key.EntityID, ContextFacts: []*protocol.ContextFact{{Kind: "speech", Text: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("requests=%d", len(provider.requests))
	}
	var invalid, noMatch bool
	for i, req := range provider.requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "仅木匠可知的旧信秘密内容") {
				t.Fatal("private entry leaked")
			}
			for _, r := range msg.ToolResults {
				if i == 1 && r.ToolCallID == "bad" {
					invalid = r.Status == "invalid"
				}
				if i == 2 && r.ToolCallID == "fixed" {
					noMatch = r.Output["status"] == "no_match"
				}
			}
		}
	}
	if !invalid || !noMatch {
		t.Fatalf("invalid=%v noMatch=%v", invalid, noMatch)
	}
}

func TestKnowledgeToolCollision(t *testing.T) {
	s, defs := knowledgeLoopFixture(t, "forest")
	cfg := DefaultConfig()
	cfg.Knowledge.Enabled = true
	cfg.MemoryEnabled = boolPtr(false)
	cfg.MemoryStore.Root = t.TempDir()
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{schedulerCapability(tool.KnowledgeToolName, tool.ConcurrencySequential)}})
	if err != nil {
		t.Fatal(err)
	}
	p := &mixedLoopProvider{}
	loop := NewLoop(p, nil, cfg, WithKnowledge(s), WithDefinitionCatalog(defs))
	key := session.AgentSessionKey{GameID: "forest", WorldID: "save", EntityID: "npc"}
	err = loop.HandleEvent(context.Background(), &mixedLoopEnvironment{}, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, &protocol.GameEvent{EventId: "input", WorldId: key.WorldID, TargetEntityId: key.EntityID})
	if err == nil || !strings.Contains(err.Error(), "duplicate tool") || len(p.requests) != 0 {
		t.Fatalf("collision: %v requests=%d", err, len(p.requests))
	}
}

func TestKnowledgeSurvivesTaskAuthorityInvalidation(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	seedContextTask(t, env, key, "active")
	s, defs := knowledgeLoopFixture(t, key.GameID)
	cfg := contextConfig(t)
	cfg.Knowledge.Enabled = true
	cfg.MemoryEnabled = boolPtr(false)
	p := &taskContextModel{run: func(req model.Request, step int) model.ModelDecision {
		var query, update bool
		for _, entry := range req.Tools {
			query = query || entry.Name == tool.KnowledgeToolName
			update = update || entry.Name == "update_task"
		}
		if !query || (step == 1 && !update) || (step == 2 && update) {
			t.Fatalf("step %d tools: %+v", step, req.Tools)
		}
		if step == 1 {
			env.world.epoch++
			return model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "lookup", Name: tool.KnowledgeToolName, Arguments: map[string]any{"query": "鸡舍"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}
		}
		if strings.Contains(req.Messages[0].Content, "[Task Context]") {
			t.Fatal("invalid task remained in context")
		}
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
	}}
	l := NewLoop(p, nil, cfg, WithKnowledge(s), WithDefinitionCatalog(defs))
	if err := l.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID, DefinitionId: "builder"}, catalog, event); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("requests=%d", len(p.requests))
	}
}

func TestKnowledgeQueryLengthUsesArgumentCorrection(t *testing.T) {
	for _, query := range []string{"", strings.Repeat("鱼", 257)} {
		t.Run(map[bool]string{true: "empty", false: "oversize"}[query == ""], func(t *testing.T) {
			s, defs := knowledgeLoopFixture(t, "forest")
			cfg := DefaultConfig()
			cfg.Knowledge.Enabled = true
			cfg.MemoryEnabled = boolPtr(false)
			cfg.MemoryStore.Root = t.TempDir()
			p := &mixedLoopProvider{decisions: []model.ModelDecision{
				{ToolCalls: []model.ToolCall{{ID: "bad", Name: tool.KnowledgeToolName, Arguments: map[string]any{"query": query}}}, Control: model.ControlDirective{Kind: model.ControlContinue}},
				{ToolCalls: []model.ToolCall{{ID: "fixed", Name: tool.KnowledgeToolName, Arguments: map[string]any{"query": "鸡舍"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}},
				{Control: model.ControlDirective{Kind: model.ControlSettle}},
			}}
			catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{})
			if err != nil {
				t.Fatal(err)
			}
			l := NewLoop(p, nil, cfg, WithKnowledge(s), WithDefinitionCatalog(defs))
			key := session.AgentSessionKey{GameID: "forest", WorldID: "save", EntityID: "npc"}
			err = l.HandleEvent(context.Background(), &mixedLoopEnvironment{}, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID, DefinitionId: "builder"}, catalog, &protocol.GameEvent{EventId: "input", WorldId: key.WorldID, TargetEntityId: key.EntityID})
			if err != nil {
				t.Fatal(err)
			}
			invalid, corrected := false, false
			for _, req := range p.requests {
				for _, msg := range req.Messages {
					for _, r := range msg.ToolResults {
						invalid = invalid || (r.ToolCallID == "bad" && r.Status == "invalid")
						corrected = corrected || (r.ToolCallID == "fixed" && r.Status == "succeeded" && r.Output["status"] == "ok")
					}
				}
			}
			if !invalid || !corrected {
				t.Fatalf("invalid=%v corrected=%v", invalid, corrected)
			}
		})
	}
}
