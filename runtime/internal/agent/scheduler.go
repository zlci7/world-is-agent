package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tool"
)

const (
	toolResultStatusSucceeded   = "succeeded"
	toolResultStatusInvalid     = "invalid"
	toolResultStatusSkipped     = "skipped"
	toolResultStatusRejected    = "rejected"
	toolResultStatusFailed      = "failed"
	toolResultStatusCancelled   = "cancelled"
	toolResultStatusInterrupted = "interrupted"

	toolResultCodeActionSucceeded        = "action_succeeded"
	toolResultCodeBatchValidationFailed  = "batch_validation_failed"
	toolResultCodeBatchAborted           = "batch_aborted"
	toolResultCodePriorGroupFailed       = "prior_group_failed"
	toolResultCodeDuplicateToolCallID    = "duplicate_tool_call_id"
	toolResultCodeToolNotRegistered      = "tool_not_registered"
	toolResultCodeToolArgumentsMissing   = "tool_arguments_missing"
	toolResultCodeToolArgumentsInvalid   = "tool_arguments_invalid"
	toolResultCodeActionRequestInvalid   = "action_request_invalid"
	toolResultCodeNonTerminalActionState = "non_terminal_action_status"
	toolResultCodeExclusiveToolBatch     = "exclusive_tool_must_be_only_tool_call"
	toolResultCodeAsyncBatchUnsupported  = "async_batch_unsupported"
	toolResultCodeAsyncActionLimit       = "async_action_limit_exceeded"
	toolResultCodeActionStartRejected    = "action_start_rejected"
)

type toolBatchScheduler struct {
	view                 tool.TurnToolView
	maxParallelToolCalls int
	actionTimeout        time.Duration
	actionStartTimeout   time.Duration
	asyncActionTimeout   time.Duration
	asyncActionLimitFull bool
	sourceEventID        string
	sourceTurnID         string
	onActionSubmit       func(plannedToolCall)
	onActionStatusUpdate func(plannedToolCall, *protocolv1alpha2.ActionStatusUpdate)
	onActionResult       func(plannedToolCall, *protocolv1alpha2.ActionResult)
}

type toolBatchOutcome struct {
	Results                []model.ToolResult
	SuccessfulActions      []completedToolAction
	Executions             []memory.HistoryExecution
	HasModelVisibleFailure bool
	SettleAfterSuccess     bool
	AsyncActionStarted     bool
}

type completedToolAction struct {
	ToolCall     model.ToolCall
	ActionResult *protocolv1alpha2.ActionResult
	Policy       tool.ToolPolicy
}

type plannedToolCall struct {
	index   int
	call    model.ToolCall
	entry   tool.Entry
	request *protocolv1alpha2.ActionRequest
}

type toolExecutionResult struct {
	result       model.ToolResult
	actionResult *protocolv1alpha2.ActionResult
	execution    memory.HistoryExecution
	err          error
}

type parallelExecutionResult struct {
	item plannedToolCall
	toolExecutionResult
}

func (r *toolExecutionResult) captureHistory() {
	r.execution.ActionResult = r.actionResult
	if r.result.Status != "" {
		r.execution.RuntimeResult = &r.result
	}
	if r.err != nil {
		r.execution.RuntimeError = truncateMessage(r.err.Error(), 512)
		if r.execution.RuntimeError == "" {
			r.execution.RuntimeError = "action execution failed"
		}
	}
	r.execution = cloneHistoryExecution(r.execution)
}

