package context_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/memory"
	"google.golang.org/protobuf/proto"
)

func TestRetrievalRestoresSummaryOmittedDetailFromVerifiedHeader(t *testing.T) {
	input := validEngineInput(t)
	old := historyProjectionSource(t, input.SessionKey, "old", 1, historyProjectionTime(1), "The old code is 7319.")
	input.History = &agentcontext.HistoryInput{
		Summary: &memory.SummaryCheckpoint{ID: "summary", Owner: input.SessionKey, Text: "A code was discussed.", Sources: []memory.HistorySourceRef{{ID: old.ID}}},
		Sources: []memory.HistorySource{old}, MaxTokens: 5000, MaxSummaryTokens: 500,
	}
	match := retrievalMatch(old)
	match.Source.Batch = nil
	input.Retrieval = retrievalInput(input, match)
	input.Retrieval.Diagnostics = []string{"search_scan_incomplete", "search_index_not_ready"}
	before, _ := json.Marshal(input)
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, result.Projection.History)
	assertRetrievalIDs(t, result.Projection.RetrievedHistory, "old")
	snippet := result.Projection.RetrievedHistory.Snippets[0]
	if snippet.Text != "The old code is 7319." || snippet.ActorID != "player" || snippet.Kind != "utterance" || snippet.Path != "/event/facts/0/Text" || snippet.Start != 0 || snippet.End != 21 || snippet.Cropped || !reflect.DeepEqual(snippet.Times, old.Times) {
		t.Fatalf("source provenance lost: %+v", snippet)
	}
	request, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	content := request.Messages[0].Content
	for _, part := range []string{"[Retrieved History]", "7319", "untrusted", "tool permissions", "Current Observation"} {
		if !strings.Contains(content, part) {
			t.Fatalf("final request missing %q: %s", part, content)
		}
	}
	if strings.LastIndex(content, "[Instruction]") < strings.Index(content, "7319") {
		t.Fatal("authority instruction must follow historical data")
	}
	if result.Report.RetrievedHistory.RetainedSnippets != 1 || result.Report.RetrievedHistory.DroppedMatches != 0 || !retrievalDiagnostic(result.Report.RetrievedHistory, "search_scan_incomplete") || !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_included") {
		t.Fatalf("report = %+v", result.Report.RetrievedHistory)
	}
	// Returned provenance and diagnostics must not alias the prepared input.
	result.Projection.RetrievedHistory.Snippets[0].Times[0].Value.Tick = 99
	result.Report.RetrievedHistory.Diagnostics[0] = "changed"
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("retrieval mutated its input")
	}
}

func TestRetrievalKeepsTextWithoutInventingActorOrFactKind(t *testing.T) {
	input := validEngineInput(t)
	source := historyProjectionSource(t, input.SessionKey, "untyped", 1, nil, "The number is 7319.")
	source.Batch.Event.Facts[0].Kind = ""
	source.Batch.Event.Facts[0].ActorEntityID = ""
	input.Retrieval = retrievalInput(input, retrievalMatch(source))
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertRetrievalIDs(t, result.Projection.RetrievedHistory, "untyped")
	snippet := result.Projection.RetrievedHistory.Snippets[0]
	if snippet.ActorID != "" || snippet.Kind != "history_text" || snippet.Text != "The number is 7319." {
		t.Fatalf("unattributed source changed: %+v", snippet)
	}
}

func TestRetrievalFiltersDuplicatesBeforeLimitAndPreservesRankSelection(t *testing.T) {
	input := validEngineInput(t)
	recent := historyProjectionSource(t, input.SessionKey, "recent", 9, nil, "7319 recent")
	old := historyProjectionSource(t, input.SessionKey, "old", 1, nil, "7319 old")
	newer := historyProjectionSource(t, input.SessionKey, "newer", 8, nil, "7319 newer")
	lower := historyProjectionSource(t, input.SessionKey, "lower", 2, nil, "7319 lower-ranked")
	input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{recent}, MaxTokens: 5000}
	var matches []memory.HistoryMatch
	for i := 0; i < 5; i++ {
		matches = append(matches, retrievalMatch(recent))
	}
	matches = append(matches, retrievalMatch(newer), retrievalMatch(newer), retrievalMatch(old), retrievalMatch(lower))
	input.Retrieval = retrievalInput(input, matches...)
	input.Retrieval.Limit = 2
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertRetrievalIDs(t, result.Projection.RetrievedHistory, "old", "newer")
	if !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_recent_duplicate") || !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_duplicate") || !retrievalDiagnostic(result.Report.RetrievedHistory, "retrieval_limit_exceeded") {
		t.Fatalf("missing selection diagnostics: %+v", result.Report.RetrievedHistory)
	}
}

