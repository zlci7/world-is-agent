package model

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateTextRequestAppliesDefaultsAndPreservesContent(t *testing.T) {
	req := TextRequest{System: "  Keep claims as claims.\n", Input: " Alice did not act.\n"}
	got, err := ValidateTextRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.System != req.System || got.Input != req.Input {
		t.Fatalf("content changed: %+v", got)
	}
	if got.MaxInputTokens != 32768 || got.MaxOutputTokens != 2048 || got.MaxResponseBytes != 1<<20 {
		t.Fatalf("defaults = %+v", got)
	}

	req.MaxInputTokens, req.MaxOutputTokens, req.MaxResponseBytes = 256, 32, 1024
	got, err = ValidateTextRequest(req)
	if err != nil || got != req {
		t.Fatalf("custom limits = %+v, %v; want %+v", got, err, req)
	}
}

func TestValidateTextRequestRejectsInvalidInputAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  TextRequest
	}{
		{"empty", TextRequest{}},
		{"whitespace", TextRequest{Input: " \n\t"}},
		{"invalid input encoding", TextRequest{Input: string([]byte{0xff})}},
		{"invalid system encoding", TextRequest{System: string([]byte{0xff}), Input: "facts"}},
		{"negative input limit", TextRequest{Input: "facts", MaxInputTokens: -1}},
		{"negative output limit", TextRequest{Input: "facts", MaxOutputTokens: -1}},
		{"negative byte limit", TextRequest{Input: "facts", MaxResponseBytes: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateTextRequest(tc.req); !errors.Is(err, ErrInvalidTextRequest) {
				t.Fatalf("error = %v, want invalid request", err)
			}
		})
	}
}

func TestValidateTextRequestCountsFullFramingAndEscaping(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  TextRequest
	}{
		{"message framing", TextRequest{Input: "a", MaxInputTokens: 46}},
		{"system content", TextRequest{System: strings.Repeat("!", 256), Input: "a", MaxInputTokens: 256}},
		{"escaped content", TextRequest{Input: strings.Repeat("\"\\", 64), MaxInputTokens: 200}},
		{"unicode", TextRequest{Input: strings.Repeat("\u672a", 128), MaxInputTokens: 128}},
		{"default input limit", TextRequest{Input: strings.Repeat("a", 32768*4)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateTextRequest(tc.req); !errors.Is(err, ErrTextInputTooLarge) {
				t.Fatalf("error = %v, want input budget failure", err)
			}
		})
	}
	if _, err := ValidateTextRequest(TextRequest{Input: "a", MaxInputTokens: 64}); err != nil {
		t.Fatalf("small complete request should fit: %v", err)
	}
}

func TestValidateTextResponseEnforcesCompleteTextBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  TextRequest
		text string
		want error
	}{
		{"empty", TextRequest{}, "", ErrInvalidTextResponse},
		{"whitespace", TextRequest{}, " \n\t", ErrInvalidTextResponse},
		{"invalid encoding", TextRequest{}, string([]byte{0xff}), ErrInvalidTextResponse},
		{"exact ascii tokens", TextRequest{MaxOutputTokens: 2}, "abcdefgh", nil},
		{"ascii overflow", TextRequest{MaxOutputTokens: 2}, "abcdefghi", ErrTextOutputTooLarge},
		{"exact unicode tokens", TextRequest{MaxOutputTokens: 2}, "\u672a\u5b8c", nil},
		{"unicode overflow", TextRequest{MaxOutputTokens: 2}, "\u672a\u5b8c\u6210", ErrTextOutputTooLarge},
		{"exact bytes", TextRequest{MaxResponseBytes: 3}, "\u672a", nil},
		{"byte overflow", TextRequest{MaxResponseBytes: 2}, "\u672a", ErrTextResponseTooLarge},
		{"default output limit", TextRequest{}, strings.Repeat("a", 2048*4+1), ErrTextOutputTooLarge},
		{"padding counts", TextRequest{MaxOutputTokens: 2}, "a" + strings.Repeat(" ", 8), ErrTextOutputTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTextResponse(tc.req, TextResponse{Text: tc.text}); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
