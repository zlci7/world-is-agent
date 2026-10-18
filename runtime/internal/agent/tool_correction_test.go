package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/llm/deepseek"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
)

type correctionWireMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCallID       string `json:"tool_call_id"`
	ToolCalls        []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type correctionProviderFunc func(context.Context, model.Request) (model.Response, error)

func (f correctionProviderFunc) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return f(ctx, request)
}

func TestToolArgumentCorrectionRespectsCancellationAndTimeout(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelTurn], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			provider := correctionProviderFunc(func(modelCtx context.Context, request model.Request) (model.Response, error) {
				calls++
				if calls == 1 {
					return model.Response{Decision: model.ModelDecision{
						Control:   model.ControlDirective{Kind: model.ControlContinue},
						ToolCalls: []model.ToolCall{{ID: "bad", Name: "speak", RawArguments: `null`, ArgumentsError: "expected object"}},
					}}, nil
				}
				if len(request.Messages) < 3 {
					t.Error("correction feedback missing")
				}
				if cancelTurn {
					cancel()
				}
				<-modelCtx.Done()
				return model.Response{}, modelCtx.Err()
			})
			config := defaultLoopConfig(t)
			config.LLMTimeout = 30 * time.Millisecond
			loop := agent.NewLoop(provider, nil, config)
			env := &fakeEnvironment{}
			conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "test"}
			key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world", EntityID: "actor"}
			err := loop.HandleEvent(ctx, env, conn, key, entityTarget(key), newSpeakRegistry(), gameEvent("event", key))
			wantErr := context.DeadlineExceeded
			if cancelTurn {
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) || calls != 2 || len(env.submittedActions) != 0 || len(env.turnCompletions) != 1 {
				t.Fatalf("err=%v calls=%d actions=%d terminal=%d", err, calls, len(env.submittedActions), len(env.turnCompletions))
			}
		})
	}
}

func correctionResponse(id, arguments string) string {
	return correctionBatchResponse([][2]string{{id, arguments}})
}

func correctionBatchResponse(calls [][2]string) string {
	items := make([]any, 0, len(calls))
	for _, call := range calls {
		items = append(items, map[string]any{
			"id": call[0], "type": "function",
			"function": map[string]any{"name": "speak", "arguments": call[1]},
		})
	}
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"finish_reason": "tool_calls",
		"message":       map[string]any{"reasoning_content": "continue this step", "tool_calls": items},
	}}})
	return string(data)
}

