package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/knowledge"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
)

func TestKnowledgeToolBoundsSnapshotAndFailures(t *testing.T) {
	c := knowledge.Catalog{SchemaVersion: "v1", GameID: "test", SourceVersion: "v1"}
	body := strings.Repeat("可靠资料。", 75)
	for i := 0; i < 5; i++ {
		c.Entries = append(c.Entries, knowledge.Entry{ID: fmt.Sprint(i), Title: "建造资料", Aliases: []string{"建造"}, Text: body, Visibility: knowledge.Visibility{Kind: "public"}})
	}
	data, _ := json.Marshal(c)
	s, err := knowledge.Open(context.Background(), data, "test", definition.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := s.Scope(session.AgentSessionKey{GameID: "test", WorldID: "world", EntityID: "npc"}, "builder", "角色")
	registry, err := NewRegistry(nil, []Entry{NewKnowledgeTools(s).Entry()})
	if err != nil {
		t.Fatal(err)
	}
	view := registry.BuildTurnToolView(ToolAdmissionConfig{}, &RuntimeCallContext{Knowledge: &scope}).View
	// Admission captures read authority; caller mutations cannot retarget it.
	scope.Owner.GameID = "foreign"
	call := model.ToolCall{ID: "query", Name: KnowledgeToolName, Arguments: map[string]any{"query": "建造"}}
	r, err := view.ExecuteRuntime(context.Background(), call)
	if err != nil || r.Status != "succeeded" || r.Output["truncated"] != true {
		t.Fatalf("bounded query: %+v %v", r, err)
	}
	size, err := tokenestimate.EstimateStableJSON(r)
	if err != nil || size > knowledge.ToolTokens {
		t.Fatalf("result size=%d err=%v", size, err)
	}
	entries, ok := r.Output["entries"].([]any)
	if !ok || len(entries) == 0 || len(entries) > knowledge.ToolLimit {
		t.Fatalf("entries: %+v", r.Output)
	}
	for _, v := range entries {
		if v.(map[string]any)["text"] != body {
			t.Fatal("knowledge returned a partial condition")
		}
	}
	call.Arguments["query"] = "你好"
	r, err = view.ExecuteRuntime(context.Background(), call)
	if err != nil || r.Status != "succeeded" || r.Output["status"] != "no_match" {
		t.Fatalf("no match: %+v %v", r, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	call.Arguments["query"] = "建造"
	r, err = view.ExecuteRuntime(context.Background(), call)
	if err != nil || r.Status != "failed" || r.Code != "knowledge_unavailable" || r.Output["status"] != "unavailable" {
		t.Fatalf("closed service: %+v %v", r, err)
	}
	data, _ = json.Marshal(r)
	if strings.Contains(string(data), "database") || strings.Contains(string(data), "SELECT") {
		t.Fatal("internal error leaked")
	}
}
