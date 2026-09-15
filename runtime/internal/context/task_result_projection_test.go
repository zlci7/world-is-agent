package context_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

func taskResultContextInput(t *testing.T, record *task.Record, results []task.Result) agentcontext.BuildInput {
	t.Helper()
	input := validEngineInput(t)
	record.Owner = input.SessionKey
	rc := tool.RuntimeCallContext{
		ObservedTask:  record,
		RecentResults: results,
		Execution: task.ExecutionContext{
			Owner: input.SessionKey, Binding: task.Binding{World: task.WorldKey{GameID: input.SessionKey.GameID, WorldID: input.SessionKey.WorldID}, RunID: "run", Generation: 3},
			Source: task.SourceRef{Kind: task.SourceKindTaskWake}, WakeID: "wake-1", WakeReason: "scheduled",
		},
	}
	registry, err := tool.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input.TurnToolView = registry.BuildTurnToolView(tool.ToolAdmissionConfig{}, &rc).View
	return input
}

func TestTaskContextPublishesCommittedResultsAndRecovery(t *testing.T) {
	tick := int64(120)
	record := task.Record{
		ID: "current-task", State: task.StatePaused, Revision: 4, NextWakeAt: &tick, PauseReason: "awaiting_world",
		Progress: json.RawMessage(`{"note":"walking to the bridge"}`),
		Spec:     task.TaskSpec{Instruction: "Inspect later", DeadlineAt: 200, Contract: json.RawMessage(`{"landmark_id":"far_shore"}`)},
		Evidence: []task.Evidence{{FactID: "revalidated-fact", RevalidatedIn: &task.Binding{RunID: "run", Generation: 3}}},
	}
	input := taskResultContextInput(t, &record, []task.Result{{
		ID: "result-2", TaskID: "earlier-task", Revision: 2, State: task.StateFailed, Reason: task.EvidenceKindUnsatisfied,
		OccurredAt: 118, EvidenceRefs: []string{"fact-2"},
	}})
	record.Evidence[0].RevalidatedIn.World = task.WorldKey{GameID: input.SessionKey.GameID, WorldID: input.SessionKey.WorldID}

	result, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	text := request.Messages[0].Content
	for _, want := range []string{
		`"task_id": "current-task"`, `"state": "paused"`, `"reason": "awaiting_world"`, `"deadline_at": 200`,
		`"note": "walking to the bridge"`, `"recovery":`, `"revalidated_fact_ids"`,
		`"recent_results"`, `"result_id": "result-2"`, `"task_id": "earlier-task"`, `"unsatisfied"`, `"occurred_at": 118`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if !strings.Contains(text, `"landmark_id": "far_shore"`) {
		t.Fatalf("contract missing from %s", text)
	}
}

func TestTaskContextOmitsRecoveryForAHealthyTask(t *testing.T) {
	record := task.Record{ID: "current-task", State: task.StateRunning, Revision: 2, Spec: task.TaskSpec{Instruction: "Inspect later", DeadlineAt: 200}}
	input := taskResultContextInput(t, &record, nil)
	result, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"recovery", "recent_results", "progress"} {
		if strings.Contains(request.Messages[0].Content, unwanted) {
			t.Fatalf("healthy task published %q: %s", unwanted, request.Messages[0].Content)
		}
	}
}

func TestTaskContextCropsResultsBeforeItsAuthoritativeFields(t *testing.T) {
	tick := int64(120)
	record := task.Record{
		ID: "current-task", State: task.StateWaiting, Revision: 5, NextWakeAt: &tick,
		Progress: json.RawMessage(`{"note":"` + strings.Repeat("walking ", 2000) + `"}`),
		Spec:     task.TaskSpec{Instruction: "Inspect later", DeadlineAt: 200, Contract: json.RawMessage(`{"landmark_id":"far_shore"}`)},
	}
	results := make([]task.Result, 0, 3)
	for i, id := range []string{"result-newest", "result-middle", "result-oldest"} {
		results = append(results, task.Result{
			ID: id, TaskID: "task-" + id, Revision: 3, State: task.StateSucceeded, Reason: task.EvidenceKindSatisfied,
			OccurredAt: int64(110 - i), EvidenceRefs: []string{strings.Repeat("fact", 1000)},
		})
	}
	input := taskResultContextInput(t, &record, results)
	record.Owner = input.SessionKey
	result, err := agentcontext.NewEngine(agentcontext.EngineConfig{MaxTaskContextTokens: 1024}).Build(input)
	if err != nil {
		t.Fatal(err)
	}
	report := result.Report.Task
	if !report.Cropped || !report.DroppedProgress || report.DroppedResults != 2 || !report.DroppedDetail {
		t.Fatalf("crop report = %+v, want progress, older results then detail dropped", report)
	}
	if report.EstimatedTokens > 1024 {
		t.Fatalf("cropped block = %d tokens, want at most 1024", report.EstimatedTokens)
	}
	request, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	text := request.Messages[0].Content
	for _, want := range []string{`"task_id": "current-task"`, `"state": "waiting"`, `"next_wakeup_at": 120`, `"deadline_at": 200`, `"landmark_id": "far_shore"`, `"result_id": "result-newest"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("crop removed %q from %s", want, text)
		}
	}
	for _, unwanted := range []string{"walking walking", `"result_id": "result-middle"`, `"result_id": "result-oldest"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("crop kept %q in %s", unwanted, text)
		}
	}
	if strings.Contains(text, "evidence_refs") {
		t.Fatalf("crop kept result detail in %s", text)
	}
}

func TestTaskContextFailsWhenIdentityCannotFit(t *testing.T) {
	record := task.Record{ID: strings.Repeat("task", 100), State: task.StateWaiting, Revision: 5, Spec: task.TaskSpec{Instruction: "Inspect later", DeadlineAt: 200}}
	input := taskResultContextInput(t, &record, nil)
	record.Owner = input.SessionKey
	if _, err := agentcontext.NewEngine(agentcontext.EngineConfig{MaxTaskContextTokens: 16}).Build(input); !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("required task identity was cropped: %v", err)
	}
}
