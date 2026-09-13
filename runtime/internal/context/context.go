package context

import (
	"errors"
	"fmt"
	"strings"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
	"gameagent/runtime/internal/tool"
)

var (
	ErrInvalidInput   = errors.New("invalid agent context input")
	ErrBudgetExceeded = errors.New("context budget exceeded")
)

const (
	ReasonDefinitionFallback             = "definition_fallback"
	ReasonMemoryBudgetExceeded           = "memory_budget_exceeded"
	ReasonTranscriptBudgetExceeded       = "transcript_budget_exceeded"
	ReasonEventBudgetExceeded            = "current_event_budget_exceeded"
	ReasonObservationBudgetExceeded      = "current_observation_budget_exceeded"
	ReasonContextFactsBudgetExceeded     = "context_facts_budget_exceeded"
	ReasonRequiredContextOverBudget      = "required_context_over_budget"
	ReasonRequiredSectionOverBudget      = "required_section_over_budget"
	ReasonDefinitionBudgetExceeded       = "definition_budget_exceeded"
	ReasonHistoryBudgetExceeded          = "history_budget_exceeded"
	ReasonRetrievedHistoryBudgetExceeded = "retrieval_budget_exceeded"
)

const truncatedMarker = "_truncated"

const defaultAuthorityInstruction = `Current Observation is the current truth.
Recent Memory is historical context.
If Recent Memory conflicts with Current Observation, follow Current Observation.
If Recent Memory is from today and current game time has not clearly advanced much, treat it as nearby conversation context, not proof that the player left and returned.

Use only tools in the current View. If no tool is needed, settle the current turn.`

type BudgetConfig struct {
	MaxRequestTokens              int
	MaxSystemTokens               int
	MaxUserMessageTokens          int
	MaxDefinitionTokens           int
	MaxObservationTokens          int
	MaxEventTokens                int
	MaxContextFactsTokens         int
	MaxRecentMemoryTokens         int
	MaxRecentMemoryRecords        int
	MaxTranscriptTokens           int
	MaxToolCount                  int
	MaxToolDescriptionTokens      int
	MaxToolSchemaTokens           int
	MaxTotalToolSchemaTokens      int
	MaxToolResultOutputTokens     int
	MaxToolResultOutputDepth      int
	MaxToolResultOutputFields     int
	MaxToolResultOutputArrayItems int
}

type EngineConfig = BudgetConfig

type BuildResult struct {
	Projection ContextProjection
	Report     ContextBuildReport
}

type ContextBuildReport struct {
	EffectiveBudget         BudgetConfig
	Sections                SectionReports
	GameDefinitionFallback  bool
	AgentDefinitionFallback bool
	RecentMemory            RetentionReport
	History                 HistoryReport
	RetrievedHistory        RetrievedHistoryReport
	Transcript              RetentionReport
	ToolAdmission           ToolAdmissionSummary
	FinalRequestSize        RequestTokenSummary
	ReasonCodes             []string
}

type SectionReports []SectionReport

type SectionReport struct {
	Name                      string
	Included                  bool
	ProjectionEstimatedTokens int
	Cropped                   bool
	Reason                    string
}

type RetentionReport struct {
	RetainedCount int
	DroppedCount  int
}

type ToolAdmissionSummary struct {
	AcceptedToolCount               int
	AcceptedToolNames               []string
	AcceptedToolNamesTruncatedCount int
	DroppedToolCount                int
	DroppedToolNames                []string
	DroppedToolNamesTruncatedCount  int
	DroppedTools                    []tool.ToolAdmissionDrop
	DroppedToolsTruncatedCount      int
	DroppedReasonCounts             map[string]int
	TotalSchemaEstimatedTokens      int
}

type RequestTokenSummary struct {
	SystemEstimatedTokens      int
	MessagesEstimatedTokens    int
	UserMessageEstimatedTokens int
	ToolsEstimatedTokens       int
	ControlsEstimatedTokens    int
	TotalEstimatedTokens       int
}

func (r SectionReports) Has(name string) bool {
	for _, section := range r {
		if section.Name == name {
			return true
		}
	}
	return false
}

func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxRequestTokens:              65536,
		MaxSystemTokens:               8192,
		MaxUserMessageTokens:          49152,
		MaxDefinitionTokens:           8192,
		MaxObservationTokens:          8192,
		MaxEventTokens:                4096,
		MaxContextFactsTokens:         4096,
		MaxRecentMemoryTokens:         4096,
		MaxRecentMemoryRecords:        5,
		MaxTranscriptTokens:           16384,
		MaxToolCount:                  64,
		MaxToolDescriptionTokens:      2048,
		MaxToolSchemaTokens:           8192,
		MaxTotalToolSchemaTokens:      32768,
		MaxToolResultOutputTokens:     8192,
		MaxToolResultOutputDepth:      4,
		MaxToolResultOutputFields:     64,
		MaxToolResultOutputArrayItems: 32,
	}
}

