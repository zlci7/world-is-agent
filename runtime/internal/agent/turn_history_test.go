package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestTurnHistoryCollectorCanonicalSourceFields(t *testing.T) {
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world:test", EntityID: "entity:test"}
	attributes, _ := structpb.NewStruct(map[string]any{"nested": map[string]any{"values": []any{"source", true}}})
	private, _ := structpb.NewStruct(map[string]any{"secret": "private payload and state"})
	event := &protocol.GameEvent{
		EventId: "event:test", EventType: "fake.source", Sequence: 42,
		GameTime: &protocol.GameTime{Tick: proto.Int64(0)}, Payload: private,
		ContextFacts: []*protocol.ContextFact{nil, {
			Kind: "utterance", ActorEntityId: "actor", TargetEntityId: "target", ScopeId: "scope",
			Text: "source words", Label: "source label", Attributes: attributes,
		}},
	}
	collector := newTurnHistoryCollector(key, "turn:test", event)
	collector.Observe(1, &protocol.Observation{Revision: 9, GameTime: &protocol.GameTime{Hour: proto.Int32(0), Minute: proto.Int32(0)}, State: private})
	call := model.ToolCall{ID: "call:test", Name: "inspect", Arguments: map[string]any{"items": []any{"one", map[string]any{"count": float64(2)}}}}
	decision := model.ModelDecision{ToolCalls: []model.ToolCall{call}, Control: model.ControlDirective{Kind: model.ControlContinue, Reason: "follow up"}}
	collector.Decision(1, decision)
	actual := historyTestActionResult("action:test", protocol.ActionStatus_ACTION_STATUS_FAILED)
	runtimeResult := model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "failed", Code: "adapter_detail", Output: actual.GetOutput().AsMap()}
	collector.Executions(1, []memory.HistoryExecution{{Call: call, ActionID: "action:test", Started: true, ActionResult: actual, RuntimeResult: &runtimeResult}})
	batch := collector.Finish("failed", "action", "action_failed", nil)
	want := memory.HistoryBatch{
		Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: "turn:test",
		Event: memory.HistoryEvent{
			ID: "event:test", Type: "fake.source", Sequence: 42,
			GameTime: &memory.GameTimeSnapshot{Tick: 0, PresentFields: 32},
			Facts:    []memory.SourceContextFact{{Kind: "utterance", ActorEntityID: "actor", TargetEntityID: "target", ScopeID: "scope", Text: "source words", Label: "source label", Attributes: map[string]any{"nested": map[string]any{"values": []any{"source", true}}}}},
		},
		Observations: []memory.HistoryObservation{{Step: 1, Revision: 9, GameTime: &memory.GameTimeSnapshot{Hour: 0, Minute: 0, PresentFields: 24}}},
		Steps:        []memory.HistoryStep{{Index: 1, Decision: decision, Executions: []memory.HistoryExecution{{Call: call, ActionID: "action:test", Started: true, ActionResult: actual, RuntimeResult: &runtimeResult}}}},
		Terminal:     memory.HistoryTerminal{Status: "failed", Stage: "action", Reason: "action_failed"},
	}
	if !reflect.DeepEqual(batch, want) {
		t.Fatalf("history batch = %+v, want %+v", batch, want)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private payload and state") {
		t.Fatalf("private observation or event payload entered history: %s", encoded)
	}
}

func TestTurnHistoryCollectorMissingObservationAndDecisionRemainMissing(t *testing.T) {
	collector := newTurnHistoryCollector(session.AgentSessionKey{}, "turn:missing", nil)
	collector.Observe(1, nil)
	batch := collector.Finish("failed", "observation", "observation_failed", errors.New("private observation diagnostics"))
	if batch.Event.ID != "" || batch.Event.GameTime != nil || len(batch.Event.Facts) != 0 || len(batch.Observations) != 0 || len(batch.Steps) != 0 {
		t.Fatalf("collector fabricated missing sources: %+v", batch)
	}
	if batch.Terminal.Error == "" || strings.Contains(batch.Terminal.Error, "private observation diagnostics") {
		t.Fatalf("terminal diagnostic = %+v", batch.Terminal)
	}
}

