package context_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestHistoryProjectionFiltersBeforeTailSelection(t *testing.T) {
	input := validEngineInput(t)
	input.Event.GameTime = &protocol.GameTime{Tick: proto.Int64(10)}
	past := historyProjectionSource(t, input.SessionKey, "past", 1, historyProjectionTime(0), "past")
	equal := historyProjectionSource(t, input.SessionKey, "equal", 2, historyProjectionTime(10), "equal")
	unknown := historyProjectionSource(t, input.SessionKey, "unknown", 3, &memory.GameTimeSnapshot{}, "unknown")
	wrongOwner := historyProjectionSource(t, input.SessionKey, "owner", 4, nil, "private-owner")
	wrongOwner.Owner.EntityID = "other"
	wrongBatch := historyProjectionSource(t, input.SessionKey, "batch-owner", 5, nil, "private-batch")
	wrongBatch.Batch.Owner.WorldID = "other"
	sources := []memory.HistorySource{unknown, wrongBatch, equal, wrongOwner, past}
	for i := 0; i < 8; i++ {
		source := historyProjectionSource(t, input.SessionKey, "future-"+strconv.Itoa(i), int64(i+10), historyProjectionTime(11), "future")
		source.Times = append([]memory.HistoryTime{{Source: "unknown"}}, source.Times...)
		sources = append(sources, source)
	}
	input.History = &agentcontext.HistoryInput{Sources: sources, MaxTokens: 20000}
	config := agentcontext.DefaultBudgetConfig()
	config.MaxRecentMemoryRecords = 1
	result := buildHistoryProjection(t, input, config)
	assertHistoryIDs(t, result.Projection.History, "past", "equal", "unknown")
	if result.Report.History.RetainedSources != 3 || result.Report.History.DroppedSources != 10 {
		t.Fatalf("report = %+v", result.Report.History)
	}
	if result.Projection.History.Sources[2].TimeRelation != "unknown" || !historyDiagnostic(result.Report.History, "history_time_unknown") || !historyDiagnostic(result.Report.History, "history_owner_mismatch") {
		t.Fatalf("visibility diagnostics = %+v", result.Report.History)
	}
	if result.Projection.History.Sources[0].TimeRelation == "unknown" || result.Projection.History.Sources[1].TimeRelation == "unknown" {
		t.Fatal("explicit zero/equal times lost their presence")
	}
	if input.History.Sources[0].ID != "unknown" {
		t.Fatal("input order changed")
	}
}

func TestHistoryProjectionChecksBatchTimesAndObservationFallback(t *testing.T) {
	input := validEngineInput(t)
	input.Observation.GameTime = &protocol.GameTime{Tick: proto.Int64(10)}
	source := historyProjectionSource(t, input.SessionKey, "future-observation", 1, historyProjectionTime(1), "future detail")
	source.Batch.Observations = []memory.HistoryObservation{{Step: 1, GameTime: historyProjectionTime(11)}}
	input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{source}, MaxTokens: 5000}
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, result.Projection.History)
	if !historyDiagnostic(result.Report.History, "history_time_hidden") {
		t.Fatalf("batch future time was not diagnosed: %+v", result.Report.History)
	}
}

func TestHistoryProjectionNoncontiguousSummaryCoverage(t *testing.T) {
	input := validEngineInput(t)
	sources := []memory.HistorySource{}
	for i := 1; i <= 4; i++ {
		sources = append(sources, historyProjectionSource(t, input.SessionKey, strconv.Itoa(i), int64(i), nil, "source-"+strconv.Itoa(i)))
	}
	before, _ := json.Marshal(sources)
	input.History = &agentcontext.HistoryInput{
		Summary: &memory.SummaryCheckpoint{ID: "summary", Owner: input.SessionKey, Text: "Earlier events.", Sources: []memory.HistorySourceRef{{ID: "1"}, {ID: "3"}}},
		Sources: []memory.HistorySource{sources[3], sources[0], sources[2], sources[1]}, MaxTokens: 10000, MaxSummaryTokens: 500,
	}
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, result.Projection.History, "2", "4")
	if !result.Report.History.SummaryIncluded || result.Projection.History.SummaryID != "summary" || !historyDiagnostic(result.Report.History, "summary_time_unknown") {
		t.Fatalf("summary = %+v, report = %+v", result.Projection.History, result.Report.History)
	}
	after, _ := json.Marshal(sources)
	if string(before) != string(after) {
		t.Fatal("summary coverage mutated original sources")
	}
}