func (c BudgetConfig) WithDefaults() BudgetConfig {
	defaults := DefaultBudgetConfig()
	c.MaxRequestTokens = positiveOrDefault(c.MaxRequestTokens, defaults.MaxRequestTokens)
	c.MaxSystemTokens = positiveOrDefault(c.MaxSystemTokens, defaults.MaxSystemTokens)
	c.MaxUserMessageTokens = positiveOrDefault(c.MaxUserMessageTokens, defaults.MaxUserMessageTokens)
	c.MaxDefinitionTokens = positiveOrDefault(c.MaxDefinitionTokens, defaults.MaxDefinitionTokens)
	c.MaxObservationTokens = positiveOrDefault(c.MaxObservationTokens, defaults.MaxObservationTokens)
	c.MaxEventTokens = positiveOrDefault(c.MaxEventTokens, defaults.MaxEventTokens)
	c.MaxContextFactsTokens = positiveOrDefault(c.MaxContextFactsTokens, defaults.MaxContextFactsTokens)
	c.MaxRecentMemoryTokens = positiveOrDefault(c.MaxRecentMemoryTokens, defaults.MaxRecentMemoryTokens)
	c.MaxRecentMemoryRecords = positiveOrDefault(c.MaxRecentMemoryRecords, defaults.MaxRecentMemoryRecords)
	c.MaxTranscriptTokens = positiveOrDefault(c.MaxTranscriptTokens, defaults.MaxTranscriptTokens)
	c.MaxToolCount = positiveOrDefault(c.MaxToolCount, defaults.MaxToolCount)
	c.MaxToolDescriptionTokens = positiveOrDefault(c.MaxToolDescriptionTokens, defaults.MaxToolDescriptionTokens)
	c.MaxToolSchemaTokens = positiveOrDefault(c.MaxToolSchemaTokens, defaults.MaxToolSchemaTokens)
	c.MaxTotalToolSchemaTokens = positiveOrDefault(c.MaxTotalToolSchemaTokens, defaults.MaxTotalToolSchemaTokens)
	c.MaxToolResultOutputTokens = positiveOrDefault(c.MaxToolResultOutputTokens, defaults.MaxToolResultOutputTokens)
	c.MaxToolResultOutputDepth = positiveOrDefault(c.MaxToolResultOutputDepth, defaults.MaxToolResultOutputDepth)
	c.MaxToolResultOutputFields = positiveOrDefault(c.MaxToolResultOutputFields, defaults.MaxToolResultOutputFields)
	c.MaxToolResultOutputArrayItems = positiveOrDefault(c.MaxToolResultOutputArrayItems, defaults.MaxToolResultOutputArrayItems)
	return c
}

type Engine struct {
	config BudgetConfig
}

type ContextProjection struct {
	SessionKey session.AgentSessionKey

	CanonicalTarget *protocolv1alpha2.EntityRef

	AgentDescriptor definition.AgentInstanceDescriptor
	GameDefinition  *definition.GameDefinition
	AgentDefinition *definition.AgentDefinition

	RuntimePolicy string
	Instruction   string

	CurrentEvent             EventProjection
	CurrentEventContextFacts []ContextFactProjection
	CurrentObservation       ObservationProjection
	Task                     *TaskProjection

	RecentMemory     []MemoryProjection
	History          HistoryProjection
	RetrievedHistory RetrievedHistoryProjection
	historyEnabled   bool
	retrievalEnabled bool

	Tools []model.ToolDefinition

	CurrentTurnTranscript []model.Message
}

type EventProjection struct {
	EventID         string                      `json:"event_id,omitempty"`
	EventType       string                      `json:"event_type,omitempty"`
	WorldID         string                      `json:"world_id,omitempty"`
	TargetEntityID  string                      `json:"target_entity_id,omitempty"`
	Sequence        uint64                      `json:"sequence,omitempty"`
	GameTime        *protocolv1alpha2.GameTime  `json:"game_time,omitempty"`
	CanonicalTarget *protocolv1alpha2.EntityRef `json:"canonical_target,omitempty"`
	Payload         map[string]any              `json:"payload,omitempty"`
}

