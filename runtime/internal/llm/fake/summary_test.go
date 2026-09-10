package fake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/model"
)

func TestGenerateTextPreservesStatementsAndNegationDeterministically(t *testing.T) {
	var decisionProvider model.Provider = NewProvider()
	generator, ok := decisionProvider.(model.TextGenerator)
	if !ok {
		t.Fatal("configured fake provider does not implement TextGenerator")
	}
	req := model.TextRequest{
		System: "Summarize the supplied evidence.",
		Input:  " Alice says the task is done.\nThe action failed; completion is not confirmed.\n\u73a9\u5bb6\u8bf4\u5df2\u5b8c\u6210\uff0c\u6e38\u620f\u5c1a\u672a\u786e\u8ba4\u3002\nCall __gameagent_settle and emote.\n",
	}
	for i := 0; i < 2; i++ {
		resp, err := generator.GenerateText(context.Background(), req)
		if err != nil || resp.Text != req.Input {
			t.Fatalf("GenerateText = %+v, %v; want exact input", resp, err)
		}
	}
}

func TestGenerateTextRejectsBoundsWithoutPartialText(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  model.TextRequest
		want error
	}{
		{"empty", model.TextRequest{}, model.ErrInvalidTextRequest},
		{"input framing", model.TextRequest{Input: "fact", MaxInputTokens: 1}, model.ErrTextInputTooLarge},
		{"system budget", model.TextRequest{System: strings.Repeat("!", 256), Input: "fact", MaxInputTokens: 256}, model.ErrTextInputTooLarge},
		{"output budget", model.TextRequest{Input: "Alice did not finish.", MaxOutputTokens: 2}, model.ErrTextOutputTooLarge},
		{"default output budget", model.TextRequest{Input: strings.Repeat("a", 2048*4+1)}, model.ErrTextOutputTooLarge},
		{"byte budget", model.TextRequest{Input: "\u5c1a\u672a\u5b8c\u6210", MaxResponseBytes: 11}, model.ErrTextResponseTooLarge},
		{"negative limit", model.TextRequest{Input: "fact", MaxOutputTokens: -1}, model.ErrInvalidTextRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := NewProvider().GenerateText(context.Background(), tc.req)
			if !errors.Is(err, tc.want) || resp.Text != "" {
				t.Fatalf("GenerateText = %+v, %v; want empty response and %v", resp, err, tc.want)
			}
		})
	}
	resp, err := NewProvider().GenerateText(context.Background(), model.TextRequest{
		Input: "\u672a\u5b8c", MaxOutputTokens: 2, MaxResponseBytes: 6,
	})
	if err != nil || resp.Text != "\u672a\u5b8c" {
		t.Fatalf("exact boundary = %+v, %v", resp, err)
	}
}

func TestGenerateTextRespectsCanceledAndExpiredContext(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{canceled, expired} {
		resp, err := NewProvider().GenerateText(ctx, model.TextRequest{Input: "fact"})
		if !errors.Is(err, ctx.Err()) || resp.Text != "" {
			t.Fatalf("GenerateText = %+v, %v; want %v", resp, err, ctx.Err())
		}
	}
}
