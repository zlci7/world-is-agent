package context_test

import (
	"strings"
	"testing"

	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/knowledge"
)

func TestKnowledgeProjectionFitsRemainingRequestAndPreservesFacts(t *testing.T) {
	input := validEngineInput(t)
	base, err := agentcontext.NewEngine(agentcontext.DefaultBudgetConfig()).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Knowledge = &agentcontext.RetrievedKnowledgeInput{
		Scope:  knowledge.ReadScope{Owner: input.SessionKey, DefinitionID: input.AgentDescriptor.DefinitionID, ContentHash: "snapshot"},
		Result: knowledge.Result{Status: "ok", SourceVersion: "v1", ContentHash: "snapshot", Entries: []knowledge.Snippet{{ID: "buildings", Title: "设施", Text: strings.Repeat("参考知识。", 70)}}},
	}
	full, err := agentcontext.NewEngine(agentcontext.DefaultBudgetConfig()).Build(input)
	if err != nil || len(full.Projection.RetrievedKnowledge.Entries) != 1 {
		t.Fatalf("full projection: %v %+v", err, full.Report.Knowledge)
	}
	req, err := agentcontext.NewRenderer().Render(full.Projection)
	if err != nil || !strings.Contains(req.Messages[0].Content, "background reference data") {
		t.Fatal("missing knowledge authority")
	}
	budget := agentcontext.DefaultBudgetConfig()
	budget.MaxRequestTokens = base.Report.FinalRequestSize.TotalEstimatedTokens
	tight, err := agentcontext.NewEngine(budget).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(tight.Projection.RetrievedKnowledge.Entries) != 0 || tight.Report.Knowledge.Diagnostic != "knowledge_budget_exceeded" {
		t.Fatalf("budget: %+v", tight.Report.Knowledge)
	}
	if tight.Projection.CurrentObservation.EntityID != input.SessionKey.EntityID || tight.Projection.AgentDefinition.Identity != base.Projection.AgentDefinition.Identity {
		t.Fatal("knowledge displaced core facts")
	}
	input.Knowledge.Scope.Owner.WorldID = "other"
	hidden, err := agentcontext.NewEngine(agentcontext.DefaultBudgetConfig()).Build(input)
	if err != nil || len(hidden.Projection.RetrievedKnowledge.Entries) != 0 || hidden.Report.Knowledge.Diagnostic != "knowledge_scope_mismatch" {
		t.Fatal("scope mismatch not hidden")
	}
}

func TestKnowledgeSkipsOversizeEntryAndRetainsWholeSmallerEntry(t *testing.T) {
	input := validEngineInput(t)
	input.Knowledge = &agentcontext.RetrievedKnowledgeInput{
		Scope: knowledge.ReadScope{Owner: input.SessionKey, DefinitionID: input.AgentDescriptor.DefinitionID, ContentHash: "snapshot"},
		Result: knowledge.Result{Status: "ok", SourceVersion: "v1", ContentHash: "snapshot", Entries: []knowledge.Snippet{
			{ID: strings.Repeat("号", 128), Title: strings.Repeat("题", 128), Text: strings.Repeat("字", 512)},
			{ID: "small", Title: "完整条件", Text: "条件和结论保持完整。"},
		}},
	}
	r, err := agentcontext.NewEngine(agentcontext.DefaultBudgetConfig()).Build(input)
	if err != nil || len(r.Projection.RetrievedKnowledge.Entries) != 1 || r.Projection.RetrievedKnowledge.Entries[0].ID != "small" {
		t.Fatalf("selection: %+v %v", r.Projection.RetrievedKnowledge, err)
	}
	if r.Report.Knowledge.EstimatedTokens > knowledge.AutomaticTokens || r.Report.Knowledge.DroppedCount != 1 || !r.Projection.RetrievedKnowledge.Truncated {
		t.Fatalf("report: %+v", r.Report.Knowledge)
	}
}
