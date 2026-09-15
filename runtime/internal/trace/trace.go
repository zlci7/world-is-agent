package trace

import (
	"context"
	"time"
)

type EventName string

const (
	EventTurnStarted                EventName = "turn_started"
	EventAgentStepStarted           EventName = "agent_step_started"
	EventAgentStepCompleted         EventName = "agent_step_completed"
	EventAgentStepFailed            EventName = "agent_step_failed"
	EventObservationRequested       EventName = "observation_requested"
	EventObservationReceived        EventName = "observation_received"
	EventContextLoaded              EventName = "context_loaded"
	EventContextLoadFailed          EventName = "context_load_failed"
	EventContextRequestBuilt        EventName = "context_request_built"
	EventContextRequestBuildFailed  EventName = "context_request_build_failed"
	EventTaskContextPrepared        EventName = "task_context_prepared"
	EventModelRequestStarted        EventName = "model_request_started"
	EventModelResponseReceived      EventName = "model_response_received"
	EventToolCallSelected           EventName = "tool_call_selected"
	EventToolBatchStarted           EventName = "tool_batch_started"
	EventToolBatchCompleted         EventName = "tool_batch_completed"
	EventToolBatchFailed            EventName = "tool_batch_failed"
	EventActionSubmitStarted        EventName = "action_submit_started"
	EventActionStatusUpdateReceived EventName = "action_status_update_received"
	EventActionResultReceived       EventName = "action_result_received"
	EventContextUpdated             EventName = "context_updated"
	EventContextUpdateFailed        EventName = "context_update_failed"
	EventTurnCompletionSent         EventName = "turn_completion_sent"
	EventTurnCompletionSendFailed   EventName = "turn_completion_send_failed"
	EventTurnSuspended              EventName = "turn_suspended"
	EventTurnResumed                EventName = "turn_resumed"
	EventTurnSettled                EventName = "turn_settled"
	EventTurnCompleted              EventName = "turn_completed"
	EventTurnFailed                 EventName = "turn_failed"
)

type Fields map[string]any

type EventData struct {
	ActionID string
	Tool     string
	Fields   Fields
}

type TurnContext struct {
	GameID    string
	WorldID   string
	SessionID string
	EventID   string
	EventType string
	EntityID  string
}

type Event struct {
	SchemaVersion int       `json:"schema_version"`
	TraceID       string    `json:"trace_id"`
	TurnID        string    `json:"turn_id"`
	Seq           uint32    `json:"seq"`
	Event         EventName `json:"event"`
	Time          time.Time `json:"time"`
	ElapsedMS     int64     `json:"elapsed_ms"`

	GameID    string `json:"game_id,omitempty"`
	WorldID   string `json:"world_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
	EntityID  string `json:"entity_id,omitempty"`

	ActionID string `json:"action_id,omitempty"`
	Tool     string `json:"tool,omitempty"`

	Stage        string `json:"stage,omitempty"`
	Reason       string `json:"reason,omitempty"`
	ErrorMessage string `json:"error,omitempty"`

	Fields Fields `json:"fields,omitempty"`
}

type Recorder interface {
	Record(event Event)
	Close(ctx context.Context) error
}