func TestHistoryProjectionRejectsSummaryOwnerAndCumulativeFuture(t *testing.T) {
	for _, kind := range []string{"owner", "future"} {
		t.Run(kind, func(t *testing.T) {
			input := validEngineInput(t)
			input.Event.GameTime = &protocol.GameTime{Tick: proto.Int64(10)}
			source := historyProjectionSource(t, input.SessionKey, "uncovered", 1, historyProjectionTime(9), "original")
			summary := &memory.SummaryCheckpoint{ID: "invalid", Owner: input.SessionKey, Text: "hidden summary", Sources: []memory.HistorySourceRef{{ID: source.ID}}, Times: []memory.HistoryTime{{Source: "parent", Value: historyProjectionTime(12)}, {Source: "latest", Value: historyProjectionTime(9)}, {Source: "unknown"}}}
			if kind == "owner" {
				summary.Owner.GameID = "other"
			}
			input.History = &agentcontext.HistoryInput{Summary: summary, Sources: []memory.HistorySource{source}, MaxTokens: 5000, MaxSummaryTokens: 500}
			result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
			assertHistoryIDs(t, result.Projection.History, source.ID)
			if result.Report.History.SummaryIncluded || strings.Contains(agentcontext.RenderHistoryProjection(result.Projection.History), "hidden summary") {
				t.Fatal("invalid summary rendered")
			}
		})
	}
}

func TestHistoryProjectionReplacesLegacyEvenWhenDisabled(t *testing.T) {
	for _, budget := range []int{-1, 0, 5000} {
		input := validEngineInput(t)
		input.RecentMemories = []memory.Record{{MemoryID: "legacy", SourceContextFacts: []memory.SourceContextFact{{Kind: "utterance", Text: "legacy duplicate"}}}}
		input.History = &agentcontext.HistoryInput{MaxTokens: budget, Sources: []memory.HistorySource{historyProjectionSource(t, input.SessionKey, "source", 1, nil, "history text")}}
		result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
		request, err := agentcontext.NewRenderer().Render(result.Projection)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Projection.RecentMemory) != 0 || strings.Contains(request.Messages[0].Content, "legacy duplicate") || strings.Contains(request.Messages[0].Content, "[Recent Memory]") {
			t.Fatalf("legacy injection remains: %s", request.Messages[0].Content)
		}
		if budget <= 0 && (len(result.Projection.History.Sources) != 0 || result.Report.History.EstimatedTokens != 0) {
			t.Fatalf("disabled history = %+v", result)
		}
	}
}

