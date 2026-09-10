package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tool"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type historyTestEnvironment struct {
	schedulerTestEnvironment
	submit func(context.Context, *protocol.ActionRequest) (*protocol.ActionResult, error)
	start  func(context.Context, *protocol.ActionRequest) (ActionStart, error)
	wait   func(context.Context, string) (*protocol.ActionResult, error)
}

func (e *historyTestEnvironment) SubmitAction(ctx context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
	return e.submit(ctx, req)
}

func (e *historyTestEnvironment) StartAction(ctx context.Context, req *protocol.ActionRequest) (ActionStart, error) {
	return e.start(ctx, req)
}

func (e *historyTestEnvironment) WaitActionResult(ctx context.Context, id string) (*protocol.ActionResult, error) {
	return e.wait(ctx, id)
}

func historyTestActionResult(id string, status protocol.ActionStatus) *protocol.ActionResult {
	output, err := structpb.NewStruct(map[string]any{"receipt": map[string]any{"items": []any{"original", float64(7)}}})
	if err != nil {
		panic(err)
	}
	return &protocol.ActionResult{
		ActionId: id,
		Status:   status,
		Output:   output,
		Error:    &protocol.Error{Code: "adapter_detail", Message: strings.Repeat("source detail ", 30)},
	}
}

func assertHistoryCallOrder(t *testing.T, executions []memory.HistoryExecution, calls []model.ToolCall) {
	t.Helper()
	if len(executions) != len(calls) {
		t.Fatalf("execution count = %d, want %d", len(executions), len(calls))
	}
	for i, execution := range executions {
		if !reflect.DeepEqual(execution.Call, calls[i]) {
			t.Fatalf("execution %d call = %+v, want %+v", i, execution.Call, calls[i])
		}
	}
}

func TestSchedulerHistoryPreflightHasOnlyRuntimeJudgements(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls []model.ToolCall
		codes []string
	}{
		{"missing_tool", []model.ToolCall{schedulerCall("a", "inspect", "a"), schedulerCall("b", "missing", "b")}, []string{toolResultCodeBatchValidationFailed, toolResultCodeToolNotRegistered}},
		{"missing_arguments", []model.ToolCall{{ID: "a", Name: "inspect"}}, []string{toolResultCodeToolArgumentsMissing}},
		{"duplicate_ids", []model.ToolCall{schedulerCall("a", "inspect", "a"), schedulerCall("a", "inspect", "b")}, []string{toolResultCodeDuplicateToolCallID, toolResultCodeDuplicateToolCallID}},
		{"exclusive", []model.ToolCall{schedulerCall("a", "exclusive", "a"), schedulerCall("b", "inspect", "b")}, []string{toolResultCodeExclusiveToolBatch, toolResultCodeBatchValidationFailed}},
		{"async_batch", []model.ToolCall{schedulerCall("a", "travel", "a"), schedulerCall("b", "inspect", "b")}, []string{toolResultCodeAsyncBatchUnsupported, toolResultCodeBatchValidationFailed}},
		{"async_limit", []model.ToolCall{schedulerCall("a", "travel", "a")}, []string{toolResultCodeAsyncActionLimit}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := toolBatchScheduler{view: schedulerRegistry(
				schedulerCapability("inspect", tool.ConcurrencySequential),
				schedulerCapabilityWithPolicy("exclusive", tool.ConcurrencySequential, tool.ToolPolicy{ExclusivePerStep: true}),
				schedulerAsyncCapability("travel", tool.ConcurrencySequential),
			), asyncActionLimitFull: true}
			outcome, err := scheduler.Run(context.Background(), nil, "world:test", "entity:test", tc.calls)
			if err != nil || !outcome.HasModelVisibleFailure {
				t.Fatalf("Run = %+v, %v", outcome, err)
			}
			assertHistoryCallOrder(t, outcome.Executions, tc.calls)
			for i, execution := range outcome.Executions {
				if execution.Started || execution.ActionID != "" || execution.ActionResult != nil || execution.RuntimeError != "" {
					t.Fatalf("preflight execution contains game evidence: %+v", execution)
				}
				if execution.RuntimeResult == nil || execution.RuntimeResult.Code != tc.codes[i] || !reflect.DeepEqual(*execution.RuntimeResult, outcome.Results[i]) {
					t.Fatalf("runtime judgement %d = %+v", i, execution.RuntimeResult)
				}
			}
		})
	}
}