type ContextFactProjection struct {
	Kind           string         `json:"kind,omitempty"`
	ActorEntityID  string         `json:"actor_entity_id,omitempty"`
	TargetEntityID string         `json:"target_entity_id,omitempty"`
	ScopeID        string         `json:"scope_id,omitempty"`
	Text           string         `json:"text,omitempty"`
	Label          string         `json:"label,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
}

type ObservationProjection struct {
	WorldID        string                        `json:"world_id,omitempty"`
	EntityID       string                        `json:"entity_id,omitempty"`
	Revision       uint64                        `json:"revision,omitempty"`
	GameTime       *protocolv1alpha2.GameTime    `json:"game_time,omitempty"`
	NearbyEntities []*protocolv1alpha2.EntityRef `json:"nearby_entities,omitempty"`
	Extensions     map[string]any                `json:"extensions,omitempty"`
	State          map[string]any                `json:"state,omitempty"`
}

type MemoryProjection struct {
	MemoryID     string   `json:"memory_id,omitempty"`
	TimeRelation string   `json:"time_relation,omitempty"`
	Summaries    []string `json:"summaries,omitempty"`
}

type BuildInput struct {
	SessionKey      session.AgentSessionKey
	CanonicalTarget *protocolv1alpha2.EntityRef
	AgentDescriptor definition.AgentInstanceDescriptor
	GameDefinition  *definition.GameDefinition
	AgentDefinition *definition.AgentDefinition

	RuntimePolicy string

	RecentMemories []memory.Record
	History        *HistoryInput
	Retrieval      *RetrievedHistoryInput

	Event       *protocolv1alpha2.GameEvent
	Observation *protocolv1alpha2.Observation

	TurnToolView tool.TurnToolView

	Transcript []model.Message
}

func NewEngine(config EngineConfig) Engine {
	return Engine{config: config.WithDefaults()}
}

func (e Engine) Build(input BuildInput) (BuildResult, error) {
	if err := validateEngineInput(input); err != nil {
		return BuildResult{}, err
	}
	currentTask, err := projectTask(input.SessionKey, input.TurnToolView.RuntimeContext())
	if err != nil {
		return BuildResult{}, err
	}

	bounds := projectionBoundsFromEngineConfig(e.config)
	currentTime := currentGameTimeFromEventObservation(input.Event, input.Observation)
	var recentMemory []MemoryProjection
	var recentMemoryReport RetentionReport
	var history HistoryProjection
	var historyReport HistoryReport
	if input.History == nil {
		recentMemory, recentMemoryReport = projectRecentMemories(input.RecentMemories, e.config.MaxRecentMemoryRecords, e.config.MaxRecentMemoryTokens, currentTime, bounds)
	} else {
		history, historyReport = projectHistory(*input.History, input.SessionKey, currentTime)
	}
	transcript, transcriptReport, err := projectCurrentTurnTranscript(input.Transcript, bounds, e.config.MaxTranscriptTokens)
	if err != nil {
		report := ContextBuildReport{
			EffectiveBudget: e.config,
			Transcript:      transcriptReport,
		}
		if errors.Is(err, ErrBudgetExceeded) {
			report.addReason(ReasonTranscriptBudgetExceeded)
			report.addReason(ReasonRequiredContextOverBudget)
		}
		return BuildResult{Report: report}, err
	}
	projection := ContextProjection{
		SessionKey:               input.SessionKey,
		CanonicalTarget:          input.CanonicalTarget,
		AgentDescriptor:          input.AgentDescriptor,
		GameDefinition:           copyGameDefinition(input.GameDefinition),
		AgentDefinition:          copyAgentDefinition(input.AgentDefinition),
		RuntimePolicy:            input.RuntimePolicy,
		Instruction:              defaultAuthorityInstruction,
		CurrentEvent:             projectCurrentEvent(input.Event, input.CanonicalTarget),
		CurrentEventContextFacts: projectCurrentEventContextFacts(input.Event.GetContextFacts()),
		CurrentObservation:       projectCurrentObservation(input.Observation),
		Task:                     currentTask,
		RecentMemory:             recentMemory,
		History:                  history,
		historyEnabled:           input.History != nil,
		retrievalEnabled:         input.Retrieval != nil,
		Tools:                    input.TurnToolView.Available(),
		CurrentTurnTranscript:    transcript,
	}
	if input.History != nil {
		projection.Instruction = historyAuthorityInstruction
	}
	report := newContextBuildReport(projection, input, e.config, recentMemoryReport, transcriptReport)
	report.History = historyReport
	projection, report, err = applyProjectionBudgets(projection, e.config, report)
	if input.Retrieval != nil {
		// Select against the actual final history and spend only remaining request
		// capacity, so retrieval cannot displace current context or causal groups.
		if err == nil {
			projection.RetrievedHistory, report.RetrievedHistory, err = projectRetrievedHistory(*input.Retrieval, input.SessionKey, currentTime, projection.History, func(retrieved RetrievedHistoryProjection) (bool, error) {
				candidate := projection
				candidate.RetrievedHistory = retrieved
				return projectionFitsRequestBudget(candidate, e.config)
			})
		} else {
			report.RetrievedHistory = RetrievedHistoryReport{DroppedMatches: len(input.Retrieval.Matches), Diagnostics: append([]string(nil), input.Retrieval.Diagnostics...)}
			if errors.Is(err, ErrBudgetExceeded) {
				report.RetrievedHistory.diagnose(ReasonRetrievedHistoryBudgetExceeded)
			}
		}
		refreshRetrievedHistoryReport(projection, &report)
	}
	if err != nil {
		return BuildResult{Projection: projection, Report: report}, err
	}
	size, err := measureProjectionRequest(projection)
	if err != nil {
		return BuildResult{Projection: projection, Report: report}, err
	}
	report = report.WithFinalRequestSize(size)
	return BuildResult{
		Projection: projection,
		Report:     report,
	}, nil
}

func newContextBuildReport(projection ContextProjection, input BuildInput, budget BudgetConfig, recentMemory RetentionReport, transcript RetentionReport) ContextBuildReport {
	report := ContextBuildReport{
		EffectiveBudget:         budget,
		Sections:                sectionReportsForProjection(projection),
		GameDefinitionFallback:  input.GameDefinition == nil,
		AgentDefinitionFallback: input.CanonicalTarget.GetDefinitionId() != "" && input.AgentDefinition == nil,
		RecentMemory:            recentMemory,
		Transcript:              transcript,
	}
	if report.GameDefinitionFallback || report.AgentDefinitionFallback {
		report.addReason(ReasonDefinitionFallback)
	}
	if report.RecentMemory.DroppedCount > 0 {
		report.addReason(ReasonMemoryBudgetExceeded)
	}
	if report.Transcript.DroppedCount > 0 {
		report.addReason(ReasonTranscriptBudgetExceeded)
	}
	return report
}

func sectionReportsForProjection(projection ContextProjection) SectionReports {
	sections := SectionReports{
		{Name: "runtime_policy", Included: projection.RuntimePolicy != "", ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.RuntimePolicy)},
		{Name: "instruction", Included: renderAuthorityInstruction(projection) != "", ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(renderAuthorityInstruction(projection))},
		{Name: "agent_descriptor", Included: true, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.AgentDescriptor)},
		{Name: "game_definition", Included: projection.GameDefinition != nil, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.GameDefinition)},
		{Name: "agent_definition", Included: projection.AgentDefinition != nil, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.AgentDefinition)},
		{Name: "current_event", Included: projection.CurrentEvent.EventID != "", ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.CurrentEvent)},
		{Name: "current_event_context_facts", Included: len(projection.CurrentEventContextFacts) > 0, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.CurrentEventContextFacts)},
		{Name: "current_observation", Included: projection.CurrentObservation.EntityID != "", ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.CurrentObservation)},
		{Name: "recent_memory", Included: len(projection.RecentMemory) > 0, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.RecentMemory)},
		{Name: "tools", Included: len(projection.Tools) > 0, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.Tools)},
		{Name: "current_turn_transcript", Included: len(projection.CurrentTurnTranscript) > 0, ProjectionEstimatedTokens: sectionProjectionEstimatedTokens(projection.CurrentTurnTranscript)},
	}
	if projection.historyEnabled || projection.History.SummaryText != "" || len(projection.History.Sources) > 0 {
		sections = append(sections, SectionReport{Name: "history", Included: projection.History.SummaryText != "" || len(projection.History.Sources) > 0, ProjectionEstimatedTokens: estimateHistoryProjection(projection.History)})
	}
	if projection.retrievalEnabled || len(projection.RetrievedHistory.Snippets) > 0 {
		sections = append(sections, SectionReport{Name: "retrieved_history", Included: len(projection.RetrievedHistory.Snippets) > 0, ProjectionEstimatedTokens: estimateRetrievedHistoryProjection(projection.RetrievedHistory)})
	}
	return sections
}

func applyProjectionBudgets(projection ContextProjection, budget BudgetConfig, report ContextBuildReport) (ContextProjection, ContextBuildReport, error) {
	sectionCropped := map[string]string{}
	for _, diagnostic := range report.History.Diagnostics {
		if diagnostic == ReasonHistoryBudgetExceeded {
			report.addReason(ReasonHistoryBudgetExceeded)
			sectionCropped["history"] = ReasonHistoryBudgetExceeded
		}
	}
	if report.RecentMemory.DroppedCount > 0 {
		sectionCropped["recent_memory"] = ReasonMemoryBudgetExceeded
	}
	if report.Transcript.DroppedCount > 0 {
		sectionCropped["current_turn_transcript"] = ReasonTranscriptBudgetExceeded
	}
	var err error
	projection, definitionCrop, definitionErr := applyDefinitionBudget(projection, budget)
	if definitionCrop.Agent {
		report.addReason(ReasonDefinitionBudgetExceeded)
		sectionCropped["agent_definition"] = ReasonDefinitionBudgetExceeded
	}
	if definitionCrop.Game {
		report.addReason(ReasonDefinitionBudgetExceeded)
		sectionCropped["game_definition"] = ReasonDefinitionBudgetExceeded
	}
	if definitionErr != nil {
		report.addReason(ReasonDefinitionBudgetExceeded)
		report.addReason(ReasonRequiredContextOverBudget)
		err = definitionErr
	}
	if budget.MaxEventTokens > 0 && sectionProjectionEstimatedTokens(projection.CurrentEvent) > budget.MaxEventTokens {
		projection.CurrentEvent.Payload = truncationMap("current event payload exceeded token limit")
		report.addReason(ReasonEventBudgetExceeded)
		sectionCropped["current_event"] = ReasonEventBudgetExceeded
	}
	if budget.MaxObservationTokens > 0 && sectionProjectionEstimatedTokens(projection.CurrentObservation) > budget.MaxObservationTokens {
		projection.CurrentObservation.NearbyEntities = nil
		projection.CurrentObservation.Extensions = nil
		if len(projection.CurrentObservation.State) > 0 {
			projection.CurrentObservation.State = truncationMap("current observation state exceeded token limit")
		}
		report.addReason(ReasonObservationBudgetExceeded)
		sectionCropped["current_observation"] = ReasonObservationBudgetExceeded
	}
	if budget.MaxContextFactsTokens > 0 && sectionProjectionEstimatedTokens(projection.CurrentEventContextFacts) > budget.MaxContextFactsTokens {
		dropContextFactOptionalFields(&projection)
		report.addReason(ReasonContextFactsBudgetExceeded)
		sectionCropped["current_event_context_facts"] = ReasonContextFactsBudgetExceeded
	}
	projection, sectionErr := enforceRequiredSectionBudgets(projection, budget, &report, sectionCropped)
	if sectionErr != nil {
		err = sectionErr
	}
	if err == nil {
		projection, report, err = enforceGlobalRequestBudget(projection, budget, report, sectionCropped)
	}
	report.Sections = markCroppedSections(sectionReportsForProjection(projection), sectionCropped)
	return projection, report, err
}

func enforceRequiredSectionBudgets(projection ContextProjection, budget BudgetConfig, report *ContextBuildReport, sectionCropped map[string]string) (ContextProjection, error) {
	var err error
	if budget.MaxEventTokens > 0 && sectionProjectionEstimatedTokens(projection.CurrentEvent) > budget.MaxEventTokens {
		if dropEventPayload(&projection) {
			report.addReason(ReasonEventBudgetExceeded)
			sectionCropped["current_event"] = ReasonEventBudgetExceeded
		}
		if sectionProjectionEstimatedTokens(projection.CurrentEvent) > budget.MaxEventTokens {
			report.addReason(ReasonEventBudgetExceeded)
			report.addReason(ReasonRequiredContextOverBudget)
			report.addReason(ReasonRequiredSectionOverBudget)
			sectionCropped["current_event"] = ReasonEventBudgetExceeded
			err = firstError(err, fmt.Errorf("%w: current event required shell exceeds token budget", ErrBudgetExceeded))
		}
	}
	if budget.MaxObservationTokens > 0 && sectionProjectionEstimatedTokens(projection.CurrentObservation) > budget.MaxObservationTokens {
		if dropObservationOptionalFields(&projection) {
			report.addReason(ReasonObservationBudgetExceeded)
			sectionCropped["current_observation"] = ReasonObservationBudgetExceeded
		}
		if sectionProjectionEstimatedTokens(projection.CurrentObservation) > budget.MaxObservationTokens {
			report.addReason(ReasonObservationBudgetExceeded)
			report.addReason(ReasonRequiredContextOverBudget)
			report.addReason(ReasonRequiredSectionOverBudget)
			sectionCropped["current_observation"] = ReasonObservationBudgetExceeded
			err = firstError(err, fmt.Errorf("%w: current observation required minimum exceeds token budget", ErrBudgetExceeded))
		}
	}
	if budget.MaxContextFactsTokens > 0 && sectionProjectionEstimatedTokens(projection.CurrentEventContextFacts) > budget.MaxContextFactsTokens {
		if dropContextFactOptionalFields(&projection) {
			report.addReason(ReasonContextFactsBudgetExceeded)
			sectionCropped["current_event_context_facts"] = ReasonContextFactsBudgetExceeded
		}
		if sectionProjectionEstimatedTokens(projection.CurrentEventContextFacts) > budget.MaxContextFactsTokens {
			report.addReason(ReasonContextFactsBudgetExceeded)
			report.addReason(ReasonRequiredContextOverBudget)
			report.addReason(ReasonRequiredSectionOverBudget)
			sectionCropped["current_event_context_facts"] = ReasonContextFactsBudgetExceeded
			err = firstError(err, fmt.Errorf("%w: current event context facts required minimum exceeds token budget", ErrBudgetExceeded))
		}
	}
	return projection, err
}

func enforceGlobalRequestBudget(projection ContextProjection, budget BudgetConfig, report ContextBuildReport, sectionCropped map[string]string) (ContextProjection, ContextBuildReport, error) {
	fits, err := projectionFitsRequestBudget(projection, budget)
	if err != nil {
		return projection, report, err
	}
	if fits {
		return projection, report, nil
	}

	for {
		fits, err := projectionFitsRequestBudget(projection, budget)
		if err != nil {
			return projection, report, err
		}
		if fits {
			return projection, report, nil
		}
		switch {
		case dropRetrievedHistoryDisplay(&projection, &report):
			report.addReason(ReasonRetrievedHistoryBudgetExceeded)
			sectionCropped["retrieved_history"] = ReasonRetrievedHistoryBudgetExceeded
		case dropOldestHistoryDisplay(&projection, &report):
			report.addReason(ReasonHistoryBudgetExceeded)
			sectionCropped["history"] = ReasonHistoryBudgetExceeded
		case dropOldestTranscriptGroup(&projection, &report):
			report.addReason(ReasonTranscriptBudgetExceeded)
			sectionCropped["current_turn_transcript"] = ReasonTranscriptBudgetExceeded
		case dropOldestRecentMemory(&projection):
			report.RecentMemory.RetainedCount = len(projection.RecentMemory)
			report.RecentMemory.DroppedCount++
			report.addReason(ReasonMemoryBudgetExceeded)
			sectionCropped["recent_memory"] = ReasonMemoryBudgetExceeded
		case dropContextFactOptionalFields(&projection):
			report.addReason(ReasonContextFactsBudgetExceeded)
			sectionCropped["current_event_context_facts"] = ReasonContextFactsBudgetExceeded
		case dropEventPayload(&projection):
			report.addReason(ReasonEventBudgetExceeded)
			sectionCropped["current_event"] = ReasonEventBudgetExceeded
		case dropObservationOptionalFields(&projection):
			report.addReason(ReasonObservationBudgetExceeded)
			sectionCropped["current_observation"] = ReasonObservationBudgetExceeded
		case dropGameDefinitionOptionalFields(&projection):
			report.addReason(ReasonDefinitionBudgetExceeded)
			sectionCropped["game_definition"] = ReasonDefinitionBudgetExceeded
		case dropAgentDefinitionOptionalFields(&projection):
			report.addReason(ReasonDefinitionBudgetExceeded)
			sectionCropped["agent_definition"] = ReasonDefinitionBudgetExceeded
		default:
			size, err := measureProjectionRequest(projection)
			if err != nil {
				return projection, report, err
			}
			report.addReason(ReasonRequiredContextOverBudget)
			report = report.WithFinalRequestSize(size)
			if RequiredRequestSectionExceedsBudget(size, budget) {
				report.addReason(ReasonRequiredSectionOverBudget)
			}
			return projection, report, RequestTokenBudgetError(size, budget)
		}
	}
}

func projectionFitsRequestBudget(projection ContextProjection, budget BudgetConfig) (bool, error) {
	size, err := measureProjectionRequest(projection)
	if err != nil {
		return false, err
	}
	return !RequestEstimatedTokensExceedBudget(size, budget), nil
}

func measureProjectionRequest(projection ContextProjection) (RequestTokenSummary, error) {
	req, err := NewRenderer().Render(projection)
	if err != nil {
		return RequestTokenSummary{}, err
	}
	return EstimateRequestTokens(req)
}

func dropOldestRecentMemory(projection *ContextProjection) bool {
	if len(projection.RecentMemory) == 0 {
		return false
	}
	projection.RecentMemory = append([]MemoryProjection(nil), projection.RecentMemory[1:]...)
	return true
}

func dropOldestTranscriptGroup(projection *ContextProjection, report *ContextBuildReport) bool {
	if len(projection.CurrentTurnTranscript) == 0 {
		return false
	}
	groups, err := transcriptCausalGroups(projection.CurrentTurnTranscript)
	if err != nil || len(groups) <= 1 {
		return false
	}
	dropped := len(groups[0].messages)
	retained := flattenTranscriptGroups(groups[1:])
	report.Transcript.DroppedCount += dropped
	report.Transcript.RetainedCount = len(retained)
	projection.CurrentTurnTranscript = retained
	return true
}

func dropContextFactOptionalFields(projection *ContextProjection) bool {
	droppedAttributes := false
	for i := range projection.CurrentEventContextFacts {
		if len(projection.CurrentEventContextFacts[i].Attributes) > 0 {
			projection.CurrentEventContextFacts[i].Attributes = nil
			droppedAttributes = true
		}
	}
	if droppedAttributes {
		return true
	}

	changed := false
	for i := range projection.CurrentEventContextFacts {
		if projection.CurrentEventContextFacts[i].Text != "" && projection.CurrentEventContextFacts[i].Text != truncatedMarker {
			projection.CurrentEventContextFacts[i].Text = truncatedMarker
			changed = true
		}
	}
	return changed
}

func dropEventPayload(projection *ContextProjection) bool {
	if len(projection.CurrentEvent.Payload) == 0 {
		return false
	}
	projection.CurrentEvent.Payload = nil
	return true
}

func dropObservationOptionalFields(projection *ContextProjection) bool {
	changed := len(projection.CurrentObservation.NearbyEntities) > 0 ||
		len(projection.CurrentObservation.Extensions) > 0 ||
		len(projection.CurrentObservation.State) > 0
	projection.CurrentObservation.NearbyEntities = nil
	projection.CurrentObservation.Extensions = nil
	projection.CurrentObservation.State = nil
	return changed
}

func dropGameDefinitionOptionalFields(projection *ContextProjection) bool {
	minimal := minimalGameDefinition(projection.GameDefinition)
	if gameDefinitionsEqual(projection.GameDefinition, minimal) {
		return false
	}
	projection.GameDefinition = minimal
	return true
}

func dropAgentDefinitionOptionalFields(projection *ContextProjection) bool {
	minimal := minimalAgentDefinition(projection.AgentDefinition)
	if agentDefinitionsEqual(projection.AgentDefinition, minimal) {
		return false
	}
	projection.AgentDefinition = minimal
	return true
}

func firstError(existing error, candidate error) error {
	if existing != nil {
		return existing
	}
	return candidate
}

type definitionBudgetCrop struct {
	Agent bool
	Game  bool
}

func applyDefinitionBudget(projection ContextProjection, budget BudgetConfig) (ContextProjection, definitionBudgetCrop, error) {
	if budget.MaxDefinitionTokens <= 0 || (projection.AgentDefinition == nil && projection.GameDefinition == nil) {
		return projection, definitionBudgetCrop{}, nil
	}

	sourceAgent := projection.AgentDefinition
	sourceGame := projection.GameDefinition
	agent := minimalAgentDefinition(sourceAgent)
	game := minimalGameDefinition(sourceGame)
	crop := definitionBudgetCrop{}

	projection.AgentDefinition = agent
	projection.GameDefinition = game
	if definitionBudgetEstimatedTokens(agent, game) > budget.MaxDefinitionTokens {
		crop.Agent = sourceAgent != nil && !agentDefinitionsEqual(sourceAgent, agent)
		crop.Game = sourceGame != nil && !gameDefinitionsEqual(sourceGame, game)
		return projection, crop, fmt.Errorf("%w: definition required minimum exceeds token budget", ErrBudgetExceeded)
	}

	if sourceAgent != nil {
		fillAgentDefinitionWithinBudget(&agent, game, sourceAgent, budget.MaxDefinitionTokens)
	}
	if sourceGame != nil {
		fillGameDefinitionWithinBudget(agent, &game, sourceGame, budget.MaxDefinitionTokens)
	}
	projection.AgentDefinition = agent
	projection.GameDefinition = game
	crop.Agent = sourceAgent != nil && !agentDefinitionsEqual(sourceAgent, agent)
	crop.Game = sourceGame != nil && !gameDefinitionsEqual(sourceGame, game)
	return projection, crop, nil
}

func minimalAgentDefinition(agent *definition.AgentDefinition) *definition.AgentDefinition {
	if agent == nil {
		return nil
	}
	return &definition.AgentDefinition{
		SchemaVersion: agent.SchemaVersion,
		GameID:        agent.GameID,
		DefinitionID:  agent.DefinitionID,
	}
}

func minimalGameDefinition(game *definition.GameDefinition) *definition.GameDefinition {
	if game == nil {
		return nil
	}
	return &definition.GameDefinition{
		SchemaVersion: game.SchemaVersion,
		GameID:        game.GameID,
	}
}

func fillAgentDefinitionWithinBudget(agent **definition.AgentDefinition, game *definition.GameDefinition, source *definition.AgentDefinition, limit int) bool {
	if source.Identity != "" {
		if !tryUpdateAgentDefinition(agent, game, limit, func(candidate *definition.AgentDefinition) {
			candidate.Identity = source.Identity
		}) {
			return true
		}
	}
	if appendAgentDefinitionItems(agent, game, limit, source.Personality, func(candidate *definition.AgentDefinition, item string) {
		candidate.Personality = append(candidate.Personality, item)
	}) {
		return true
	}
	if appendAgentDefinitionItems(agent, game, limit, source.SpeechStyle, func(candidate *definition.AgentDefinition, item string) {
		candidate.SpeechStyle = append(candidate.SpeechStyle, item)
	}) {
		return true
	}
	if appendAgentDefinitionItems(agent, game, limit, source.Preferences, func(candidate *definition.AgentDefinition, item string) {
		candidate.Preferences = append(candidate.Preferences, item)
	}) {
		return true
	}
	if appendAgentDefinitionItems(agent, game, limit, source.BehaviorGuidelines, func(candidate *definition.AgentDefinition, item string) {
		candidate.BehaviorGuidelines = append(candidate.BehaviorGuidelines, item)
	}) {
		return true
	}
	if source.SourceVersion != "" {
		if !tryUpdateAgentDefinition(agent, game, limit, func(candidate *definition.AgentDefinition) {
			candidate.SourceVersion = source.SourceVersion
		}) {
			return true
		}
	}
	return false
}

func fillGameDefinitionWithinBudget(agent *definition.AgentDefinition, game **definition.GameDefinition, source *definition.GameDefinition, limit int) bool {
	if source.Title != "" {
		if !tryUpdateGameDefinition(agent, game, limit, func(candidate *definition.GameDefinition) {
			candidate.Title = source.Title
		}) {
			return true
		}
	}
	if source.Summary != "" {
		if !tryUpdateGameDefinition(agent, game, limit, func(candidate *definition.GameDefinition) {
			candidate.Summary = source.Summary
		}) {
			return true
		}
	}
	if appendGameDefinitionItems(agent, game, limit, source.WorldRules, func(candidate *definition.GameDefinition, item string) {
		candidate.WorldRules = append(candidate.WorldRules, item)
	}) {
		return true
	}
	if appendGameDefinitionItems(agent, game, limit, source.Lore, func(candidate *definition.GameDefinition, item string) {
		candidate.Lore = append(candidate.Lore, item)
	}) {
		return true
	}
	if appendGameDefinitionItems(agent, game, limit, source.NarrativeConstraints, func(candidate *definition.GameDefinition, item string) {
		candidate.NarrativeConstraints = append(candidate.NarrativeConstraints, item)
	}) {
		return true
	}
	if source.SourceVersion != "" {
		if !tryUpdateGameDefinition(agent, game, limit, func(candidate *definition.GameDefinition) {
			candidate.SourceVersion = source.SourceVersion
		}) {
			return true
		}
	}
	return false
}

func appendAgentDefinitionItems(
	agent **definition.AgentDefinition,
	game *definition.GameDefinition,
	limit int,
	items []string,
	appendItem func(*definition.AgentDefinition, string),
) bool {
	for _, item := range items {
		if !tryUpdateAgentDefinition(agent, game, limit, func(candidate *definition.AgentDefinition) {
			appendItem(candidate, item)
		}) {
			return true
		}
	}
	return false
}

func appendGameDefinitionItems(
	agent *definition.AgentDefinition,
	game **definition.GameDefinition,
	limit int,
	items []string,
	appendItem func(*definition.GameDefinition, string),
) bool {
	for _, item := range items {
		if !tryUpdateGameDefinition(agent, game, limit, func(candidate *definition.GameDefinition) {
			appendItem(candidate, item)
		}) {
			return true
		}
	}
	return false
}

func tryUpdateAgentDefinition(agent **definition.AgentDefinition, game *definition.GameDefinition, limit int, update func(*definition.AgentDefinition)) bool {
	if *agent == nil {
		return false
	}
	candidate := copyAgentDefinition(*agent)
	update(candidate)
	if definitionBudgetEstimatedTokens(candidate, game) > limit {
		return false
	}
	*agent = candidate
	return true
}

func tryUpdateGameDefinition(agent *definition.AgentDefinition, game **definition.GameDefinition, limit int, update func(*definition.GameDefinition)) bool {
	if *game == nil {
		return false
	}
	candidate := copyGameDefinition(*game)
	update(candidate)
	if definitionBudgetEstimatedTokens(agent, candidate) > limit {
		return false
	}
	*game = candidate
	return true
}

func definitionBudgetEstimatedTokens(agent *definition.AgentDefinition, game *definition.GameDefinition) int {
	total := 0
	if agent != nil {
		total += sectionProjectionEstimatedTokens(agent)
	}
	if game != nil {
		total += sectionProjectionEstimatedTokens(game)
	}
	return total
}

func agentDefinitionsEqual(left, right *definition.AgentDefinition) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.SchemaVersion == right.SchemaVersion &&
		left.GameID == right.GameID &&
		left.DefinitionID == right.DefinitionID &&
		left.Identity == right.Identity &&
		stringSlicesEqual(left.Personality, right.Personality) &&
		stringSlicesEqual(left.SpeechStyle, right.SpeechStyle) &&
		stringSlicesEqual(left.Preferences, right.Preferences) &&
		stringSlicesEqual(left.BehaviorGuidelines, right.BehaviorGuidelines) &&
		left.SourceVersion == right.SourceVersion
}

func gameDefinitionsEqual(left, right *definition.GameDefinition) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.SchemaVersion == right.SchemaVersion &&
		left.GameID == right.GameID &&
		left.Title == right.Title &&
		left.Summary == right.Summary &&
		stringSlicesEqual(left.WorldRules, right.WorldRules) &&
		stringSlicesEqual(left.Lore, right.Lore) &&
		stringSlicesEqual(left.NarrativeConstraints, right.NarrativeConstraints) &&
		left.SourceVersion == right.SourceVersion
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func markCroppedSections(sections SectionReports, cropped map[string]string) SectionReports {
	if len(cropped) == 0 {
		return sections
	}
	for i := range sections {
		if reason, ok := cropped[sections[i].Name]; ok {
			sections[i].Cropped = true
			sections[i].Reason = reason
		}
	}
	return sections
}

func truncationMap(message string) map[string]any {
	return map[string]any{"_truncated": message}
}

func sectionProjectionEstimatedTokens(value any) int {
	tokens, err := tokenestimate.EstimateStableJSON(value)
	if err != nil {
		panic(fmt.Sprintf("estimate projection tokens: %v", err))
	}
	return tokens
}

func (r *ContextBuildReport) addReason(reason string) {
	if reason == "" {
		return
	}
	for _, existing := range r.ReasonCodes {
		if existing == reason {
			return
		}
	}
	r.ReasonCodes = append(r.ReasonCodes, reason)
}

func validateEngineInput(input BuildInput) error {
	if input.Event == nil {
		return fmt.Errorf("%w: event is required", ErrInvalidInput)
	}
	if input.Observation == nil {
		return fmt.Errorf("%w: observation is required", ErrInvalidInput)
	}
	if input.CanonicalTarget == nil {
		return fmt.Errorf("%w: canonical target is required", ErrInvalidInput)
	}
	if input.RuntimePolicy == "" {
		return fmt.Errorf("%w: runtime policy is required", ErrInvalidInput)
	}
	if input.SessionKey.GameID == "" {
		return fmt.Errorf("%w: session key game_id is required", ErrInvalidInput)
	}
	if input.SessionKey.WorldID == "" {
		return fmt.Errorf("%w: session key world_id is required", ErrInvalidInput)
	}
	if input.SessionKey.EntityID == "" {
		return fmt.Errorf("%w: session key entity_id is required", ErrInvalidInput)
	}
	if input.CanonicalTarget.GetEntityId() != input.SessionKey.EntityID {
		return fmt.Errorf("%w: canonical target entity_id does not match session key", ErrInvalidInput)
	}
	if input.AgentDescriptor.SessionKey != input.SessionKey {
		return fmt.Errorf("%w: agent descriptor session key does not match session key", ErrInvalidInput)
	}
	if input.AgentDescriptor.DefinitionID != input.CanonicalTarget.GetDefinitionId() {
		return fmt.Errorf("%w: agent descriptor definition_id does not match canonical target", ErrInvalidInput)
	}
	eventWorldID := strings.TrimSpace(input.Event.GetWorldId())
	eventTargetEntityID := strings.TrimSpace(input.Event.GetTargetEntityId())
	if eventWorldID != input.SessionKey.WorldID {
		return fmt.Errorf("%w: event world_id does not match session key", ErrInvalidInput)
	}
	if eventTargetEntityID != input.SessionKey.EntityID {
		return fmt.Errorf("%w: event target_entity_id does not match session key", ErrInvalidInput)
	}
	if input.Observation.GetWorldId() != input.SessionKey.WorldID {
		return fmt.Errorf("%w: observation world_id does not match session key", ErrInvalidInput)
	}
	if input.Observation.GetEntityId() != input.SessionKey.EntityID {
		return fmt.Errorf("%w: observation entity_id does not match session key", ErrInvalidInput)
	}
	if input.GameDefinition != nil && input.GameDefinition.GameID != input.SessionKey.GameID {
		return fmt.Errorf("%w: game definition game_id does not match session key", ErrInvalidInput)
	}
	if input.CanonicalTarget.GetDefinitionId() == "" && input.AgentDefinition != nil {
		return fmt.Errorf("%w: agent definition must be nil when canonical target definition_id is empty", ErrInvalidInput)
	}
	if input.AgentDefinition != nil {
		if input.AgentDefinition.GameID != input.SessionKey.GameID {
			return fmt.Errorf("%w: agent definition game_id does not match session key", ErrInvalidInput)
		}
		if input.AgentDefinition.DefinitionID != input.AgentDescriptor.DefinitionID {
			return fmt.Errorf("%w: agent definition definition_id does not match agent descriptor", ErrInvalidInput)
		}
		if input.AgentDefinition.DefinitionID != input.CanonicalTarget.GetDefinitionId() {
			return fmt.Errorf("%w: agent definition definition_id does not match canonical target", ErrInvalidInput)
		}
	}
	return nil
}

func projectCurrentEvent(event *protocolv1alpha2.GameEvent, canonicalTarget *protocolv1alpha2.EntityRef) EventProjection {
	payload := map[string]any(nil)
	if event.GetPayload() != nil {
		payload = copyMap(event.GetPayload().AsMap())
	}

	return EventProjection{
		EventID:         event.GetEventId(),
		EventType:       event.GetEventType(),
		WorldID:         strings.TrimSpace(event.GetWorldId()),
		TargetEntityID:  strings.TrimSpace(event.GetTargetEntityId()),
		Sequence:        event.GetSequence(),
		GameTime:        event.GetGameTime(),
		CanonicalTarget: canonicalTarget,
		Payload:         payload,
	}
}

func projectCurrentEventContextFacts(facts []*protocolv1alpha2.ContextFact) []ContextFactProjection {
	if len(facts) == 0 {
		return nil
	}

	out := make([]ContextFactProjection, 0, len(facts))
	for _, fact := range facts {
		if fact == nil {
			continue
		}
		attributes := map[string]any(nil)
		if fact.GetAttributes() != nil {
			attributes = copyMap(fact.GetAttributes().AsMap())
		}
		out = append(out, ContextFactProjection{
			Kind:           strings.TrimSpace(fact.GetKind()),
			ActorEntityID:  strings.TrimSpace(fact.GetActorEntityId()),
			TargetEntityID: strings.TrimSpace(fact.GetTargetEntityId()),
			ScopeID:        strings.TrimSpace(fact.GetScopeId()),
			Text:           strings.TrimSpace(fact.GetText()),
			Label:          strings.TrimSpace(fact.GetLabel()),
			Attributes:     attributes,
		})
	}
	return out
}

func projectCurrentObservation(observation *protocolv1alpha2.Observation) ObservationProjection {
	state := map[string]any(nil)
	if observation.GetState() != nil {
		state = copyMap(observation.GetState().AsMap())
	}
	extensions := map[string]any(nil)
	if observation.GetExtensions() != nil {
		extensions = copyMap(observation.GetExtensions().AsMap())
	}

	return ObservationProjection{
		WorldID:        observation.GetWorldId(),
		EntityID:       observation.GetEntityId(),
		Revision:       observation.GetRevision(),
		GameTime:       observation.GetGameTime(),
		NearbyEntities: copyEntityRefs(observation.GetNearbyEntities()),
		Extensions:     extensions,
		State:          state,
	}
}

func copyEntityRefs(refs []*protocolv1alpha2.EntityRef) []*protocolv1alpha2.EntityRef {
	if len(refs) == 0 {
		return nil
	}

	out := make([]*protocolv1alpha2.EntityRef, 0, len(refs))
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		out = append(out, &protocolv1alpha2.EntityRef{
			EntityId:     strings.TrimSpace(ref.GetEntityId()),
			EntityType:   strings.TrimSpace(ref.GetEntityType()),
			DisplayName:  strings.TrimSpace(ref.GetDisplayName()),
			DefinitionId: strings.TrimSpace(ref.GetDefinitionId()),
		})
	}
	return out
}

func copyGameDefinition(game *definition.GameDefinition) *definition.GameDefinition {
	if game == nil {
		return nil
	}
	out := *game
	out.WorldRules = append([]string(nil), game.WorldRules...)
	out.Lore = append([]string(nil), game.Lore...)
	out.NarrativeConstraints = append([]string(nil), game.NarrativeConstraints...)
	return &out
}

func copyAgentDefinition(agent *definition.AgentDefinition) *definition.AgentDefinition {
	if agent == nil {
		return nil
	}
	out := *agent
	out.Personality = append([]string(nil), agent.Personality...)
	out.SpeechStyle = append([]string(nil), agent.SpeechStyle...)
	out.Preferences = append([]string(nil), agent.Preferences...)
	out.BehaviorGuidelines = append([]string(nil), agent.BehaviorGuidelines...)
	return &out
}

func copyToolCalls(calls []model.ToolCall) []model.ToolCall {
	if len(calls) == 0 {
		return nil
	}

	out := make([]model.ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		out[i].Arguments = copyMap(call.Arguments)
	}
	return out
}

func copyToolResults(results []model.ToolResult) []model.ToolResult {
	if len(results) == 0 {
		return nil
	}

	out := make([]model.ToolResult, len(results))
	for i, result := range results {
		out[i] = result
		out[i].Output = copyMap(result.Output)
	}
	return out
}

func copyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}

	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
