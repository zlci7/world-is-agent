package context

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
)

const historyAuthorityInstruction = `Current Observation is the current truth.
History is historical context. Summary text is lossy, not a new game fact.
If History conflicts with Current Observation, follow Current Observation.
If History is from today and current game time has not clearly advanced much, treat it as nearby conversation context, not proof that the player left and returned.

Use only tools in the current View. If no tool is needed, settle the current turn.`

type HistoryInput struct {
	Summary *memory.SummaryCheckpoint
	Sources []memory.HistorySource
	// MaxTokens bounds summary and sources together; nonpositive values disable display.
	MaxTokens        int
	MaxSummaryTokens int
}

type HistoryProjection struct {
	SummaryID   string
	SummaryText string
	Sources     []HistoryUnit
}

type HistoryUnit struct {
	SourceID     string
	Sequence     int64
	TimeRelation string
	Content      string
	Complete     bool
	VisibleSpans []memory.HistoryTextSpan
}

type HistoryReport struct {
	RetainedSources int
	// DroppedSources counts input sources omitted by visibility, coverage or budget.
	DroppedSources  int
	SummaryIncluded bool
	EstimatedTokens int
	Diagnostics     []string
}

// EstimateHistoryInputTokens measures the supplied logical candidates, including
// summary and source framing, independently of display limits or coverage.
func EstimateHistoryInputTokens(input HistoryInput) int {
	projection := HistoryProjection{}
	if input.Summary != nil {
		projection.SummaryID = input.Summary.ID
		projection.SummaryText = input.Summary.Text
	}
	for _, source := range input.Sources {
		unit, err := fullHistoryUnit(source, "unknown")
		if err == nil {
			projection.Sources = append(projection.Sources, unit)
		}
	}
	return estimateHistoryProjection(projection)
}

// RenderHistoryProjection includes the section framing counted by history budgets.
func RenderHistoryProjection(projection HistoryProjection) string {
	if projection.SummaryText == "" && len(projection.Sources) == 0 {
		return ""
	}
	type renderedUnit struct {
		SourceID     string          `json:"source_id"`
		Sequence     int64           `json:"sequence"`
		TimeRelation string          `json:"time_relation"`
		Content      json.RawMessage `json:"content"`
		Complete     bool            `json:"complete"`
	}
	rendered := struct {
		SummaryID   string         `json:"summary_id,omitempty"`
		SummaryText string         `json:"summary_text,omitempty"`
		Sources     []renderedUnit `json:"sources,omitempty"`
	}{SummaryID: projection.SummaryID, SummaryText: projection.SummaryText}
	for _, unit := range projection.Sources {
		rendered.Sources = append(rendered.Sources, renderedUnit{SourceID: unit.SourceID, Sequence: unit.Sequence, TimeRelation: unit.TimeRelation, Content: json.RawMessage(unit.Content), Complete: unit.Complete})
	}
	data, err := json.Marshal(rendered)
	if err != nil {
		return ""
	}
	return "[History]\n" + string(data) + "\n\n"
}

