package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tool"
)

func TestRuntimeToolTaskRedaction(t *testing.T) {
	for _, scenario := range []string{"success", "schema"} {
		t.Run(scenario, func(t *testing.T) {
			env, key, event, catalog := taskContextFixture(t)
			record := seedContextTask(t, env, key, "current")
			loop := NewLoop(nil, nil, contextConfig(t))
			turn := loop.beginTaskContext(env, key, event, "turn", catalog)
			if turn == nil {
				t.Fatal("task context missing")
			}
			defer turn.tools.Close()
			admission, err := turn.snapshot(context.Background(), tool.ToolAdmissionConfig{})
			if err != nil {
				t.Fatal(err)
			}
			call := model.ToolCall{ID: "cancel", Name: "update_task", Arguments: map[string]any{"task_id": record.ID, "intent": "cancel", "reason": "SECRET PROMPT"}}
			if scenario == "schema" {
				call.Arguments["SECRET FIELD"] = "SECRET VALUE"
			}
			outcome, err := (toolBatchScheduler{view: admission.View}).Run(context.Background(), env, key.WorldID, key.EntityID, []model.ToolCall{call})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(outcome.Executions)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "SECRET") {
				t.Fatalf("history exposed task arguments: %s", data)
			}
			data, _ = json.Marshal(outcome.Results)
			if strings.Contains(string(data), "SECRET") {
				t.Fatalf("result exposed task arguments: %s", data)
			}
		})
	}
}