func TestTurnHistoryCollectorPreservesZeroTimePresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		time *protocol.GameTime
		want *memory.GameTimeSnapshot
	}{
		{"missing", nil, nil},
		{"empty", &protocol.GameTime{}, nil},
		{"zero_tick", &protocol.GameTime{Tick: proto.Int64(0)}, &memory.GameTimeSnapshot{PresentFields: 32}},
		{"zero_calendar", &protocol.GameTime{Year: proto.Int32(0), Season: proto.Int32(0), Day: proto.Int32(0), Hour: proto.Int32(0), Minute: proto.Int32(0)}, &memory.GameTimeSnapshot{PresentFields: 31}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newTurnHistoryCollector(session.AgentSessionKey{}, "turn:test", &protocol.GameEvent{GameTime: tc.time})
			collector.Observe(1, &protocol.Observation{GameTime: tc.time})
			batch := collector.Finish("completed", "", "", nil)
			if len(batch.Observations) != 1 || batch.Observations[0].Revision != 0 {
				t.Fatalf("zero-revision observation lost: %+v", batch.Observations)
			}
			if !reflect.DeepEqual(batch.Event.GameTime, tc.want) || !reflect.DeepEqual(batch.Observations[0].GameTime, tc.want) {
				t.Fatalf("time presence: event = %+v, observation = %+v, want %+v", batch.Event.GameTime, batch.Observations[0].GameTime, tc.want)
			}
		})
	}
}

func TestTurnHistoryCollectorClonesAtEachCaptureAndFinish(t *testing.T) {
	attributes, _ := structpb.NewStruct(map[string]any{"nested": []any{map[string]any{"text": "fact"}}})
	event := &protocol.GameEvent{EventId: "event:original", GameTime: &protocol.GameTime{Tick: proto.Int64(0)}, ContextFacts: []*protocol.ContextFact{{Text: "fact", Attributes: attributes}}}
	collector := newTurnHistoryCollector(session.AgentSessionKey{}, "turn:test", event)
	event.EventId = "changed"
	*event.GameTime.Tick = 99
	event.ContextFacts[0].Text = "changed"
	attributes.Fields["nested"].GetListValue().Values[0].GetStructValue().Fields["text"] = structpb.NewStringValue("changed")
	obs := &protocol.Observation{Revision: 7, GameTime: &protocol.GameTime{Tick: proto.Int64(2)}}
	collector.Observe(1, obs)
	obs.Revision = 99
	*obs.GameTime.Tick = 99
	call := model.ToolCall{ID: "call:original", Name: "inspect", Arguments: map[string]any{"nested": []any{map[string]any{"text": "decision"}}}}
	decision := model.ModelDecision{ToolCalls: []model.ToolCall{call}, Control: model.ControlDirective{Kind: model.ControlContinue, Reason: "original"}}
	collector.Decision(1, decision)
	call.Arguments["nested"].([]any)[0].(map[string]any)["text"] = "execution"
	decision.ToolCalls[0].ID = "changed"
	decision.Control.Reason = "changed"
	actual := historyTestActionResult("action:original", protocol.ActionStatus_ACTION_STATUS_FAILED)
	runtimeResult := model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "failed", Output: map[string]any{"nested": []any{map[string]any{"text": "result"}}}}
	executions := []memory.HistoryExecution{{Call: call, ActionID: "action:original", Started: true, ActionResult: actual, RuntimeResult: &runtimeResult, RuntimeError: "source diagnostic"}}
	collector.Executions(1, executions)
	call.Arguments["nested"].([]any)[0].(map[string]any)["text"] = "changed"
	executions[0].ActionID = "changed"
	executions[0].RuntimeError = "changed"
	actual.Status = protocol.ActionStatus_ACTION_STATUS_SUCCEEDED
	actual.Error.Message = "changed"
	actual.Output.Fields["receipt"].GetStructValue().Fields["items"].GetListValue().Values[0] = structpb.NewStringValue("changed")
	runtimeResult.Output["nested"].([]any)[0].(map[string]any)["text"] = "changed"

	assertCaptured := func(batch memory.HistoryBatch) {
		t.Helper()
		if batch.Event.ID != "event:original" || batch.Event.GameTime.Tick != 0 || batch.Event.Facts[0].Text != "fact" || batch.Event.Facts[0].Attributes["nested"].([]any)[0].(map[string]any)["text"] != "fact" {
			t.Fatalf("event capture mutated: %+v", batch.Event)
		}
		if batch.Observations[0].Revision != 7 || batch.Observations[0].GameTime.Tick != 2 {
			t.Fatalf("observation capture mutated: %+v", batch.Observations)
		}
		step := batch.Steps[0]
		if step.Decision.ToolCalls[0].ID != "call:original" || step.Decision.Control.Reason != "original" || step.Decision.ToolCalls[0].Arguments["nested"].([]any)[0].(map[string]any)["text"] != "decision" {
			t.Fatalf("decision capture mutated: %+v", step.Decision)
		}
		execution := step.Executions[0]
		if execution.ActionID != "action:original" || execution.RuntimeError != "source diagnostic" || execution.Call.Arguments["nested"].([]any)[0].(map[string]any)["text"] != "execution" || execution.RuntimeResult.Output["nested"].([]any)[0].(map[string]any)["text"] != "result" {
			t.Fatalf("execution capture mutated: %+v", execution)
		}
		if !proto.Equal(execution.ActionResult, historyTestActionResult("action:original", protocol.ActionStatus_ACTION_STATUS_FAILED)) {
			t.Fatalf("game result capture mutated: %v", execution.ActionResult)
		}
	}
	batch := collector.Finish("failed", "action", "submit_action_failed", nil)
	assertCaptured(batch)
	batch.Event.GameTime.Tick = 99
	batch.Event.Facts[0].Attributes["nested"].([]any)[0].(map[string]any)["text"] = "changed"
	batch.Observations[0].GameTime.Tick = 99
	batch.Steps[0].Decision.ToolCalls[0].Arguments["nested"].([]any)[0].(map[string]any)["text"] = "changed"
	batch.Steps[0].Executions[0].RuntimeResult.Output["nested"].([]any)[0].(map[string]any)["text"] = "changed"
	batch.Steps[0].Executions[0].ActionResult.Error.Message = "changed"
	assertCaptured(collector.Finish("failed", "action", "submit_action_failed", nil))
	collector.Observe(2, &protocol.Observation{Revision: 10})
	collector.Decision(2, model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}})
	if len(batch.Steps) != 1 || len(batch.Observations) != 1 {
		t.Fatal("later captures rewrote a finished batch")
	}
}