func TestSchedulerHistoryRetainsActualTerminalFailures(t *testing.T) {
	for _, mode := range []tool.ConcurrencyMode{tool.ConcurrencySequential, tool.ConcurrencyParallelSafe} {
		for _, status := range []protocol.ActionStatus{
			protocol.ActionStatus_ACTION_STATUS_FAILED, protocol.ActionStatus_ACTION_STATUS_REJECTED,
			protocol.ActionStatus_ACTION_STATUS_CANCELLED, protocol.ActionStatus_ACTION_STATUS_INTERRUPTED,
		} {
			t.Run(string(mode)+"/"+status.String(), func(t *testing.T) {
				scheduler := toolBatchScheduler{view: schedulerRegistry(
					schedulerCapability("inspect", mode), schedulerCapability("barrier", tool.ConcurrencySequential),
				), maxParallelToolCalls: 2}
				calls := []model.ToolCall{schedulerCall("a", "inspect", "a"), schedulerCall("b", "inspect", "b"), schedulerCall("c", "barrier", "c")}
				env := &historyTestEnvironment{submit: func(_ context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
					actual := protocol.ActionStatus_ACTION_STATUS_SUCCEEDED
					if schedulerActionLabel(req) == "b" {
						actual = status
					}
					return historyTestActionResult(req.GetActionId(), actual), nil
				}}
				outcome, err := scheduler.Run(context.Background(), env, "world:test", "entity:test", calls)
				if err != nil || !outcome.HasModelVisibleFailure || len(outcome.SuccessfulActions) != 1 {
					t.Fatalf("Run = %+v, %v", outcome, err)
				}
				assertHistoryCallOrder(t, outcome.Executions, calls)
				for i := 0; i < 2; i++ {
					execution := outcome.Executions[i]
					if !execution.Started || execution.ActionID == "" || execution.ActionID != execution.ActionResult.GetActionId() || execution.RuntimeError != "" {
						t.Fatalf("execution %d = %+v", i, execution)
					}
					if !reflect.DeepEqual(*execution.RuntimeResult, outcome.Results[i]) {
						t.Fatalf("runtime result %d differs from compatibility result", i)
					}
				}
				failed := outcome.Executions[1].ActionResult
				if !proto.Equal(failed, historyTestActionResult(failed.GetActionId(), status)) {
					t.Fatalf("actual failed result lost fields: %v", failed)
				}
				skipped := outcome.Executions[2]
				if skipped.Started || skipped.ActionID != "" || skipped.ActionResult != nil || skipped.RuntimeResult == nil || skipped.RuntimeResult.Code != toolResultCodePriorGroupFailed {
					t.Fatalf("never-started execution = %+v", skipped)
				}
			})
		}
	}
}

