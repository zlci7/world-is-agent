package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type runtimeExecutorFunc func(context.Context, tool.RuntimeCallContext, model.ToolCall) (model.ToolResult, error)

func (f runtimeExecutorFunc) Execute(ctx context.Context, rc tool.RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
	return f(ctx, rc, call)
}

type mixedLoopProvider struct {
	requests  []model.Request
	decisions []model.ModelDecision
}

func (p *mixedLoopProvider) Generate(_ context.Context, request model.Request) (model.Response, error) {
	p.requests = append(p.requests, request)
	index := len(p.requests) - 1
	if index >= len(p.decisions) {
		return model.Response{}, errors.New("unexpected model call")
	}
	return model.Response{Decision: p.decisions[index]}, nil
}

type mixedLoopEnvironment struct{ schedulerTestEnvironment }

func (e *mixedLoopEnvironment) Observe(_ context.Context, world, entity string) (*protocol.Observation, error) {
	return &protocol.Observation{WorldId: world, EntityId: entity}, nil
}

func TestMixedToolLoopBudgetsOutputAndHistory(t *testing.T) {
	for _, scenario := range []string{"output", "step budget", "turn budget", "settle"} {
		t.Run(scenario, func(t *testing.T) {
			localCalls := 0
			entry := runtimeSchedulerEntry(func(_ context.Context, _ tool.RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
				localCalls++
				return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "succeeded", Output: map[string]any{"large": strings.Repeat("payload ", 1000)}}, nil
			})
			entry.Policy.SettleAfterSuccess = scenario == "settle"
			config := DefaultConfig()
			config.MemoryStore.Root = t.TempDir()
			config.MaxToolResultOutputTokens = 32
			store := memory.NewInMemoryHistoryStore(config.History)
			local := model.ToolCall{ID: "local", Name: "local_check", Arguments: map[string]any{"value": "ok"}}
			remote := schedulerCall("remote", "environment_check", "remote")
			provider := &mixedLoopProvider{decisions: []model.ModelDecision{{ToolCalls: []model.ToolCall{local, remote}, Control: model.ControlDirective{Kind: model.ControlContinue}}, {Control: model.ControlDirective{Kind: model.ControlSettle}}}}
			switch scenario {
			case "step budget":
				config.MaxToolCallsPerStep = 1
			case "turn budget":
				config.MaxToolCallsPerTurn = 1
				provider.decisions = []model.ModelDecision{{ToolCalls: []model.ToolCall{local}, Control: model.ControlDirective{Kind: model.ControlContinue}}, {ToolCalls: []model.ToolCall{remote}, Control: model.ControlDirective{Kind: model.ControlContinue}}}
			case "settle":
				provider.decisions = provider.decisions[:1]
			}
			loop := NewLoop(provider, nil, config, WithHistoryStore(store))
			view := mixedSchedulerView(t, entry, &tool.RuntimeCallContext{})
			key := session.AgentSessionKey{GameID: "generic", WorldID: "world", EntityID: "entity"}
			env := &mixedLoopEnvironment{}
			err := loop.handleEventWithToolAdmission(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, tool.ToolAdmissionResult{View: view}, &protocol.GameEvent{EventId: "event", WorldId: key.WorldID, TargetEntityId: key.EntityID, EventType: "input"})
			if scenario == "step budget" {
				if err == nil || !strings.Contains(err.Error(), "per step exceeded") || localCalls != 0 || len(env.callOrder()) != 0 {
					t.Fatalf("step budget: local=%d remote=%v err=%v", localCalls, env.callOrder(), err)
				}
				return
			}
			if scenario == "turn budget" {
				if err == nil || !strings.Contains(err.Error(), "per turn exceeded") || localCalls != 1 || len(env.callOrder()) != 0 {
					t.Fatalf("turn budget: local=%d remote=%v err=%v", localCalls, env.callOrder(), err)
				}
				return
			}
			if err != nil || localCalls != 1 || len(env.callOrder()) != 1 {
				t.Fatalf("mixed loop: local=%d remote=%v err=%v", localCalls, env.callOrder(), err)
			}
			if scenario == "output" {
				if len(provider.requests) != 2 {
					t.Fatalf("requests=%d", len(provider.requests))
				}
				var results []model.ToolResult
				for _, msg := range provider.requests[1].Messages {
					results = append(results, msg.ToolResults...)
				}
				if len(results) != 2 || results[0].Output["_truncated"] != true {
					t.Fatalf("output trimming: %+v", results)
				}
			}
			snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			defer store.ReleaseHistorySnapshot(snapshot)
			page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 10, Bytes: 1 << 20})
			if err != nil || len(page.Sources) != 1 {
				t.Fatalf("history: %+v %v", page, err)
			}
			executions := page.Sources[0].Batch.Steps[0].Executions
			if len(executions) != 2 || executions[0].ActionID != "" || executions[0].RuntimeResult.Output["large"] != strings.Repeat("payload ", 1000) || executions[1].ActionID == "" {
				t.Fatalf("mixed history: %+v", executions)
			}
		})
	}
}

