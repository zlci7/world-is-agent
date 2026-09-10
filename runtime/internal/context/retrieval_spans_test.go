package context

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
)

func TestRetrievalSubtractsVisibleRangesAndKeepsUnmappedText(t *testing.T) {
	owner := session.AgentSessionKey{GameID: "g", WorldID: "w", EntityID: "e"}
	path := "/event/facts/0/Text"
	for _, test := range []struct {
		name    string
		visible []memory.HistoryTextSpan
		want    [][2]int
	}{
		{"prefix", []memory.HistoryTextSpan{{Path: path, Start: 0, End: 5}}, [][2]int{{5, 10}}},
		{"suffix", []memory.HistoryTextSpan{{Path: path, Start: 5, End: 10}}, [][2]int{{0, 5}}},
		{"middle", []memory.HistoryTextSpan{{Path: path, Start: 3, End: 7}}, [][2]int{{0, 3}, {7, 10}}},
		{"overlap_halves", []memory.HistoryTextSpan{{Path: path, Start: 4, End: 10}, {Path: path, Start: 0, End: 6}}, nil},
		{"overlapping_middle", []memory.HistoryTextSpan{{Path: path, Start: 2, End: 5}, {Path: path, Start: 4, End: 7}}, [][2]int{{0, 2}, {7, 10}}},
		{"unmapped", []memory.HistoryTextSpan{{Path: "/other", End: 10}}, [][2]int{{0, 10}}},
		{"no_mapping", nil, [][2]int{{0, 10}}},
		{"outside", []memory.HistoryTextSpan{{Path: path, Start: 10, End: 15}}, [][2]int{{0, 10}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := "\u4e00\U0001f680\u4e8c\u4e09\u56db\u4e94\u516d\u4e0373"
			match := memory.HistoryMatch{Source: memory.HistorySource{ID: "s", Owner: owner, Sequence: 1}, Field: memory.HistoryTextField{Path: path, Kind: "utterance", Text: text}, Span: memory.HistoryTextSpan{Path: path, End: 10}, MatchedTerms: []string{"73"}}
			input := DefaultRetrievedHistoryInput()
			input.Snapshot = memory.HistorySnapshot{Owner: owner, Watermark: 1}
			input.Matches = []memory.HistoryMatch{match, match}
			recent := HistoryProjection{Sources: []HistoryUnit{{SourceID: "s", Complete: false, VisibleSpans: test.visible}}}
			projection, report, err := projectRetrievedHistory(input, owner, nil, recent, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got [][2]int
			for _, snippet := range projection.Snippets {
				got = append(got, [2]int{snippet.Start, snippet.End})
				if snippet.Text != string([]rune(text)[snippet.Start:snippet.End]) || !utf8.ValidString(snippet.Text) || snippet.Cropped != (snippet.Start != 0 || snippet.End != 10) {
					t.Fatalf("inexact original span: %+v", snippet)
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ranges = %v; want %v; report=%+v", got, test.want, report)
			}
		})
	}
}

func TestRetrievalDeduplicatesOverlappingCandidateRanges(t *testing.T) {
	owner := session.AgentSessionKey{GameID: "g", WorldID: "w", EntityID: "e"}
	input := DefaultRetrievedHistoryInput()
	input.Snapshot = memory.HistorySnapshot{Owner: owner, Watermark: 1}
	match := memory.HistoryMatch{Source: memory.HistorySource{ID: "s", Owner: owner, Sequence: 1}, Field: memory.HistoryTextField{Path: "/text", Kind: "action_output", Text: "0123456789"}, Span: memory.HistoryTextSpan{Path: "/text", End: 6}}
	input.Matches = append(input.Matches, match)
	match.Span.Start, match.Span.End = 4, 10
	input.Matches = append(input.Matches, match)
	projection, _, err := projectRetrievedHistory(input, owner, nil, HistoryProjection{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Snippets) != 2 || projection.Snippets[0].Text != "012345" || projection.Snippets[1].Text != "6789" {
		t.Fatalf("overlapping search text was repeated: %+v", projection)
	}
}

func TestRetrievalCroppingPreservesLateUnicodeMatchedTerm(t *testing.T) {
	owner := session.AgentSessionKey{GameID: "g", WorldID: "w", EntityID: "e"}
	input := DefaultRetrievedHistoryInput()
	input.Snapshot = memory.HistorySnapshot{Owner: owner, Watermark: 1}
	raw := strings.Repeat("\u4f60\U0001f680\"\\\n", 200) + "\u5bc6\u78017319" + strings.Repeat("\u540e", 200)
	input.Matches = []memory.HistoryMatch{{Source: memory.HistorySource{ID: "s", Owner: owner, Sequence: 1}, Field: memory.HistoryTextField{Path: "/text", Kind: "action_output", Text: raw}, Span: memory.HistoryTextSpan{Path: "/text", End: utf8.RuneCountInString(raw)}, MatchedTerms: []string{"absent", "\u5bc6\u78017319"}}}
	input.MaxTokens = 250
	projection, report, err := projectRetrievedHistory(input, owner, nil, HistoryProjection{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Snippets) != 1 {
		t.Fatalf("late match lost: %+v", report)
	}
	snippet := projection.Snippets[0]
	if !strings.Contains(snippet.Text, "\u5bc6\u78017319") || !snippet.Cropped || snippet.Start == 0 || snippet.Text != string([]rune(raw)[snippet.Start:snippet.End]) || report.EstimatedTokens > 250 {
		t.Fatalf("wrong match-centered crop: %+v; %+v", snippet, report)
	}
}

func TestRetrievalGlobalGateDropsRetrievalBeforeHistory(t *testing.T) {
	projection := ContextProjection{
		RuntimePolicy: "policy", Instruction: "current authority",
		History: HistoryProjection{Sources: []HistoryUnit{{SourceID: "recent", Sequence: 2, Content: `{}`, Complete: true}}},
	}
	size, err := measureProjectionRequest(projection)
	if err != nil {
		t.Fatal(err)
	}
	projection.RetrievedHistory = RetrievedHistoryProjection{Snippets: []RetrievedHistorySnippet{
		{SourceID: "old", Sequence: 1, Path: "/text", Kind: "utterance", Text: "7319", End: 4},
	}}
	report := ContextBuildReport{History: HistoryReport{RetainedSources: 1}, RetrievedHistory: RetrievedHistoryReport{RetainedSnippets: 1, RetainedMatches: 1, DroppedMatches: 2, Diagnostics: []string{"search_scan_incomplete", "retrieval_included"}}}
	budget := DefaultBudgetConfig()
	budget.MaxRequestTokens = size.TotalEstimatedTokens
	final, report, err := applyProjectionBudgets(projection, budget, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.History.Sources) != 1 || len(final.RetrievedHistory.Snippets) != 0 {
		t.Fatal("retrieval displaced history")
	}
	if report.RetrievedHistory.RetainedMatches != 0 || report.RetrievedHistory.DroppedMatches != 3 || report.RetrievedHistory.hasDiagnostic("retrieval_included") || !report.RetrievedHistory.hasDiagnostic("search_scan_incomplete") {
		t.Fatalf("report describes pre-gate retrieval: %+v", report.RetrievedHistory)
	}
	if !report.Sections.Has("retrieved_history") {
		t.Fatal("retrieval section report missing")
	}
}
