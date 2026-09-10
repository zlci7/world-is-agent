package context_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tokenestimate"
)

func TestRetrievalZeroOneAndExactIndependentBudgets(t *testing.T) {
	input := validEngineInput(t)
	match := retrievalMatch(historyProjectionSource(t, input.SessionKey, "s", 1, nil, "x"))
	match.Source.Batch, match.Source.Times = nil, nil
	match.MatchedTerms = []string{"x"}
	input.Retrieval = retrievalInput(input, match)
	full := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	exact := tokenestimate.EstimateText(agentcontext.RenderRetrievedHistoryProjection(full.Projection.RetrievedHistory))
	if exact == 0 {
		t.Fatal("single-rune snippet missing")
	}
	for _, budget := range []int{-1, 0, 1, exact - 1, exact} {
		input.Retrieval.MaxTokens = budget
		result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
		if budget == exact {
			assertRetrievalIDs(t, result.Projection.RetrievedHistory, "s")
		} else {
			assertRetrievalIDs(t, result.Projection.RetrievedHistory)
			if !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_budget_exceeded") {
				t.Fatal("missing budget diagnostic")
			}
		}
		if result.Report.RetrievedHistory.EstimatedTokens > max(budget, 0) {
			t.Fatal("independent budget overflow")
		}
	}
	input.Retrieval.MaxTokens = 1024
	input.Retrieval.Limit = 0
	assertRetrievalIDs(t, buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig()).Projection.RetrievedHistory)
}

func TestRetrievalDefaultsAndCountOverride(t *testing.T) {
	input := validEngineInput(t)
	input.Retrieval = retrievalInput(input)
	for i := 6; i >= 1; i-- {
		match := retrievalMatch(historyProjectionSource(t, input.SessionKey, strconv.Itoa(i), int64(i), nil, "7319"))
		match.Source.Batch, match.Source.Times = nil, nil
		input.Retrieval.Matches = append(input.Retrieval.Matches, match)
	}
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertRetrievalIDs(t, result.Projection.RetrievedHistory, "2", "3", "4", "5", "6")
	if result.Report.RetrievedHistory.EstimatedTokens > 1024 {
		t.Fatal("default token budget overflow")
	}
	input.Retrieval.Limit = 6
	result = buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertRetrievalIDs(t, result.Projection.RetrievedHistory, "1", "2", "3", "4", "5", "6")
}

func TestRetrievalDeduplicatesAgainstFinalGloballyCroppedHistory(t *testing.T) {
	input := validEngineInput(t)
	old := historyProjectionSource(t, input.SessionKey, "old", 1, nil, strings.Repeat("padding ", 1000)+"old code 7319")
	newest := historyProjectionSource(t, input.SessionKey, "newest", 2, nil, "latest history")
	input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{newest}, MaxTokens: 20000}
	base := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	input.History.Sources = []memory.HistorySource{old, newest}
	input.Retrieval = retrievalInput(input, retrievalMatch(old))
	for _, gate := range []string{"request", "user"} {
		config := agentcontext.DefaultBudgetConfig()
		if gate == "request" {
			config.MaxRequestTokens = base.Report.FinalRequestSize.TotalEstimatedTokens + 400
		} else {
			config.MaxUserMessageTokens = base.Report.FinalRequestSize.UserMessageEstimatedTokens + 400
		}
		result := buildHistoryProjection(t, input, config)
		assertHistoryIDs(t, result.Projection.History, "newest")
		assertRetrievalIDs(t, result.Projection.RetrievedHistory, "old")
		if !strings.Contains(result.Projection.RetrievedHistory.Snippets[0].Text, "7319") {
			t.Fatal("late detail lost after global history removal")
		}
		assertRetrievalRequestBudget(t, result, config)
	}
}

func TestRetrievalKeepsDetailOutsideActualRecentPrefix(t *testing.T) {
	input := validEngineInput(t)
	source := historyProjectionSource(t, input.SessionKey, "large", 1, nil, strings.Repeat("prefix ", 1200)+"secret 7319")
	input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{source}, MaxTokens: 600}
	input.Retrieval = retrievalInput(input, retrievalMatch(source))
	input.Retrieval.MaxTokens = 400
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, result.Projection.History, "large")
	assertRetrievalIDs(t, result.Projection.RetrievedHistory, "large")
	unit, snippet := result.Projection.History.Sources[0], result.Projection.RetrievedHistory.Snippets[0]
	if unit.Complete || !strings.Contains(snippet.Text, "7319") {
		t.Fatalf("partial history detail lost: %+v", snippet)
	}
	if !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_recent_overlap") {
		t.Fatalf("partial de-duplication was not diagnosed: %+v", result.Report.RetrievedHistory)
	}
	for _, visible := range unit.VisibleSpans {
		if visible.Path == snippet.Path && visible.Start < snippet.End && snippet.Start < visible.End {
			t.Fatal("retrieval overlaps visible recent prefix")
		}
	}
}

