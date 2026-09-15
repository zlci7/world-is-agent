package context

import (
	"encoding/json"
	"fmt"
	"slices"

	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tokenestimate"
	"gameagent/runtime/internal/tool"
)

// defaultMaxTaskContextTokens bounds the task block on its own. The block still
// counts inside the user message and request budgets; this cap never raises them.
const defaultMaxTaskContextTokens = 2048

// maxProjectedTaskResults matches the fixed return bound of the task service, so a
// projection cannot publish more results than the contract allows.
const maxProjectedTaskResults = 3

type TaskProjection struct {
	TaskID        string                 `json:"task_id"`
	Instruction   string                 `json:"instruction"`
	State         task.State             `json:"state"`
	Revision      uint64                 `json:"revision"`
	NextWakeupAt  *int64                 `json:"next_wakeup_at"`
	DeadlineAt    int64                  `json:"deadline_at"`
	WakeReason    string                 `json:"wake_reason,omitempty"`
	WakeDueAt     int64                  `json:"wake_due_at,omitempty"`
	WakeID        string                 `json:"wake_id,omitempty"`
	Reason        string                 `json:"reason,omitempty"`
	Progress      json.RawMessage        `json:"progress,omitempty"`
	Recovery      *TaskRecovery          `json:"recovery,omitempty"`
	RecentResults []TaskResultProjection `json:"recent_results,omitempty"`
	Contract      json.RawMessage        `json:"contract,omitempty"`
}

// TaskRecovery states why this task is not making progress and which authority its
// facts belong to. The reason itself stays in TaskProjection.Reason.
type TaskRecovery struct {
	NeedsReconcile      bool     `json:"needs_reconcile,omitempty"`
	ExecutionGeneration uint64   `json:"execution_generation,omitempty"`
	RevalidatedFactIDs  []string `json:"revalidated_fact_ids,omitempty"`
}

// TaskResultProjection is a committed result: what the world confirmed, when, and
// the facts it rests on. It is never a model progress note.
type TaskResultProjection struct {
	ResultID     string     `json:"result_id"`
	TaskID       string     `json:"task_id"`
	State        task.State `json:"state"`
	Reason       string     `json:"reason"`
	OccurredAt   int64      `json:"occurred_at"`
	EvidenceRefs []string   `json:"evidence_refs,omitempty"`
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
	if len(r.Progress) > 0 {
		projection.Progress = append(json.RawMessage(nil), r.Progress...)
	}
	if recovery := projectTaskRecovery(r, rc); recovery != nil {
		projection.Recovery = recovery
	}
	for _, result := range rc.RecentResults {
		if len(projection.RecentResults) == maxProjectedTaskResults {
			break
		}
		projection.RecentResults = append(projection.RecentResults, projectTaskResult(result))
	}
	return projection, nil
}

func projectTaskRecovery(record *task.Record, rc *tool.RuntimeCallContext) *TaskRecovery {
	recovery := TaskRecovery{NeedsReconcile: record.NeedsReconcile, ExecutionGeneration: rc.Execution.Binding.Generation}
	for _, evidence := range record.Evidence {
		if evidence.RevalidatedIn != nil {
			recovery.RevalidatedFactIDs = append(recovery.RevalidatedFactIDs, evidence.FactID)
		}
	}
	if !recovery.NeedsReconcile && len(recovery.RevalidatedFactIDs) == 0 {
		return nil
	}
	return &recovery
}

func projectTaskResult(result task.Result) TaskResultProjection {
	return TaskResultProjection{
		ResultID: result.ID, TaskID: result.TaskID, State: result.State, Reason: result.Reason,
		OccurredAt: result.OccurredAt, EvidenceRefs: slices.Clone(result.EvidenceRefs),
	}
}

func renderTask(projection *TaskProjection) string {
	if projection == nil {
		return ""
	}
	return "[Task Context]\n" + renderJSON(projection) + "\n\n"
}

// TaskContextReport records what the task block cost and what had to go.
type TaskContextReport struct {
	EstimatedTokens int
	Cropped         bool
	DroppedProgress bool
	DroppedResults  int
	DroppedDetail   bool
}

type taskContextCrop struct {
	projection *TaskProjection
	report     TaskContextReport
}

// boundTaskContext fits the task block into its own budget. Cropping follows the
// declared order: progress note, older results, optional result detail. Task
// identity, state, deadline, recovery and the contract are never cropped, so a block
// whose required fields alone exceed the budget fails instead of misleading the model.
func boundTaskContext(projection *TaskProjection, budget int) (taskContextCrop, error) {
	if projection == nil {
		return taskContextCrop{}, nil
	}
	budget = positiveOrDefault(budget, defaultMaxTaskContextTokens)
	crop := taskContextCrop{projection: cloneTaskProjection(projection)}
	crop.report.EstimatedTokens = estimateTaskProjection(crop.projection)
	if crop.report.EstimatedTokens <= budget {
		return crop, nil
	}
	crop.report.Cropped = true
	if len(crop.projection.Progress) > 0 {
		crop.projection.Progress = nil
		crop.report.DroppedProgress = true
		if crop.report.EstimatedTokens = estimateTaskProjection(crop.projection); crop.report.EstimatedTokens <= budget {
			return crop, nil
		}
	}
	for len(crop.projection.RecentResults) > 1 {
		crop.projection.RecentResults = crop.projection.RecentResults[:len(crop.projection.RecentResults)-1]
		crop.report.DroppedResults++
		if crop.report.EstimatedTokens = estimateTaskProjection(crop.projection); crop.report.EstimatedTokens <= budget {
			return crop, nil
		}
	}
	if len(crop.projection.RecentResults) == 1 && len(crop.projection.RecentResults[0].EvidenceRefs) > 0 {
		crop.projection.RecentResults[0].EvidenceRefs = nil
		crop.report.DroppedDetail = true
		if crop.report.EstimatedTokens = estimateTaskProjection(crop.projection); crop.report.EstimatedTokens <= budget {
			return crop, nil
		}
	}
	return crop, fmt.Errorf("%w: task context required fields exceed token budget", ErrBudgetExceeded)
}

func estimateTaskProjection(projection *TaskProjection) int {
	return tokenestimate.EstimateText(renderTask(projection))
}

// cloneTaskProjection detaches the published block from the record it was read from.
func cloneTaskProjection(projection *TaskProjection) *TaskProjection {
	cloned := *projection
	if projection.NextWakeupAt != nil {
		tick := *projection.NextWakeupAt
		cloned.NextWakeupAt = &tick
	}
	cloned.Progress = slices.Clone(projection.Progress)
	cloned.Contract = slices.Clone(projection.Contract)
	cloned.RecentResults = slices.Clone(projection.RecentResults)
	for i := range cloned.RecentResults {
		cloned.RecentResults[i].EvidenceRefs = slices.Clone(cloned.RecentResults[i].EvidenceRefs)
	}
	if projection.Recovery != nil {
		recovery := *projection.Recovery
		recovery.RevalidatedFactIDs = slices.Clone(projection.Recovery.RevalidatedFactIDs)
		cloned.Recovery = &recovery
	}
	return &cloned
}
