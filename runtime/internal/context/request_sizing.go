package context

import (
	"fmt"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tokenestimate"
	"gameagent/runtime/internal/tool"
)

type requestMessageStructureForSizing struct {
	Role model.Role `json:"role,omitempty"`
}

type requestToolForSizing struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
}

func EstimateRequestTokens(req model.Request) (RequestTokenSummary, error) {
	toolsEstimatedTokens, err := estimateRequestTools(req.Tools)
	if err != nil {
		return RequestTokenSummary{}, err
	}
	controlsEstimatedTokens, err := estimateRequestSectionTokens(req.Controls)
	if err != nil {
		return RequestTokenSummary{}, err
	}
	messagesEstimatedTokens, userMessageEstimatedTokens, err := measureMessages(req.Messages)
	if err != nil {
		return RequestTokenSummary{}, err
	}
	summary := RequestTokenSummary{
		SystemEstimatedTokens:   tokenestimate.EstimateText(req.System),
		ToolsEstimatedTokens:    toolsEstimatedTokens,
		ControlsEstimatedTokens: controlsEstimatedTokens,
	}
	summary.MessagesEstimatedTokens = messagesEstimatedTokens
	summary.UserMessageEstimatedTokens = userMessageEstimatedTokens
	summary.TotalEstimatedTokens = summary.SystemEstimatedTokens + summary.MessagesEstimatedTokens + summary.ToolsEstimatedTokens + summary.ControlsEstimatedTokens
	return summary, nil
}

func measureMessages(messages []model.Message) (int, int, error) {
	if len(messages) == 0 {
		return 0, 0, nil
	}
	items := make([]requestMessageStructureForSizing, 0, len(messages))
	contentEstimatedTokens := 0
	userEstimatedTokens := 0
	for _, message := range messages {
		contentTokens := tokenestimate.EstimateText(message.Content)
		contentEstimatedTokens += contentTokens
		items = append(items, requestMessageStructureForSizing{
			Role: message.Role,
		})
		if message.Role == model.RoleUser {
			userEstimatedTokens += contentTokens
		}
	}
	structureEstimatedTokens, err := estimateRequestSectionTokens(items)
	if err != nil {
		return 0, 0, err
	}
	return contentEstimatedTokens + structureEstimatedTokens, userEstimatedTokens, nil
}

func estimateRequestSectionTokens(value any) (int, error) {
	return tokenestimate.EstimateStableJSON(value)
}

func estimateRequestTools(tools []model.ToolDefinition) (int, error) {
	if len(tools) == 0 {
		return 0, nil
	}
	items := make([]requestToolForSizing, 0, len(tools))
	for _, tool := range tools {
		schema, err := tokenestimate.ParseJSONDocument(tool.InputSchema)
		if err != nil {
			return 0, err
		}
		items = append(items, requestToolForSizing{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}
	return estimateRequestSectionTokens(items)
}

func RequestEstimatedTokensExceedBudget(summary RequestTokenSummary, budget BudgetConfig) bool {
	if budget.MaxSystemTokens > 0 && summary.SystemEstimatedTokens > budget.MaxSystemTokens {
		return true
	}
	if budget.MaxUserMessageTokens > 0 && summary.UserMessageEstimatedTokens > budget.MaxUserMessageTokens {
		return true
	}
	if budget.MaxRequestTokens > 0 && summary.TotalEstimatedTokens > budget.MaxRequestTokens {
		return true
	}
	return false
}

// RequiredRequestSectionExceedsBudget is used after Engine has removed optional
// projection content; a remaining system or user-message overflow means the
// required request envelope still cannot fit the configured hard gate.
func RequiredRequestSectionExceedsBudget(summary RequestTokenSummary, budget BudgetConfig) bool {
	if budget.MaxSystemTokens > 0 && summary.SystemEstimatedTokens > budget.MaxSystemTokens {
		return true
	}
	return budget.MaxUserMessageTokens > 0 && summary.UserMessageEstimatedTokens > budget.MaxUserMessageTokens
}

func RequestTokenBudgetError(summary RequestTokenSummary, budget BudgetConfig) error {
	return fmt.Errorf(
		"%w: final model request exceeds token budget (total=%d max_request=%d system=%d max_system=%d user=%d max_user=%d)",
		ErrBudgetExceeded,
		summary.TotalEstimatedTokens,
		budget.MaxRequestTokens,
		summary.SystemEstimatedTokens,
		budget.MaxSystemTokens,
		summary.UserMessageEstimatedTokens,
		budget.MaxUserMessageTokens,
	)
}

func ToolAdmissionSummaryFromReport(report tool.ToolAdmissionReport) ToolAdmissionSummary {
	return ToolAdmissionSummary{
		AcceptedToolCount:               report.AcceptedToolCount,
		AcceptedToolNames:               append([]string(nil), report.AcceptedToolNames...),
		AcceptedToolNamesTruncatedCount: report.AcceptedToolNamesTruncatedCount,
		DroppedToolCount:                report.DroppedToolCount,
		DroppedToolNames:                append([]string(nil), report.DroppedToolNames...),
		DroppedToolNamesTruncatedCount:  report.DroppedToolNamesTruncatedCount,
		DroppedTools:                    append([]tool.ToolAdmissionDrop(nil), report.DroppedTools...),
		DroppedToolsTruncatedCount:      report.DroppedToolsTruncatedCount,
		DroppedReasonCounts:             copyStringIntMap(report.DroppedReasonCounts),
		TotalSchemaEstimatedTokens:      report.TotalSchemaEstimatedTokens,
	}
}

func copyStringIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func (r ContextBuildReport) WithToolAdmission(summary ToolAdmissionSummary) ContextBuildReport {
	r.ToolAdmission = summary
	return r
}

func (r ContextBuildReport) WithFinalRequestSize(summary RequestTokenSummary) ContextBuildReport {
	r.FinalRequestSize = summary
	return r
}

func (r ContextBuildReport) WithReason(reason string) ContextBuildReport {
	r.addReason(reason)
	return r
}
