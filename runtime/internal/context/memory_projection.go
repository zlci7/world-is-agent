package context

import (
	"fmt"
	"sort"
	"strings"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
)

func projectRecentMemories(records []memory.Record, recordLimit int, tokenLimit int, currentTime *memory.GameTimeSnapshot, bounds projectionBounds) ([]MemoryProjection, RetentionReport) {
	if len(records) == 0 {
		return nil, RetentionReport{}
	}

	selected := selectTimelineMemories(records, currentTime)
	visibleCount := len(selected)
	selected = trimRecentMemoryRecords(selected, recordLimit)
	projections := make([]MemoryProjection, 0, len(selected))
	for _, record := range selected {
		projections = append(projections, projectRecentMemory(record, currentTime, bounds))
	}
	trimmed := trimMemoryProjections(projections, tokenLimit)
	return trimmed, RetentionReport{
		RetainedCount: len(trimmed),
		DroppedCount:  visibleCount - len(trimmed),
	}
}

func projectRecentMemory(record memory.Record, currentTime *memory.GameTimeSnapshot, bounds projectionBounds) MemoryProjection {
	summaries := make([]string, 0, len(record.SourceContextFacts)+len(record.Outcomes))
	for _, fact := range record.SourceContextFacts {
		if summary := visibleContextFactSummary(fact); summary != "" {
			summaries = append(summaries, summary)
		}
	}
	for _, outcome := range record.Outcomes {
		summaries = append(summaries, visibleActionSummary(outcome, bounds))
	}
	if len(summaries) == 0 {
		summaries = append(summaries, "completed turn")
	}

	return MemoryProjection{
		MemoryID:     record.MemoryID,
		TimeRelation: gameTimeRelation(record.GameTime, currentTime),
		Summaries:    summaries,
	}
}

func selectTimelineMemories(records []memory.Record, currentTime *memory.GameTimeSnapshot) []memory.Record {
	selected := make([]memory.Record, 0, len(records))
	times := make([]*memory.GameTimeSnapshot, 0, len(records))
	for _, record := range records {
		if isFutureMemory(record.GameTime, currentTime) {
			continue
		}
		selected = append(selected, record)
		times = append(times, record.GameTime)
	}

	basis := memory.SharedGameTimeBasis(times...)
	sort.SliceStable(selected, func(i, j int) bool {
		if order := basis.Compare(selected[i].GameTime, selected[j].GameTime); order != 0 {
			return order < 0
		}
		return memoryCreatedBefore(selected[i], selected[j])
	})
	if basis == memory.GameTimeUnknown {
		return selected
	}

	// A group uses sequence only when every member supplies it.
	for start := 0; start < len(selected); {
		end := start + 1
		completeSequence := selected[start].SourceEventSequence != 0
		for end < len(selected) && basis.Compare(selected[start].GameTime, selected[end].GameTime) == 0 {
			completeSequence = completeSequence && selected[end].SourceEventSequence != 0
			end++
		}
		if completeSequence {
			group := selected[start:end]
			sort.SliceStable(group, func(i, j int) bool {
				if group[i].SourceEventSequence != group[j].SourceEventSequence {
					return group[i].SourceEventSequence < group[j].SourceEventSequence
				}
				return memoryCreatedBefore(group[i], group[j])
			})
		}
		start = end
	}
	return selected
}

func trimRecentMemoryRecords(records []memory.Record, limit int) []memory.Record {
	if len(records) == 0 || limit <= 0 {
		return nil
	}
	if len(records) <= limit {
		out := make([]memory.Record, len(records))
		copy(out, records)
		return out
	}
	out := make([]memory.Record, limit)
	copy(out, records[len(records)-limit:])
	return out
}

func memoryCreatedBefore(left, right memory.Record) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.MemoryID < right.MemoryID
}