func runtimeSchedulerEntry(executor runtimeExecutorFunc) tool.Entry {
	return tool.Entry{Definition: model.ToolDefinition{Name: "local_check", InputSchema: `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`}, Kind: tool.KindRuntime, Execution: tool.ExecutionSync, Concurrency: tool.ConcurrencySequential, Executor: executor}
}
func mixedSchedulerView(t *testing.T, entry tool.Entry, rc *tool.RuntimeCallContext) tool.TurnToolView {
	t.Helper()
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{schedulerCapability("environment_check", tool.ConcurrencySequential)}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(catalog, []tool.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	return registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, rc).View
}
func TestRuntimeToolExecutionAndHistory(t *testing.T) {
	for _, withEnvironment := range []bool{false, true} {
		for _, status := range []string{"succeeded", "failed"} {
			t.Run(status+map[bool]string{false: "/nil", true: "/environment"}[withEnvironment], func(t *testing.T) {
				called := 0
				entry := runtimeSchedulerEntry(func(_ context.Context, rc tool.RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
					called++
					if rc.Execution.TaskID != "captured-task" || rc.InteractionSourceID != "captured-source" || call.Arguments["value"] != "argument" {
						t.Fatalf("incorrect execution: %+v %+v", rc, call)
					}
					return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: status, Code: "local_result", Output: map[string]any{"value": "local"}}, nil
				})
				entry.Policy.SettleAfterSuccess = true
				rc := tool.RuntimeCallContext{Execution: task.ExecutionContext{TaskID: "captured-task"}, InteractionSourceID: "captured-source"}
				scheduler := toolBatchScheduler{view: mixedSchedulerView(t, entry, &rc), beforeEnvironmentAction: func(context.Context, plannedToolCall) error { t.Fatal("runtime invoked environment hook"); return nil }, onActionSubmit: func(plannedToolCall) { t.Fatal("runtime notified action") }}
				rc.Execution.TaskID = "changed"
				fake := &schedulerTestEnvironment{}
				var env Environment
				if withEnvironment {
					env = fake
				}
				call := model.ToolCall{ID: "local-1", Name: "local_check", Arguments: map[string]any{"value": "argument", "executor": "injected"}}
				plan, _, failed := scheduler.preflight("", "", []model.ToolCall{call})
				if failed || len(plan) != 1 || plan[0].request != nil {
					t.Fatalf("runtime allocated action request: %+v", plan)
				}
				outcome, err := scheduler.Run(context.Background(), env, "", "", []model.ToolCall{call})
				if err != nil || called != 1 || len(fake.callOrder()) != 0 {
					t.Fatalf("run: %+v, %v, calls %d", outcome, err, called)
				}
				if len(outcome.Results) != 1 || outcome.Results[0].Status != status || outcome.Results[0].Output["value"] != "local" {
					t.Fatalf("results: %+v", outcome.Results)
				}
				execution := outcome.Executions[0]
				if !execution.Started || execution.ActionID != "" || execution.ActionResult != nil || execution.RuntimeResult == nil || !reflect.DeepEqual(*execution.RuntimeResult, outcome.Results[0]) {
					t.Fatalf("history: %+v", execution)
				}
				if outcome.HasModelVisibleFailure != (status == "failed") || outcome.SettleAfterSuccess != (status == "succeeded") {
					t.Fatalf("policies: %+v", outcome)
				}
				if len(outcome.SuccessfulActions) != 0 {
					t.Fatal("local result became an environment action")
				}
			})
		}
	}
}

func TestRuntimeToolTechnicalError(t *testing.T) {
	failure := errors.New(strings.Repeat("failure", 100))
	entry := runtimeSchedulerEntry(func(context.Context, tool.RuntimeCallContext, model.ToolCall) (model.ToolResult, error) {
		return model.ToolResult{}, failure
	})
	scheduler := toolBatchScheduler{view: mixedSchedulerView(t, entry, &tool.RuntimeCallContext{})}
	outcome, err := scheduler.Run(context.Background(), nil, "", "", []model.ToolCall{{ID: "local", Name: "local_check", Arguments: map[string]any{"value": "x"}}})
	if !errors.Is(err, failure) || len(outcome.Executions[0].RuntimeError) != 512 || !outcome.Executions[0].Started || outcome.Executions[0].ActionID != "" {
		t.Fatalf("technical error: %+v, %v", outcome, err)
	}
}

func TestRuntimeToolUsesExecutionTimeout(t *testing.T) {
	entry := runtimeSchedulerEntry(func(ctx context.Context, _ tool.RuntimeCallContext, _ model.ToolCall) (model.ToolResult, error) {
		if _, ok := ctx.Deadline(); !ok {
			return model.ToolResult{}, errors.New("execution deadline missing")
		}
		<-ctx.Done()
		return model.ToolResult{}, ctx.Err()
	})
	scheduler := toolBatchScheduler{view: mixedSchedulerView(t, entry, &tool.RuntimeCallContext{}), actionTimeout: time.Millisecond}
	outcome, err := scheduler.Run(context.Background(), nil, "", "", []model.ToolCall{{ID: "local", Name: "local_check", Arguments: map[string]any{"value": "ok"}}})
	if !errors.Is(err, context.DeadlineExceeded) || outcome.Executions[0].RuntimeError != context.DeadlineExceeded.Error() {
		t.Fatalf("execution timeout: %+v, %v", outcome, err)
	}
}

