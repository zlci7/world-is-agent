package deepseek

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

const defaultEndpoint = "https://api.deepseek.com/chat/completions"

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
		p.endpoint = strings.TrimRight(baseURL, "/") + "/chat/completions"
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

// Generate 调用 DeepSeek Chat Completions，并把 tool_call 转回 Runtime 统一模型。
func (p *Provider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if p.apiKey == "" {
		return model.Response{}, errors.New("deepseek api key is empty")
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
		return model.Response{}, fmt.Errorf("deepseek response failed: status=%d", httpResp.StatusCode)
	}

	return parseResponse(respBody)
}

func (p *Provider) buildRequest(req model.Request) ([]byte, error) {
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		var schema map[string]any
		if err := json.Unmarshal([]byte(tool.InputSchema), &schema); err != nil {
			return nil, fmt.Errorf("invalid input schema for tool %q: %w", tool.Name, err)
		}

		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  schema,
			},
		})
	}

	payload := map[string]any{
		"model":    p.model,
		"messages": buildMessages(req),
		"stream":   false,
	}
	if p.window.OutputTokens > 0 {
		payload["max_tokens"] = p.window.OutputTokens
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}

	return json.Marshal(payload)
}

func buildMessages(req model.Request) []map[string]string {
	messages := make([]map[string]string, 0, len(req.Messages)+1)

	if system := buildSystemContent(req.System, req.Controls); system != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": system,
		})
	}

	for _, message := range req.Messages {
		messages = append(messages, map[string]string{
			"role":    providerMessageRole(message.Role),
			"content": message.Content,
		})
	}

	return messages
}

func providerMessageRole(role model.Role) string {
	if role == model.RoleTool {
		return string(model.RoleUser)
	}
	return string(role)
}

func parseResponse(data []byte) (model.Response, error) {
	var raw struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return model.Response{}, err
	}

	if raw.Error != nil {
		return model.Response{}, fmt.Errorf("deepseek error %s: %s", raw.Error.Code, raw.Error.Message)
	}

	var calls []model.ToolCall
	decision := model.ModelDecision{
		Control: model.ControlDirective{Kind: model.ControlUnspecified},
	}
	functionOrdinal := 0
	for _, choice := range raw.Choices {
		for _, call := range choice.Message.ToolCalls {
			if call.Type != "" && call.Type != "function" {
				continue
			}
			functionOrdinal++
			name := strings.TrimSpace(call.Function.Name)
			if name == "" {
				return model.Response{}, errors.New("deepseek function_call name is empty")
			}

			args, err := parseArguments(call.Function.Arguments)
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
				ID:        deepSeekToolCallID(call.ID, functionOrdinal),
				Name:      name,
				Arguments: args,
			})
		}
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

func buildSystemContent(system string, controls []model.ControlDefinition) string {
	content := strings.TrimSpace(system)
	for _, control := range controls {
		if control.Kind != model.ControlSettle {
			continue
		}
		settleInstruction := "When the current turn needs no environment action, return no tool calls."
		if control.Description != "" {
			settleInstruction += " " + control.Description
		}
		if content == "" {
			content = settleInstruction
		} else {
			content += "\n\n" + settleInstruction
		}
	}
	return content
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

func deepSeekToolCallID(id string, ordinal int) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return fmt.Sprintf("deepseek_call_%d", ordinal)
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
