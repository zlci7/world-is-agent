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
		return model.TextResponse{}, errors.New("openai api key is empty")
	}

	payload := map[string]any{
		"model":             p.model,
		"input":             []map[string]string{{"role": "user", "content": req.Input}},
		"max_output_tokens": req.MaxOutputTokens,
		"stream":            false,
		"truncation":        "disabled",
	}
	if req.System != "" {
		payload["instructions"] = req.System
	}
	body, err := json.Marshal(payload)
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
		return model.TextResponse{}, fmt.Errorf("openai response failed: status=%d", httpResp.StatusCode)
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
		return model.TextResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	return resp, nil
}

func parseTextResponse(data []byte) (model.TextResponse, error) {
	var raw struct {
		Status            string           `json:"status"`
		Error             *json.RawMessage `json:"error"`
		IncompleteDetails *json.RawMessage `json:"incomplete_details"`
		Output            []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if model.ValidateTextJSON(data) != nil || json.Unmarshal(data, &raw) != nil || raw.Status != "completed" ||
		raw.Error != nil || raw.IncompleteDetails != nil {
		return model.TextResponse{}, model.ErrInvalidTextResponse
	}
	var text strings.Builder
	for _, item := range raw.Output {
		if item.Status != "" && item.Status != "completed" {
			return model.TextResponse{}, model.ErrInvalidTextResponse
		}
		switch item.Type {
		case "reasoning":
			continue
		case "message":
			if item.Role != "assistant" || item.Status != "completed" || len(item.Content) == 0 {
				return model.TextResponse{}, model.ErrInvalidTextResponse
			}
			for _, part := range item.Content {
				if part.Type != "output_text" || part.Text == "" {
					return model.TextResponse{}, model.ErrInvalidTextResponse
				}
				text.WriteString(part.Text)
			}
		default:
			return model.TextResponse{}, model.ErrInvalidTextResponse
		}
	}
	return model.TextResponse{Text: text.String()}, nil
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
	return errors.New("openai text request failed")
}