func TestHistoryProjectionCropsRawLeavesWithExactRuneSpans(t *testing.T) {
	for _, location := range []string{"fact", "argument", "output", "error", "legacy"} {
		t.Run(location, func(t *testing.T) {
			input := validEngineInput(t)
			raw := strings.Repeat("\u4f60\U0001f680\"\\\n<end>", 1600)
			source := historyProjectionSource(t, input.SessionKey, "large", 1, nil, "short")
			path := "/event/facts/0/Text"
			switch location {
			case "fact":
				source.Batch.Event.Facts[0].Text = raw
			case "legacy":
				record := &memory.Record{MemoryID: "legacy-memory", SessionKey: input.SessionKey, SourceTurnID: source.Batch.TurnID, SourceEventID: source.Batch.Event.ID, ProjectionKind: memory.ProjectionKindSettledTurn, ProjectionVersion: memory.ProjectionVersionRecentV2, CreatedAt: time.Unix(1, 0), SourceContextFacts: []memory.SourceContextFact{{Kind: "utterance", Text: raw}}}
				var err error
				record.ProjectionBatchKey, err = memory.BuildProjectionBatchKey(record.SessionKey, record.SourceTurnID, record.SourceEventID, record.ProjectionKind, record.ProjectionVersion)
				if err != nil {
					t.Fatal(err)
				}
				source.Batch.Kind = memory.HistoryKindLegacy
				source.Batch.Legacy = record
				path = "/legacy/SourceContextFacts/0/Text"
			case "argument":
				source.Batch.Steps = []memory.HistoryStep{{Index: 1, Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call", Name: "say", Arguments: map[string]any{"a~/b": []string{raw}, "number": json.Number("9007199254740993")}}}}}}
				path = "/steps/0/decision/ToolCalls/0/Arguments/a~0~1b/0"
			case "output", "error":
				result := &protocol.ActionResult{ActionId: "action"}
				if location == "output" {
					result.Output, _ = structpb.NewStruct(map[string]any{"text": raw})
				} else {
					result.Error = &protocol.Error{Code: "failed", Message: raw}
				}
				source.Batch.Steps = []memory.HistoryStep{{Index: 1, Executions: []memory.HistoryExecution{{Call: model.ToolCall{ID: "call", Name: "say"}, ActionID: "action", ActionResult: result}}}}
				path = "/steps/0/executions/0/action_result/output/text"
				if location == "error" {
					path = "/steps/0/executions/0/action_result/error/message"
				}
			}
			before, _ := json.Marshal(source)
			input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{source}, MaxTokens: 900}
			result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
			assertHistoryIDs(t, result.Projection.History, "large")
			unit := result.Projection.History.Sources[0]
			if unit.Complete || !json.Valid([]byte(unit.Content)) || !utf8.ValidString(unit.Content) || !historyDiagnostic(result.Report.History, "history_source_cropped") {
				t.Fatalf("invalid crop: %+v", unit)
			}
			body := decodeHistoryJSON(t, unit.Content)
			if historyJSONPointer(t, body, "/kind") != source.Batch.Kind || historyJSONPointer(t, body, "/turn_id") != "turn-large" || historyJSONPointer(t, body, "/event/id") != "event-large" || historyJSONPointer(t, body, "/_truncated") != true {
				t.Fatalf("source framing lost: %s", unit.Content)
			}
			shown := historyJSONPointer(t, body, path).(string)
			if shown == "" || shown == raw || !strings.HasPrefix(raw, shown) {
				t.Fatalf("raw leaf was not partially retained: %q", shown)
			}
			assertHistoryVisibleSpans(t, source, unit)
			request, err := agentcontext.NewRenderer().Render(result.Projection)
			if err != nil {
				t.Fatal(err)
			}
			rendered, _, found := strings.Cut(strings.TrimPrefix(request.Messages[0].Content, "[History]\n"), "\n\n[Game Definition]")
			if !found {
				t.Fatal("history missing from final request")
			}
			finalBody := decodeHistoryJSON(t, rendered)
			if historyJSONPointer(t, finalBody, "/sources/0/content"+path) != shown || historyJSONPointer(t, finalBody, "/sources/0/complete") != false || historyJSONPointer(t, finalBody, "/sources/0/source_id") != "large" {
				t.Fatal("final request disagrees with display provenance")
			}
			if location == "argument" && historyJSONPointer(t, body, "/steps/0/decision/ToolCalls/0/Arguments/number") != json.Number("9007199254740993") {
				t.Fatal("integer precision lost during crop")
			}
			if result.Report.History.EstimatedTokens > 900 || result.Report.History.EstimatedTokens != tokenestimate.EstimateText(agentcontext.RenderHistoryProjection(result.Projection.History)) {
				t.Fatalf("history size = %+v", result.Report.History)
			}
			after, _ := json.Marshal(source)
			if string(before) != string(after) {
				t.Fatal("projection changed durable source")
			}
		})
	}
}

func TestHistoryProjectionKeepsCompleteTailAndDropsOversizedFraming(t *testing.T) {
	input := validEngineInput(t)
	var sources []memory.HistorySource
	for i := 1; i <= 3; i++ {
		sources = append(sources, historyProjectionSource(t, input.SessionKey, strconv.Itoa(i), int64(i), nil, "text"))
	}
	input.History = &agentcontext.HistoryInput{Sources: sources, MaxTokens: agentcontext.EstimateHistoryInputTokens(agentcontext.HistoryInput{Sources: sources[1:]})}
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, result.Projection.History, "2", "3")
	for i, unit := range result.Projection.History.Sources {
		canonical, _, _, err := memory.CanonicalHistoryBatch(*sources[i+1].Batch, 0)
		if err != nil || !unit.Complete || unit.Content != string(canonical) {
			t.Fatalf("full canonical unit not exposed: %+v, %v", unit, err)
		}
		assertHistoryVisibleSpans(t, sources[i+1], unit)
	}
	framing := historyProjectionSource(t, input.SessionKey, "framing", 4, nil, "x")
	framing.Batch.TurnID = strings.Repeat("association", 2000)
	input.History = &agentcontext.HistoryInput{Sources: []memory.HistorySource{framing}, MaxTokens: 100}
	result = buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, result.Projection.History)
	if !historyDiagnostic(result.Report.History, "history_framing_over_budget") || result.Report.History.DroppedSources != 1 {
		t.Fatalf("framing drop = %+v", result.Report.History)
	}
}

