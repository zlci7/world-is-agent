package agent_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
)

func TestHistoryRealModelLatencyDiagnostic(t *testing.T) {
	if os.Getenv("GAMEAGENT_HISTORY_REAL_DIAGNOSTIC") != "1" {
		t.Skip("real latency comparison requires explicit opt-in")
	}
	for _, profile := range []string{"default-thinking-10s", "disabled-thinking-10s"} {
		t.Run(profile, func(t *testing.T) {
			verifyRealHistoryProcessCycle(t, profile)
		})
	}
}

func configureHistoryProcessDiagnostic(t *testing.T, profile string) {
	t.Helper()
	switch profile {
	case "", "default-thinking-10s":
		return
	case "disabled-thinking-10s":
		client := *http.DefaultClient
		client.Transport = summaryThinkingDiagnosticTransport{next: http.DefaultTransport}
		http.DefaultClient = &client
	default:
		t.Fatalf("unknown diagnostic profile %q", profile)
	}
}

// The transport is used only in opt-in diagnostic child processes.
type summaryThinkingDiagnosticTransport struct{ next http.RoundTripper }

func (d summaryThinkingDiagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err := req.Body.Close(); err != nil {
		return nil, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if string(payload["max_tokens"]) == "2048" {
		payload["thinking"] = json.RawMessage(`{"type":"disabled"}`)
		data, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	cloned := req.Clone(req.Context())
	cloned.Body = io.NopCloser(bytes.NewReader(data))
	cloned.ContentLength = int64(len(data))
	cloned.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	return d.next.RoundTrip(cloned)
}