func projectHistory(input HistoryInput, owner session.AgentSessionKey, current *memory.GameTimeSnapshot) (HistoryProjection, HistoryReport) {
	projection := HistoryProjection{}
	report := HistoryReport{DroppedSources: len(input.Sources)}
	if input.MaxTokens <= 0 {
		if len(input.Sources) > 0 || input.Summary != nil {
			report.diagnose(ReasonHistoryBudgetExceeded)
		}
		return projection, report
	}
	covered := map[string]bool{}
	if summary := input.Summary; summary != nil {
		visible, unknown := memory.HistoryVisibility(summary.Times, current)
		switch {
		case summary.Owner != owner:
			report.diagnose("summary_owner_mismatch")
		case summary.ID == "" || strings.TrimSpace(summary.Text) == "":
			report.diagnose("summary_invalid")
		default:
			if unknown {
				report.diagnose("summary_time_unknown")
			}
			if !visible {
				report.diagnose("summary_time_hidden")
				break
			}
			projection = boundHistorySummary(summary, min(input.MaxSummaryTokens, input.MaxTokens))
			if projection.SummaryText != summary.Text {
				report.diagnose(ReasonHistoryBudgetExceeded)
				report.diagnose("summary_budget_exceeded")
			}
			if projection.SummaryText != "" {
				for _, ref := range summary.Sources {
					covered[ref.ID] = true
				}
			}
		}
	}
	ordered := append([]memory.HistorySource(nil), input.Sources...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Sequence == ordered[j].Sequence {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})
	type candidate struct {
		source   memory.HistorySource
		relation string
	}
	var candidates []candidate
	for _, source := range ordered {
		if source.Owner != owner || (source.Batch != nil && source.Batch.Owner != owner) || (source.Batch != nil && source.Batch.Legacy != nil && source.Batch.Legacy.SessionKey != owner) {
			report.diagnose("history_owner_mismatch: " + source.ID)
			continue
		}
		if source.Batch == nil || (source.Availability != "" && source.Availability != memory.HistoryAvailable) {
			report.diagnose("history_source_unavailable: " + source.ID)
			continue
		}
		times := append(memory.HistoryTimes(*source.Batch), source.Times...)
		visible, unknown := memory.HistoryVisibility(times, current)
		relation := "visible"
		if unknown {
			relation = "unknown"
			report.diagnose("history_time_unknown: " + source.ID)
		}
		if !visible {
			report.diagnose("history_time_hidden: " + source.ID)
			continue
		}
		if !covered[source.ID] {
			candidates = append(candidates, candidate{source: source, relation: relation})
		}
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		candidate := candidates[i]
		unit, err := fullHistoryUnit(candidate.source, candidate.relation)
		if err != nil {
			report.diagnose("history_source_invalid: " + candidate.source.ID)
			continue
		}
		withUnit := projection
		withUnit.Sources = append([]HistoryUnit{unit}, projection.Sources...)
		if estimateHistoryProjection(withUnit) <= input.MaxTokens {
			projection = withUnit
			continue
		}
		report.diagnose(ReasonHistoryBudgetExceeded)
		if i == len(candidates)-1 {
			cropped, ok := cropHistoryUnit(unit, *candidate.source.Batch, projection, input.MaxTokens)
			if ok {
				projection.Sources = []HistoryUnit{cropped}
				report.diagnose("history_source_cropped: " + unit.SourceID)
			} else {
				report.diagnose("history_framing_over_budget: " + unit.SourceID)
			}
		}
		break
	}
	report.refresh(projection, len(input.Sources))
	return projection, report
}

func fullHistoryUnit(source memory.HistorySource, relation string) (HistoryUnit, error) {
	if source.ID == "" || source.Batch == nil {
		return HistoryUnit{}, memory.ErrInvalidHistory
	}
	data, _, _, err := memory.CanonicalHistoryBatch(*source.Batch, 0)
	if err != nil {
		return HistoryUnit{}, err
	}
	unit := HistoryUnit{SourceID: source.ID, Sequence: source.Sequence, TimeRelation: relation, Content: string(data), Complete: true}
	for _, field := range memory.HistoryTextFields(*source.Batch) {
		unit.VisibleSpans = append(unit.VisibleSpans, memory.HistoryTextSpan{Path: field.Path, End: utf8.RuneCountInString(field.Text)})
	}
	return unit, nil
}

