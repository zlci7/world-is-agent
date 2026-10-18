package workerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
)

func TestWorkerAPIRequiresServiceCredentialAndRedactsPaths(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "config"), 0755)
	os.WriteFile(filepath.Join(root, "config", "model.json"), []byte(`{"provider":"fake"}`), 0644)
	runtime, err := bootstrap.Open(root, dataroot.Env{GOOS: "windows", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err = runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s", 32)
	h := Handler(runtime, token, "test")
	for _, path := range []string{"/status", "/task-worlds", "/turns"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unprotected private path %s", path)
		}
	}
	r := httptest.NewRequest("GET", "/status", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), root) || strings.Contains(w.Body.String(), "model.json") || strings.Contains(w.Body.String(), token) {
		t.Fatalf("private status exposed data: %s", w.Body.String())
	}
	r = httptest.NewRequest("GET", "/turns", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var records struct {
		InstanceID string `json:"instance_id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &records) != nil || records.InstanceID != runtime.Snapshot().InstanceID {
		t.Fatalf("turn response has no authoritative instance scope: %s", w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/setup/model", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatal("private API permits configuration mutations")
	}
}

func TestPreferencesRefreshKeepsLastConfirmedValuesOnFailure(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer service-token" {
			t.Error("missing private auth")
		}
		json.NewEncoder(w).Encode(map[string]any{"name": "小雨", "language": "zh-CN", "style": "brief", "version": 2})
	}))
	defer server.Close()
	p := NewPreferences()
	p.Refresh(context.Background(), server.URL, "service-token")
	if p.Read().Name != "小雨" || p.Read().Style != "brief" {
		t.Fatal("preferences not projected")
	}
	fail.Store(true)
	p.Refresh(context.Background(), server.URL, "service-token")
	if p.Read().Name != "小雨" {
		t.Fatal("outage erased confirmed preferences")
	}
}
