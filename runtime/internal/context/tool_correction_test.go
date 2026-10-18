package context_test

import (
	"errors"
	"strings"
	"testing"

	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/model"
)

func TestCorrectionTranscriptPreservesRawArgumentsAndContinuationWithinBudget(t *testing.T) {
	input := validEngineInput(t)
	const raw = "  {\"text\":你好}\n"
	input.Transcript = []model.Message{
		{Role: model.RoleAssistant, ReasoningContent: "required provider continuation", ToolCalls: []model.ToolCall{{ID: "bad", Name: "say", RawArguments: raw, ArgumentsError: "invalid JSON"}}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{ToolCallID: "bad", Status: "invalid", Code: "tool_arguments_invalid_json"}}},
	}
	result := buildHistoryProjection(t, input, agentcontext.DefaultBudgetConfig())
	req, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatal(err)
	}
	call := req.Messages[1]
	if call.ToolCalls[0].RawArguments != raw || call.ReasoningContent != input.Transcript[0].ReasoningContent || call.ToolCalls[0].Arguments != nil {
		t.Fatalf("correction transcript changed: %+v", call)
	}
	withReasoning, err := agentcontext.EstimateRequestTokens(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages[1].ReasoningContent = ""
	withoutReasoning, _ := agentcontext.EstimateRequestTokens(req)
	if withReasoning.MessagesEstimatedTokens <= withoutReasoning.MessagesEstimatedTokens {
		t.Fatal("provider continuation was omitted from request sizing")
	}
	originalMessage := input.Transcript[0]
	for _, large := range []string{"arguments", "continuation"} {
		t.Run(large, func(t *testing.T) {
			message := originalMessage
			message.ToolCalls = append([]model.ToolCall(nil), message.ToolCalls...)
			if large == "arguments" {
				message.ToolCalls[0].RawArguments = strings.Repeat("original bad output ", 500)
			} else {
				message.ReasoningContent = strings.Repeat("provider continuation ", 500)
			}
			input.Transcript[0] = message
			_, err := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxTranscriptTokens: 128}).Build(input)
			if !errors.Is(err, agentcontext.ErrBudgetExceeded) {
				t.Fatalf("oversized causal group = %v, want budget failure", err)
			}
		})
	}
}