func boundHistorySummary(summary *memory.SummaryCheckpoint, budget int) HistoryProjection {
	if budget <= 0 {
		return HistoryProjection{}
	}
	projection := HistoryProjection{SummaryID: summary.ID, SummaryText: summary.Text}
	if estimateHistoryProjection(projection) <= budget {
		return projection
	}
	text := []rune(summary.Text)
	best := HistoryProjection{}
	for low, high := 1, len(text)-1; low <= high; {
		middle := low + (high-low)/2
		projection.SummaryText = string(text[:middle]) + "\n_truncated"
		if estimateHistoryProjection(projection) <= budget {
			best = projection
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}

func cropHistoryUnit(unit HistoryUnit, batch memory.HistoryBatch, summary HistoryProjection, budget int) (HistoryUnit, bool) {
	value, err := tokenestimate.ParseJSONDocument(unit.Content)
	if err != nil {
		return HistoryUnit{}, false
	}
	root, ok := value.(map[string]any)
	if !ok {
		return HistoryUnit{}, false
	}
	root[truncatedMarker] = true
	fields := memory.HistoryTextFields(batch)
	texts := make([][]rune, len(fields))
	maxRunes := 0
	for i, field := range fields {
		texts[i] = []rune(field.Text)
		maxRunes = max(maxRunes, len(texts[i]))
	}
	// Every candidate changes raw leaves before serialization. Identifiers,
	// associations, origin, numbers and structural fields stay intact.
	makeCandidate := func(limit int) (HistoryUnit, bool) {
		candidate := unit
		candidate.Complete = false
		candidate.VisibleSpans = nil
		for i, field := range fields {
			end := min(limit, len(texts[i]))
			if !setHistoryString(root, field.Path, string(texts[i][:end])) {
				return HistoryUnit{}, false
			}
			if end > 0 {
				candidate.VisibleSpans = append(candidate.VisibleSpans, memory.HistoryTextSpan{Path: field.Path, End: end})
			}
		}
		data, err := json.Marshal(root)
		if err != nil {
			return HistoryUnit{}, false
		}
		candidate.Content = string(data)
		projection := summary
		projection.Sources = []HistoryUnit{candidate}
		return candidate, estimateHistoryProjection(projection) <= budget
	}
	best, fits := makeCandidate(0)
	if !fits || maxRunes == 0 {
		return HistoryUnit{}, false
	}
	for low, high := 1, maxRunes-1; low <= high; {
		middle := low + (high-low)/2
		candidate, fits := makeCandidate(middle)
		if fits {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, true
}

func setHistoryString(root map[string]any, path, text string) bool {
	var node any = root
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch value := node.(type) {
		case map[string]any:
			child, exists := value[part]
			if !exists {
				return false
			}
			if i == len(parts)-1 {
				if _, ok := child.(string); !ok {
					return false
				}
				value[part] = text
				return true
			}
			node = child
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) {
				return false
			}
			if i == len(parts)-1 {
				if _, ok := value[index].(string); !ok {
					return false
				}
				value[index] = text
				return true
			}
			node = value[index]
		default:
			return false
		}
	}
	return false
}

func estimateHistoryProjection(projection HistoryProjection) int {
	return tokenestimate.EstimateText(RenderHistoryProjection(projection))
}

func (report *HistoryReport) refresh(projection HistoryProjection, totalSources int) {
	report.RetainedSources = len(projection.Sources)
	report.DroppedSources = totalSources - report.RetainedSources
	report.SummaryIncluded = projection.SummaryText != ""
	report.EstimatedTokens = estimateHistoryProjection(projection)
}

func (report *HistoryReport) diagnose(diagnostic string) {
	for _, existing := range report.Diagnostics {
		if existing == diagnostic {
			return
		}
	}
	report.Diagnostics = append(report.Diagnostics, diagnostic)
}

func dropOldestHistoryDisplay(projection *ContextProjection, report *ContextBuildReport) bool {
	total := report.History.RetainedSources + report.History.DroppedSources
	switch {
	case len(projection.History.Sources) > 0:
		projection.History.Sources = projection.History.Sources[1:]
	case projection.History.SummaryText != "":
		projection.History.SummaryID = ""
		projection.History.SummaryText = ""
	default:
		return false
	}
	report.History.diagnose(ReasonHistoryBudgetExceeded)
	report.History.refresh(projection.History, total)
	return true
}
