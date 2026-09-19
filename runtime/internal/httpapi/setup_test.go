package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupBody(fields map[string]any) string {
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(encoded)
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

// Until the connection probe exists, the only accepted commit is one the caller
// explicitly asked not to verify. Committing silently would produce exactly the
// configuration the first-run flow exists to prevent: one that looks verified.
func TestSetupModelRefusesToCommitWithoutAnExplicitSkip(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek", "api_key": "sk-unverified",
	}), cookie)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	response := decodeBody[errorResponse](t, recorder)
	if response.Error.Code != "connection_test_unavailable" {
		t.Fatalf("code = %q", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "skip_connection_test") {
		t.Fatalf("message %q does not name the field that would allow it", response.Error.Message)
	}
	assertNoCredentialWritten(t, f)
}

func TestSetupModelCommitsAndReportsTheNewState(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider":             "deepseek",
		"model":                "test-model",
		"api_key":              "sk-from-the-wizard",
		"skip_connection_test": true,
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
	secretPath := filepath.Join(f.runtime.Layout().SecretsDir(), "model.key")
	stored, err := os.ReadFile(secretPath)
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
		"no provider":  setupBody(map[string]any{"api_key": "sk-value", "skip_connection_test": true}),
		"no key":       setupBody(map[string]any{"provider": "deepseek", "skip_connection_test": true}),
		"unknown wire": setupBody(map[string]any{"provider": "nowhere", "api_key": "sk-value", "skip_connection_test": true}),
	} {
		recorder := f.do(t, http.MethodPost, "/api/setup/model", body, cookie)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "sk-value") {
			t.Errorf("%s: the response echoed the credential", name)
		}
	}
}

func TestSetupModelRejectsAForeignOrigin(t *testing.T) {
	f := newFixture(t, nil)

	request := httptest.NewRequest(http.MethodPost, f.origin+"/api/setup/model", strings.NewReader(setupBody(map[string]any{
		"provider": "deepseek", "api_key": "sk-foreign", "skip_connection_test": true,
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
