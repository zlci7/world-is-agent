package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gameagent/runtime/internal/model"
)

func TestBuildRequestUsesDeepSeekChatCompletionsShape(t *testing.T) {
	provider := NewProvider("test-key", "deepseek-v4-flash")

	body, err := provider.buildRequest(model.Request{
		System: "You are controlling an NPC in a game.",
		Messages: []model.Message{
			{
				Role:    model.RoleUser,
				Content: "Say hello as Linus.",
			},
		},
		Tools: []model.ToolDefinition{
			{
				Name:        "speak",
				Description: "Make the NPC say a short line of dialogue.",
				InputSchema: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			},
		},
		Controls: []model.ControlDefinition{
			{Kind: model.ControlSettle, Description: "Finish the turn without an environment action."},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}

	if got := payload["model"]; got != "deepseek-v4-flash" {
		t.Fatalf("model = %v, want deepseek-v4-flash", got)
	}
	if _, exists := payload["tool_choice"]; exists {
		t.Fatal("tool_choice should be omitted for DeepSeek thinking models")
	}

	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want system and user messages", payload["messages"])
	}

	systemMessage, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("system message has unexpected shape: %#v", messages[0])
	}
	if got := systemMessage["role"]; got != "system" {
		t.Fatalf("system message role = %v, want system", got)
	}

	userMessage, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("user message has unexpected shape: %#v", messages[1])
	}
	if got := userMessage["role"]; got != "user" {
		t.Fatalf("user message role = %v, want user", got)
	}

	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", payload["tools"])
	}

	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool has unexpected shape: %#v", tools[0])
	}
	if got := tool["type"]; got != "function" {
		t.Fatalf("tool.type = %v, want function", got)
	}

	function, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool.function has unexpected shape: %#v", tool["function"])
	}
	if got := function["name"]; got != "speak" {
		t.Fatalf("function.name = %v, want speak", got)
	}
	systemContent, _ := systemMessage["content"].(string)
	if strings.Contains(systemContent, "__gameagent_settle") {
		t.Fatalf("system message advertises undeclared settle sentinel:\n%s", systemContent)
	}
	if !strings.Contains(systemContent, "return no tool calls") {
		t.Fatalf("system message missing no-tool settle guidance:\n%s", systemContent)
	}
}

func TestGenerateHTTPErrorDoesNotExposeRawBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("line one\nsecret-token"))
	}))
	defer server.Close()

	provider := NewProvider("test-key", "deepseek-v4-flash", WithBaseURL(server.URL))
	_, err := provider.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}},
		Tools: []model.ToolDefinition{{
			Name:        "speak",
			Description: "Make the NPC speak.",
			InputSchema: `{"type":"object"}`,
		}},
	})
	if err == nil {
		t.Fatal("Generate returned nil error, want HTTP failure")
	}

	message := err.Error()
	if !strings.Contains(message, "status=400") {
		t.Fatalf("error = %q, want status code", message)
	}
	if strings.Contains(message, "\n") || strings.Contains(message, "secret-token") || strings.Contains(message, "line one") {
		t.Fatalf("error exposed raw response body: %q", message)
	}
}

