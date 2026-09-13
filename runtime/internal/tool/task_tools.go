package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/task"
)

type TaskWorld interface {
	Current() (task.Head, uint64, bool)
	Guard(task.Binding, uint64, func(task.Head) error) error
}

const createTaskSchema = `{"type":"object","properties":{"proposal_ref":{"type":"string","minLength":1},"instruction":{"type":"string","minLength":1,"maxLength":2048}},"required":["proposal_ref","instruction"],"additionalProperties":false}`
const updateTaskSchema = `{"type":"object","properties":{"task_id":{"type":"string","minLength":1},"intent":{"type":"string","enum":["wait","cancel"]},"next_wakeup_at":{"type":"integer"},"progress_note":{"type":"string","maxLength":2048},"reason":{"type":"string","maxLength":512}},"required":["task_id","intent"],"additionalProperties":false}`

// TaskTools owns references for one Turn. The Loop serializes its calls.
type TaskTools struct {
	service   *task.Service
	world     TaskWorld
	authority RuntimeCallContext
	proposals map[string]taskProposal
	creates   map[string]taskCreateResponse
	closed    bool
}

type taskCreateResponse struct {
	arguments string
	result    task.CreateResult
}

func NewTaskTools(service *task.Service, world TaskWorld, authority RuntimeCallContext) *TaskTools {
	return &TaskTools{service: service, world: world, authority: cloneRuntimeCallContext(authority), proposals: make(map[string]taskProposal), creates: make(map[string]taskCreateResponse)}
}

func (t *TaskTools) Close() { t.closed = true; clear(t.proposals); clear(t.creates) }

func validTaskSource(rc RuntimeCallContext) bool {
	exec := rc.Execution
	if exec.Validate() != nil || strings.TrimSpace(exec.Source.EventID) == "" || strings.TrimSpace(exec.Source.TurnID) == "" {
		return false
	}
	switch exec.Source.Kind {
	case task.SourceKindInteraction:
		return strings.TrimSpace(rc.InteractionSourceID) != "" && exec.TaskID == "" && exec.WakeID == ""
	case task.SourceKindTaskWake:
		return strings.TrimSpace(exec.TaskID) != "" && strings.TrimSpace(exec.WakeID) != "" && rc.InteractionSourceID == ""
	default:
		return false
	}
}

func (t *TaskTools) checkContext(rc RuntimeCallContext) error {
	if t == nil || t.closed || t.service == nil || t.world == nil {
		return task.ErrWorldNotReady
	}
	a, b := t.authority, rc
	if !validTaskSource(rc) || a.Execution.Owner != b.Execution.Owner || a.Execution.Binding != b.Execution.Binding ||
		a.Execution.Source.Kind != b.Execution.Source.Kind || a.Execution.Source.EventID != b.Execution.Source.EventID || a.Execution.Source.TurnID != b.Execution.Source.TurnID ||
		a.Execution.TaskID != b.Execution.TaskID || a.Execution.WakeID != b.Execution.WakeID || a.InteractionSourceID != b.InteractionSourceID || a.AuthorityEpoch != b.AuthorityEpoch {
		return task.ErrSourceInvalid
	}
	return nil
}

func (t *TaskTools) guard(ctx context.Context, rc RuntimeCallContext, fn func(task.ExecutionContext) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.checkContext(rc); err != nil {
		return err
	}
	return t.world.Guard(rc.Execution.Binding, rc.AuthorityEpoch, func(head task.Head) error {
		if head.Clock.ID != t.authority.Execution.Clock.ID {
			return task.ErrClockMismatch
		}
		if head.Clock.Tick < t.authority.Execution.Clock.Tick || head.Clock.Sequence < t.authority.Execution.Clock.Sequence {
			return task.ErrClockRewound
		}
		exec := t.authority.Execution
		exec.Clock = head.Clock
		return fn(exec)
	})
}