func TestHistoryProjectionBoundsSummaryAndEstimatesUncroppedInput(t *testing.T) {
	input := validEngineInput(t)
	input.History = &agentcontext.HistoryInput{Summary: &memory.SummaryCheckpoint{ID: "summary", Owner: input.SessionKey, Text: strings.Repeat("\u6458\u8981", 400)}, Sources: []memory.HistorySource{historyProjectionSource(t, input.SessionKey, "tail", 1, nil, strings.Repeat("\u539f\u6587", 800))}, MaxTokens: 1000, MaxSummaryTokens: 100}
	estimate := agentcontext.EstimateHistoryInputTokens(*input.History)
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	if !result.Report.History.SummaryIncluded || tokenestimate.EstimateText(result.Projection.History.SummaryText) > 100 || result.Report.History.EstimatedTokens > 1000 || estimate <= 1000 {
		t.Fatalf("bounded = %+v, estimate = %d", result.Report.History, estimate)
	}
	input.History.MaxTokens = 20000
	input.History.MaxSummaryTokens = 20000
	full := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	if full.Report.History.EstimatedTokens != estimate || agentcontext.EstimateHistoryInputTokens(agentcontext.HistoryInput{}) != 0 {
		t.Fatalf("uncropped framing estimate = %d; rendered = %d", estimate, full.Report.History.EstimatedTokens)
	}
}

func TestHistoryProjectionGlobalBudgetPreservesCurrentContext(t *testing.T) {
	input := validEngineInput(t)
	input.Event.ContextFacts = []*protocol.ContextFact{{Kind: "utterance", Text: "required current words"}}
	input.Observation.State, _ = structpb.NewStruct(map[string]any{"current": "state"})
	input.Transcript = []model.Message{
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "first", Name: "say", Arguments: map[string]any{"text": "first"}}}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{ToolCallID: "first", Name: "say", Status: "succeeded"}}},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "latest", Name: "say", Arguments: map[string]any{"text": "latest"}}}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{ToolCallID: "latest", Name: "say", Status: "succeeded"}}},
	}
	input.History = &agentcontext.HistoryInput{}
	base := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	request, _ := agentcontext.NewRenderer().Render(base.Projection)
	size, err := agentcontext.EstimateRequestTokens(request)
	if err != nil {
		t.Fatal(err)
	}
	input.History = &agentcontext.HistoryInput{Summary: &memory.SummaryCheckpoint{ID: "summary", Owner: input.SessionKey, Text: strings.Repeat("history ", 100)}, Sources: []memory.HistorySource{historyProjectionSource(t, input.SessionKey, "large", 1, nil, strings.Repeat("historical ", 1000))}, MaxTokens: 10000, MaxSummaryTokens: 1000}
	unbounded := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	assertHistoryIDs(t, unbounded.Projection.History, "large")
	for _, limit := range []string{"request", "user"} {
		t.Run(limit, func(t *testing.T) {
			config := agentcontext.DefaultBudgetConfig()
			if limit == "request" {
				config.MaxRequestTokens = size.TotalEstimatedTokens + 20
			} else {
				config.MaxUserMessageTokens = size.UserMessageEstimatedTokens + 20
			}
			result := buildHistoryProjection(t, input, config)
			request, _ := agentcontext.NewRenderer().Render(result.Projection)
			actual, err := agentcontext.EstimateRequestTokens(request)
			if err != nil || agentcontext.RequestEstimatedTokensExceedBudget(actual, config) {
				t.Fatalf("final request overflow: %+v, %v", actual, err)
			}
			if !reflect.DeepEqual(result.Projection.CurrentTurnTranscript, base.Projection.CurrentTurnTranscript) || !reflect.DeepEqual(result.Projection.CurrentEventContextFacts, base.Projection.CurrentEventContextFacts) || !reflect.DeepEqual(result.Projection.CurrentObservation, base.Projection.CurrentObservation) {
				t.Fatal("history displaced current context")
			}
			if result.Report.History.RetainedSources != 0 || result.Report.History.SummaryIncluded || result.Report.History.EstimatedTokens != 0 {
				t.Fatalf("final history report = %+v", result.Report.History)
			}
		})
	}
	config := agentcontext.DefaultBudgetConfig()
	config.MaxRequestTokens = 1
	result, err := agentcontext.NewEngine(config).Build(input)
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) || len(result.Projection.CurrentTurnTranscript) != 2 || result.Report.History.EstimatedTokens != 0 {
		t.Fatalf("hard gate or latest causal group lost: %v, %+v", err, result.Report)
	}
}