func TestSchedulerHistorySyncTechnicalPaths(t *testing.T) {
	transportErr := errors.New("adapter transport closed")
	for _, tc := range []struct {
		name   string
		nilEnv bool
		status protocol.ActionStatus
		err    error
	}{
		{name: "nil_environment", nilEnv: true},
		{name: "transport", err: transportErr},
		{name: "empty_error", err: errors.New(" \n ")},
		{name: "nil_result"},
		{name: "nonterminal", status: protocol.ActionStatus_ACTION_STATUS_RUNNING},
		{name: "cancelled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "actual_result_and_error", status: protocol.ActionStatus_ACTION_STATUS_FAILED, err: transportErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := toolBatchScheduler{view: schedulerRegistry(schedulerCapability("inspect", tool.ConcurrencySequential))}
			calls := []model.ToolCall{schedulerCall("a", "inspect", "a"), schedulerCall("b", "inspect", "b")}
			var submittedID string
			var actual *protocol.ActionResult
			var env Environment
			if !tc.nilEnv {
				env = &historyTestEnvironment{submit: func(_ context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
					submittedID = req.GetActionId()
					if tc.status != protocol.ActionStatus_ACTION_STATUS_UNSPECIFIED {
						actual = historyTestActionResult(submittedID, tc.status)
					}
					return actual, tc.err
				}}
			}
			outcome, err := scheduler.Run(context.Background(), env, "world:test", "entity:test", calls)
			if err == nil || tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("Run error = %v, want technical error %v", err, tc.err)
			}
			assertHistoryCallOrder(t, outcome.Executions, calls)
			first := outcome.Executions[0]
			if first.Started == tc.nilEnv || first.ActionID != submittedID || first.RuntimeError == "" || first.RuntimeResult != nil || !proto.Equal(first.ActionResult, actual) {
				t.Fatalf("technical provenance = %+v, actual = %v", first, actual)
			}
			later := outcome.Executions[1]
			if later.Started || later.ActionID != "" || later.ActionResult != nil || later.RuntimeResult == nil || later.RuntimeResult.Status != toolResultStatusSkipped || later.RuntimeResult.Code != "batch_aborted" {
				t.Fatalf("never-started provenance = %+v", later)
			}
			if len(outcome.SuccessfulActions) != 0 || outcome.Results[0].Status != "" || outcome.Results[1].Status != "" {
				t.Fatalf("technical error changed compatibility results: %+v", outcome)
			}
		})
	}
}