func TestRetrievalValidatesOwnerWatermarkAndAllSourceTimes(t *testing.T) {
	for _, kind := range []string{"snapshot_owner", "source_owner", "batch_owner", "legacy_owner", "watermark", "zero_sequence", "future", "batch_future", "unavailable", "invalid_span"} {
		t.Run(kind, func(t *testing.T) {
			input := validEngineInput(t)
			input.Observation.GameTime = &protocol.GameTime{Tick: proto.Int64(10)}
			bad := retrievalMatch(historyProjectionSource(t, input.SessionKey, "bad", 2, historyProjectionTime(9), "private 7319"))
			good := retrievalMatch(historyProjectionSource(t, input.SessionKey, "good", 1, historyProjectionTime(10), "visible 7319"))
			input.Retrieval = retrievalInput(input)
			switch kind {
			case "snapshot_owner":
				input.Retrieval.Snapshot.Owner.EntityID = "other"
			case "source_owner":
				bad.Source.Owner.WorldID = "other"
			case "batch_owner":
				bad.Source.Batch.Owner.GameID = "other"
			case "legacy_owner":
				bad.Source.Batch.Legacy = &memory.Record{}
			case "watermark":
				bad.Source.Sequence = input.Retrieval.Snapshot.Watermark + 1
			case "zero_sequence":
				bad.Source.Sequence = 0
			case "future":
				bad.Source.Times = []memory.HistoryTime{{Source: "unknown"}, {Source: "future", Value: historyProjectionTime(11)}}
			case "batch_future":
				bad.Source.Batch.Observations = []memory.HistoryObservation{{GameTime: historyProjectionTime(11)}}
			case "unavailable":
				bad.Source.Availability = "pruned"
			case "invalid_span":
				bad.Span.End++
			}
			input.Retrieval.Matches = []memory.HistoryMatch{bad, good}
			input.Retrieval.Limit = 1
			result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
			if kind == "snapshot_owner" {
				assertRetrievalIDs(t, result.Projection.RetrievedHistory)
			} else {
				assertRetrievalIDs(t, result.Projection.RetrievedHistory, "good")
			}
			if strings.Contains(agentcontext.RenderRetrievedHistoryProjection(result.Projection.RetrievedHistory), "private") {
				t.Fatal("hidden source leaked")
			}
		})
	}
}

func TestRetrievalVerifiedHeaderAndFullSourceHaveSameProjection(t *testing.T) {
	input := validEngineInput(t)
	match := retrievalMatch(historyProjectionSource(t, input.SessionKey, "s", 1, historyProjectionTime(1), "7319"))
	input.Retrieval = retrievalInput(input, match)
	full := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	input.Retrieval.Matches[0].Source.Batch = nil
	header := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	if !reflect.DeepEqual(full.Projection.RetrievedHistory, header.Projection.RetrievedHistory) || full.Report.RetrievedHistory.EstimatedTokens != header.Report.RetrievedHistory.EstimatedTokens {
		t.Fatalf("batch presence changed verified provenance or budget: full=%+v; header=%+v", full.Projection.RetrievedHistory, header.Projection.RetrievedHistory)
	}
}

func retrievalMatch(source memory.HistorySource) memory.HistoryMatch {
	field := memory.HistoryTextFields(*source.Batch)[0]
	return memory.HistoryMatch{Source: source, Field: field, Span: memory.HistoryTextSpan{Path: field.Path, End: utf8.RuneCountInString(field.Text)}, MatchedTerms: []string{"7319"}, Score: 1}
}

func retrievalInput(input agentcontext.BuildInput, matches ...memory.HistoryMatch) *agentcontext.RetrievedHistoryInput {
	retrieval := agentcontext.DefaultRetrievedHistoryInput()
	retrieval.Snapshot = memory.HistorySnapshot{Owner: input.SessionKey, Watermark: 100}
	retrieval.Matches = matches
	return &retrieval
}

func assertRetrievalIDs(t *testing.T, projection agentcontext.RetrievedHistoryProjection, want ...string) {
	t.Helper()
	var got []string
	for _, snippet := range projection.Snippets {
		got = append(got, snippet.SourceID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retrieved IDs = %v; want %v", got, want)
	}
}

func retrievalDiagnostic(report agentcontext.RetrievedHistoryReport, code string) bool {
	for _, diagnostic := range report.Diagnostics {
		if diagnostic == code || strings.HasPrefix(diagnostic, code+":") {
			return true
		}
	}
	return false
}
