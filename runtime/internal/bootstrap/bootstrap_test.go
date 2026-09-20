package bootstrap_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gameagent/runtime/config"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/secret"
)

const validModelConfig = `{"provider":"fake","model":"fake","context_window_tokens":32768,"max_output_tokens":2048}`

// stubEnv keeps the tests independent from whatever the developer has exported.
func stubEnv(overrides map[string]string) dataroot.Env {
	return dataroot.Env{
		GOOS: "linux",
		Getenv: func(name string) string {
			return overrides[name]
		},
		HomeDir: func() (string, error) { return "", os.ErrNotExist },
	}
}

func writeConfig(t *testing.T, root, name, content string) {
	t.Helper()
	layout := dataroot.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.ConfigPath(name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCreatesTheDataRootWithoutConfiguration(t *testing.T) {
	root := t.TempDir()
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, dir := range []string{r.Layout().ConfigDir(), r.Layout().DataDir(), r.Layout().SecretsDir()} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("layout %s: %v", dir, err)
		}
	}
	if r.Ready() || r.HistoryStore() != nil || r.Snapshot().ReasonCode != "game_not_selected" {
		t.Fatalf("fresh: %+v", r.Snapshot())
	}
}
func TestMissingModelConfigurationIsAStateNotAnExit(t *testing.T) {
	r, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	if r.Ready() || r.Snapshot().ReasonCode != "model_configuration_required" || !strings.Contains(r.Reason(), r.ModelConfigPath()) {
		t.Fatalf("%+v", r.Snapshot())
	}
	if err := r.Configure(); err == nil {
		t.Fatal("missing model accepted")
	}
}
func TestConfigureInstallsTheAgentCoreOnce(t *testing.T) {
	root := t.TempDir()
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "model.json", validModelConfig)
	if err := r.Configure(); err != nil {
		t.Fatal(err)
	}
	first := r.HistoryStore()
	writeConfig(t, root, "model.json", "{")
	if err := r.Configure(); err != nil {
		t.Fatal(err)
	}
	if !r.Ready() || first != r.HistoryStore() {
		t.Fatal("Configure replaced live core")
	}
}
func TestCloseReleasesTheTraceRecorderOnce(t *testing.T) {
	r, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
func prepareSelected(t *testing.T, root, id string) {
	t.Helper()
	writeConfig(t, root, "active-game.json", `{"game_id":"`+id+`"}`)
	p, err := config.PrepareGame(filepath.Join(root, "config"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.CommitAssets(); err != nil {
		t.Fatal(err)
	}
}
func TestSelectedInvalidProfileCanBeRecoveredByChoosingAnotherGame(t *testing.T) {
	root := t.TempDir()
	prepareSelected(t, root, "rimworld")
	writeConfig(t, root, "games/rimworld/agent.json", `{"task":`)
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Snapshot(); got.State != bootstrap.StateNeedsConfiguration || got.ReasonCode != "profile_invalid" {
		t.Fatalf("%+v", got)
	}
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().ConfiguredGame.ID != "stardew-valley" {
		t.Fatal("cannot recover")
	}
}
func TestConfiguredPathsResolveAgainstTheDataRoot(t *testing.T) {
	root := t.TempDir()
	prepareSelected(t, root, "rimworld")
	writeConfig(t, root, "model.json", validModelConfig)
	// External profile content does not determine the selected game.
	writeConfig(t, root, "custom.json", `{"task":{"enabled":true,"db_path":"rel/tasks.sqlite"},"memory_store":{"root":"rel/memory"},"definition_catalog_root":"config/games"}`)
	r, err := bootstrap.Open(root, stubEnv(map[string]string{agent.ConfigEnvName: "config/custom.json"}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Ready() {
		t.Fatal(r.Reason())
	}
	cfg := r.AgentConfig()
	if cfg.Task.DBPath != filepath.Join(root, "rel", "tasks.sqlite") || cfg.MemoryStore.Root != filepath.Join(root, "rel", "memory") || cfg.DefinitionCatalogRoot != filepath.Join(root, "config", "games") {
		t.Fatalf("paths: %+v", cfg)
	}
	if r.Snapshot().LoadedGame.ID != "rimworld" {
		t.Fatal("override changed identity")
	}
}
func TestInvalidExplicitOverrideBlocksWithoutFallingBack(t *testing.T) {
	for _, content := range []string{"missing", "{"} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			prepareSelected(t, root, "rimworld")
			if content != "missing" {
				writeConfig(t, root, "custom.json", content)
			}
			r, err := bootstrap.Open(root, stubEnv(map[string]string{agent.ConfigEnvName: "config/custom.json"}))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if got := r.Snapshot(); got.State != bootstrap.StateBlocked || got.ReasonCode != "override_configuration_invalid" {
				t.Fatalf("%+v", got)
			}
			if err := r.SelectGame("stardew-valley"); err == nil {
				t.Fatal("blocked override allowed save")
			}
		})
	}
}

func TestOverrideWithUnusableCatalogRootBlocksAllGameChoices(t *testing.T) {
	root := t.TempDir()
	prepareSelected(t, root, "rimworld")
	writeConfig(t, root, "custom.json", `{"definition_catalog_root":"missing-catalog"}`)
	r, err := bootstrap.Open(root, stubEnv(map[string]string{agent.ConfigEnvName: "config/custom.json"}))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Snapshot(); got.State != bootstrap.StateBlocked || got.ReasonCode != "override_configuration_invalid" {
		t.Fatalf("invalid override root: %+v", got)
	}
}
func TestFreshRootBecomesReadyWithAFileCredential(t *testing.T) {
	root := t.TempDir()
	layout := dataroot.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}

	// The layout the shipped profile expects: <root>/config/model.json naming
	// <root>/secrets/model.key.
	if err := secret.Write(filepath.Join(layout.SecretsDir(), "model.key"), "sk-file-credential"); err != nil {
		t.Fatalf("secret.Write: %v", err)
	}
	writeConfig(t, root, "model.json", `{"provider":"deepseek","model":"test-model","api_key":"file:../secrets/model.key"}`)

	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()
	if err := runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}

	if !runtime.Ready() {
		t.Fatalf("state = %s (%s), want ready", runtime.State(), runtime.Reason())
	}

	// Ready only means the configuration is usable. It must be the seeded profile
	// that is in effect, not the generic baseline that loads no definitions.
	catalogRoot := runtime.AgentConfig().DefinitionCatalogRoot
	if catalogRoot == "" {
		t.Fatal("the runtime is running on a profile with no definition catalog")
	}
	if _, err := os.Stat(catalogRoot); err != nil {
		t.Fatalf("the seeded definition catalog is not there: %v", err)
	}

	// And what a client can read carries no credential and no path to it.
	summary, err := llm.DescribeConfig(runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	if !summary.APIKeyConfigured {
		t.Fatalf("api_key_configured = false with a readable credential: %+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	for _, leak := range []string{"sk-file-credential", layout.SecretsDir(), "model.key"} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("the client-visible summary carries %q: %s", leak, encoded)
		}
	}
}

// The first-run commit, end to end on a root that nothing has configured.
func TestApplyModelConfigurationMakesAFreshRootReady(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()
	if err := runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}

	if runtime.Ready() {
		t.Fatal("a fresh root reported ready before anything was configured")
	}

	err = runtime.ApplyModelConfiguration(bootstrap.ModelSetup{
		Provider: "deepseek",
		Model:    "test-model",
		APIKey:   "sk-entered-by-the-user",
	})
	if err != nil {
		t.Fatalf("ApplyModelConfiguration: %v", err)
	}

	if !runtime.Ready() {
		t.Fatalf("state = %s (%s), want ready", runtime.State(), runtime.Reason())
	}

	// The written configuration references the credential rather than containing
	// it, and the reference is relative so the data root stays movable.
	written, err := os.ReadFile(runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("read the written configuration: %v", err)
	}
	if strings.Contains(string(written), "sk-entered-by-the-user") {
		t.Fatalf("the written configuration contains the credential: %s", written)
	}
	var document map[string]any
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatalf("parse the written configuration: %v", err)
	}
	if reference, _ := document["api_key"].(string); !strings.HasPrefix(reference, "file:../secrets/model_") {
		t.Fatalf("api_key = %q, want a relative reference to the secret", reference)
	}

	// And it resolves: the credential is readable where the configuration says.
	reference := strings.TrimPrefix(document["api_key"].(string), "file:")
	value, err := secret.Read(filepath.Join(filepath.Dir(runtime.ModelConfigPath()), reference))
	if err != nil {
		t.Fatalf("read the stored credential: %v", err)
	}
	if value != "sk-entered-by-the-user" {
		t.Fatalf("stored credential = %q", value)
	}

	// A written configuration has to keep the window: without it the provider
	// skips its own check and summarisation is switched off.
	if document["context_window_tokens"] == nil || document["max_output_tokens"] == nil {
		t.Fatalf("the written configuration has no window: %s", written)
	}
}

