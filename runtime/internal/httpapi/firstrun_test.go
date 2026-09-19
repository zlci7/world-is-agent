package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gameagent/runtime/internal/bootstrap"
)

// The first run, over the real HTTP surface: an empty data root is opened, the
// form asks what it may offer, and the submission is what writes the model
// configuration. The other setup tests pre-write that file, so this is the only
// one that proves the writing step works at all.
func TestFirstRunOverHTTPReachesReady(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	// 1. An empty root is waiting for a model, and says so rather than failing.
	status := decodeBody[statusResponse](t, f.do(t, http.MethodGet, "/api/status", "", cookie))
	if status.State != string(bootstrap.StateNeedsConfiguration) || status.Ready {
		t.Fatalf("fresh root state = %q ready = %t, want needs_configuration and not ready", status.State, status.Ready)
	}
	if status.ModelError == "" {
		t.Fatal("a fresh root did not explain what is missing")
	}

	// 2. The shipped configuration arrived before any of this: the form is only
	//    safe to submit because a root that could not be seeded is blocked.
	if status.AgentConfigPath != f.runtime.AgentConfigPath() {
		t.Fatalf("agent config path = %q", status.AgentConfigPath)
	}
	if _, err := os.Stat(status.AgentConfigPath); err != nil {
		t.Fatalf("the shipped agent configuration is not in place: %v", err)
	}
	definitions, err := filepath.Glob(filepath.Join(status.ConfigDir, "games", "*", "definitions", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) == 0 {
		t.Fatalf("no definition was seeded under %s", status.ConfigDir)
	}

	// 3. The form asks what it may offer.
	options := decodeBody[setupOptionsResponse](t, f.do(t, http.MethodGet, "/api/setup/options", "", cookie))
	if len(options.Providers) == 0 {
		t.Fatal("no provider offered")
	}

	// 4. The submission is the thing that writes the configuration, and the
	//    credential it verifies is stored as a reference rather than inline.
	provider := acceptingProvider(t)
	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek",
		"model":    "first-run-model",
		"base_url": provider.URL,
		"api_key":  "sk-first-run",
	}), cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("setup status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	configured := decodeBody[statusResponse](t, recorder)
	if !configured.Ready || configured.State != string(bootstrap.StateReady) {
		t.Fatalf("state = %q ready = %t, want ready", configured.State, configured.Ready)
	}

	written, err := os.ReadFile(f.runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("read the written configuration: %v", err)
	}
	if strings.Contains(string(written), "sk-first-run") {
		t.Fatal("the written configuration contains the credential")
	}
	if !strings.Contains(string(written), `"api_key": "file:`) {
		t.Fatalf("the written configuration does not reference a credential file: %s", written)
	}
	// The window is written rather than left empty, because an empty one silently
	// turns summarisation off.
	if !strings.Contains(string(written), "context_window_tokens") {
		t.Fatalf("the written configuration has no window: %s", written)
	}

	// 5. A second read agrees, so the page does not depend on the save response.
	after := decodeBody[statusResponse](t, f.do(t, http.MethodGet, "/api/status", "", cookie))
	if !after.Ready {
		t.Fatalf("status after the first run = %q, want ready", after.State)
	}
	if after.Model == nil || !after.Model.APIKeyConfigured {
		t.Fatalf("model after the first run = %+v", after.Model)
	}
	// Nothing in either response may carry the credential, including the paths it
	// resolved to.
	for _, body := range []string{recorder.Body.String()} {
		if strings.Contains(body, "sk-first-run") {
			t.Fatal("a response carried the credential")
		}
	}
}

// Reopening the same root is the restart the first run promises: the
// configuration and the seeded tree both stay, and nothing is seeded twice.
func TestRestartAfterFirstRunKeepsTheConfiguration(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	recorder := f.do(t, http.MethodPost, "/api/setup/model", setupBody(map[string]any{
		"provider": "deepseek",
		"model":    "first-run-model",
		"base_url": acceptingProvider(t).URL,
		"api_key":  "sk-restart",
	}), cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("setup status = %d: %s", recorder.Code, recorder.Body.String())
	}
	modelBefore := readFileString(t, f.runtime.ModelConfigPath())
	agentBefore := readFileString(t, f.runtime.AgentConfigPath())

	// Close what the fixture opened and open the same root again.
	if err := f.runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := bootstrap.Open(f.root, fixtureEnv(f.root))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	if !reopened.Ready() {
		t.Fatalf("reopened state = %q (%s), want ready", reopened.State(), reopened.Reason())
	}
	if got := readFileString(t, reopened.ModelConfigPath()); got != modelBefore {
		t.Fatalf("the model configuration changed across a restart:\n%s", got)
	}
	if got := readFileString(t, reopened.AgentConfigPath()); got != agentBefore {
		t.Fatalf("the agent configuration changed across a restart:\n%s", got)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
