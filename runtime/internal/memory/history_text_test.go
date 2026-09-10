package memory

import (
	"encoding/json"
	"reflect"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestHistoryTextFieldsExactProvenance(t *testing.T) {
	output, err := structpb.NewStruct(map[string]any{"spoken": []any{"actual", map[string]any{"a~/b": "quoted\n\"text\""}}, "count": 4})
	if err != nil {
		t.Fatal(err)
	}
	details, err := structpb.NewStruct(map[string]any{"why": "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	batch := HistoryBatch{
		Event: HistoryEvent{Facts: []SourceContextFact{{Kind: "utterance", ActorEntityID: "player", Text: "\u4f60\u597d\U0001f680", Label: "not text", Attributes: map[string]any{"private": "not text"}}}},
		Steps: []HistoryStep{{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{ID: "call-1", Name: "say", Arguments: map[string]any{
					"z":    []any{"last", true, json.Number("9007199254740993")},
					"a~/b": map[string]any{"text": "first"},
				}}},
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "not text"},
			},
			Executions: []HistoryExecution{{
				Call:          model.ToolCall{ID: "call-1", Name: "say", Arguments: map[string]any{"text": "intent"}},
				ActionID:      "action-1",
				ActionResult:  &protocol.ActionResult{ActionId: "action-1", Output: output, Error: &protocol.Error{Code: "blocked", Message: "failed", Details: details}},
				RuntimeResult: &model.ToolResult{Message: "not actual", Output: map[string]any{"text": "not actual"}},
				RuntimeError:  "not actual",
			}},
		}},
		Terminal: HistoryTerminal{Reason: "not text", Error: "not actual"},
	}
	want := []HistoryTextField{
		{Path: "/event/facts/0/Text", ActorID: "player", Kind: "utterance", Text: "\u4f60\u597d\U0001f680"},
		{Path: "/steps/0/decision/ToolCalls/0/Arguments/a~0~1b/text", Kind: "tool_argument", Text: "first"},
		{Path: "/steps/0/decision/ToolCalls/0/Arguments/z/0", Kind: "tool_argument", Text: "last"},
		{Path: "/steps/0/executions/0/call/Arguments/text", Kind: "tool_argument", Text: "intent"},
		{Path: "/steps/0/executions/0/action_result/output/spoken/0", Kind: "action_output", Text: "actual"},
		{Path: "/steps/0/executions/0/action_result/output/spoken/1/a~0~1b", Kind: "action_output", Text: "quoted\n\"text\""},
		{Path: "/steps/0/executions/0/action_result/error/code", Kind: "action_error", Text: "blocked"},
		{Path: "/steps/0/executions/0/action_result/error/details/why", Kind: "action_error", Text: "blocked"},
		{Path: "/steps/0/executions/0/action_result/error/message", Kind: "action_error", Text: "failed"},
	}
	for i := 0; i < 10; i++ {
		if got := HistoryTextFields(batch); !reflect.DeepEqual(got, want) {
			t.Fatalf("fields = %#v; want %#v", got, want)
		}
	}
	if got := batch.Steps[0].Decision.ToolCalls[0].Arguments["z"].([]any)[2]; got != json.Number("9007199254740993") {
		t.Fatalf("number mutated: %#v", got)
	}
}

func TestHistoryTextFieldsLegacyAndExplicitActors(t *testing.T) {
	batch := HistoryBatch{Legacy: &Record{
		SourceContextFacts: []SourceContextFact{{Kind: "utterance", Text: "legacy", Label: "not text"}, {Kind: "utterance", ActorEntityID: "named", Text: ""}},
		Outcomes:           []TurnOutcome{{ToolName: "old", ToolArguments: map[string]any{"text": "not a model ToolCall"}}},
	}}
	want := []HistoryTextField{{Path: "/legacy/SourceContextFacts/0/Text", Kind: "utterance", Text: "legacy"}}
	if got := HistoryTextFields(batch); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy fields = %#v; want %#v", got, want)
	}
	if got := HistoryTextFields(HistoryBatch{}); len(got) != 0 {
		t.Fatalf("empty batch fields = %#v", got)
	}
}