func TestTurnHistoryCollectorOrdersStepsAndKeepsExecutionOrder(t *testing.T) {
	collector := newTurnHistoryCollector(session.AgentSessionKey{}, "turn:test", nil)
	collector.Observe(2, &protocol.Observation{Revision: 20})
	collector.Observe(1, &protocol.Observation{Revision: 10})
	collector.Decision(2, model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}})
	executions := []memory.HistoryExecution{{Call: model.ToolCall{ID: "b"}}, {Call: model.ToolCall{ID: "a"}}}
	collector.Executions(1, executions[:1])
	collector.Decision(1, model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "b"}, {ID: "a"}}, Control: model.ControlDirective{Kind: model.ControlContinue}})
	collector.Executions(1, executions[1:])
	batch := collector.Finish("completed", "", "", nil)
	if len(batch.Steps) != 2 || batch.Steps[0].Index != 1 || batch.Steps[1].Index != 2 || batch.Observations[0].Revision != 10 || batch.Observations[1].Revision != 20 {
		t.Fatalf("source order = %+v", batch)
	}
	if !reflect.DeepEqual(batch.Steps[0].Executions, executions) || len(batch.Steps[1].Executions) != 0 {
		t.Fatalf("execution order = %+v", batch.Steps)
	}
}

func TestTurnHistoryCollectorTerminalErrorsUseSourceDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
		want   string
	}{
		{"none", nil, "settled", ""},
		{"provider", errors.New(strings.Repeat("private prompt and raw response", 100)), "provider_failed", "provider_failed"},
		{"cancel", fmt.Errorf("private prompt: %w", context.Canceled), "provider_failed", context.Canceled.Error()},
		{"timeout", fmt.Errorf("private prompt: %w", context.DeadlineExceeded), "provider_timeout", context.DeadlineExceeded.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newTurnHistoryCollector(session.AgentSessionKey{}, "turn:test", nil)
			batch := collector.Finish("failed", "model", tc.reason, tc.err)
			if batch.Terminal.Error != tc.want || batch.Terminal.Status != "failed" || batch.Terminal.Stage != "model" || batch.Terminal.Reason != tc.reason {
				t.Fatalf("terminal = %+v, want diagnostic %q", batch.Terminal, tc.want)
			}
		})
	}
}

