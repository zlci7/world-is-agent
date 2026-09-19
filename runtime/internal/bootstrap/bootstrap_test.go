package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/secret"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tool"
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
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	layout := runtime.Layout()
	if layout.Root() != filepath.Clean(root) {
		t.Fatalf("data root = %q, want %q", layout.Root(), filepath.Clean(root))
	}
	for _, dir := range []string{layout.ConfigDir(), layout.DataDir(), layout.SecretsDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}

func TestMissingModelConfigurationIsAStateNotAnExit(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open must succeed without any configuration: %v", err)
	}
	defer runtime.Close()

	if runtime.HistoryStore() == nil {
		t.Fatal("the memory backend must exist before the agent core does")
	}

	if state := runtime.State(); state != bootstrap.StateNeedsConfiguration {
		t.Fatalf("state = %q, want %q", state, bootstrap.StateNeedsConfiguration)
	}
	if runtime.Ready() {
		t.Fatal("an unconfigured Runtime must not report itself ready; durable dispatch depends on this")
	}
	if reason := runtime.Reason(); !strings.Contains(reason, runtime.ModelConfigPath()) {
		t.Fatalf("reason %q does not name the expected model path %q", reason, runtime.ModelConfigPath())
	}

	if err := runtime.Configure(); err == nil {
		t.Fatal("configure must report the missing model configuration")
	}
	if state := runtime.State(); state != bootstrap.StateNeedsConfiguration {
		t.Fatalf("state after a failed configure = %q", state)
	}

	err = runtime.HandleEvent(context.Background(), nil, agent.ConnectionContext{}, session.AgentSessionKey{}, nil, nil, nil)
	if err == nil {
		t.Fatal("a turn with no session identity must be rejected")
	}

	// History maintenance has nothing to do without a core, but it must not panic.
	runtime.MaintainHistory(context.Background(), session.AgentSessionKey{}, nil)
}

// recordingEnvironment captures what the Runtime tells the adapter.
type recordingEnvironment struct {
	completions []*protocolv1alpha2.TurnCompletion
}

func (e *recordingEnvironment) Observe(_ context.Context, worldID string, entityID string) (*protocolv1alpha2.Observation, error) {
	return &protocolv1alpha2.Observation{WorldId: worldID, EntityId: entityID}, nil
}

func (e *recordingEnvironment) SubmitAction(context.Context, *protocolv1alpha2.ActionRequest) (*protocolv1alpha2.ActionResult, error) {
	return nil, errors.New("unexpected SubmitAction")
}

func (e *recordingEnvironment) StartAction(context.Context, *protocolv1alpha2.ActionRequest) (agent.ActionStart, error) {
	return agent.ActionStart{}, errors.New("unexpected StartAction")
}

func (e *recordingEnvironment) WaitActionResult(context.Context, string) (*protocolv1alpha2.ActionResult, error) {
	return nil, errors.New("unexpected WaitActionResult")
}

func (e *recordingEnvironment) CancelAction(string, string) {}

func (e *recordingEnvironment) SendTurnCompletion(_ context.Context, completion *protocolv1alpha2.TurnCompletion) error {
	e.completions = append(e.completions, completion)
	return nil
}

// A turn that never reaches its terminal state would block the adapter's next
// interaction with that NPC, so an unconfigured Runtime must still complete it.
func TestUnreadyCoreStillEndsTheTurn(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{})
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	env := &recordingEnvironment{}

	err = runtime.HandleEvent(context.Background(), env,
		agent.ConnectionContext{GameID: "stardew", SessionID: "session"},
		session.AgentSessionKey{GameID: "stardew", WorldID: "world", EntityID: "npc:Abigail"},
		&protocolv1alpha2.EntityRef{EntityId: "npc:Abigail"},
		catalog,
		&protocolv1alpha2.GameEvent{EventId: "event_1", WorldId: "world", TargetEntityId: "npc:Abigail"},
	)
	if err == nil {
		t.Fatal("the turn must fail while the model is not configured")
	}
	if len(env.completions) != 1 {
		t.Fatalf("turn completions = %d, want exactly one so the adapter releases the interaction", len(env.completions))
	}
	completion := env.completions[0]
	if completion.GetStatus() != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED {
		t.Fatalf("completion status = %v, want failed", completion.GetStatus())
	}
	if message := completion.GetError().GetMessage(); !strings.Contains(message, runtime.Reason()) {
		t.Fatalf("completion message %q must carry the configuration reason %q", message, runtime.Reason())
	}
}

// The first-run flow writes the model configuration while the Runtime is already
// running, so the placeholder core must be replaceable.
func TestConfigureReplacesTheUnavailableCore(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if state := runtime.State(); state != bootstrap.StateNeedsConfiguration {
		t.Fatalf("state = %q, want %q", state, bootstrap.StateNeedsConfiguration)
	}
	writeConfig(t, root, "model.json", validModelConfig)

	if err := runtime.Configure(); err != nil {
		t.Fatalf("configure after writing the model: %v", err)
	}
	if state := runtime.State(); state != bootstrap.StateReady {
		t.Fatalf("state = %q, want %q", state, bootstrap.StateReady)
	}
}

func TestConfigureInstallsTheAgentCoreOnce(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "model.json", validModelConfig)

	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if err := runtime.Configure(); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if state := runtime.State(); state != bootstrap.StateReady {
		t.Fatalf("state = %q, want %q", state, bootstrap.StateReady)
	}
	if !runtime.Ready() {
		t.Fatal("a configured Runtime must report itself ready so durable dispatch can resume")
	}
	if reason := runtime.Reason(); reason != "" {
		t.Fatalf("ready runtime kept a reason: %q", reason)
	}
	if err := runtime.Configure(); err != nil {
		t.Fatalf("configure must be idempotent while ready: %v", err)
	}
}