func TestMixedToolParallelPoliciesAndHistory(t *testing.T) {
	entry := runtimeSchedulerEntry(func(_ context.Context, _ tool.RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
		return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "succeeded"}, nil
	})
	entry.Concurrency = tool.ConcurrencyParallelSafe
	entry.Policy.SettleAfterSuccess = true
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{schedulerCapability("environment_check", tool.ConcurrencyParallelSafe)}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(catalog, []tool.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := toolBatchScheduler{view: registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, &tool.RuntimeCallContext{}).View, maxParallelToolCalls: 2}
	env := &schedulerTestEnvironment{}
	calls := []model.ToolCall{{ID: "local", Name: "local_check", Arguments: map[string]any{"value": "ok"}}, schedulerCall("remote", "environment_check", "remote")}
	outcome, err := scheduler.Run(context.Background(), env, "world", "entity", calls)
	if err != nil || !outcome.SettleAfterSuccess || outcome.HasModelVisibleFailure || len(outcome.SuccessfulActions) != 1 || outcome.SuccessfulActions[0].ToolCall.ID != "remote" || len(env.callOrder()) != 1 {
		t.Fatalf("parallel mixed execution: %+v %v", outcome, err)
	}
	assertHistoryCallOrder(t, outcome.Executions, calls)
	if outcome.Executions[0].ActionID != "" || outcome.Executions[0].RuntimeResult.Status != "succeeded" || outcome.Executions[1].ActionID == "" {
		t.Fatalf("parallel history: %+v", outcome.Executions)
	}
}

func TestMixedToolValidationIsAtomic(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		called := 0
		entry := runtimeSchedulerEntry(func(context.Context, tool.RuntimeCallContext, model.ToolCall) (model.ToolResult, error) {
			called++
			return model.ToolResult{Status: "succeeded"}, nil
		})
		entry.Policy.ExclusivePerStep = exclusive
		scheduler := toolBatchScheduler{view: mixedSchedulerView(t, entry, &tool.RuntimeCallContext{})}
		env := &schedulerTestEnvironment{}
		calls := []model.ToolCall{{ID: "local", Name: "local_check", Arguments: map[string]any{"value": "valid"}}, schedulerCall("env", "environment_check", "check")}
		if !exclusive {
			calls[0].Arguments["value"] = 42
		}
		outcome, err := scheduler.Run(context.Background(), env, "world", "entity", calls)
		expected := toolResultCodeToolArgumentsInvalid
		if exclusive {
			expected = toolResultCodeExclusiveToolBatch
		}
		if err != nil || !outcome.HasModelVisibleFailure || outcome.Results[0].Code != expected || outcome.Results[1].Code != toolResultCodeBatchValidationFailed || called != 0 || len(env.callOrder()) != 0 {
			t.Fatalf("validation: %+v, %v", outcome, err)
		}
	}
}

func TestActionPreSendOrderingAndFailure(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			name := map[bool]string{false: "sync", true: "async"}[async] + map[bool]string{false: "/success", true: "/failure"}[fail]
			t.Run(name, func(t *testing.T) {
				capability := schedulerCapability("remote_check", tool.ConcurrencySequential)
				if async {
					capability = schedulerAsyncCapability("remote_check", tool.ConcurrencySequential)
				}
				env := &schedulerTestEnvironment{}
				failure := errors.New("pre-send failed")
				hookCount := 0
				scheduler := toolBatchScheduler{view: schedulerToolView(t, capability), beforeEnvironmentAction: func(_ context.Context, item plannedToolCall) error {
					hookCount++
					if item.request.GetActionId() == "" {
						t.Fatal("hook missing action id")
					}
					env.recordEvent("hook")
					if fail {
						return failure
					}
					return nil
				}, onActionSubmit: func(plannedToolCall) { env.recordEvent("notify") }}
				outcome, err := scheduler.Run(context.Background(), env, "world", "entity", []model.ToolCall{schedulerCall("remote", "remote_check", "remote")})
				if hookCount != 1 {
					t.Fatalf("hook called %d times", hookCount)
				}
				if fail {
					if !errors.Is(err, failure) || len(env.callOrder()) != 0 || !reflect.DeepEqual(env.eventOrder(), []string{"hook"}) || outcome.Executions[0].Started || outcome.Executions[0].RuntimeError != "pre-send failed" {
						t.Fatalf("hook failure: %+v, %v, %v", outcome, err, env.eventOrder())
					}
					return
				}
				events := env.eventOrder()
				if err != nil || len(events) < 3 || !reflect.DeepEqual(events[:3], []string{"hook", "notify", "start:remote"}) || len(env.callOrder()) != 1 || !outcome.Executions[0].Started || outcome.Executions[0].ActionResult == nil || outcome.AsyncActionStarted != async {
					t.Fatalf("send order: %+v, %v, %v", outcome, err, events)
				}
			})
		}
	}
}