func TestTurnHistoryCollectorPreservesLargeIntegersThroughCanonicalMemory(t *testing.T) {
	for _, value := range []any{int64(9007199254740993), json.Number("9007199254740993")} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world:test", EntityID: "entity:test"}
			collector := newTurnHistoryCollector(key, "turn:large", &protocol.GameEvent{EventId: "event:large"})
			args := map[string]any{"nested": []any{map[string]any{"integer": value}}}
			call := model.ToolCall{ID: "call:large", Name: "inspect", Arguments: args}
			collector.Decision(1, model.ModelDecision{ToolCalls: []model.ToolCall{call}, Control: model.ControlDirective{Kind: model.ControlContinue}})
			collector.Executions(1, []memory.HistoryExecution{{Call: call, RuntimeResult: &model.ToolResult{ToolCallID: call.ID, Output: args}}})
			args["nested"].([]any)[0].(map[string]any)["integer"] = int64(0)
			batch := collector.Finish("completed", "", "", nil)
			for _, got := range []any{
				batch.Steps[0].Decision.ToolCalls[0].Arguments["nested"].([]any)[0].(map[string]any)["integer"],
				batch.Steps[0].Executions[0].Call.Arguments["nested"].([]any)[0].(map[string]any)["integer"],
				batch.Steps[0].Executions[0].RuntimeResult.Output["nested"].([]any)[0].(map[string]any)["integer"],
			} {
				if got != value {
					t.Fatalf("captured integer = %v (%T), want %v (%T)", got, got, value, value)
				}
			}
			encoded, batchKey, fingerprint, err := memory.CanonicalHistoryBatch(batch, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(encoded), "9007199254740993") != 3 {
				t.Fatalf("canonical integers lost precision: %s", encoded)
			}
			cloned, err := memory.CloneHistoryBatch(batch)
			if err != nil {
				t.Fatal(err)
			}
			_, cloneKey, cloneFingerprint, err := memory.CanonicalHistoryBatch(cloned, 1<<20)
			if err != nil || batchKey != cloneKey || fingerprint != cloneFingerprint {
				t.Fatalf("clone changed canonical identity: %q %q, error %v", cloneKey, cloneFingerprint, err)
			}
		})
	}
}

func TestTurnHistoryCollectorClonesTypedJSONContainers(t *testing.T) {
	args := map[string]any{
		"labels": []string{"original"},
		"groups": []map[string]any{{"integer": json.Number("9007199254740993")}},
		"ids":    map[string][]int64{"values": {9007199254740993}},
		"array":  [1]map[string]any{{"label": "original"}},
		"empty":  []any{nil},
	}
	collector := newTurnHistoryCollector(session.AgentSessionKey{}, "turn:typed", nil)
	call := model.ToolCall{ID: "call:typed", Name: "inspect", Arguments: args}
	collector.Decision(1, model.ModelDecision{ToolCalls: []model.ToolCall{call}})
	collector.Executions(1, []memory.HistoryExecution{{Call: call, RuntimeResult: &model.ToolResult{Output: args}}})
	args["labels"].([]string)[0] = "changed"
	args["groups"].([]map[string]any)[0]["integer"] = json.Number("0")
	args["ids"].(map[string][]int64)["values"][0] = 0
	args["array"].([1]map[string]any)[0]["label"] = "changed"
	want := map[string]any{
		"labels": []string{"original"},
		"groups": []map[string]any{{"integer": json.Number("9007199254740993")}},
		"ids":    map[string][]int64{"values": {9007199254740993}},
		"array":  [1]map[string]any{{"label": "original"}},
		"empty":  []any{nil},
	}
	batch := collector.Finish("completed", "", "", nil)
	for _, got := range []map[string]any{batch.Steps[0].Decision.ToolCalls[0].Arguments, batch.Steps[0].Executions[0].Call.Arguments, batch.Steps[0].Executions[0].RuntimeResult.Output} {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("typed JSON snapshot = %+v, want %+v", got, want)
		}
	}
}
