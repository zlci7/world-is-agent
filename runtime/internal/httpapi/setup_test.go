package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/llm"
)

// providerStub answers one model request. The first-run flow only needs to know
// whether the provider accepts the credential, so the response is the smallest
// one the provider parses.
func providerStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func acceptingProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return providerStub(t, http.StatusOK, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
}

func rejectingProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return providerStub(t, http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`)
}

func setupBody(fields map[string]any) string {
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestSetupModelRequiresASession(t *testing.T) {
	f := newModelFixture(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek", "api_key": "sk-must-not-be-written",
	}))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	assertNoCredentialWritten(t, f)
}

// The commit probes what it is about to write instead of trusting a test the
// client already ran, and a failed probe leaves nothing behind.
func TestSetupModelWritesNothingWhenTheProbeFails(t *testing.T) {
	f := newModelFixture(t)
	cookie := f.session(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek", "model": "test-model", "base_url": rejectingProvider(t).URL, "api_key": "sk-rejected",
	}), cookie)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeBody[errorResponse](t, recorder)
	if response.Error.Code != "authentication_failed" {
		t.Fatalf("code = %q, want the probe's own code", response.Error.Code)
	}
	if strings.Contains(recorder.Body.String(), "sk-rejected") {
		t.Fatal("the failure response echoed the credential")
	}
	assertNoCredentialWritten(t, f)
}

func TestSetupModelCommitsAndReportsTheNewState(t *testing.T) {
	f := newModelFixture(t)
	cookie := f.session(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek",
		"model":    "test-model",
		"base_url": acceptingProvider(t).URL,
		"api_key":  "sk-from-the-wizard",
	}), cookie)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	// The response is the same payload the client polls, so a successful save
	// needs no second request to learn what changed.
	status := decodeBody[statusResponse](t, recorder)
	if !status.Ready {
		t.Fatalf("state = %q (%s), want ready", status.State, status.Reason)
	}
	if status.Model == nil || !status.Model.APIKeyConfigured {
		t.Fatalf("model = %+v, want a configured credential", status.Model)
	}
	if status.Model.APIKeyEnvName != "" {
		t.Fatalf("env name = %q: the credential is a file, and a path is not reported", status.Model.APIKeyEnvName)
	}
	if strings.Contains(recorder.Body.String(), "sk-from-the-wizard") {
		t.Fatal("the response echoed the credential")
	}

	// The configuration on disk references the credential instead of holding it.
	written, err := os.ReadFile(f.runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("read the written configuration: %v", err)
	}
	if strings.Contains(string(written), "sk-from-the-wizard") {
		t.Fatalf("the written configuration holds the credential: %s", written)
	}
	cfg, err := llm.LoadConfig(f.runtime.ModelConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(filepath.Dir(f.runtime.ModelConfigPath()), strings.TrimPrefix(cfg.APIKey, "file:")))
	if err != nil {
		t.Fatalf("read the stored credential: %v", err)
	}
	if strings.TrimSpace(string(stored)) != "sk-from-the-wizard" {
		t.Fatalf("stored credential = %q", stored)
	}
}

func TestSetupModelValidatesTheBody(t *testing.T) {
	f := newModelFixture(t)
	cookie := f.session(t)

	for name, body := range map[string]string{
		"not json":     "{",
		"no provider":  setupBody(map[string]any{"api_key": "sk-value"}),
		"no key":       setupBody(map[string]any{"provider": "deepseek"}),
		"unknown wire": setupBody(map[string]any{"provider": "nowhere", "api_key": "sk-value"}),
	} {
		recorder := f.do(t, http.MethodPost, "/api/setup/model", body, cookie)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "sk-value") {
			t.Errorf("%s: the response echoed the credential", name)
		}
		assertNoCredentialWritten(t, f)
	}
}

// A root whose shipped configuration could not be prepared can never become ready
// in this process, so a submission must be answered from the reason rather than
// from a model call. The stub provider is deliberately reachable: a probe that
// runs here would succeed, install a credential, and still be refused.
func TestSetupModelRefusesABlockedDataRootBeforeProbing(t *testing.T) {
	f := newBlockedFixture(t)
	cookie := f.session(t)
	provider := acceptingProvider(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek", "model": "test-model", "base_url": provider.URL, "api_key": "sk-blocked-root",
	}), cookie)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeBody[errorResponse](t, recorder)
	if response.Error.Code != "setup_blocked" {
		t.Fatalf("code = %q, want setup_blocked", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "storage_unavailable") {
		t.Fatalf("message = %q, want it to name the initialization failure", response.Error.Message)
	}
	if strings.Contains(recorder.Body.String(), "sk-blocked-root") {
		t.Fatal("the failure response echoed the credential")
	}
	// Refused before the probe, so nothing was written for it either.
	assertNoCredentialWritten(t, f)
	if state := f.runtime.State(); state != bootstrap.StateBlocked {
		t.Fatalf("state = %q, want %q", state, bootstrap.StateBlocked)
	}
}

// newBlockedFixture opens a data root whose shipped configuration cannot be
// written. A plain file where the shipped tree expects a directory is a real
// initialization failure, and it behaves the same on every platform.
func newBlockedFixture(t *testing.T) *fixture {
	t.Helper()

	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "games"), nil, 0o644); err != nil {
		t.Fatalf("occupy the shipped tree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "active-game.json"), []byte(`{"game_id":"rimworld"}`), 0600); err != nil {
		t.Fatal(err)
	}
	env := dataroot.Env{
		GOOS:    "linux",
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return root, nil },
	}
	runtime, err := bootstrap.Open(root, env)
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if state := runtime.State(); state != bootstrap.StateBlocked {
		t.Fatalf("the fixture produced state %q, want %q", state, bootstrap.StateBlocked)
	}
	server, err := New(Options{
		Addr:     "127.0.0.1:0",
		Runtime:  runtime,
		GRPCAddr: "127.0.0.1:50051",
		Version:  "test",
		Logger:   log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown() })

	_, port, err := net.SplitHostPort(server.listener.Addr().String())
	if err != nil {
		t.Fatalf("listener address: %v", err)
	}
	host := "127.0.0.1:" + port
	return &fixture{
		server:  server,
		runtime: runtime,
		root:    root,
		host:    host,
		origin:  "http://" + host,
		token:   server.sessions.bootstrapToken(),
	}
}

func TestSetupModelRejectsAForeignOrigin(t *testing.T) {
	f := newModelFixture(t)

	request := httptest.NewRequest(http.MethodPost, f.origin+"/api/setup/model", strings.NewReader(setupBody(map[string]any{
		"provider": "deepseek", "api_key": "sk-foreign",
	})))
	request.AddCookie(f.session(t))
	request.Header.Set("Origin", "http://evil.example.com")
	recorder := httptest.NewRecorder()
	f.server.server.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	assertNoCredentialWritten(t, f)
}

func TestSetupModelIsNotReachableWithAGet(t *testing.T) {
	f := newModelFixture(t)

	recorder := f.do(t, http.MethodGet, "/api/setup/model", "", f.session(t))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

// The form offers what this reports, so an empty or unreachable answer is a form
// with nothing to submit.
func TestSetupOptionsRequiresASession(t *testing.T) {
	f := newModelFixture(t)

	recorder := f.do(t, http.MethodGet, "/api/setup/options", "")

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestSetupOptionsReportsProvidersWithTheirDefaultModels(t *testing.T) {
	f := newModelFixture(t)

	recorder := f.do(t, http.MethodGet, "/api/setup/options", "", f.session(t))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	// Asserted against the wire text rather than a decoded struct: decoding into
	// the same type the handler encodes accepts any field name it happens to use,
	// which is exactly how a capitalised name reaches a client that reads the
	// documented lowercase one.
	body := recorder.Body.String()
	if !strings.Contains(body, `"provider"`) || !strings.Contains(body, `"model"`) {
		t.Fatalf("options are not named as the client reads them: %s", body)
	}
	if strings.Contains(body, `"Provider"`) || strings.Contains(body, `"Model"`) {
		t.Fatalf("options use Go field names: %s", body)
	}

	options := decodeBody[setupOptionsResponse](t, recorder)
	if len(options.Providers) == 0 {
		t.Fatal("no provider offered, so the first-run form cannot be submitted")
	}
	for _, choice := range options.Providers {
		if strings.TrimSpace(choice.Provider) == "" || strings.TrimSpace(choice.Model) == "" {
			t.Fatalf("offered choice is incomplete: %+v", choice)
		}
	}
}

func TestSetupOptionsIsNotReachableWithAPost(t *testing.T) {
	f := newModelFixture(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/options", "{}", f.session(t))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestSetupModelUpdatesAReadyRuntimeAndRejectsInvalidBodies(t *testing.T) {
	f := newModelFixture(t)
	cookie := f.session(t)
	for _, name := range []string{"first-model", "second-model"} {
		response := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{"provider": "deepseek", "model": name, "base_url": acceptingProvider(t).URL, "api_key": "test-key"}), cookie)
		if response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
		if got := decodeBody[statusResponse](t, response); !got.Ready || got.Model.Model != name {
			t.Fatal("model not applied")
		}
	}
	before, _ := os.ReadFile(f.runtime.ModelConfigPath())
	response := f.do(t, http.MethodPost, "/api/setup/model", "{", cookie)
	if response.Code != http.StatusBadRequest || decodeBody[errorResponse](t, response).Error.Code != "invalid_body" {
		t.Fatal(response.Body.String())
	}
	after, _ := os.ReadFile(f.runtime.ModelConfigPath())
	if string(before) != string(after) || !f.runtime.Ready() {
		t.Fatal("invalid body changed model")
	}
}

func assertNoCredentialWritten(t *testing.T, f *fixture) {
	t.Helper()

	entries, err := os.ReadDir(f.runtime.Layout().SecretsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("a credential was written by a request that must not write one")
	}
	if _, err := os.Stat(f.runtime.ModelConfigPath()); err == nil {
		t.Fatal("a model configuration was written by a request that must not write one")
	}
}

func newModelFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, nil)
	if err := f.runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReadyModelProbeKeepsCurrentInstanceUntilCandidateAccepted(t *testing.T) {
	f := newModelFixture(t)
	cookie := f.session(t)
	first := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{"provider": "deepseek", "model": "first", "base_url": acceptingProvider(t).URL, "api_key": "test-first-key"}), cookie)
	if first.Code != http.StatusOK {
		t.Fatal(first.Body.String())
	}
	history := f.runtime.HistoryStore()
	before, _ := os.ReadFile(f.runtime.ModelConfigPath())
	entered, release := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "Bearer test-second-key" || body.Model != "second" {
			t.Error("probe did not receive submitted candidate")
		}
		close(entered)
		<-release
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer provider.Close()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{"provider": "deepseek", "model": "second", "base_url": provider.URL, "api_key": "test-second-key"}), cookie)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("probe did not begin")
	}
	if !f.runtime.Ready() || f.runtime.HistoryStore() != history || f.runtime.Snapshot().Model.Model != "first" {
		t.Error("probe disrupted current instance")
	}
	during, _ := os.ReadFile(f.runtime.ModelConfigPath())
	if string(before) != string(during) {
		t.Error("probe changed disk configuration")
	}
	close(release)
	response := <-done
	if response.Code != http.StatusBadRequest {
		t.Fatal(response.Body.String())
	}
	after, _ := os.ReadFile(f.runtime.ModelConfigPath())
	if string(before) != string(after) || f.runtime.HistoryStore() != history || !f.runtime.Ready() {
		t.Fatal("failed probe changed current instance")
	}
}