func (s toolBatchScheduler) Run(
	ctx context.Context,
	env Environment,
	worldID string,
	entityID string,
	calls []model.ToolCall,
) (outcome toolBatchOutcome, runErr error) {
	outcome.Executions = make([]memory.HistoryExecution, len(calls))
	for i, call := range calls {
		outcome.Executions[i].Call = cloneHistoryCall(call)
	}
	defer func() {
		for i := range outcome.Executions {
			execution := &outcome.Executions[i]
			if execution.RuntimeResult != nil || execution.RuntimeError != "" {
				continue
			}
			if i < len(outcome.Results) && outcome.Results[i].Status != "" {
				result := outcome.Results[i]
				result.Output = cloneHistoryMap(result.Output)
				execution.RuntimeResult = &result
			} else if runErr != nil && !execution.Started {
				result := skippedToolResult(execution.Call, toolResultCodeBatchAborted, "batch stopped after technical error")
				execution.RuntimeResult = &result
			}
		}
	}()

	plan, validationResults, validationFailed := s.preflight(worldID, entityID, calls)
	if validationFailed {
		outcome.Results = validationResults
		outcome.HasModelVisibleFailure = true
		return outcome, nil
	}
	if len(plan) == 1 && plan[0].entry.Execution == tool.ExecutionAsync {
		executed := s.runAsyncOne(ctx, env, plan[0])
		outcome.Executions[0] = executed.execution
		if executed.err != nil {
			return outcome, executed.err
		}
		outcome.Results = []model.ToolResult{executed.result}
		outcome.AsyncActionStarted = true
		if executed.result.Status == toolResultStatusSucceeded {
			outcome.SuccessfulActions = []completedToolAction{{
				ToolCall:     plan[0].call,
				ActionResult: executed.actionResult,
				Policy:       plan[0].entry.Policy,
			}}
			outcome.SettleAfterSuccess = plan[0].entry.Policy.SettleAfterSuccess
		} else {
			outcome.HasModelVisibleFailure = true
		}
		return outcome, nil
	}

	outcome.Results = make([]model.ToolResult, len(calls))
	outcome.SuccessfulActions = make([]completedToolAction, 0, len(calls))
	for i := 0; i < len(plan); {
		if plan[i].entry.Concurrency == tool.ConcurrencyParallelSafe {
			end := i + 1
			for end < len(plan) && plan[end].entry.Concurrency == tool.ConcurrencyParallelSafe {
				end++
			}
			groupSuccessfulActions, failed, err := s.runParallelGroup(ctx, env, plan[i:end], outcome.Results, outcome.Executions)
			outcome.SuccessfulActions = append(outcome.SuccessfulActions, groupSuccessfulActions...)
			outcome.SettleAfterSuccess = outcome.SettleAfterSuccess || completedActionsShouldSettle(groupSuccessfulActions)
			if err != nil {
				return outcome, err
			}
			if failed {
				fillPriorGroupSkipped(outcome.Results, plan[end:])
				outcome.HasModelVisibleFailure = true
				return outcome, nil
			}
			i = end
			continue
		}

		executed := s.runOne(ctx, env, plan[i])
		outcome.Executions[plan[i].index] = executed.execution
		if executed.err != nil {
			return outcome, executed.err
		}
		outcome.Results[plan[i].index] = executed.result
		if executed.result.Status != toolResultStatusSucceeded {
			fillPriorGroupSkipped(outcome.Results, plan[i+1:])
			outcome.HasModelVisibleFailure = true
			return outcome, nil
		}
		action := completedToolAction{
			ToolCall:     plan[i].call,
			ActionResult: executed.actionResult,
			Policy:       plan[i].entry.Policy,
		}
		outcome.SuccessfulActions = append(outcome.SuccessfulActions, action)
		outcome.SettleAfterSuccess = outcome.SettleAfterSuccess || action.Policy.SettleAfterSuccess
		i++
	}

	return outcome, nil
}