func trimMemoryProjections(projections []MemoryProjection, limit int) []MemoryProjection {
	if len(projections) == 0 || limit <= 0 {
		return projections
	}

	start := len(projections) - 1
	if sectionProjectionEstimatedTokens(projections[start:]) > limit {
		return nil
	}
	for start > 0 {
		nextStart := start - 1
		if sectionProjectionEstimatedTokens(projections[nextStart:]) > limit {
			break
		}
		start = nextStart
	}

	out := make([]MemoryProjection, len(projections[start:]))
	copy(out, projections[start:])
	return out
}

func renderRecentMemoryProjection(projections []MemoryProjection) string {
	if len(projections) == 0 {
		return "(none)"
	}

	lines := make([]string, 0, len(projections))
	for _, projection := range projections {
		lines = append(lines, renderMemoryProjection(projection))
	}
	return strings.Join(lines, "\n")
}

func renderMemoryProjection(projection MemoryProjection) string {
	summaries := projection.Summaries
	if len(summaries) == 0 {
		summaries = []string{"completed turn"}
	}
	return fmt.Sprintf("- %s: %s", projection.TimeRelation, strings.Join(summaries, "; "))
}

func visibleContextFactSummary(fact memory.SourceContextFact) string {
	kind := strings.ToLower(strings.TrimSpace(fact.Kind))
	text := strings.TrimSpace(fact.Text)
	label := strings.TrimSpace(fact.Label)
	actor := strings.TrimSpace(fact.ActorEntityID)
	if actor == "" {
		actor = "actor"
	}

	switch kind {
	case "utterance":
		if text != "" {
			return fmt.Sprintf("%s said %q", actor, text)
		}
		return ""
	default:
		if text != "" {
			if kind == "" {
				return fmt.Sprintf("%s context %q", actor, text)
			}
			return fmt.Sprintf("%s %s %q", actor, kind, text)
		}
		if label != "" {
			if kind == "" {
				return fmt.Sprintf("%s context %q", actor, label)
			}
			return fmt.Sprintf("%s %s %q", actor, kind, label)
		}
		return ""
	}
}

func visibleActionSummary(outcome memory.TurnOutcome, bounds projectionBounds) string {
	parts := []string{"tool"}
	if name := strings.TrimSpace(outcome.ToolName); name != "" {
		parts[0] = fmt.Sprintf("tool %q", name)
	}
	if status := strings.TrimSpace(outcome.ActionStatus); status != "" {
		parts = append(parts, fmt.Sprintf("status %q", status))
	}
	if len(outcome.ToolArguments) > 0 {
		parts = append(parts, fmt.Sprintf("arguments %s", stableCompactJSON(projectToolArguments(outcome.ToolArguments, bounds))))
	}
	if len(parts) == 1 && parts[0] == "tool" {
		return "completed a visible action"
	}
	return strings.Join(parts, " ")
}

func currentGameTimeFromEventObservation(event *protocolv1alpha2.GameEvent, observation *protocolv1alpha2.Observation) *memory.GameTimeSnapshot {
	return memory.CurrentGameTime(event, observation)
}

func gameTimeRelation(memoryTime, currentTime *memory.GameTimeSnapshot) string {
	basis := memory.SharedGameTimeBasis(memoryTime, currentTime)
	if basis != memory.GameTimeCalendar && basis != memory.GameTimeCalendarWithTick {
		return "previous interaction"
	}
	if sameGameDay(memoryTime, currentTime) {
		return fmt.Sprintf("today %02d:%02d", memoryTime.Hour, memoryTime.Minute)
	}
	return fmt.Sprintf("previous day %s", formatGameTime(memoryTime))
}

func sameGameDay(left, right *memory.GameTimeSnapshot) bool {
	return left.Year == right.Year &&
		left.Season == right.Season &&
		left.Day == right.Day
}

func isFutureMemory(memoryTime, currentTime *memory.GameTimeSnapshot) bool {
	return memory.SharedGameTimeBasis(memoryTime, currentTime).Compare(memoryTime, currentTime) > 0
}

func formatGameTime(gameTime *memory.GameTimeSnapshot) string {
	return fmt.Sprintf("Y%d S%d D%d %02d:%02d", gameTime.Year, gameTime.Season, gameTime.Day, gameTime.Hour, gameTime.Minute)
}