func TestApplyModelConfigurationReplacesARunningCore(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "deepseek", Model: "first", APIKey: "test-first"}); err != nil {
		t.Fatal(err)
	}
	first, err := llm.LoadConfig(runtime.ModelConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "deepseek", Model: "second", APIKey: "test-second"}); err != nil {
		t.Fatal(err)
	}
	second, err := llm.LoadConfig(runtime.ModelConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if first.APIKey == second.APIKey || second.Model != "second" || runtime.Snapshot().Model.Model != "second" {
		t.Fatal("replacement credential or model not committed")
	}
	oldPath := filepath.Join(filepath.Dir(runtime.ModelConfigPath()), strings.TrimPrefix(first.APIKey, "file:"))
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("previous Runtime-owned credential still exists: %v", err)
	}
	newPath := filepath.Join(filepath.Dir(runtime.ModelConfigPath()), strings.TrimPrefix(second.APIKey, "file:"))
	if key, err := secret.Read(newPath); err != nil || key != "test-second" {
		t.Fatalf("replacement credential = %q, %v", key, err)
	}
}

func TestApplyModelConfigurationPreservesExternalCredential(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runtime")
	externalPath := filepath.Join(base, "user-model.key")
	if err := os.WriteFile(externalPath, []byte("user-managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepareSelected(t, root, "rimworld")
	document, err := json.Marshal(map[string]any{
		"provider": "fake",
		"model":    "first",
		"api_key":  "file:" + filepath.ToSlash(externalPath),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "model.json", string(document))
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "fake", Model: "second", APIKey: "test-second"}); err != nil {
		t.Fatal(err)
	}
	if key, err := secret.Read(externalPath); err != nil || key != "user-managed" {
		t.Fatalf("external credential = %q, %v", key, err)
	}
}

func TestApplyModelConfigurationValidatesItsInput(t *testing.T) {
	runtime, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()
	if err := runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}

	for name, setup := range map[string]bootstrap.ModelSetup{
		"no provider": {APIKey: "sk-value"},
		"no key":      {Provider: "deepseek"},
		"blank key":   {Provider: "deepseek", APIKey: "   "},
	} {
		if err := runtime.ApplyModelConfiguration(setup); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestApplyModelConfigurationKeepsAChosenWindow(t *testing.T) {
	runtime, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()
	if err := runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}

	err = runtime.ApplyModelConfiguration(bootstrap.ModelSetup{
		Provider:            "deepseek",
		APIKey:              "sk-value",
		ContextWindowTokens: 4096,
		MaxOutputTokens:     512,
	})
	if err != nil {
		t.Fatalf("ApplyModelConfiguration: %v", err)
	}

	written, err := os.ReadFile(runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("read the configuration: %v", err)
	}
	var document struct {
		ContextWindowTokens int `json:"context_window_tokens"`
		MaxOutputTokens     int `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatalf("parse the configuration: %v", err)
	}
	if document.ContextWindowTokens != 4096 || document.MaxOutputTokens != 512 {
		t.Fatalf("window = %d/%d, want the chosen 4096/512", document.ContextWindowTokens, document.MaxOutputTokens)
	}
}