func historyProjectionTime(tick int64) *memory.GameTimeSnapshot {
	return memory.SnapshotGameTime(&protocol.GameTime{Tick: proto.Int64(tick)})
}

func historyProjectionSource(t *testing.T, owner session.AgentSessionKey, id string, sequence int64, when *memory.GameTimeSnapshot, text string) memory.HistorySource {
	t.Helper()
	batch := memory.HistoryBatch{Owner: owner, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: "turn-" + id, Event: memory.HistoryEvent{ID: "event-" + id, GameTime: when, Facts: []memory.SourceContextFact{{Kind: "utterance", ActorEntityID: "player", Text: text}}}, Terminal: memory.HistoryTerminal{Status: "completed"}}
	data, key, fingerprint, err := memory.CanonicalHistoryBatch(batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	return memory.HistorySource{ID: id, Sequence: sequence, Owner: owner, Batch: &batch, Times: memory.HistoryTimes(batch), BatchKey: key, Fingerprint: fingerprint, Bytes: len(data), Availability: memory.HistoryAvailable}
}

func buildHistoryProjection(t *testing.T, input agentcontext.BuildInput, config agentcontext.BudgetConfig) agentcontext.BuildResult {
	t.Helper()
	result, err := agentcontext.NewEngine(config).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertHistoryIDs(t *testing.T, projection agentcontext.HistoryProjection, want ...string) {
	t.Helper()
	var got []string
	for _, unit := range projection.Sources {
		got = append(got, unit.SourceID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source IDs = %v; want %v", got, want)
	}
}

func historyDiagnostic(report agentcontext.HistoryReport, code string) bool {
	for _, diagnostic := range report.Diagnostics {
		if diagnostic == code || strings.HasPrefix(diagnostic, code+":") {
			return true
		}
	}
	return false
}

func decodeHistoryJSON(t *testing.T, text string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func historyJSONPointer(t *testing.T, value any, path string) any {
	t.Helper()
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[part]
			if !ok {
				t.Fatalf("missing pointer %s", path)
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(current) {
				t.Fatalf("invalid pointer %s", path)
			}
			value = current[index]
		default:
			t.Fatalf("non-container at %s", path)
		}
	}
	return value
}

func assertHistoryVisibleSpans(t *testing.T, source memory.HistorySource, unit agentcontext.HistoryUnit) {
	t.Helper()
	body := decodeHistoryJSON(t, unit.Content)
	spans := map[string]memory.HistoryTextSpan{}
	for _, span := range unit.VisibleSpans {
		if _, exists := spans[span.Path]; exists {
			t.Fatalf("duplicate span: %+v", span)
		}
		spans[span.Path] = span
	}
	for _, field := range memory.HistoryTextFields(*source.Batch) {
		shown := historyJSONPointer(t, body, field.Path).(string)
		span, exists := spans[field.Path]
		if shown == "" {
			if exists {
				t.Fatalf("empty field marked visible: %+v", span)
			}
			continue
		}
		if !exists || span.Start != 0 || span.End != utf8.RuneCountInString(shown) || span.End > utf8.RuneCountInString(field.Text) || string([]rune(field.Text)[span.Start:span.End]) != shown {
			t.Fatalf("span %+v does not describe actual leaf %q", span, shown)
		}
		delete(spans, field.Path)
	}
	if len(spans) != 0 {
		t.Fatalf("ineligible spans = %+v", spans)
	}
}
