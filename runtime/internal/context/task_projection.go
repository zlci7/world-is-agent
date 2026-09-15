package context

import (
	"encoding/json"

	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type TaskProjection struct {
	TaskID       string          `json:"task_id"`
	Instruction  string          `json:"instruction"`
	State        task.State      `json:"state"`
	Revision     uint64          `json:"revision"`
	NextWakeupAt *int64          `json:"next_wakeup_at"`
	DeadlineAt   int64           `json:"deadline_at"`
	WakeReason   string          `json:"wake_reason,omitempty"`
	WakeDueAt    int64           `json:"wake_due_at,omitempty"`
	WakeID       string          `json:"wake_id,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Contract     json.RawMessage `json:"contract,omitempty"`
}

func projectTask(owner session.AgentSessionKey, rc *tool.RuntimeCallContext) (*TaskProjection, error) {
	if rc == nil || rc.ObservedTask == nil {
		return nil, nil
	}
	r := rc.ObservedTask
	if r.Owner != owner {
		return nil, ErrInvalidInput
	}
	projection := &TaskProjection{TaskID: r.ID, Instruction: r.Spec.Instruction, State: r.State, Revision: r.Revision, DeadlineAt: r.Spec.DeadlineAt}
	if r.NextWakeAt != nil {
		tick := *r.NextWakeAt
		projection.NextWakeupAt = &tick
	}
	if rc.Execution.Source.Kind == task.SourceKindTaskWake {
		projection.WakeReason = rc.Execution.WakeReason
		projection.WakeDueAt = rc.Execution.WakeDueAt
		projection.WakeID = rc.Execution.WakeID
	}
	if r.State == task.StatePaused {
		projection.Reason = r.PauseReason
	}
	if len(r.Spec.Contract) > 0 {
		projection.Contract = append(json.RawMessage(nil), r.Spec.Contract...)
	}
	return projection, nil
}

func renderTask(projection *TaskProjection) string {
	if projection == nil {
		return ""
	}
	return "[Task Context]\n" + renderJSON(projection) + "\n\n"
}
