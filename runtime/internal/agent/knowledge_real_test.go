package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/knowledge"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tool"
	"google.golang.org/protobuf/types/known/structpb"
)

type knowledgeRealProvider struct {
	model.Provider
	requests  []model.Request
	decisions []model.ModelDecision
}

func (p *knowledgeRealProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.requests = append(p.requests, req)
	res, err := p.Provider.Generate(ctx, req)
	p.decisions = append(p.decisions, res.Decision)
	return res, err
}

func TestKnowledgeRealModel(t *testing.T) {
	path := os.Getenv("WIA_KNOWLEDGE_REAL_CONFIG")
	if path == "" {
		t.Skip("set WIA_KNOWLEDGE_REAL_CONFIG to opt into external model calls")
	}
	provider, modelConfig, err := llm.NewProviderFromConfigFile(path)
	if err != nil {
		t.Fatal("configured real provider is unavailable")
	}
	if modelConfig.Provider == "fake" {
		t.Fatal("real knowledge checks require a non-fake provider")
	}
	root := filepath.Join("..", "..", "config", "games")
	defs, err := definition.LoadGameCatalogFromDir(root, "stardew-valley")
	if err != nil {
		t.Fatal(err)
	}
	s, err := knowledge.Load(context.Background(), root, "stardew-valley", defs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		name, input string
		query       bool
	}{
		{"automatic", "我想了解鸡舍的一般用途，你能简单介绍吗？", false},
		{"tool-and-progress", "请先用 search_knowledge 查询温室的一般用途，再结合当前观察，告诉我现在能不能使用温室。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := agent.LoadConfigFile(filepath.Join(root, "stardew-valley", "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.MemoryEnabled = new(bool)
			cfg.Task.Enabled = false
			cfg.MemoryStore.Root = t.TempDir()
			cfg.DefinitionCatalogRoot = root
			cfg.MaxRequestTokens = min(cfg.MaxRequestTokens, modelConfig.InputTokens())
			p := &knowledgeRealProvider{Provider: provider}
			l := agent.NewLoop(p, nil, cfg, agent.WithKnowledge(s), agent.WithDefinitionCatalog(defs))
			key := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "isolated-knowledge-check", EntityID: "npc:Robin"}
			state, _ := structpb.NewStruct(map[string]any{"simulation": map[string]any{"greenhouse_available": false, "progress_fact": "温室尚未修复，当前不能使用。"}})
			obs := &protocol.Observation{WorldId: key.WorldID, EntityId: key.EntityID, State: state}
			env := &fakeEnvironment{observations: []*protocol.Observation{obs, obs, obs, obs, obs}}
			ext, _ := structpb.NewStruct(map[string]any{"gameagent": map[string]any{"tool_policy": map[string]any{"settle_after_success": true, "exclusive_per_step": true}}})
			catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{{Name: "present_dialogue", Description: "Displays NPC dialogue. For continuing dialogue provide exactly three distinct player replies and allow_free_text=true; for ending dialogue provide reply_options=[] and allow_free_text=false. It must be the only tool call in its model response. After success the turn ends.", InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string","maxLength":240},"reply_options":{"type":"array","maxItems":3,"items":{"type":"string","maxLength":80}},"allow_free_text":{"type":"boolean","default":true}},"required":["text","reply_options"],"additionalProperties":false}`, Extensions: ext}}})
			if err != nil {
				t.Fatal(err)
			}
			err = l.HandleEvent(context.Background(), env, agent.ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID, DefinitionId: "npc:Robin", DisplayName: "罗宾"}, catalog, &protocol.GameEvent{EventId: tc.name, EventType: "player_said_to_npc", WorldId: key.WorldID, TargetEntityId: key.EntityID, ContextFacts: []*protocol.ContextFact{{Kind: "speech", ActorEntityId: "player", TargetEntityId: key.EntityID, Label: "玩家发言", Text: tc.input}}})
			if err != nil {
				t.Fatalf("real model turn failed: %v", err)
			}
			if len(p.requests) == 0 || !strings.Contains(p.requests[0].Messages[0].Content, "[Retrieved Knowledge]") {
				t.Fatal("first provider request omitted automatic knowledge")
			}
			queried := false
			for _, req := range p.requests {
				for _, msg := range req.Messages {
					for _, r := range msg.ToolResults {
						queried = queried || (r.Name == tool.KnowledgeToolName && r.Status == "succeeded" && r.Output["status"] == "ok")
					}
				}
			}
			if tc.query && !queried {
				t.Fatal("requested knowledge lookup was not observed in continuation")
			}
			var replies []string
			for _, action := range env.submittedActions {
				if action.Capability != "present_dialogue" {
					t.Fatal("unexpected environment action")
				}
				replies = append(replies, action.GetArguments().GetFields()["text"].GetStringValue())
			}
			if len(replies) == 0 {
				t.Fatalf("no visible reply; decisions=%+v", p.decisions)
			}
			t.Logf("provider=%s model=%s requests=%d knowledge_tool=%v replies=%q", modelConfig.Provider, modelConfig.Model, len(p.requests), queried, replies)
		})
	}
}