func TestToolArgumentCorrectionThroughDeepSeekAndLoop(t *testing.T) {
	const settle = `{"choices":[{"finish_reason":"stop","message":{"content":"done"}}]}`
	const bad = `{"text":你好}`
	for _, tc := range []struct {
		name       string
		responses  []string
		wantTexts  []string
		wantCalls  int
		wantError  string
		maxSteps   int
		feedbackAt int
	}{
		{name: "malformed JSON repaired", responses: []string{correctionResponse("bad", bad), correctionResponse("fixed", `{"text":"corrected"}`), settle}, wantTexts: []string{"corrected"}, wantCalls: 3, feedbackAt: 1},
		{name: "schema error repaired", responses: []string{correctionResponse("bad", `{"text":3}`), correctionResponse("fixed", `{"text":"corrected"}`), settle}, wantTexts: []string{"corrected"}, wantCalls: 3, feedbackAt: 1},
		{name: "null is not empty object", responses: []string{correctionResponse("bad", `null`), correctionResponse("fixed", `{"text":"corrected"}`), settle}, wantTexts: []string{"corrected"}, wantCalls: 3, feedbackAt: 1},
		{name: "second JSON error stops", responses: []string{correctionResponse("bad", bad), correctionResponse("still_bad", bad), settle}, wantCalls: 2, wantError: "tool_argument_correction_failed", feedbackAt: 1},
		{name: "JSON then schema error stops", responses: []string{correctionResponse("bad", bad), correctionResponse("still_bad", `{"text":3}`), settle}, wantCalls: 2, wantError: "tool_argument_correction_failed", feedbackAt: 1},
		{name: "schema then JSON error stops", responses: []string{correctionResponse("bad", `{"text":3}`), correctionResponse("still_bad", bad), settle}, wantCalls: 2, wantError: "tool_argument_correction_failed", feedbackAt: 1},
		{name: "settle cannot hide uncorrected call", responses: []string{correctionResponse("bad", bad), settle}, wantCalls: 2, wantError: "tool_argument_correction_failed", feedbackAt: 1},
		{name: "reused correction ID stops", responses: []string{correctionResponse("bad", bad), correctionResponse("bad", `{"text":"corrected"}`), settle}, wantCalls: 2, wantError: "tool_argument_correction_failed", feedbackAt: 1},
		{name: "whole batch waits for correction", responses: []string{correctionBatchResponse([][2]string{{"valid", `{"text":"first"}`}, {"bad", bad}}), correctionBatchResponse([][2]string{{"fixed_one", `{"text":"first"}`}, {"fixed_two", `{"text":"second"}`}}), settle}, wantTexts: []string{"first", "second"}, wantCalls: 3, feedbackAt: 1},
		{name: "prior successful action executes once", responses: []string{correctionResponse("prior", `{"text":"prior"}`), correctionResponse("bad", bad), correctionResponse("fixed", `{"text":"corrected"}`), settle}, wantTexts: []string{"prior", "corrected"}, wantCalls: 4, feedbackAt: 2},
		{name: "one correction budget for entire turn", responses: []string{correctionResponse("bad", bad), correctionResponse("fixed", `{"text":"corrected"}`), correctionResponse("later_bad", bad), settle}, wantTexts: []string{"corrected"}, wantCalls: 3, wantError: "tool_argument_correction_failed", feedbackAt: 1},
		{name: "step budget wins", responses: []string{correctionResponse("bad", bad)}, wantCalls: 1, maxSteps: 1, wantError: "max_steps_exceeded"},
		{name: "broken envelope stops", responses: []string{`{"choices":`}, wantCalls: 1, wantError: "model_response_invalid_json"},
		{name: "truncation executes nothing", responses: []string{`{"choices":[{"finish_reason":"length","message":{"tool_calls":[{"id":"bad","type":"function","function":{"name":"speak","arguments":"{\"text\":\"partial\"}"}}]}}]}`}, wantCalls: 1, wantError: "model_response_truncated"},
		{name: "unidentified bad call stops", responses: []string{correctionResponse("", bad)}, wantCalls: 1, wantError: "provider_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests [][]correctionWireMessage
			var mu sync.Mutex
			env := &fakeEnvironment{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Messages []correctionWireMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				index := len(requests)
				requests = append(requests, payload.Messages)
				if index >= len(tc.responses) {
					t.Error("unexpected additional model request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.responses[index]))
			}))
			defer server.Close()
			config := defaultLoopConfig(t)
			config.MaxSteps = 6
			if tc.maxSteps > 0 {
				config.MaxSteps = tc.maxSteps
			}
			loop := agent.NewLoop(deepseek.NewProvider("test-key", "test-model", deepseek.WithBaseURL(server.URL)), nil, config)
			conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
			key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "actor:test"}
			err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), newSpeakRegistry(), gameEvent("event", key))
			mu.Lock()
			defer mu.Unlock()
			if (err != nil) != (tc.wantError != "") {
				t.Fatalf("HandleEvent = %v, want failure %q", err, tc.wantError)
			}
			if len(requests) != tc.wantCalls {
				t.Fatalf("model requests = %d, want %d", len(requests), tc.wantCalls)
			}
			var texts []string
			for _, action := range env.submittedActions {
				texts = append(texts, action.Arguments.Fields["text"].GetStringValue())
			}
			if !reflect.DeepEqual(texts, tc.wantTexts) {
				t.Fatalf("executed texts = %v, want %v", texts, tc.wantTexts)
			}
			if len(env.turnCompletions) != 1 {
				t.Fatalf("terminal signals = %d, want 1", len(env.turnCompletions))
			}
			terminal := env.turnCompletions[0]
			if tc.wantError != "" {
				if terminal.Status != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED || terminal.GetError().GetCode() != tc.wantError {
					t.Fatalf("terminal = %v, want failure %s", terminal, tc.wantError)
				}
			} else if terminal.Status != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
				t.Fatalf("terminal = %v, want completed", terminal)
			}
			if tc.feedbackAt == 0 {
				return
			}
			messages := requests[tc.feedbackAt]
			var original string
			var invalid bool
			for _, message := range messages {
				for _, call := range message.ToolCalls {
					if call.ID == "bad" {
						original = call.Function.Arguments
						if message.Role != "assistant" || message.ReasoningContent != "continue this step" || strings.Contains(message.Content, original) {
							t.Fatalf("call lost native shape or is duplicated: %+v", message)
						}
					}
				}
				if message.ToolCallID == "bad" {
					var result map[string]any
					if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
						t.Fatal(err)
					}
					invalid = message.Role == "tool" && result["status"] == "invalid"
				}
			}
			if original == "" || !invalid {
				t.Fatalf("request lost original bad call or paired error: %+v", messages)
			}
		})
	}
}