func (s toolBatchScheduler) preflight(
	worldID string,
	entityID string,
	calls []model.ToolCall,
) ([]plannedToolCall, []model.ToolResult, bool) {
	duplicateIDs := duplicateToolCallIDs(calls)
	results := make([]model.ToolResult, len(calls))
	invalid := make([]bool, len(calls))
	plan := make([]plannedToolCall, 0, len(calls))
	hasFailure := false

	for i, call := range calls {
		if duplicateIDs[strings.TrimSpace(call.ID)] {
			results[i] = invalidToolResult(call, toolResultCodeDuplicateToolCallID, "duplicate tool call id")
			invalid[i] = true
			hasFailure = true
			continue
		}

		entry, ok := s.lookup(call.Name)
		if !ok {
			results[i] = invalidToolResult(call, toolResultCodeToolNotRegistered, fmt.Sprintf("tool %q is not registered", call.Name))
			invalid[i] = true
			hasFailure = true
			continue
		}
		if entry.Policy.ExclusivePerStep && len(calls) > 1 {
			results[i] = invalidToolResult(call, toolResultCodeExclusiveToolBatch, "tool with exclusive_per_step policy must be the only tool call in the model response")
			invalid[i] = true
			hasFailure = true
			continue
		}
		if entry.Execution == tool.ExecutionAsync && len(calls) > 1 {
			results[i] = invalidToolResult(call, toolResultCodeAsyncBatchUnsupported, "async tool must be the only tool call in the model response")
			invalid[i] = true
			hasFailure = true
			continue
		}
		if call.Arguments == nil {
			results[i] = invalidToolResult(call, toolResultCodeToolArgumentsMissing, "tool arguments are missing")
			invalid[i] = true
			hasFailure = true
			continue
		}
		if err := tool.ValidateArguments(entry.Definition.InputSchema, call.Arguments); err != nil {
			results[i] = invalidToolResult(call, toolResultCodeToolArgumentsInvalid, err.Error())
			invalid[i] = true
			hasFailure = true
			continue
		}

		actionRequest, err := tool.BuildActionRequest(tool.ActionRequestInput{
			WorldID:       worldID,
			EntityID:      entityID,
			SourceEventID: s.sourceEventID,
			SourceTurnID:  s.sourceTurnID,
			ToolCall:      call,
		})
		if err != nil {
			results[i] = invalidToolResult(call, toolResultCodeActionRequestInvalid, err.Error())
			invalid[i] = true
			hasFailure = true
			continue
		}
		if entry.Execution == tool.ExecutionAsync && s.asyncActionLimitFull {
			results[i] = invalidToolResult(call, toolResultCodeAsyncActionLimit, "async action limit exceeded for this turn")
			invalid[i] = true
			hasFailure = true
			continue
		}

		plan = append(plan, plannedToolCall{
			index:   i,
			call:    call,
			entry:   entry,
			request: actionRequest,
		})
	}

	if hasFailure {
		for i, call := range calls {
			if invalid[i] {
				continue
			}
			results[i] = skippedToolResult(call, toolResultCodeBatchValidationFailed, "batch validation failed")
		}
		return nil, results, true
	}

	return plan, nil, false
}

func (s toolBatchScheduler) lookup(name string) (tool.Entry, bool) {
	return s.view.Lookup(name)
}

func (s toolBatchScheduler) runParallelGroup(
	ctx context.Context,
	env Environment,
	group []plannedToolCall,
	results []model.ToolResult,
	executions []memory.HistoryExecution,
) ([]completedToolAction, bool, error) {
	groupCtx, cancelGroup := context.WithCancel(ctx)
	defer cancelGroup()

	limit := s.maxParallelToolCalls
	if limit <= 0 {
		limit = 1
	}

	resultCh := make(chan parallelExecutionResult, len(group))
	active := 0
	next := 0
	launchingStopped := false

	launch := func(item plannedToolCall) {
		active++
		go func() {
			resultCh <- parallelExecutionResult{
				item:                item,
				toolExecutionResult: s.runOne(groupCtx, env, item),
			}
		}()
	}

	for active < limit && next < len(group) {
		launch(group[next])
		next++
	}

	var firstErr error
	modelVisibleFailure := false
	successfulActionsByIndex := make(map[int]completedToolAction, len(group))
	for active > 0 {
		executed := <-resultCh
		active--
		executions[executed.item.index] = executed.execution

		if executed.err != nil {
			firstErr = preferTechnicalError(firstErr, executed.err)
			launchingStopped = true
			cancelGroup()
		} else {
			results[executed.item.index] = executed.result
			if executed.result.Status != toolResultStatusSucceeded {
				modelVisibleFailure = true
			} else {
				successfulActionsByIndex[executed.item.index] = completedToolAction{
					ToolCall:     executed.item.call,
					ActionResult: executed.actionResult,
					Policy:       executed.item.entry.Policy,
				}
			}
		}

		for !launchingStopped && active < limit && next < len(group) {
			launch(group[next])
			next++
		}
	}

	if firstErr != nil {
		return successfulActionsFromParallelGroup(group, successfulActionsByIndex), false, firstErr
	}
	return successfulActionsFromParallelGroup(group, successfulActionsByIndex), modelVisibleFailure, nil
}

