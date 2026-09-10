package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tokenestimate"
)

const defaultEndpoint = "https://api.openai.com/v1/responses"

var _ model.Provider = (*Provider)(nil)

type Provider struct {
	apiKey   string
	model    string
	endpoint string
	client   *http.Client
	window   model.WindowLimits
}

func (p *Provider) ModelWindow() model.WindowLimits { return p.window }
func WithModelWindow(window model.WindowLimits) Option {
	return func(p *Provider) { p.window = window }
}

type Option func(*Provider)

func WithBaseURL(baseURL string) Option {
	return func(p *Provider) {
		if baseURL == "" {
			return
		}
		p.endpoint = strings.TrimRight(baseURL, "/") + "/responses"
	}
}

func NewProvider(apiKey string, modelName string, opts ...Option) *Provider {
	provider := &Provider{
		apiKey:   apiKey,
		model:    modelName,
		endpoint: defaultEndpoint,
		client:   http.DefaultClient,
	}

	for _, opt := range opts {
		opt(provider)
	}

	return provider
}

func (p *Provider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if p.apiKey == "" {
		return model.Response{}, errors.New("openai api key is empty")
	}

	body, err := p.buildRequest(req)
	if err != nil {
		return model.Response{}, err
	}
	if p.window.ContextTokens > 0 {
		if err := p.window.Check(tokenestimate.EstimateText(string(body)), p.window.OutputTokens); err != nil {
			return model.Response{}, err
		}
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return model.Response{}, err
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return model.Response{}, err
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return model.Response{}, err
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return model.Response{}, fmt.Errorf("openai response failed: status=%d", httpResp.StatusCode)
	}

	return parseResponse(respBody)
}

func (p *Provider) buildRequest(req model.Request) ([]byte, error) {
	tools := make([]map[string]any, 0, len(req.Tools))

	for _, tool := range req.Tools {
		var schema map[string]any
		if err := json.Unmarshal([]byte(tool.InputSchema), &schema); err != nil {
			return nil, err
		}
		schema = normalizeStrictSchema(schema)

		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  schema,
			"strict":      true,
		})
	}

	payload := map[string]any{
		"model": p.model,
		"input": buildInput(req.Messages),
	}
	if p.window.OutputTokens > 0 {
		payload["max_output_tokens"] = p.window.OutputTokens
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}

	if instructions := buildInstructions(req.System, req.Controls); instructions != "" {
		payload["instructions"] = instructions
	}

	return json.Marshal(payload)
}

func normalizeStrictSchema(schema map[string]any) map[string]any {
	normalized := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		normalized[key] = value
	}

	if normalized["type"] == "object" {
		if _, exists := normalized["additionalProperties"]; !exists {
			normalized["additionalProperties"] = false
		}
	}

	return normalized
}

func parseResponse(data []byte) (model.Response, error) {
	var raw struct {
		Output []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return model.Response{}, err
	}

	var calls []model.ToolCall
	decision := model.ModelDecision{
		Control: model.ControlDirective{Kind: model.ControlUnspecified},
	}
	functionOrdinal := 0
	for _, item := range raw.Output {
		if item.Type != "function_call" {
			continue
		}
		functionOrdinal++

		name := strings.TrimSpace(item.Name)
		if name == "" {
			return model.Response{}, errors.New("openai function_call name is empty")
		}

		args, err := parseArguments(item.Arguments)
		if err != nil {
			return model.Response{}, err
		}

		if name == model.InternalSettleToolName {
			decision.Control = model.ControlDirective{
				Kind:   model.ControlSettle,
				Reason: stringArgument(args, "reason"),
			}
			continue
		}

		calls = append(calls, model.ToolCall{
			ID:        openAIToolCallID(item.CallID, item.ID, functionOrdinal),
			Name:      name,
			Arguments: args,
		})
	}

	if decision.Control.Kind == model.ControlUnspecified {
		if len(calls) == 0 {
			decision.Control = model.ControlDirective{Kind: model.ControlSettle}
		} else {
			decision.Control = model.ControlDirective{Kind: model.ControlContinue}
		}
	}
	decision.ToolCalls = calls
	return model.Response{Decision: decision}, nil
}

func buildInput(messages []model.Message) []map[string]string {
	input := make([]map[string]string, 0, len(messages))

	for _, message := range messages {
		input = append(input, map[string]string{
			"role":    providerInputRole(message.Role),
			"content": message.Content,
		})
	}

	return input
}

func providerInputRole(role model.Role) string {
	if role == model.RoleTool {
		return string(model.RoleUser)
	}
	return string(role)
}

func buildInstructions(system string, controls []model.ControlDefinition) string {
	instructions := strings.TrimSpace(system)
	for _, control := range controls {
		if control.Kind != model.ControlSettle {
			continue
		}
		settleInstruction := "When the current turn needs no environment action, return no tool calls."
		if control.Description != "" {
			settleInstruction += " " + control.Description
		}
		if instructions == "" {
			instructions = settleInstruction
		} else {
			instructions += "\n\n" + settleInstruction
		}
	}
	return instructions
}

func parseArguments(arguments string) (map[string]any, error) {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}, nil
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func openAIToolCallID(callID string, id string, ordinal int) string {
	if strings.TrimSpace(callID) != "" {
		return callID
	}
	if strings.TrimSpace(id) != "" {
		return id
	}
	return fmt.Sprintf("openai_call_%d", ordinal)
}

func stringArgument(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}
