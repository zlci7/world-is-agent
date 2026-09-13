package context_test

import (
	"errors"
	"strings"
	"testing"

	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

func TestTaskContextRequiredSnapshotAndBudget(t *testing.T) {
	input := validEngineInput(t)
	tick := int64(120)
	record := task.Record{ID: "current-task", Owner: input.SessionKey, State: task.StatePaused, Revision: 4, NextWakeAt: &tick, PauseReason: "awaiting_world", Spec: task.TaskSpec{Instruction: "Inspect later", DeadlineAt: 200}}
	rc := tool.RuntimeCallContext{ObservedTask: &record, Execution: task.ExecutionContext{Owner: input.SessionKey, Source: task.SourceRef{Kind: task.SourceKindTaskWake}}, WakeReason: "scheduled"}
	registry, err := tool.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input.TurnToolView = registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, &rc).View
	record.Revision = 99
	*record.NextWakeAt = 999
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	result, err := engine.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	text := req.Messages[0].Content
	for _, want := range []string{"[Task Context]", `"task_id": "current-task"`, `"revision": 4`, `"next_wakeup_at": 120`, `"deadline_at": 200`, `"state": "paused"`, `"reason": "awaiting_world"`, `"wake_reason": "scheduled"`, "Inspect later"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	size, _ := agentcontext.EstimateRequestTokens(req)
	boundary := agentcontext.EngineConfig{MaxUserMessageTokens: size.UserMessageEstimatedTokens}
	if _, err := agentcontext.NewEngine(boundary).Build(input); err != nil {
		t.Fatalf("exact boundary: %v", err)
	}
	boundary.MaxUserMessageTokens = 1
	if _, err := agentcontext.NewEngine(boundary).Build(input); !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("task authority dropped to fit: %v", err)
	}
}

func TestTaskContextOwnerMismatch(t *testing.T) {
	input := validEngineInput(t)
	registry, _ := tool.NewRegistry(nil, nil)
	rc := tool.RuntimeCallContext{ObservedTask: &task.Record{ID: "foreign", Revision: 4, Owner: input.SessionKey}}
	rc.ObservedTask.Owner.EntityID = "other"
	input.TurnToolView = registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, &rc).View
	if _, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(input); !errors.Is(err, agentcontext.ErrInvalidInput) {
		t.Fatalf("foreign task context accepted: %v", err)
	}
}

func TestTaskContextAuthorityCannotBeCroppedToFit(t *testing.T) {
	input := validEngineInput(t)
	input.GameDefinition = nil
	input.AgentDefinition = nil
	registry, _ := tool.NewRegistry(nil, nil)
	input.TurnToolView = registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, nil).View
	base, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := agentcontext.NewRenderer().Render(base.Projection)
	size, _ := agentcontext.EstimateRequestTokens(request)
	rc := tool.RuntimeCallContext{Execution: task.ExecutionContext{Owner: input.SessionKey}, ObservedTask: &task.Record{ID: strings.Repeat("task", 1000), Owner: input.SessionKey, Revision: 4, State: task.StateWaiting, Spec: task.TaskSpec{Instruction: "Inspect later", DeadlineAt: 200}}}
	input.TurnToolView = registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, &rc).View
	if _, err := agentcontext.NewEngine(agentcontext.EngineConfig{MaxUserMessageTokens: size.UserMessageEstimatedTokens}).Build(input); !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("task identity was cropped to fit an otherwise valid envelope: %v", err)
	}
}
