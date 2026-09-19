package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestSetupTestRequiresASession(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodPost, "/api/setup/test", setupBody(map[string]any{
		"provider": "deepseek", "api_key": "sk-probe",
	}))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

// The test is a question, not a commit: it reports and writes nothing.
func TestSetupTestReportsBothOutcomes(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	accepted := f.do(t, http.MethodPost, "/api/setup/test", setupBody(map[string]any{
		"provider": "deepseek", "model": "test-model", "base_url": acceptingProvider(t).URL, "api_key": "sk-good",
	}), cookie)

	if accepted.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", accepted.Code, accepted.Body.String())
	}
	if outcome := decodeBody[llm.ProbeOutcome](t, accepted); !outcome.OK || outcome.Code != "ok" {
		t.Fatalf("outcome = %+v, want ok", outcome)
	}

	rejected := f.do(t, http.MethodPost, "/api/setup/test", setupBody(map[string]any{
		"provider": "deepseek", "model": "test-model", "base_url": rejectingProvider(t).URL, "api_key": "sk-bad",
	}), cookie)

	if rejected.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even for a rejected credential", rejected.Code)
	}
	outcome := decodeBody[llm.ProbeOutcome](t, rejected)
	if outcome.OK {
		t.Fatal("a rejected credential was reported as working")
	}
	if outcome.Code != "authentication_failed" {
		t.Fatalf("code = %q, want authentication_failed", outcome.Code)
	}
	if strings.Contains(rejected.Body.String(), "sk-bad") {
		t.Fatal("the probe response echoed the credential")
	}
	assertNoCredentialWritten(t, f)
}

func TestSetupModelRequiresASession(t *testing.T) {
	f := newFixture(t, nil)

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
	f := newFixture(t, nil)
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
	f := newFixture(t, nil)
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
	stored, err := os.ReadFile(filepath.Join(f.runtime.Layout().SecretsDir(), "model.key"))
	if err != nil {
		t.Fatalf("read the stored credential: %v", err)
	}
	if strings.TrimSpace(string(stored)) != "sk-from-the-wizard" {
		t.Fatalf("stored credential = %q", stored)
	}
}

func TestSetupModelValidatesTheBody(t *testing.T) {
	f := newFixture(t, nil)
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

func TestSetupModelRejectsAForeignOrigin(t *testing.T) {
	f := newFixture(t, nil)

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
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodGet, "/api/setup/model", "", f.session(t))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

// The client is told whether the root was seeded, because that is the difference
// between "the shipped defaults are in place" and "you are on the fallback".
func TestStatusReportsWhetherTheRootWasSeeded(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodGet, "/api/status", "", f.session(t))

	status := decodeBody[statusResponse](t, recorder)
	if !status.Seeded {
		t.Fatal("seeded = false on a root that had no configuration")
	}
}

func assertNoCredentialWritten(t *testing.T, f *fixture) {
	t.Helper()

	secretPath := filepath.Join(f.runtime.Layout().SecretsDir(), "model.key")
	if _, err := os.Stat(secretPath); err == nil {
		t.Fatal("a credential was written by a request that must not write one")
	}
	if _, err := os.Stat(f.runtime.ModelConfigPath()); err == nil {
		t.Fatal("a model configuration was written by a request that must not write one")
	}
}
