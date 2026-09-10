package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gameagent/runtime/internal/model"
)

func TestConfiguredProviderWindowCapsDecisionAndSummary(t *testing.T) {
	for _, kind := range []string{"deepseek", "openai"} {
		t.Run(kind, func(t *testing.T) {
			var received []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received = append(received, body)
				if kind == "deepseek" {
					_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"A bounded summary."}}]}`))
				} else {
					_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A bounded summary."}]}]}`))
				}
			}))
			defer server.Close()
			t.Setenv("WINDOW_TEST_API_KEY", "test-value")
			provider, err := NewProvider(Config{Provider: kind, Model: "test-model", APIKey: "env:WINDOW_TEST_API_KEY", BaseURL: server.URL, WindowLimits: model.WindowLimits{ContextTokens: 4096, OutputTokens: 512}})
			if err != nil {
				t.Fatal(err)
			}
			if provider.(model.WindowProvider).ModelWindow().InputTokens() != 3584 {
				t.Fatal("window missing")
			}
			_, err = provider.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			key := "max_tokens"
			if kind == "openai" {
				key = "max_output_tokens"
			}
			if received[0][key] != float64(512) {
				t.Fatalf("missing output cap: %+v", received[0])
			}
			_, err = provider.(model.TextGenerator).GenerateText(context.Background(), model.TextRequest{Input: "Summarize this fact.", MaxOutputTokens: 128})
			if err != nil {
				t.Fatal(err)
			}
			if received[1][key] != float64(128) {
				t.Fatalf("missing summary output cap: %+v", received[1])
			}
			_, err = provider.Generate(context.Background(), model.Request{System: strings.Repeat("wide ", 5000)})
			if err == nil {
				t.Fatal("oversized decision submitted")
			}
			_, err = provider.(model.TextGenerator).GenerateText(context.Background(), model.TextRequest{Input: "hello", MaxOutputTokens: 513})
			if err == nil {
				t.Fatal("oversized summary output accepted")
			}
			if len(received) != 2 {
				t.Fatalf("rejected requests reached network: %d", len(received))
			}
		})
	}
}

func TestLoadProviderWindowConfig(t *testing.T) {
	path := writeConfig(t, `{"provider":"fake","context_window_tokens":65536,"max_output_tokens":8192}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ContextTokens != 65536 || config.OutputTokens != 8192 {
		t.Fatalf("window not loaded: %+v", config.WindowLimits)
	}
	_, err = NewProvider(Config{Provider: "fake", WindowLimits: model.WindowLimits{ContextTokens: 100, OutputTokens: 101}})
	if err == nil {
		t.Fatal("invalid model window accepted")
	}
}
