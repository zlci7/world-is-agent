package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tokenestimate"
)

var _ model.TextGenerator = (*Provider)(nil)

func (p *Provider) GenerateText(ctx context.Context, req model.TextRequest) (model.TextResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	req, err := model.ValidateTextRequest(req)
	if err != nil {
		return model.TextResponse{}, err
	}
	if p.apiKey == "" {
		return model.TextResponse{}, errors.New("deepseek api key is empty")
	}

	messages := make([]map[string]string, 0, 2)
	if req.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.System})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Input})
	body, err := json.Marshal(map[string]any{
		"model":      p.model,
		"messages":   messages,
		"stream":     false,
		"max_tokens": req.MaxOutputTokens,
	})
	if err != nil {
		return model.TextResponse{}, model.ErrInvalidTextRequest
	}
	inputTokens := tokenestimate.EstimateText(string(body))
	if inputTokens > req.MaxInputTokens {
		return model.TextResponse{}, model.ErrTextInputTooLarge
	}
	if p.window.ContextTokens > 0 {
		if err := p.window.Check(inputTokens, req.MaxOutputTokens); err != nil {
			return model.TextResponse{}, err
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return model.TextResponse{}, textRequestError(ctx, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return model.TextResponse{}, textRequestError(ctx, err)
	}
	defer httpResp.Body.Close()
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return model.TextResponse{}, fmt.Errorf("deepseek response failed: status=%d", httpResp.StatusCode)
	}

	readLimit := int64(req.MaxResponseBytes)
	if readLimit < 1<<63-1 {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, readLimit))
	if err != nil {
		return model.TextResponse{}, textRequestError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	if len(data) > req.MaxResponseBytes {
		return model.TextResponse{}, model.ErrTextResponseTooLarge
	}
	resp, err := parseTextResponse(data)
	if err != nil {
		return model.TextResponse{}, err
	}
	if err := model.ValidateTextResponse(req, resp); err != nil {
		if errors.Is(err, model.ErrInvalidTextResponse) {
			return model.TextResponse{}, fmt.Errorf("%w: response text is empty or invalid UTF-8", err)
		}
		return model.TextResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	return resp, nil
}

func parseTextResponse(data []byte) (model.TextResponse, error) {
	var raw struct {
		Error   *json.RawMessage `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role         string            `json:"role"`
				Content      string            `json:"content"`
				Refusal      *json.RawMessage  `json:"refusal"`
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall *json.RawMessage  `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if model.ValidateTextJSON(data) != nil {
		return model.TextResponse{}, fmt.Errorf("%w: response body is not strict JSON", model.ErrInvalidTextResponse)
	}
	if json.Unmarshal(data, &raw) != nil {
		return model.TextResponse{}, fmt.Errorf("%w: response body does not match the chat schema", model.ErrInvalidTextResponse)
	}
	if raw.Error != nil {
		return model.TextResponse{}, fmt.Errorf("%w: provider returned an error object", model.ErrInvalidTextResponse)
	}
	if len(raw.Choices) != 1 {
		return model.TextResponse{}, fmt.Errorf("%w: choice count is %d", model.ErrInvalidTextResponse, len(raw.Choices))
	}
	choice := raw.Choices[0]
	if choice.FinishReason != "stop" {
		return model.TextResponse{}, fmt.Errorf("%w: finish_reason is %q", model.ErrInvalidTextResponse, choice.FinishReason)
	}
	if choice.Message.Role != "assistant" {
		return model.TextResponse{}, fmt.Errorf("%w: message role is %q", model.ErrInvalidTextResponse, choice.Message.Role)
	}
	if choice.Message.Refusal != nil {
		return model.TextResponse{}, fmt.Errorf("%w: response contains a refusal", model.ErrInvalidTextResponse)
	}
	if len(choice.Message.ToolCalls) != 0 || choice.Message.FunctionCall != nil {
		return model.TextResponse{}, fmt.Errorf("%w: response contains tool calls", model.ErrInvalidTextResponse)
	}
	return model.TextResponse{Text: choice.Message.Content}, nil
}

func textRequestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("deepseek text request failed")
}
