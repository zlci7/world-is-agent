package model

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gameagent/runtime/internal/tokenestimate"
)

const (
	DefaultTextMaxInputTokens   = 32768
	DefaultTextMaxOutputTokens  = 2048
	DefaultTextMaxResponseBytes = 1 << 20
)

var (
	ErrInvalidTextRequest   = errors.New("invalid text request")
	ErrTextInputTooLarge    = errors.New("text input exceeds token limit")
	ErrTextOutputTooLarge   = errors.New("text output exceeds token limit")
	ErrTextResponseTooLarge = errors.New("text response exceeds byte limit")
	ErrInvalidTextResponse  = errors.New("invalid or incomplete text response")
)

// TextRequest is a tool-free generation request. Zero limits use the defaults;
// token limits use tokenestimate, and MaxResponseBytes bounds the full response body.
type TextRequest struct {
	System           string
	Input            string
	MaxInputTokens   int
	MaxOutputTokens  int
	MaxResponseBytes int
}

type TextResponse struct {
	Text string
}

type TextGenerator interface {
	GenerateText(context.Context, TextRequest) (TextResponse, error)
}

// ValidateTextRequest applies defaults and checks the complete framed input.
func ValidateTextRequest(req TextRequest) (TextRequest, error) {
	req = textRequestDefaults(req)
	if req.MaxInputTokens < 0 || req.MaxOutputTokens < 0 || req.MaxResponseBytes < 0 ||
		!utf8.ValidString(req.System) || !utf8.ValidString(req.Input) || strings.TrimSpace(req.Input) == "" {
		return TextRequest{}, ErrInvalidTextRequest
	}

	systemTokens := tokenestimate.EstimateText(req.System)
	if systemTokens > req.MaxInputTokens || tokenestimate.EstimateText(req.Input) > req.MaxInputTokens-systemTokens {
		return TextRequest{}, ErrTextInputTooLarge
	}

	// Count escaped JSON for both roles, including an empty system message. The
	// reserve covers native message delimiters and the assistant response prefix.
	const framingReserve = 16
	tokens, err := tokenestimate.EstimateStableJSON(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.Input},
		},
	})
	if err != nil {
		return TextRequest{}, ErrInvalidTextRequest
	}
	if tokens > req.MaxInputTokens-framingReserve {
		return TextRequest{}, ErrTextInputTooLarge
	}
	return req, nil
}

// ValidateTextResponse checks text without trimming or truncating its content.
func ValidateTextResponse(req TextRequest, resp TextResponse) error {
	req = textRequestDefaults(req)
	if req.MaxInputTokens < 0 || req.MaxOutputTokens < 0 || req.MaxResponseBytes < 0 {
		return ErrInvalidTextRequest
	}
	if !utf8.ValidString(resp.Text) || strings.TrimSpace(resp.Text) == "" {
		return ErrInvalidTextResponse
	}
	if len(resp.Text) > req.MaxResponseBytes {
		return ErrTextResponseTooLarge
	}
	if tokenestimate.EstimateText(resp.Text) > req.MaxOutputTokens {
		return ErrTextOutputTooLarge
	}
	return nil
}

func textRequestDefaults(req TextRequest) TextRequest {
	if req.MaxInputTokens == 0 {
		req.MaxInputTokens = DefaultTextMaxInputTokens
	}
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = DefaultTextMaxOutputTokens
	}
	if req.MaxResponseBytes == 0 {
		req.MaxResponseBytes = DefaultTextMaxResponseBytes
	}
	return req
}