func successfulActionsFromParallelGroup(group []plannedToolCall, actionsByIndex map[int]completedToolAction) []completedToolAction {
	successfulActions := make([]completedToolAction, 0, len(actionsByIndex))
	for _, item := range group {
		if action, ok := actionsByIndex[item.index]; ok {
			successfulActions = append(successfulActions, action)
		}
	}
	return successfulActions
}

func completedActionsShouldSettle(actions []completedToolAction) bool {
	for _, action := range actions {
		if action.Policy.SettleAfterSuccess {
			return true
		}
	}
	return false
}

func (s toolBatchScheduler) runOne(ctx context.Context, env Environment, item plannedToolCall) (executed toolExecutionResult) {
	executed.execution.Call = cloneHistoryCall(item.call)
	defer executed.captureHistory()
	if env == nil {
		executed.err = errors.New("environment is nil")
		return
	}

	actionCtx := ctx
	cancel := func() {}
	if s.actionTimeout > 0 {
		actionCtx, cancel = context.WithTimeout(ctx, s.actionTimeout)
	}
	defer cancel()

	if s.onActionSubmit != nil {
		s.onActionSubmit(item)
	}

	executed.execution.ActionID = item.request.GetActionId()
	executed.execution.Started = true
	executed.actionResult, executed.err = env.SubmitAction(actionCtx, item.request)
	if executed.err != nil {
		return
	}
	if executed.actionResult == nil {
		executed.err = errors.New("action result is nil")
		return
	}
	if s.onActionResult != nil {
		s.onActionResult(item, executed.actionResult)
	}

	executed.result, executed.err = toolResultFromActionResult(item.call, executed.actionResult)
	return
}

func (s toolBatchScheduler) runAsyncOne(ctx context.Context, env Environment, item plannedToolCall) (executed toolExecutionResult) {
	executed.execution.Call = cloneHistoryCall(item.call)
	defer executed.captureHistory()
	if env == nil {
		executed.err = errors.New("environment is nil")
		return
	}

	startCtx := ctx
	cancelStart := func() {}
	if s.actionStartTimeout > 0 {
		startCtx, cancelStart = context.WithTimeout(ctx, s.actionStartTimeout)
	}
	defer cancelStart()

	if s.onActionSubmit != nil {
		s.onActionSubmit(item)
	}

	executed.execution.ActionID = item.request.GetActionId()
	executed.execution.Started = true
	var start ActionStart
	start, executed.err = env.StartAction(startCtx, item.request)
	executed.actionResult = start.Result
	if executed.err != nil {
		return
	}
	if start.Result != nil {
		if s.onActionResult != nil {
			s.onActionResult(item, start.Result)
		}
		executed.result, executed.err = toolResultFromActionResultWithDefaultRejectedCode(item.call, start.Result, toolResultCodeActionStartRejected)
		return
	}
	if start.Update == nil {
		executed.err = errors.New("action start is missing status update or terminal result")
		return
	}
	if s.onActionStatusUpdate != nil {
		s.onActionStatusUpdate(item, start.Update)
	}

	waitCtx := ctx
	cancelWait := func() {}
	if s.asyncActionTimeout > 0 {
		waitCtx, cancelWait = context.WithTimeout(ctx, s.asyncActionTimeout)
	}
	defer cancelWait()

	executed.actionResult, executed.err = env.WaitActionResult(waitCtx, item.request.GetActionId())
	if executed.err != nil {
		return
	}
	if executed.actionResult == nil {
		executed.err = errors.New("action result is nil")
		return
	}
	if s.onActionResult != nil {
		s.onActionResult(item, executed.actionResult)
	}

	executed.result, executed.err = toolResultFromActionResult(item.call, executed.actionResult)
	return
}

