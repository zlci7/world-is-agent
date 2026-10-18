package context_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/memory"
	"google.golang.org/protobuf/types/known/structpb"
)

func experienceInput(t *testing.T) agentcontext.BuildInput {
	t.Helper()
	input := validEngineInput(t)
	input.RecentExperiences = &agentcontext.RecentExperiencesInput{Snapshot: memory.HistorySnapshot{Owner: input.SessionKey, Watermark: 20}, MaxTokens: 768}
	ids := []any{}
	for i := 0; i < 12; i++ {
		id := strconv.Itoa(i)
		ids = append(ids, id)
		source := memory.HistorySource{ID: "experience-" + id, Sequence: int64(i + 1), Owner: input.SessionKey, Availability: memory.HistoryAvailable}
		source.Batch = &memory.HistoryBatch{Owner: input.SessionKey, Kind: memory.HistoryKindExperience, Experience: &experience.Record{ClockID: "generic.clock", OccurredTick: int64(i + 1), FocusUntilTick: 400, Priority: 50}, Event: memory.HistoryEvent{ID: id, GameTime: memory.TickGameTime(int64(i + 1)), Facts: []memory.SourceContextFact{{Kind: "interaction", ActorEntityID: "player", TargetEntityID: input.SessionKey.EntityID, Text: fmt.Sprintf("确认的共同经历 %d", i)}}}}
		input.RecentExperiences.Sources = append(input.RecentExperiences.Sources, source)
	}
	input.Observation.Extensions, _ = structpb.NewStruct(map[string]any{experience.Extension: map[string]any{"version": 1, "clock_id": "generic.clock", "now_tick": "100", "valid_fact_ids": ids}, "other_extension": "preserved"})
	return input
}

func TestRecentExperiencesCountBudgetAndDeterministicPriority(t *testing.T) {
	input := experienceInput(t)
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	units := result.Projection.RecentExperiences
	if len(units) == 0 || len(units) > 8 || units[0].FactID != "11" || result.Report.RecentExperiences.EstimatedTokens > 768 || result.Report.RecentExperiences.DroppedCount != 12-len(units) {
		t.Fatalf("units=%+v report=%+v", units, result.Report.RecentExperiences)
	}
	if _, ok := result.Projection.CurrentObservation.Extensions[experience.Extension]; ok {
		t.Fatal("internal extension in model projection")
	}
	if result.Projection.CurrentObservation.Extensions["other_extension"] != "preserved" {
		t.Fatal("unrelated extension removed")
	}
	input.RecentExperiences.MaxTokens = 1
	result = buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	if len(result.Projection.RecentExperiences) != 0 || result.Report.RecentExperiences.DroppedCount != 12 {
		t.Fatal(result.Report.RecentExperiences)
	}
}

func TestRecentExperiencesGlobalBudgetPreservesCurrentInput(t *testing.T) {
	input := experienceInput(t)
	empty := input
	empty.RecentExperiences = nil
	base := buildHistoryProjection(t, empty, agentcontext.DefaultBudgetConfig())
	config := agentcontext.DefaultBudgetConfig()
	config.MaxRequestTokens = base.Report.FinalRequestSize.TotalEstimatedTokens + 1
	result := buildHistoryProjection(t, input, config)
	assertRetrievalRequestBudget(t, result, config)
	if len(result.Projection.RecentExperiences) != 0 || result.Projection.CurrentEvent.EventID != input.Event.EventId || result.Report.RecentExperiences.DroppedCount != 12 {
		t.Fatal("budget displaced current input or retained oversized experiences")
	}
}

func TestRecentExperiencesAndRetrievalFilterRolledBackSource(t *testing.T) {
	input := experienceInput(t)
	source := input.RecentExperiences.Sources[0]
	source.Experience = source.Batch.Experience
	source.FactID = source.Batch.Event.ID
	match := retrievalMatch(source)
	input.Retrieval = retrievalInput(input, match)
	input.Observation.Extensions.Fields[experience.Extension].GetStructValue().Fields["valid_fact_ids"] = structpb.NewListValue(&structpb.ListValue{})
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	req, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.Messages[0].Content, source.ID) || len(result.Projection.RecentExperiences) != 0 || len(result.Projection.RetrievedHistory.Snippets) != 0 {
		t.Fatal("rolled-back record entered context")
	}
}