func TestCloseReleasesTheTraceRecorderOnce(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close must be idempotent: %v", err)
	}
}

func TestUnusableModelConfigurationIsReported(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "model.json", `{"provider":"not-a-provider"}`)

	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	err = runtime.Configure()
	if err == nil {
		t.Fatal("configure must reject an unsupported provider")
	}
	if reason := runtime.Reason(); !strings.Contains(reason, "unusable") {
		t.Fatalf("reason %q must distinguish an unusable file from a missing one", reason)
	}
	if state := runtime.State(); state != bootstrap.StateNeedsConfiguration {
		t.Fatalf("state = %q", state)
	}
}

func TestUnusableAgentConfigurationDoesNotStopTheProcess(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "agent.json", `{"task":`)

	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open must survive a broken agent configuration: %v", err)
	}
	defer runtime.Close()

	if reason := runtime.Reason(); !strings.Contains(reason, "agent configuration") {
		t.Fatalf("reason %q must name the agent configuration", reason)
	}
	if err := runtime.Configure(); err == nil {
		t.Fatal("a broken agent configuration must keep the core unready")
	}
}

func TestConfiguredPathsResolveAgainstTheDataRoot(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "agent.json", `{
		"task": {"enabled": true, "db_path": "rel/tasks.sqlite"},
		"memory_store": {"kind": "sqlite", "root": "rel/memory"},
		"definition_catalog_root": "rel/games"
	}`)

	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	config := runtime.AgentConfig()
	if got, want := config.Task.DBPath, filepath.Join(root, "rel", "tasks.sqlite"); got != want {
		t.Fatalf("task db path = %q, want %q", got, want)
	}
	if got, want := config.MemoryStore.Root, filepath.Join(root, "rel", "memory"); got != want {
		t.Fatalf("memory root = %q, want %q", got, want)
	}
	if got, want := config.DefinitionCatalogRoot, filepath.Join(root, "rel", "games"); got != want {
		t.Fatalf("definition catalog root = %q, want %q", got, want)
	}
}

func TestDefaultPathsLiveUnderTheDataRoot(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	config := runtime.AgentConfig()
	if got, want := config.MemoryStore.Root, filepath.Join(root, "data", "memory"); got != want {
		t.Fatalf("memory root = %q, want %q", got, want)
	}
	if got, want := config.Task.DBPath, filepath.Join(root, "data", "tasks", "tasks.sqlite"); got != want {
		t.Fatalf("task db path = %q, want %q", got, want)
	}
	if got, want := runtime.Layout().TracePath(), filepath.Join(root, "data", "traces.jsonl"); got != want {
		t.Fatalf("trace path = %q, want %q", got, want)
	}
}

func TestConfigurationEnvironmentOverridesResolveAgainstTheDataRoot(t *testing.T) {
	root := t.TempDir()
	absoluteModel := filepath.Join(t.TempDir(), "model.json")

	runtime, err := bootstrap.Open(root, stubEnv(map[string]string{
		agent.ConfigEnvName:      "custom/agent.json",
		"GAMEAGENT_MODEL_CONFIG": absoluteModel,
	}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if got, want := runtime.AgentConfigPath(), filepath.Join(root, "custom", "agent.json"); got != want {
		t.Fatalf("agent config path = %q, want the relative override resolved against the data root %q", got, want)
	}
	if got, want := runtime.ModelConfigPath(), filepath.Clean(absoluteModel); got != want {
		t.Fatalf("model config path = %q, want the absolute override %q", got, want)
	}
}

// The seed and a stored credential together: a data root nobody has configured
// becomes ready, on the shipped profile, without a file being edited by hand.
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
	if reference, _ := document["api_key"].(string); reference != "file:../secrets/model.key" {
		t.Fatalf("api_key = %q, want a relative reference to the secret", reference)
	}

	// And it resolves: the credential is readable where the configuration says.
	layout := runtime.Layout()
	value, err := secret.Read(filepath.Join(layout.SecretsDir(), "model.key"))
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

func TestApplyModelConfigurationRefusesToReplaceARunningCore(t *testing.T) {
	root := t.TempDir()
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "deepseek", Model: "first-model", APIKey: "sk-first"}); err != nil {
		t.Fatalf("first ApplyModelConfiguration: %v", err)
	}
	before, err := os.ReadFile(runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("read the configuration: %v", err)
	}

	// Configure is a no-op once a real core is installed, so writing here would
	// leave the files and the running core disagreeing.
	err = runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "deepseek", Model: "second-model", APIKey: "sk-second"})

	if err == nil {
		t.Fatal("a second configuration replaced a running core")
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Fatalf("error = %v, want it to say a restart is needed", err)
	}
	after, readErr := os.ReadFile(runtime.ModelConfigPath())
	if readErr != nil {
		t.Fatalf("re-read the configuration: %v", readErr)
	}
	if string(before) != string(after) {
		t.Fatal("the refused call still changed the configuration")
	}
}

func TestApplyModelConfigurationValidatesItsInput(t *testing.T) {
	runtime, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

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

// A root whose shipped configuration could not be prepared must not reach ready,
// even once a valid model configuration exists. Otherwise the user is shown a
// ready Runtime that is running the fallback agent configuration: no definitions,
// a smaller step budget, and nothing in the flow that would notice.
//
// The test lives in seed_internal_test.go, because provoking that failure needs
// the unexported seam.
