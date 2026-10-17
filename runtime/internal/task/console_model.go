package task

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

var (
	ErrConsoleQuery           = errors.New("invalid_task_query")
	ErrConsoleSnapshotChanged = errors.New("task_snapshot_changed")
	ErrConsoleBudget          = errors.New("task_query_budget_exceeded")
)

const ConsoleResponseBytes = 256 << 10
const consoleReadBytes = 16 << 20

type ConsoleQuery struct {
	InstanceID, GameID, WorldID, Collection, State, Cursor, EntityID, TaskID string
	Limit                                                                    int
}

type ConsoleWorkingSet struct {
	RunID        string `json:"run_id"`
	Generation   string `json:"generation"`
	CheckpointID string `json:"checkpoint_id"`
}

type ConsoleWorld struct {
	WorldID           string            `json:"world_id"`
	WorkingSet        ConsoleWorkingSet `json:"working_set"`
	Availability      string            `json:"availability"`
	ReasonCode        string            `json:"reason_code,omitempty"`
	Head              Head              `json:"-"`
	SaveBarrierActive bool              `json:"-"`
}

type ConsoleWorlds struct {
	InstanceID string         `json:"instance_id"`
	GameID     string         `json:"game_id"`
	Supported  bool           `json:"supported"`
	ObservedAt time.Time      `json:"observed_at"`
	Worlds     []ConsoleWorld `json:"worlds"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type ConsoleResult struct {
	State      State  `json:"state"`
	Reason     string `json:"reason"`
	OccurredAt int64  `json:"occurred_at,string"`
}

type ConsoleFacts struct {
	OperationCount        int              `json:"operation_count"`
	ProgressEvidenceCount int              `json:"progress_evidence_count"`
	Outcome               string           `json:"outcome,omitempty"`
	OutcomeGameTime       *ConsoleGameTime `json:"outcome_game_time,omitempty"`
}

type ConsoleGameTime struct {
	Year   *int32 `json:"year,omitempty"`
	Season *int32 `json:"season,omitempty"`
	Day    *int32 `json:"day,omitempty"`
	Hour   *int32 `json:"hour,omitempty"`
	Minute *int32 `json:"minute,omitempty"`
	Tick   *int64 `json:"tick,omitempty,string"`
}

type ConsoleAdvanced struct {
	ClockID      string `json:"clock_id"`
	WakeTick     int64  `json:"wake_tick,string"`
	DeadlineTick int64  `json:"deadline_tick,string"`
	NextWakeTick *int64 `json:"next_wake_tick,omitempty,string"`
}

type ConsoleTask struct {
	TaskID            string           `json:"task_id"`
	EntityID          string           `json:"entity_id"`
	EntityDisplayName string           `json:"entity_display_name,omitempty"`
	Instruction       string           `json:"instruction"`
	State             State            `json:"state"`
	NeedsReconcile    bool             `json:"needs_reconcile"`
	Revision          uint64           `json:"revision,string"`
	CreatedAt         time.Time        `json:"created_at"`
	PauseReasonCode   string           `json:"pause_reason_code,omitempty"`
	Result            *ConsoleResult   `json:"result,omitempty"`
	CleanupStatus     string           `json:"cleanup_status"`
	CleanupReasons    []string         `json:"cleanup_reasons,omitempty"`
	Facts             *ConsoleFacts    `json:"facts,omitempty"`
	Advanced          *ConsoleAdvanced `json:"advanced,omitempty"`
}

type ConsolePage struct {
	InstanceID string            `json:"instance_id"`
	GameID     string            `json:"game_id"`
	WorldID    string            `json:"world_id"`
	Supported  bool              `json:"supported"`
	ObservedAt time.Time         `json:"observed_at"`
	WorkingSet ConsoleWorkingSet `json:"working_set"`
	World      ConsoleWorld      `json:"world"`
	Counts     map[State]int     `json:"counts"`
	Items      []ConsoleTask     `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func projectConsoleTask(record Record, detail bool) ConsoleTask {
	item := ConsoleTask{TaskID: record.ID, EntityID: record.Owner.EntityID, Instruction: record.Spec.Instruction,
		State: record.State, NeedsReconcile: record.NeedsReconcile, Revision: record.Revision,
		CreatedAt: time.UnixMilli(record.CreatedAtUnixMS).UTC(), PauseReasonCode: record.PauseReason,
		CleanupStatus: "not_required"}
	cleanup := make(map[string]string)
	for _, value := range record.Cleanup {
		cleanup[value.OperationID] = value.Status
		if value.Reason != "" {
			item.CleanupReasons = append(item.CleanupReasons, value.Reason)
		}
	}
	for _, op := range record.Operations {
		if op.Status == OperationStatusNotSent {
			continue
		}
		if item.CleanupStatus == "not_required" {
			item.CleanupStatus = "confirmed"
		}
		switch cleanup[op.ID] {
		case CleanupStatusReleased, CleanupStatusHandedOff:
		case CleanupStatusUnconfirmed:
			item.CleanupStatus = "unconfirmed"
		default:
			if item.CleanupStatus != "unconfirmed" {
				item.CleanupStatus = "pending"
			}
		}
	}
	if record.Result != nil {
		item.Result = &ConsoleResult{State: record.Result.State, Reason: record.Result.Reason, OccurredAt: record.Result.OccurredAt}
	}
	if detail {
		item.Facts = &ConsoleFacts{OperationCount: len(record.Operations)}
		for _, evidence := range record.Evidence {
			if !evidence.Applied {
				continue
			}
			if evidence.Kind == EvidenceKindProgress {
				item.Facts.ProgressEvidenceCount++
			}
			if record.Result != nil {
				for _, ref := range record.Result.EvidenceRefs {
					if ref == evidence.FactID {
						item.Facts.Outcome = evidence.Kind
					}
				}
			}
		}
		if record.Result != nil && len(record.Result.Source.GameTime) > 0 {
			var gameTime ConsoleGameTime
			var raw map[string]json.RawMessage
			if json.Unmarshal(record.Result.Source.GameTime, &raw) == nil {
				if tick, ok := raw["tick"]; ok {
					var number json.Number
					if json.Unmarshal(tick, &number) == nil {
						if value, err := strconv.ParseInt(string(number), 10, 64); err == nil {
							gameTime.Tick = &value
						}
					}
					delete(raw, "tick")
				}
				data, _ := json.Marshal(raw)
				_ = json.Unmarshal(data, &gameTime)
			}
			if gameTime.Year != nil || gameTime.Season != nil || gameTime.Day != nil || gameTime.Hour != nil || gameTime.Minute != nil || gameTime.Tick != nil {
				item.Facts.OutcomeGameTime = &gameTime
			}
		}
		item.Advanced = &ConsoleAdvanced{ClockID: record.Spec.ClockID, WakeTick: record.Spec.WakeAt, DeadlineTick: record.Spec.DeadlineAt, NextWakeTick: record.NextWakeAt}
	}
	return item
}