func TestBuildRequestAllowsSettleOnlyWithoutEnvironmentTools(t *testing.T) {
	provider := NewProvider("test-key", "deepseek-v4-flash")

	body, err := provider.buildRequest(model.Request{
		System:   "You are controlling an NPC.",
		Messages: []model.Message{{Role: model.RoleUser, Content: "Nothing to do."}},
		Controls: []model.ControlDefinition{
			{Kind: model.ControlSettle, Description: "Finish the turn without an environment action."},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if _, exists := payload["tools"]; exists {
		t.Fatalf("tools should be omitted when no environment tools are available: %#v", payload["tools"])
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want system and user messages", payload["messages"])
	}
	systemMessage, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("system message has unexpected shape: %#v", messages[0])
	}
	systemContent, _ := systemMessage["content"].(string)
	if !strings.Contains(systemContent, "return no tool calls") {
		t.Fatalf("system message missing no-tool settle guidance:\n%s", systemContent)
	}
}

func TestBuildRequestSplitsOneToolMessagePerResult(t *testing.T) {
	provider := NewProvider("test-key", "deepseek-v4-flash")

	body, err := provider.buildRequest(model.Request{
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "current context"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
				{ID: "call_1", Name: "speak", Arguments: map[string]any{}},
				{ID: "call_2", Name: "speak", Arguments: map[string]any{}},
			}},
			{Role: model.RoleTool, ToolResults: []model.ToolResult{
				{ToolCallID: "call_1", Status: "succeeded", Code: "action_succeeded"},
				{ToolCallID: "call_2", Status: "skipped", Code: "batch_validation_failed"},
			}},
		},
		Tools: []model.ToolDefinition{
			{
				Name:        "speak",
				Description: "Make the NPC speak.",
				InputSchema: `{"type":"object"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %v, want user, assistant and two results", messages)
	}
	toolTranscript := messages[2].(map[string]any)
	if got := toolTranscript["role"]; got != "tool" {
		t.Fatalf("tool transcript role = %v, want tool", got)
	}
	if content := toolTranscript["content"].(string); !strings.Contains(content, "action_succeeded") {
		t.Fatalf("tool transcript content missing result:\n%s", content)
	}
	if messages[3].(map[string]any)["tool_call_id"] != "call_2" {
		t.Fatalf("second result lost call id: %v", messages[3])
	}
}

func TestParseResponseReturnsModelDecisionToolCalls(t *testing.T) {
	resp, err := parseResponse([]byte(`{
		"choices": [
			{
				"message": {
					"tool_calls": [
						{
							"id": "native_call_1",
							"type": "function",
							"function": {
								"name": "speak",
								"arguments": "{\"text\":\"Hi, friend.\"}"
							}
						},
						{
							"type": "function",
							"function": {
								"name": "emote",
								"arguments": "{\"emote\":\"happy\"}"
							}
						}
					]
				}
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	if resp.Decision.Control.Kind != model.ControlContinue {
		t.Fatalf("control = %q, want continue", resp.Decision.Control.Kind)
	}
	if got := len(resp.Decision.ToolCalls); got != 2 {
		t.Fatalf("tool call count = %d, want 2", got)
	}
	if resp.Decision.ToolCalls[0].ID != "native_call_1" {
		t.Fatalf("first call id = %q, want native_call_1", resp.Decision.ToolCalls[0].ID)
	}
	if resp.Decision.ToolCalls[1].ID != "deepseek_call_2" {
		t.Fatalf("second call id = %q, want deepseek_call_2", resp.Decision.ToolCalls[1].ID)
	}
	if resp.Decision.ToolCalls[0].Name != "speak" {
		t.Fatalf("first tool name = %q, want speak", resp.Decision.ToolCalls[0].Name)
	}

	if text := resp.Decision.ToolCalls[0].Arguments["text"]; text != "Hi, friend." {
		t.Fatalf("text = %v, want Hi, friend.", text)
	}
	if emote := resp.Decision.ToolCalls[1].Arguments["emote"]; emote != "happy" {
		t.Fatalf("emote = %v, want happy", emote)
	}
}

func TestParseResponseNoToolCallSettles(t *testing.T) {
	resp, err := parseResponse([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	if resp.Decision.Control.Kind != model.ControlSettle {
		t.Fatalf("control = %q, want settle", resp.Decision.Control.Kind)
	}
	if len(resp.Decision.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want none", resp.Decision.ToolCalls)
	}
}

func TestParseResponseKeepsMalformedArgumentsForCorrection(t *testing.T) {
	for _, arguments := range []string{`{"text":你好}`, `null`, `[]`, `"hello"`, ``, `{} trailing`} {
		t.Run(arguments, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"tool_calls": []any{map[string]any{
					"id": "bad_call", "type": "function",
					"function": map[string]any{"name": "show_message", "arguments": arguments},
				}}},
			}}})
			response, err := parseResponse(data)
			if err != nil {
				t.Fatalf("identifiable bad call should reach validation: %v", err)
			}
			if len(response.Decision.ToolCalls) != 1 {
				t.Fatalf("calls = %+v, want original call", response.Decision.ToolCalls)
			}
			call := response.Decision.ToolCalls[0]
			if call.ID != "bad_call" || call.Name != "show_message" || call.Arguments != nil || call.ArgumentsError == "" || call.RawArguments != arguments {
				t.Fatalf("bad arguments became executable: %+v", call)
			}
		})
	}
}

func TestParseResponseRejectsTruncatedToolCalls(t *testing.T) {
	_, err := parseResponse([]byte(`{"choices":[{"finish_reason":"length","message":{"tool_calls":[{"id":"call","type":"function","function":{"name":"show_message","arguments":"{}"}}]}}]}`))
	if err == nil {
		t.Fatal("truncated response must fail even when arguments happen to be valid JSON")
	}
}

func TestBuildRequestPairsStructuredToolMessages(t *testing.T) {
	provider := NewProvider("test-key", "deepseek-v4-flash")
	body, err := provider.buildRequest(model.Request{Messages: []model.Message{
		{Role: model.RoleUser, Content: "current context"},
		{Role: model.RoleAssistant, Content: "rendered tool call", ToolCalls: []model.ToolCall{{ID: "call", Name: "show_message", Arguments: map[string]any{"text": "hello"}}}},
		{Role: model.RoleTool, Content: "rendered result", ToolResults: []model.ToolResult{{ToolCallID: "call", Name: "show_message", Status: "invalid", Code: "tool_arguments_invalid"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	assistant, result := payload.Messages[1], payload.Messages[2]
	if assistant["tool_calls"] == nil || assistant["content"] == "rendered tool call" {
		t.Fatalf("assistant call is flattened or duplicated: %v", assistant)
	}
	if result["role"] != "tool" || result["tool_call_id"] != "call" || !strings.Contains(result["content"].(string), "tool_arguments_invalid") {
		t.Fatalf("result is not paired with call: %v", result)
	}
}

func TestParseResponseStripsSettleSentinel(t *testing.T) {
	resp, err := parseResponse([]byte(`{
		"choices": [
			{
				"message": {
					"tool_calls": [
						{
							"id": "settle_call",
							"type": "function",
							"function": {
								"name": "__gameagent_settle",
								"arguments": "{\"reason\":\"done\"}"
							}
						}
					]
				}
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	if resp.Decision.Control.Kind != model.ControlSettle {
		t.Fatalf("control = %q, want settle", resp.Decision.Control.Kind)
	}
	if resp.Decision.Control.Reason != "done" {
		t.Fatalf("control reason = %q, want done", resp.Decision.Control.Reason)
	}
	if len(resp.Decision.ToolCalls) != 0 {
		t.Fatalf("sentinel leaked as tool call: %+v", resp.Decision.ToolCalls)
	}
}
