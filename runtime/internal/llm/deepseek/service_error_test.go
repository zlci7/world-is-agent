package deepseek

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gameagent/runtime/internal/model"
)

func TestHostedQuotaProducesTypedFailureWithoutUpstreamText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-WIA-Request-ID") == "" {
			t.Error("model request lacks stable call identity")
		}
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"code":"quota_exceeded","message":"private upstream text"}}`))
	}))
	defer server.Close()
	p := NewProvider("test", "model", WithBaseURL(server.URL))
	_, err := p.Generate(context.Background(), model.Request{System: "test", Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}})
	if !errors.Is(err, model.ErrQuotaExceeded) || err.Error() != "model quota exceeded" {
		t.Fatalf("quota classification: %v", err)
	}
}
