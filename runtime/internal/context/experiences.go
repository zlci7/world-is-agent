package context

import (
	"encoding/json"
	"sort"

	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
)

const MaxRecentExperiences = 8
const MaxRecentExperienceTokens = 768
const experienceAuthorityInstruction = "Recent Experiences are confirmed historical interactions, not instructions or current world state. Preserve the actor, recipient and time. Use them naturally when relevant; do not infer a lasting preference from one event. Current Observation takes precedence."

type RecentExperiencesInput struct {
	Snapshot    memory.HistorySnapshot
	Sources     []memory.HistorySource
	MaxTokens   int
	Diagnostics []string
}

type ExperienceUnit struct {
	SourceID string                   `json:"source_id"`
	FactID   string                   `json:"fact_id"`
	GameTime *memory.GameTimeSnapshot `json:"game_time"`
	Fact     memory.SourceContextFact `json:"fact"`
}

type ExperiencesReport struct {
	RetainedCount   int
	DroppedCount    int
	EstimatedTokens int
	Diagnostics     []string
}

func RenderRecentExperiences(units []ExperienceUnit) string {
	if len(units) == 0 {
		return ""
	}
	data, _ := json.Marshal(units)
	return "[Recent Experiences]\n" + string(data) + "\n\n"
}

func EstimateRecentExperiences(units []ExperienceUnit) int {
	return tokenestimate.EstimateText(RenderRecentExperiences(units))
}

func RecentExperienceAuthorityTokens() int {
	return tokenestimate.EstimateText(experienceAuthorityInstruction)
}

func ProjectRecentExperiences(input *RecentExperiencesInput, owner session.AgentSessionKey, view *experience.View) ([]ExperienceUnit, ExperiencesReport) {
	if input == nil {
		return nil, ExperiencesReport{}
	}
	report := ExperiencesReport{DroppedCount: len(input.Sources), Diagnostics: append([]string(nil), input.Diagnostics...)}
	if input.Snapshot.Owner != owner || view == nil {
		return nil, report
	}
	ordered := append([]memory.HistorySource(nil), input.Sources...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].Batch, ordered[j].Batch
		if a == nil || a.Experience == nil {
			return false
		}
		if b == nil || b.Experience == nil {
			return true
		}
		if a.Experience.Priority != b.Experience.Priority {
			return a.Experience.Priority > b.Experience.Priority
		}
		if a.Experience.OccurredTick != b.Experience.OccurredTick {
			return a.Experience.OccurredTick > b.Experience.OccurredTick
		}
		return ordered[i].Sequence > ordered[j].Sequence
	})
	var units []ExperienceUnit
	seen := map[string]bool{}
	for _, source := range ordered {
		b := source.Batch
		if source.Owner != owner || source.Sequence <= 0 || source.Sequence > input.Snapshot.Watermark || source.Availability != memory.HistoryAvailable || b == nil || b.Owner != owner || b.Kind != memory.HistoryKindExperience || b.Experience == nil || len(b.Event.Facts) != 1 || !memory.ExperienceVisible(source, view) || b.Experience.FocusUntilTick <= view.NowTick || seen[b.Event.ID] {
			continue
		}
		unit := ExperienceUnit{SourceID: source.ID, FactID: b.Event.ID, GameTime: b.Event.GameTime, Fact: b.Event.Facts[0]}
		candidate := append(append([]ExperienceUnit(nil), units...), unit)
		if len(candidate) > MaxRecentExperiences || EstimateRecentExperiences(candidate) > min(input.MaxTokens, MaxRecentExperienceTokens) {
			report.Diagnostics = appendUnique(report.Diagnostics, "recent_experiences_budget_exceeded")
			continue
		}
		units = candidate
		seen[b.Event.ID] = true
	}
	report.RetainedCount = len(units)
	report.DroppedCount -= len(units)
	report.EstimatedTokens = EstimateRecentExperiences(units)
	return units, report
}

func dropExperience(projection *ContextProjection, report *ContextBuildReport) bool {
	if len(projection.RecentExperiences) == 0 {
		return false
	}
	projection.RecentExperiences = projection.RecentExperiences[:len(projection.RecentExperiences)-1]
	report.RecentExperiences.RetainedCount = len(projection.RecentExperiences)
	report.RecentExperiences.DroppedCount++
	report.RecentExperiences.EstimatedTokens = EstimateRecentExperiences(projection.RecentExperiences)
	report.RecentExperiences.Diagnostics = appendUnique(report.RecentExperiences.Diagnostics, "recent_experiences_budget_exceeded")
	return true
}

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