func (t *TaskTools) Entries(rc RuntimeCallContext) []Entry {
	if t.checkContext(rc) != nil {
		return nil
	}
	head, epoch, ready := t.world.Current()
	if !ready || head.Binding != rc.Execution.Binding || epoch != rc.AuthorityEpoch || head.Clock.ID != rc.Execution.Clock.ID {
		clear(t.proposals)
		return nil
	}
	entries := []Entry{}
	if rc.Execution.Source.Kind == task.SourceKindInteraction && len(t.proposals) > 0 {
		entries = append(entries, t.entry("create_task", "Create the agreed task using a proposal_ref from a successful tool result. Confirm the task only after this tool succeeds.", createTaskSchema))
	}
	if record := rc.ObservedTask; record != nil && record.Owner == rc.Execution.Owner && activeTask(record.State) && (rc.Execution.Source.Kind == task.SourceKindInteraction || record.ID == rc.Execution.TaskID) {
		entries = append(entries, t.entry("update_task", "Update the task shown in Task Context. A player interaction may cancel it; a task wake may wait or cancel its own task.", updateTaskSchema))
	}
	return entries
}

func (t *TaskTools) entry(name, description, schema string) Entry {
	return Entry{Definition: model.ToolDefinition{Name: name, Description: description, InputSchema: schema}, Kind: KindRuntime, Execution: ExecutionSync, Concurrency: ConcurrencySequential, Policy: ToolPolicy{ExclusivePerStep: true}, Executor: t}
}

func activeTask(state task.State) bool {
	return state == task.StateWaiting || state == task.StateRunning || state == task.StatePaused
}

func (t *TaskTools) Execute(ctx context.Context, rc RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
	switch call.Name {
	case "create_task":
		if ValidateArguments(createTaskSchema, call.Arguments) != nil || !validTaskText(call.Arguments["instruction"], 2048, true) || !validTaskText(call.Arguments["proposal_ref"], 0, true) {
			return invalidTaskArguments(call), nil
		}
		var created task.CreateResult
		err := t.guard(ctx, rc, func(exec task.ExecutionContext) error {
			if exec.Source.Kind != task.SourceKindInteraction {
				return task.ErrSourceInvalid
			}
			proposal, ok := t.proposals[call.Arguments["proposal_ref"].(string)]
			if !ok {
				return task.ErrSourceInvalid
			}
			if proposal.Clock.ID != exec.Clock.ID {
				return task.ErrClockMismatch
			}
			arguments, err := json.Marshal(call.Arguments)
			if err != nil {
				return errTaskArguments
			}
			if prior, ok := t.creates[call.ID]; ok {
				if prior.arguments != string(arguments) {
					return task.ErrIdempotencyConflict
				}
				created = prior.result
				return nil
			}
			if exec.Clock.Tick >= proposal.WakeAt || proposal.WakeAt > proposal.DeadlineAt {
				return errTaskArguments
			}
			exec.Source.CallID = call.ID
			contract, err := json.Marshal(proposal)
			if err != nil {
				return task.ErrInvalidTaskSpec
			}
			spec := task.TaskSpec{Instruction: call.Arguments["instruction"].(string), ClockID: proposal.Clock.ID, WakeAt: proposal.WakeAt, DeadlineAt: proposal.DeadlineAt, EquivalenceKey: proposal.EquivalenceKey, Contract: contract, ResultContract: task.ResultContractAuthoritativeEvidence, Source: exec.Source}
			created, err = t.service.Create(ctx, exec, spec, task.Admission{MaxActivePerOwner: 1})
			if err == nil {
				t.creates[call.ID] = taskCreateResponse{arguments: string(arguments), result: created}
			}
			return err
		})
		if err != nil {
			return taskToolError(call, err)
		}
		result := taskSuccess(call, created.Task)
		result.Output["created"] = created.Created
		return result, nil
	case "update_task":
		intent, taskID, err := decodeTaskIntent(call.Arguments)
		if err != nil {
			return invalidTaskArguments(call), nil
		}
		var record task.Record
		err = t.guard(ctx, rc, func(exec task.ExecutionContext) error {
			observed := rc.ObservedTask
			if observed == nil || observed.Owner != exec.Owner || observed.ID != taskID || (exec.Source.Kind == task.SourceKindTaskWake && taskID != exec.TaskID) {
				return task.ErrSourceInvalid
			}
			if exec.Source.Kind == task.SourceKindInteraction && intent.Kind != "cancel" {
				return task.ErrSourceInvalid
			}
			if observed.Spec.ClockID != exec.Clock.ID {
				return task.ErrClockMismatch
			}
			if intent.Kind == "wait" && (*intent.NextWakeAt <= exec.Clock.Tick || *intent.NextWakeAt > observed.Spec.DeadlineAt) {
				return errTaskArguments
			}
			exec.TaskID, exec.ExpectedRevision, exec.Source.CallID = taskID, observed.Revision, call.ID
			var err error
			record, err = t.service.ApplyIntent(ctx, exec, intent)
			return err
		})
		if err != nil {
			return taskToolError(call, err)
		}
		return taskSuccess(call, record), nil
	default:
		return invalidTaskArguments(call), nil
	}
}

