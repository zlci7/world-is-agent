package memory

import (
	"time"

	"gameagent/runtime/internal/session"
)

type Record struct {
	MemoryID string

	ProjectionKind     ProjectionKind
	ProjectionVersion  int
	ProjectionBatchKey string

	SessionKey session.AgentSessionKey

	SourceTurnID        string
	SourceEventID       string
	SourceEventSequence uint64

	EventType string

	GameTime *GameTimeSnapshot

	SourceContextFacts []SourceContextFact
	Outcomes           []TurnOutcome

	CreatedAt time.Time
}

type ProjectionKind string

const (
	ProjectionKindSettledTurn            ProjectionKind = "settled_turn"
	ProjectionKindPriorSuccessfulActions ProjectionKind = "prior_successful_actions"

	ProjectionVersionRecentV1 = 1
	ProjectionVersionRecentV2 = 2
)

type GameTimeSnapshot struct {
	Year   int32
	Season int32
	Day    int32
	Hour   int32
	Minute int32
	Tick   int64

	PresentFields uint8 `json:",omitempty"`
}

type SourceContextFact struct {
	Kind           string
	ActorEntityID  string
	TargetEntityID string
	ScopeID        string
	Text           string
	Label          string
	Attributes     map[string]any
}

type TurnOutcome struct {
	ToolName      string
	ToolArguments map[string]any

	ActionStatus string
}