func TestSchedulerHistoryParallelErrorDrainsOrderedPartialSiblings(t *testing.T) {
	scheduler := toolBatchScheduler{view: schedulerRegistry(
		schedulerCapabilityWithPolicy("barrier", tool.ConcurrencySequential, tool.ToolPolicy{SettleAfterSuccess: true}),
		schedulerCapability("inspect", tool.ConcurrencyParallelSafe),
	), maxParallelToolCalls: 4}
	calls := []model.ToolCall{
		schedulerCall("prior", "barrier", "prior"), schedulerCall("fatal", "inspect", "fatal"),
		schedulerCall("success", "inspect", "success"), schedulerCall("failed", "inspect", "failed"),
		schedulerCall("cancelled", "inspect", "cancelled"), schedulerCall("queued", "inspect", "queued"),
		schedulerCall("later", "barrier", "later"),
	}
	ready := make(chan struct{}, 3)
	transportErr := errors.New("adapter transport closed")
	env := &historyTestEnvironment{submit: func(ctx context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
		switch schedulerActionLabel(req) {
		case "prior":
			return historyTestActionResult(req.GetActionId(), protocol.ActionStatus_ACTION_STATUS_SUCCEEDED), nil
		case "fatal":
			for i := 0; i < 3; i++ {
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, transportErr
		case "success", "failed", "cancelled":
			ready <- struct{}{}
			<-ctx.Done()
			if schedulerActionLabel(req) == "cancelled" {
				return nil, ctx.Err()
			}
			status := protocol.ActionStatus_ACTION_STATUS_SUCCEEDED
			if schedulerActionLabel(req) == "failed" {
				status = protocol.ActionStatus_ACTION_STATUS_FAILED
			}
			return historyTestActionResult(req.GetActionId(), status), nil
		default:
			return nil, errors.New("unexpected submission")
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outcome, err := scheduler.Run(ctx, env, "world:test", "entity:test", calls)
	if !errors.Is(err, transportErr) {
		t.Fatalf("Run error = %v, want transport error", err)
	}
	assertHistoryCallOrder(t, outcome.Executions, calls)
	if len(outcome.SuccessfulActions) != 2 || outcome.SuccessfulActions[0].ToolCall.ID != "prior" || outcome.SuccessfulActions[1].ToolCall.ID != "success" || !outcome.SettleAfterSuccess {
		t.Fatalf("successful siblings = %+v", outcome.SuccessfulActions)
	}
	for i, execution := range outcome.Executions {
		if execution.Started != (i < 5) || (execution.ActionID != "") != (i < 5) {
			t.Fatalf("submission provenance %d = %+v", i, execution)
		}
	}
	for _, i := range []int{0, 2, 3} {
		execution := outcome.Executions[i]
		if execution.ActionResult == nil || execution.RuntimeResult == nil || !reflect.DeepEqual(*execution.RuntimeResult, outcome.Results[i]) {
			t.Fatalf("known sibling result %d = %+v", i, execution)
		}
	}
	if outcome.Executions[3].ActionResult.GetStatus() != protocol.ActionStatus_ACTION_STATUS_FAILED || outcome.Executions[1].RuntimeError != transportErr.Error() || outcome.Executions[4].RuntimeError != context.Canceled.Error() {
		t.Fatalf("partial error provenance = %+v", outcome.Executions)
	}
	for _, i := range []int{5, 6} {
		execution := outcome.Executions[i]
		if execution.ActionResult != nil || execution.RuntimeResult == nil || execution.RuntimeResult.Status != toolResultStatusSkipped || execution.RuntimeResult.Code != "batch_aborted" {
			t.Fatalf("unstarted sibling %d = %+v", i, execution)
		}
	}
}

func TestSchedulerHistoryAsyncPaths(t *testing.T) {
	transportErr := errors.New("adapter transport closed")
	for _, tc := range []struct {
		name       string
		nilEnv     bool
		fast       bool
		missing    bool
		status     protocol.ActionStatus
		startErr   error
		waitErr    error
		wantErr    bool
		wantWait   bool
		wantResult bool
	}{
		{name: "success", status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, wantWait: true, wantResult: true},
		{name: "failed_wait", status: protocol.ActionStatus_ACTION_STATUS_FAILED, wantWait: true, wantResult: true},
		{name: "rejected_start", fast: true, status: protocol.ActionStatus_ACTION_STATUS_REJECTED, wantResult: true},
		{name: "fast_success", fast: true, status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, wantResult: true},
		{name: "nil_environment", nilEnv: true, wantErr: true},
		{name: "missing_start", missing: true, wantErr: true},
		{name: "start_transport", startErr: transportErr, wantErr: true},
		{name: "start_deadline", startErr: context.DeadlineExceeded, wantErr: true},
		{name: "start_result_and_error", fast: true, status: protocol.ActionStatus_ACTION_STATUS_FAILED, startErr: transportErr, wantErr: true, wantResult: true},
		{name: "missing_wait_result", wantErr: true, wantWait: true},
		{name: "nonterminal_wait", status: protocol.ActionStatus_ACTION_STATUS_RUNNING, wantErr: true, wantWait: true, wantResult: true},
		{name: "nonterminal_start", fast: true, status: protocol.ActionStatus_ACTION_STATUS_RUNNING, wantErr: true, wantResult: true},
		{name: "wait_cancel", waitErr: context.Canceled, wantErr: true, wantWait: true},
		{name: "wait_deadline", waitErr: context.DeadlineExceeded, wantErr: true, wantWait: true},
		{name: "wait_result_and_error", status: protocol.ActionStatus_ACTION_STATUS_FAILED, waitErr: transportErr, wantErr: true, wantWait: true, wantResult: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := toolBatchScheduler{view: schedulerRegistry(schedulerAsyncCapabilityWithPolicy("travel", tool.ConcurrencySequential, tool.ToolPolicy{SettleAfterSuccess: true}))}
			calls := []model.ToolCall{schedulerCall("a", "travel", "a")}
			var submittedID, waitedID string
			var actual *protocol.ActionResult
			var env Environment
			if !tc.nilEnv {
				env = &historyTestEnvironment{
					start: func(_ context.Context, req *protocol.ActionRequest) (ActionStart, error) {
						submittedID = req.GetActionId()
						if tc.fast {
							actual = historyTestActionResult(submittedID, tc.status)
							return ActionStart{Result: actual}, tc.startErr
						}
						if tc.missing || tc.startErr != nil {
							return ActionStart{}, tc.startErr
						}
						return ActionStart{Update: &protocol.ActionStatusUpdate{ActionId: submittedID, Status: protocol.ActionStatus_ACTION_STATUS_ACCEPTED}}, nil
					},
					wait: func(_ context.Context, id string) (*protocol.ActionResult, error) {
						waitedID = id
						if tc.wantResult {
							actual = historyTestActionResult(id, tc.status)
						}
						return actual, tc.waitErr
					},
				}
			}
			outcome, err := scheduler.Run(context.Background(), env, "world:test", "entity:test", calls)
			if (err != nil) != tc.wantErr || tc.startErr != nil && !errors.Is(err, tc.startErr) || tc.waitErr != nil && !errors.Is(err, tc.waitErr) {
				t.Fatalf("Run error = %v, want error %v", err, tc.wantErr)
			}
			assertHistoryCallOrder(t, outcome.Executions, calls)
			execution := outcome.Executions[0]
			if execution.Started == tc.nilEnv || execution.ActionID != submittedID || (waitedID != "") != tc.wantWait || tc.wantWait && waitedID != submittedID {
				t.Fatalf("async lifecycle provenance = %+v, waited ID = %q", execution, waitedID)
			}
			if !proto.Equal(execution.ActionResult, actual) || (execution.RuntimeError != "") != tc.wantErr || (execution.RuntimeResult != nil) == tc.wantErr {
				t.Fatalf("async result provenance = %+v, actual = %v", execution, actual)
			}
			if !tc.wantErr && !reflect.DeepEqual(*execution.RuntimeResult, outcome.Results[0]) {
				t.Fatalf("async runtime result = %+v", execution.RuntimeResult)
			}
			wantSuccess := !tc.wantErr && tc.status == protocol.ActionStatus_ACTION_STATUS_SUCCEEDED
			if (len(outcome.SuccessfulActions) == 1) != wantSuccess || outcome.SettleAfterSuccess != wantSuccess || outcome.AsyncActionStarted == tc.wantErr {
				t.Fatalf("async compatibility flags = %+v", outcome)
			}
		})
	}
}

func TestSchedulerHistorySnapshotsAreIndependentOfCompatibilityResults(t *testing.T) {
	scheduler := toolBatchScheduler{view: schedulerRegistry(schedulerCapability("inspect", tool.ConcurrencySequential))}
	call := schedulerCall("a", "inspect", "original")
	var actual *protocol.ActionResult
	env := &historyTestEnvironment{submit: func(_ context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
		actual = historyTestActionResult(req.GetActionId(), protocol.ActionStatus_ACTION_STATUS_SUCCEEDED)
		return actual, nil
	}}
	outcome, err := scheduler.Run(context.Background(), env, "world:test", "entity:test", []model.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryCallOrder(t, outcome.Executions, []model.ToolCall{call})
	call.Arguments["label"] = "changed"
	actual.Output.Fields["receipt"] = structpb.NewStringValue("changed")
	outcome.Results[0].Output["receipt"].(map[string]any)["items"].([]any)[0] = "changed"
	outcome.SuccessfulActions[0].ActionResult.Error.Message = "changed"
	execution := outcome.Executions[0]
	if execution.Call.Arguments["label"] != "original" || !proto.Equal(execution.ActionResult, historyTestActionResult(execution.ActionID, protocol.ActionStatus_ACTION_STATUS_SUCCEEDED)) || execution.RuntimeResult.Output["receipt"].(map[string]any)["items"].([]any)[0] != "original" {
		t.Fatalf("source snapshot mutated through compatibility results: %+v", execution)
	}
}

func TestSchedulerHistoryPreservesLargeToolArgumentIntegers(t *testing.T) {
	for _, value := range []any{int64(9007199254740993), json.Number("9007199254740993")} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			call := model.ToolCall{ID: "call:large", Name: "missing", Arguments: map[string]any{"nested": []any{map[string]any{"integer": value}}}}
			outcome, err := (toolBatchScheduler{}).Run(context.Background(), nil, "world:test", "entity:test", []model.ToolCall{call})
			if err != nil {
				t.Fatal(err)
			}
			call.Arguments["nested"].([]any)[0].(map[string]any)["integer"] = int64(0)
			got := outcome.Executions[0].Call.Arguments["nested"].([]any)[0].(map[string]any)["integer"]
			if got != value {
				t.Fatalf("captured integer = %v (%T), want %v (%T)", got, got, value, value)
			}
		})
	}
}

func TestSchedulerHistoryOrdersSuccessfulResultsAcrossQueuedWorkers(t *testing.T) {
	scheduler := toolBatchScheduler{view: schedulerRegistry(schedulerCapability("inspect", tool.ConcurrencyParallelSafe)), maxParallelToolCalls: 2}
	calls := []model.ToolCall{schedulerCall("first", "inspect", "first"), schedulerCall("second", "inspect", "second"), schedulerCall("third", "inspect", "third")}
	thirdStarted := make(chan struct{})
	env := &historyTestEnvironment{submit: func(ctx context.Context, req *protocol.ActionRequest) (*protocol.ActionResult, error) {
		switch schedulerActionLabel(req) {
		case "first":
			select {
			case <-thirdStarted:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case "third":
			close(thirdStarted)
		}
		output, _ := structpb.NewStruct(map[string]any{"label": schedulerActionLabel(req)})
		return &protocol.ActionResult{ActionId: req.GetActionId(), Status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, Output: output}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outcome, err := scheduler.Run(ctx, env, "world:test", "entity:test", calls)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryCallOrder(t, outcome.Executions, calls)
	if len(outcome.SuccessfulActions) != 3 {
		t.Fatalf("successful actions = %+v", outcome.SuccessfulActions)
	}
	for i, label := range []string{"first", "second", "third"} {
		execution := outcome.Executions[i]
		if execution.ActionResult.GetOutput().AsMap()["label"] != label || execution.RuntimeResult.Output["label"] != label || outcome.Results[i].Output["label"] != label || outcome.SuccessfulActions[i].ToolCall.ID != calls[i].ID {
			t.Fatalf("ordered result %d = %+v", i, execution)
		}
	}
}

func TestSchedulerHistoryBoundsSourceErrorDiagnostics(t *testing.T) {
	sourceErr := errors.New("transport: " + strings.Repeat("detail ", 500))
	scheduler := toolBatchScheduler{view: schedulerRegistry(schedulerCapability("inspect", tool.ConcurrencySequential))}
	env := &historyTestEnvironment{submit: func(context.Context, *protocol.ActionRequest) (*protocol.ActionResult, error) { return nil, sourceErr }}
	outcome, err := scheduler.Run(context.Background(), env, "world:test", "entity:test", []model.ToolCall{schedulerCall("a", "inspect", "a")})
	if err != sourceErr {
		t.Fatalf("caller error was changed: %v", err)
	}
	diagnostic := outcome.Executions[0].RuntimeError
	if !strings.HasPrefix(diagnostic, "transport:") || len(diagnostic) > 512 {
		t.Fatalf("source diagnostic = %q", diagnostic)
	}
}