func decodeTaskIntent(args map[string]any) (task.Intent, string, error) {
	if ValidateArguments(updateTaskSchema, args) != nil || !validTaskText(args["task_id"], 0, true) {
		return task.Intent{}, "", errTaskArguments
	}
	for key, max := range map[string]int{"progress_note": 2048, "reason": 512} {
		if value, ok := args[key]; ok && !validTaskText(value, max, false) {
			return task.Intent{}, "", errTaskArguments
		}
	}
	intent := task.Intent{Kind: args["intent"].(string)}
	switch intent.Kind {
	case "wait":
		if _, ok := args["reason"]; ok {
			return task.Intent{}, "", errTaskArguments
		}
		value, ok := args["next_wakeup_at"]
		if !ok {
			return task.Intent{}, "", errTaskArguments
		}
		data, err := json.Marshal(value)
		if err != nil {
			return task.Intent{}, "", errTaskArguments
		}
		var tick int64
		if json.Unmarshal(data, &tick) != nil {
			return task.Intent{}, "", errTaskArguments
		}
		intent.NextWakeAt = &tick
		intent.ProgressNote, _ = args["progress_note"].(string)
	case "cancel":
		if _, ok := args["next_wakeup_at"]; ok {
			return task.Intent{}, "", errTaskArguments
		}
		if _, ok := args["progress_note"]; ok {
			return task.Intent{}, "", errTaskArguments
		}
		intent.Reason, _ = args["reason"].(string)
	}
	if intent.Validate() != nil {
		return task.Intent{}, "", errTaskArguments
	}
	return intent, args["task_id"].(string), nil
}

func validTaskText(value any, max int, required bool) bool {
	text, ok := value.(string)
	return ok && (!required || strings.TrimSpace(text) != "") && (max <= 0 || utf8.RuneCountInString(text) <= max)
}

var errTaskArguments = errors.New("tool_arguments_invalid")

func invalidTaskArguments(call model.ToolCall) model.ToolResult {
	return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "invalid", Code: "tool_arguments_invalid", Message: "tool_arguments_invalid"}
}

func taskSuccess(call model.ToolCall, record task.Record) model.ToolResult {
	var wake any
	if record.NextWakeAt != nil {
		wake = *record.NextWakeAt
	}
	return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "succeeded", Code: "task_updated", Message: "task_updated", Output: map[string]any{"task_id": record.ID, "revision": record.Revision, "state": string(record.State), "next_wakeup_at": wake, "deadline_at": record.Spec.DeadlineAt}}
}

// TaskExecutionError exposes a stable diagnostic and preserves safe cancellation identity.
type TaskExecutionError struct {
	code  string
	cause error
}

func SanitizeTaskError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &TaskExecutionError{code: "task_cancelled", cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &TaskExecutionError{code: "task_timeout", cause: context.DeadlineExceeded}
	}
	var typed *task.Error
	if errors.As(err, &typed) && typed.Code.Valid() {
		return &TaskExecutionError{code: string(typed.Code)}
	}
	var safe *TaskExecutionError
	if errors.As(err, &safe) {
		return safe
	}
	return &TaskExecutionError{code: "task_execution_failed"}
}

func (e *TaskExecutionError) Error() string { return e.code }
func (e *TaskExecutionError) Unwrap() error { return e.cause }

func taskToolError(call model.ToolCall, err error) (model.ToolResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return model.ToolResult{}, SanitizeTaskError(err)
	}
	if errors.Is(err, errTaskArguments) {
		return invalidTaskArguments(call), nil
	}
	var typed *task.Error
	if errors.As(err, &typed) && typed.Code.Valid() {
		if typed.Technical() {
			return model.ToolResult{}, SanitizeTaskError(err)
		}
		code := string(typed.Code)
		switch typed.Code {
		case task.CodeTaskNotFound, task.CodeTaskConflict, task.CodeTaskChanged, task.CodeIdempotencyConflict, task.CodeTaskTerminal, task.CodeTaskCapacityExceeded:
			return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "rejected", Code: code, Message: code}, nil
		default:
			return model.ToolResult{}, SanitizeTaskError(err)
		}
	}
	return model.ToolResult{}, SanitizeTaskError(err)
}