func TestRetrievalNeverDisplacesCurrentContextOrLatestCausalChain(t *testing.T) {
	input := validEngineInput(t)
	input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{historyProjectionSource(t, input.SessionKey, "recent", 2, nil, "recent words")}, MaxTokens: 10000}
	input.Event.ContextFacts = []*protocol.ContextFact{{Kind: "utterance", Text: "required current words"}}
	input.Transcript = []model.Message{
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "latest", Name: "say", Arguments: map[string]any{"text": "latest"}}}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{ToolCallID: "latest", Name: "say", Status: "succeeded"}}},
	}
	base := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	input.Retrieval = retrievalInput(input, retrievalMatch(historyProjectionSource(t, input.SessionKey, "old", 1, nil, strings.Repeat("7319 ", 1000))))
	for _, gate := range []string{"request", "user"} {
		config := agentcontext.DefaultBudgetConfig()
		if gate == "request" {
			config.MaxRequestTokens = base.Report.FinalRequestSize.TotalEstimatedTokens
		} else {
			config.MaxUserMessageTokens = base.Report.FinalRequestSize.UserMessageEstimatedTokens
		}
		result := buildHistoryProjection(t, input, config)
		assertRetrievalIDs(t, result.Projection.RetrievedHistory)
		if !reflect.DeepEqual(result.Projection.History, base.Projection.History) || !reflect.DeepEqual(result.Projection.CurrentTurnTranscript, base.Projection.CurrentTurnTranscript) || !reflect.DeepEqual(result.Projection.CurrentEventContextFacts, base.Projection.CurrentEventContextFacts) || !reflect.DeepEqual(result.Projection.CurrentObservation, base.Projection.CurrentObservation) {
			t.Fatal("retrieval displaced required context or history")
		}
		assertRetrievalRequestBudget(t, result, config)
	}
	config := agentcontext.DefaultBudgetConfig()
	config.MaxRequestTokens = 1
	result, err := agentcontext.NewEngine(config).Build(input)
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) || len(result.Projection.CurrentTurnTranscript) != 2 || len(result.Projection.RetrievedHistory.Snippets) != 0 {
		t.Fatalf("hard gate/latest chain: %v, %+v", err, result.Report)
	}
	if result.Report.RetrievedHistory.DroppedMatches != 1 || !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_budget_exceeded") || !result.Report.Sections.Has("retrieved_history") {
		t.Fatalf("hard gate lost retrieval diagnostics: %+v", result.Report)
	}
}

func TestRetrievalSectionReportMeasuresFinalAuthorityInstruction(t *testing.T) {
	input := validEngineInput(t)
	input.Retrieval = retrievalInput(input, retrievalMatch(historyProjectionSource(t, input.SessionKey, "s", 1, nil, "7319")))
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	request, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	_, instruction, found := strings.Cut(request.Messages[0].Content, "[Instruction]\n")
	if !found {
		t.Fatal("authority missing")
	}
	measured, err := tokenestimate.EstimateStableJSON(strings.TrimSuffix(instruction, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range result.Report.Sections {
		if section.Name == "instruction" && section.ProjectionEstimatedTokens != measured {
			t.Fatalf("instruction report=%d; rendered=%d", section.ProjectionEstimatedTokens, measured)
		}
	}
}

func TestRetrievalExactFinalRequestBudgetsIncludeAuthorityAndFraming(t *testing.T) {
	input := validEngineInput(t)
	input.Retrieval = retrievalInput(input, retrievalMatch(historyProjectionSource(t, input.SessionKey, "s", 1, nil, "x")))
	full := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertRetrievalIDs(t, full.Projection.RetrievedHistory, "s")
	for _, gate := range []string{"request", "user"} {
		for _, spare := range []int{-1, 0} {
			budget := agentcontext.DefaultBudgetConfig()
			if gate == "request" {
				budget.MaxRequestTokens = full.Report.FinalRequestSize.TotalEstimatedTokens + spare
			} else {
				budget.MaxUserMessageTokens = full.Report.FinalRequestSize.UserMessageEstimatedTokens + spare
			}
			result := buildHistoryProjection(t, input, budget)
			if spare == 0 {
				assertRetrievalIDs(t, result.Projection.RetrievedHistory, "s")
			} else {
				assertRetrievalIDs(t, result.Projection.RetrievedHistory)
			}
			assertRetrievalRequestBudget(t, result, budget)
		}
	}
}

func assertRetrievalRequestBudget(t *testing.T, result agentcontext.BuildResult, budget agentcontext.BudgetConfig) {
	t.Helper()
	request, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := agentcontext.EstimateRequestTokens(request)
	if err != nil || agentcontext.RequestEstimatedTokensExceedBudget(actual, budget) || actual != result.Report.FinalRequestSize {
		t.Fatalf("actual final request/report exceeds budget: %+v; %+v; %v", actual, result.Report.FinalRequestSize, err)
	}
}
