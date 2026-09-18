package bootstrap_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
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
	if reason := runtime.Reason(); reason != "" {
		t.Fatalf("ready runtime kept a reason: %q", reason)
	}
	if err := runtime.Configure(); err != nil {
		t.Fatalf("configure must be idempotent while ready: %v", err)
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
