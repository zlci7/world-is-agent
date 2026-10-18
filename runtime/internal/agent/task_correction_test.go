package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/task"
)

func TestTaskArgumentCorrectionKeepsCurrentStateAndRedactsHistory(t *testing.T) {
	for _, scenario := range []string{"repaired", "repeated error", "task revised", "task cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			env, key, event, catalog := taskContextFixture(t)
			record := seedContextTask(t, env, key, "initial")
			const original = `{"intent":取消,"reason":"PRIVATE_TASK_ARGUMENT"}`
			config := contextConfig(t)
			store := memory.NewInMemoryHistoryStore(config.History)
			provider := &taskContextModel{run: func(req model.Request, step int) model.ModelDecision {
				badCall := model.ToolCall{ID: "bad", Name: "update_task", RawArguments: original, ArgumentsError: "invalid JSON"}
				switch step {
				case 1:
					if scenario == "task revised" || scenario == "task cancelled" {
						intent := task.Intent{Kind: "cancel", Reason: "another input"}
						if scenario == "task revised" {
							nextWake := int64(150)
							intent = task.Intent{Kind: "wait", NextWakeAt: &nextWake}
						}
						exec := task.ExecutionContext{Owner: key, Binding: env.world.head.Binding, Clock: env.world.head.Clock,
							TaskID: record.ID, ExpectedRevision: record.Revision,
							Source: task.SourceRef{Kind: task.SourceKindInternal, EventID: "another-input", TurnID: "another-turn", CallID: "another-call"}}
						updated, err := env.svc.ApplyIntent(context.Background(), exec, intent)
						if err != nil {
							t.Fatal(err)
						}
						record = updated
					}
					return model.ModelDecision{ToolCalls: []model.ToolCall{badCall}, Control: model.ControlDirective{Kind: model.ControlContinue}}
				case 2:
					current, err := env.svc.Read(context.Background(), key, record.ID)
					if err != nil || current.Revision != record.Revision || current.State != record.State {
						t.Fatalf("bad call changed the task: %+v %v", current, err)
					}
					var hasOriginal, hasError bool
					for _, message := range req.Messages {
						for _, call := range message.ToolCalls {
							hasOriginal = hasOriginal || call.ID == "bad" && call.RawArguments == original && call.Arguments == nil
						}
						for _, result := range message.ToolResults {
							hasError = hasError || result.ToolCallID == "bad" && result.Status == "invalid" && result.Code == "tool_arguments_invalid_json"
						}
					}
					if !hasOriginal || !hasError {
						t.Fatal("task correction lost the original call or paired error")
					}
					if scenario == "repeated error" {
						badCall.ID = "still_bad"
						return model.ModelDecision{ToolCalls: []model.ToolCall{badCall}, Control: model.ControlDirective{Kind: model.ControlContinue}}
					}
					return model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "fixed", Name: "update_task", Arguments: map[string]any{
						"task_id": record.ID, "intent": "cancel", "reason": "PRIVATE_TASK_ARGUMENT",
					}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}
				}
				return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
			}}
			loop := NewLoop(provider, nil, config, WithHistoryStore(store))
			err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event)
			wantFailure := scenario == "repeated error" || scenario == "task cancelled"
			if (err != nil) != wantFailure {
				t.Fatalf("turn error = %v, want failure %v", err, wantFailure)
			}
			wantCalls := 3
			if wantFailure {
				wantCalls = 2
				if !strings.Contains(err.Error(), "tool argument correction failed") {
					t.Fatalf("unexpected failure: %v", err)
				}
			}
			if len(provider.requests) != wantCalls || env.submits != 0 || env.starts != 0 {
				t.Fatalf("calls=%d environment effects=%d/%d", len(provider.requests), env.submits, env.starts)
			}
			current, err := env.svc.Read(context.Background(), key, record.ID)
			wantState, wantRevision := task.StateCancelled, record.Revision
			if !wantFailure {
				wantRevision++
			} else if scenario == "repeated error" {
				wantState = task.StateWaiting
			}
			if err != nil || current.State != wantState || current.Revision != wantRevision {
				t.Fatalf("task = %+v %v, want %s revision %d", current, err, wantState, wantRevision)
			}
			snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			defer store.ReleaseHistorySnapshot(snapshot)
			page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 10, Bytes: 1 << 20})
			if err != nil || len(page.Sources) != 1 {
				t.Fatalf("terminal history missing: %+v %v", page, err)
			}
			data, _ := json.Marshal(page.Sources)
			if strings.Contains(string(data), "PRIVATE_TASK_ARGUMENT") {
				t.Fatal("persisted history contains task tool arguments")
			}
		})
	}
}
