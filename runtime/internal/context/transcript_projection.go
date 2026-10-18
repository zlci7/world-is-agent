package context

import (
	"fmt"
	"strings"

	"gameagent/runtime/internal/model"
)

func projectCurrentTurnTranscript(messages []model.Message, bounds projectionBounds, limit int) ([]model.Message, RetentionReport, error) {
	if len(messages) == 0 {
		return nil, RetentionReport{}, nil
	}

	out := make([]model.Message, len(messages))
	for i, message := range messages {
		out[i] = model.Message{
			Role:             message.Role,
			Content:          strings.TrimSpace(message.Content),
			ToolCalls:        projectTranscriptToolCalls(message.ToolCalls, bounds),
			ToolResults:      projectTranscriptToolResults(message.ToolResults, bounds),
			ReasoningContent: message.ReasoningContent,
		}
	}
	groups, err := transcriptCausalGroups(out)
	if err != nil {
		return nil, RetentionReport{}, err
	}
	trimmed, dropped, err := trimTranscriptGroups(groups, limit)
	if err != nil {
		return nil, RetentionReport{
			DroppedCount: len(out),
		}, err
	}
	return trimmed, RetentionReport{
		RetainedCount: len(trimmed),
		DroppedCount:  dropped,
	}, nil
}

type transcriptCausalGroup struct {
	messages []model.Message
}

func transcriptCausalGroups(messages []model.Message) ([]transcriptCausalGroup, error) {
	groups := make([]transcriptCausalGroup, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		if len(message.ToolCalls) == 0 {
			if len(message.ToolResults) > 0 {
				return nil, fmt.Errorf("%w: transcript tool result has no corresponding call", ErrInvalidInput)
			}
			groups = append(groups, transcriptCausalGroup{messages: []model.Message{message}})
			continue
		}

		if i+1 >= len(messages) {
			return nil, fmt.Errorf("%w: transcript tool call has no corresponding result", ErrInvalidInput)
		}
		resultMessage := messages[i+1]
		if len(resultMessage.ToolResults) == 0 || len(resultMessage.ToolCalls) > 0 {
			return nil, fmt.Errorf("%w: transcript tool call has no corresponding result", ErrInvalidInput)
		}
		if !toolResultsMatchCalls(message.ToolCalls, resultMessage.ToolResults) {
			return nil, fmt.Errorf("%w: transcript tool call has no corresponding result", ErrInvalidInput)
		}
		groups = append(groups, transcriptCausalGroup{messages: []model.Message{message, resultMessage}})
		i++
	}
	return groups, nil
}

func toolResultsMatchCalls(calls []model.ToolCall, results []model.ToolResult) bool {
	if len(calls) == 0 || len(calls) != len(results) {
		return false
	}
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			return false
		}
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
	}
	matched := make(map[string]struct{}, len(results))
	for _, result := range results {
		id := strings.TrimSpace(result.ToolCallID)
		if id == "" {
			return false
		}
		if _, ok := matched[id]; ok {
			return false
		}
		if _, ok := seen[id]; !ok {
			return false
		}
		matched[id] = struct{}{}
	}
	return len(matched) == len(seen)
}

func trimTranscriptGroups(groups []transcriptCausalGroup, limit int) ([]model.Message, int, error) {
	if len(groups) == 0 {
		return nil, 0, nil
	}
	if limit <= 0 {
		return flattenTranscriptGroups(groups), 0, nil
	}

	selectedStart := len(groups)
	totalTokens := 0
	for i := len(groups) - 1; i >= 0; i-- {
		groupTokens := sectionProjectionEstimatedTokens(groups[i].messages)
		if totalTokens+groupTokens > limit {
			if selectedStart == len(groups) {
				return nil, len(flattenTranscriptGroups(groups)), fmt.Errorf("%w: latest transcript causal group exceeds token budget", ErrBudgetExceeded)
			}
			break
		}
		selectedStart = i
		totalTokens += groupTokens
	}

	kept := flattenTranscriptGroups(groups[selectedStart:])
	dropped := len(flattenTranscriptGroups(groups[:selectedStart]))
	return kept, dropped, nil
}

func flattenTranscriptGroups(groups []transcriptCausalGroup) []model.Message {
	count := 0
	for _, group := range groups {
		count += len(group.messages)
	}
	if count == 0 {
		return nil
	}
	out := make([]model.Message, 0, count)
	for _, group := range groups {
		out = append(out, group.messages...)
	}
	return out
}

func projectTranscriptToolCalls(calls []model.ToolCall, bounds projectionBounds) []model.ToolCall {
	if len(calls) == 0 {
		return nil
	}

	out := make([]model.ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		out[i].Arguments = projectToolArguments(call.Arguments, bounds)
	}
	return out
}

func projectTranscriptToolResults(results []model.ToolResult, bounds projectionBounds) []model.ToolResult {
	if len(results) == 0 {
		return nil
	}

	out := make([]model.ToolResult, len(results))
	for i, result := range results {
		out[i] = result
		out[i].Message = sanitizeToolResultMessage(result.Message)
		out[i].Output = projectToolResultOutput(result.Output, bounds)
	}
	return out
}

func sanitizeToolResultMessage(message string) string {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if message == "" {
		return ""
	}
	if index := strings.Index(message, "\n"); index >= 0 {
		message = strings.TrimSpace(message[:index])
	}
	if index := strings.Index(message, "{"); index >= 0 {
		message = strings.TrimSpace(message[:index])
	}
	if len([]rune(message)) <= 120 {
		return message
	}
	runes := []rune(message)
	return strings.TrimSpace(string(runes[:120]))
}