func toolResultFromActionResult(call model.ToolCall, actionResult *protocolv1alpha2.ActionResult) (model.ToolResult, error) {
	switch actionResult.GetStatus() {
	case protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED:
		return model.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     toolResultStatusSucceeded,
			Code:       toolResultCodeActionSucceeded,
			Output:     actionResultOutput(actionResult),
		}, nil
	case protocolv1alpha2.ActionStatus_ACTION_STATUS_REJECTED:
		return terminalActionToolResult(call, actionResult, toolResultStatusRejected), nil
	case protocolv1alpha2.ActionStatus_ACTION_STATUS_FAILED:
		return terminalActionToolResult(call, actionResult, toolResultStatusFailed), nil
	case protocolv1alpha2.ActionStatus_ACTION_STATUS_CANCELLED:
		return terminalActionToolResult(call, actionResult, toolResultStatusCancelled), nil
	case protocolv1alpha2.ActionStatus_ACTION_STATUS_INTERRUPTED:
		return terminalActionToolResult(call, actionResult, toolResultStatusInterrupted), nil
	default:
		return model.ToolResult{}, fmt.Errorf("%s: %s", toolResultCodeNonTerminalActionState, actionResult.GetStatus().String())
	}
}

func toolResultFromActionResultWithDefaultRejectedCode(call model.ToolCall, actionResult *protocolv1alpha2.ActionResult, defaultRejectedCode string) (model.ToolResult, error) {
	if actionResult.GetStatus() != protocolv1alpha2.ActionStatus_ACTION_STATUS_REJECTED ||
		strings.TrimSpace(actionResult.GetError().GetCode()) != "" {
		return toolResultFromActionResult(call, actionResult)
	}
	result, err := toolResultFromActionResult(call, actionResult)
	if err != nil {
		return model.ToolResult{}, err
	}
	result.Code = defaultRejectedCode
	return result, nil
}

func terminalActionToolResult(call model.ToolCall, actionResult *protocolv1alpha2.ActionResult, status string) model.ToolResult {
	code := strings.TrimSpace(actionResult.GetError().GetCode())
	if code == "" {
		code = "action_" + status
	}
	return model.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     status,
		Code:       code,
		Message:    truncateMessage(actionResult.GetError().GetMessage(), 120),
		Output:     actionResultOutput(actionResult),
	}
}

func actionResultOutput(actionResult *protocolv1alpha2.ActionResult) map[string]any {
	if actionResult.GetOutput() == nil {
		return nil
	}
	return actionResult.GetOutput().AsMap()
}

func fillPriorGroupSkipped(results []model.ToolResult, plan []plannedToolCall) {
	for _, item := range plan {
		results[item.index] = skippedToolResult(item.call, toolResultCodePriorGroupFailed, "prior group failed")
	}
}

func invalidToolResult(call model.ToolCall, code string, message string) model.ToolResult {
	return model.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     toolResultStatusInvalid,
		Code:       code,
		Message:    truncateMessage(message, 120),
	}
}

func skippedToolResult(call model.ToolCall, code string, message string) model.ToolResult {
	return model.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     toolResultStatusSkipped,
		Code:       code,
		Message:    truncateMessage(message, 120),
	}
}

func duplicateToolCallIDs(calls []model.ToolCall) map[string]bool {
	counts := make(map[string]int, len(calls))
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		counts[id]++
	}

	duplicates := make(map[string]bool)
	for id, count := range counts {
		if count > 1 {
			duplicates[id] = true
		}
	}
	return duplicates
}

func preferTechnicalError(current error, candidate error) error {
	if current == nil {
		return candidate
	}
	if errors.Is(current, context.Canceled) && !errors.Is(candidate, context.Canceled) {
		return candidate
	}
	return current
}

func truncateMessage(message string, maxRunes int) string {
	message = strings.TrimSpace(message)
	if maxRunes <= 0 || utf8.RuneCountInString(message) <= maxRunes {
		return message
	}

	runes := []rune(message)
	return string(runes[:maxRunes])
}
