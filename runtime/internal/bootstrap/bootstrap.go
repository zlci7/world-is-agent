// Package bootstrap turns configuration on disk into a running agent core.
//
// It exists because two startup concerns are not the same concern:
//
//   - Bootstrap owns the data root, the configuration files and the stores. It
//     always succeeds for a writable data root, even when nothing is configured.
//   - The agent core needs a usable model configuration. A missing or broken
//     model configuration is a state the Runtime reports, not a reason to exit:
//     the process must stay alive so the user can fix it and Configure again.
//
// Nothing outside this package resolves runtime-owned paths, so the Runtime no
// longer depends on the process working directory.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"sync"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/config"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/secret"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
	"gameagent/runtime/internal/trace"
)

const (
	modelConfigFile = "model.json"
	agentConfigFile = "agent.json"
	// modelSecretFile is where the first-run flow stores the credential. The model
	// configuration references it relatively, so the two move together with the
	// data root.
	modelSecretFile = "model.key"
)

// ModelSetup is a candidate model configuration from the first-run flow. APIKey
// is transient: it is written to the secrets directory and never returned,
// logged or traced. A zero window means the caller did not choose one, and the
// shipped default for the provider is written instead.
type ModelSetup struct {
	Provider            string
	Model               string
	BaseURL             string
	APIKey              string
	ContextWindowTokens int
	MaxOutputTokens     int
}

// modelConfig is the written form of the model configuration. It restates the
// fields rather than reusing llm.Config: this is what the Runtime writes for its
// user, and a field added to the loader should not start appearing in files as a
// side effect.
type modelConfig struct {
	Provider            string `json:"provider"`
	Model               string `json:"model,omitempty"`
	APIKey              string `json:"api_key"`
	BaseURL             string `json:"base_url,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     int    `json:"max_output_tokens,omitempty"`
}

// ApplyModelConfiguration stores a model configuration and installs the agent
// core from it.
//
// The credential is written first and the configuration that references it
// second: a configuration naming a file that is not there leaves a root whose
// credential is missing, which is a worse state to recover from than having
// written neither.
//
// It refuses to run while a real core is installed, because Configure is a no-op
// in that state: writing anyway would leave the files and the running core
// disagreeing about which model is in use.
func (r *Runtime) ApplyModelConfiguration(setup ModelSetup) error {
	if strings.TrimSpace(setup.Provider) == "" {
		return errors.New("a model provider is required")
	}
	if strings.TrimSpace(setup.APIKey) == "" {
		return errors.New("an API key is required")
	}

	r.mu.Lock()
	installed := r.loop != nil && !r.placeholder
	r.mu.Unlock()
	if installed {
		return errors.New("the agent core is already configured; changing the model requires a restart")
	}

	secretPath := filepath.Join(r.layout.SecretsDir(), modelSecretFile)
	if err := secret.Write(secretPath, setup.APIKey); err != nil {
		return fmt.Errorf("store the API key: %w", err)
	}

	reference, err := filepath.Rel(r.layout.ConfigDir(), secretPath)
	if err != nil {
		return fmt.Errorf("reference the API key file: %w", err)
	}

	// A configuration with no window is valid, so this is about the quality of the
	// default rather than about validity: without a window the provider skips its
	// own check and the Runtime turns summarisation off.
	window := model.WindowLimits{ContextTokens: setup.ContextWindowTokens, OutputTokens: setup.MaxOutputTokens}
	if window == (model.WindowLimits{}) {
		window = llm.DefaultWindowLimits(setup.Provider, setup.Model)
	}

	document, err := json.MarshalIndent(modelConfig{
		Provider:            setup.Provider,
		Model:               setup.Model,
		BaseURL:             setup.BaseURL,
		APIKey:              "file:" + filepath.ToSlash(reference),
		ContextWindowTokens: window.ContextTokens,
		MaxOutputTokens:     window.OutputTokens,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the model configuration: %w", err)
	}
	if err := config.WriteFile(r.modelPath, append(document, '\n')); err != nil {
		return fmt.Errorf("write the model configuration: %w", err)
	}

	return r.Configure()
}

// seedDefaults is a variable so a test can make initialization fail. A fresh root
// that cannot be given the shipped configuration must not reach ready, and that
// cannot be provoked on a data root that behaves.
var seedDefaults = config.Seed

// State reports how far the Runtime got.
type State string

const (
	// StateNeedsConfiguration means Bootstrap is running but the agent core is
	// not. Reason says what is missing, and configuring a model can resolve it.
	StateNeedsConfiguration State = "needs_configuration"
	// StateBlocked means the data root has a configuration problem that
	// configuring a model cannot resolve, so the core will not become ready in
	// this process. It is separate from StateNeedsConfiguration because the two
	// ask the user for opposite things: one asks for a model, the other asks for
	// a restart with the initialization problem fixed.
	StateBlocked State = "blocked"
	// StateReady means the agent core is serving turns.
	StateReady State = "ready"
)

// Runtime is the agent core holder. The gateway only ever sees this value, so
// installing or replacing the core never requires rewiring the transport.
type Runtime struct {
	mu        sync.RWMutex
	layout    dataroot.Layout
	agentCfg  agent.Config
	recorder  trace.Recorder
	store     memory.HistoryStore
	modelPath string
	agentPath string
	loop      *agent.Loop
	// placeholder marks a core that exists only to report an unusable
	// configuration, so Configure may replace it.
	placeholder bool
	state       State
	reason      string
	blocked     bool
}

// Open resolves the data root, loads what configuration exists and opens the
// stores. It fails only when the Runtime cannot run at all: the data root is
// unusable, or a store cannot be opened. An unusable agent or model
// configuration is reported through State and Reason instead, because the user
// has to be able to see the error.
//
// Open also attempts to install the agent core, so State and Reason are accurate
// as soon as it returns. Call Configure again once the model configuration has
// been written.
func Open(explicitRoot string, env dataroot.Env) (*Runtime, error) {
	root, err := dataroot.Resolve(explicitRoot, env)
	if err != nil {
		return nil, err
	}
	layout := dataroot.New(root)
	if err := layout.Ensure(); err != nil {
		return nil, err
	}

	// A fresh data root starts from the shipped configuration. This runs before
	// anything reads the agent configuration, and does nothing once the root has
	// one.
	//
	// A failure here blocks the core rather than being logged and forgotten. The
	// agent configuration loader treats a missing file as "use the defaults", so a
	// root whose shipped configuration could not be prepared would otherwise
	// configure a key, reach ready, and run an agent with no definitions and a
	// smaller step budget -- exactly the state seeding exists to prevent, and one
	// nothing later in the flow would notice.
	blocked, blockedReason := false, ""
	if wroteSeed, err := seedDefaults(layout.ConfigDir(), strings.TrimSpace(env.Getenv(agent.ConfigEnvName)) != ""); err != nil {
		blocked, blockedReason = true, fmt.Sprintf("the shipped configuration could not be prepared: %v", err)
		log.Printf("seed shipped configuration failed: %v", err)
	} else if wroteSeed {
		log.Printf("GameAgent seeded default configuration into %s", layout.ConfigDir())
	}

	// Reported as a state rather than exited on: the process has to stay up so the
	// client can show why the configuration was refused.
	r := &Runtime{layout: layout, state: StateNeedsConfiguration}
	if blocked {
		r.state, r.reason, r.blocked = StateBlocked, blockedReason, true
	}
	agentPath := resolveConfigPath(env, agent.ConfigEnvName, root, layout.ConfigPath(agentConfigFile))
	r.modelPath = resolveConfigPath(env, llm.ConfigEnvName, root, layout.ConfigPath(modelConfigFile))
	r.agentPath = agentPath

	agentConfig, err := agent.LoadConfigFile(agentPath)
	if err != nil {
		r.state = StateBlocked
		r.reason = fmt.Sprintf("agent configuration at %s is unusable: %v", agentPath, err)
		r.blocked = true
		agentConfig = agent.DefaultConfig()
	}
	r.agentCfg = resolveConfigPaths(root, agentConfig)

	recorder, err := trace.NewJSONLRecorder(layout.TracePath(), trace.JSONLRecorderOptions{})
	if err != nil {
		// A missing trace must not stop the Runtime, but it must not be silent
		// either: the trace is what the client shows.
		log.Printf("create trace recorder failed: %v, fallback to noop", err)
		r.recorder = trace.NoopRecorder{}
	} else {
		r.recorder = recorder
	}

	r.store = memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{
		Root:                          r.agentCfg.MemoryStore.Root,
		BusyTimeout:                   r.agentCfg.MemoryStore.BusyTimeout,
		MaxRecordsPerEntity:           r.agentCfg.MemoryStore.MaxRecordsPerEntity,
		MaxProjectionBatchesPerEntity: r.agentCfg.MemoryStore.MaxProjectionBatchesPerEntity,
	}, r.agentCfg.History, memory.WithHistoryIndexLimits(r.agentCfg.HistoryIndex))

	// The returned error is the reason the core is not ready, which is a state,
	// not a failure to open the Runtime.
	_ = r.Configure()
	return r, nil
}

// Configure installs the agent core. It reads the model configuration, which is
// the one file the first-run flow replaces, and keeps the agent configuration it
// loaded at startup. Repeating it while a real core is installed is a no-op.
//
// When the configuration is unusable, Configure installs a core that fails every
// model call with the reason. A turn must still reach its terminal state: the
// adapter releases the interaction on TurnCompletion, and a turn that never
// completes would block the next interaction with that NPC.
func (r *Runtime) Configure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loop != nil && !r.placeholder {
		return nil
	}
	if r.blocked {
		r.installUnavailable()
		return errors.New(r.reason)
	}

	provider, _, err := llm.NewProviderFromConfigFile(r.modelPath)
	if err != nil {
		r.state = StateNeedsConfiguration
		r.reason = modelReason(r.modelPath, err)
		r.installUnavailable()
		return errors.New(r.reason)
	}

	var catalog definition.Catalog
	if root := r.agentCfg.DefinitionCatalogRoot; root != "" {
		catalog, err = definition.LoadCatalogFromDir(root)
		if err != nil {
			r.state = StateNeedsConfiguration
			r.reason = fmt.Sprintf("definition catalog at %s is unusable: %v", root, err)
			r.installUnavailable()
			return errors.New(r.reason)
		}
	}

	r.loop = agent.NewLoop(provider, r.recorder, r.agentCfg,
		agent.WithDefinitionCatalog(catalog),
		agent.WithHistoryStore(r.store),
	)
	r.placeholder = false
	r.state = StateReady
	r.reason = ""
	return nil
}

func (r *Runtime) installUnavailable() {
	r.loop = agent.NewLoop(unavailableProvider{reason: r.reason}, r.recorder, r.agentCfg,
		agent.WithHistoryStore(r.store),
	)
	r.placeholder = true
}

// unavailableProvider stands in for a model that is not configured yet. It keeps
// the turn lifecycle intact so the failure is reported instead of silently
// hanging the interaction.
type unavailableProvider struct {
	reason string
}

func (p unavailableProvider) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, fmt.Errorf("agent core is not ready: %s", p.reason)
}

// HandleEvent serves one adapter GameEvent. When the core is not configured the
// caller gets the reason instead of silence.
func (r *Runtime) HandleEvent(ctx context.Context, env agent.Environment, conn agent.ConnectionContext, key session.AgentSessionKey, target *protocolv1alpha2.EntityRef, catalog *tool.EnvironmentToolCatalog, event *protocolv1alpha2.GameEvent) error {
	core, err := r.core()
	if err != nil {
		return err
	}
	return core.HandleEvent(ctx, env, conn, key, target, catalog, event)
}

// HandleTaskWake serves one durable task wake.
func (r *Runtime) HandleTaskWake(ctx context.Context, env agent.Environment, conn agent.ConnectionContext, target *protocolv1alpha2.EntityRef, catalog *tool.EnvironmentToolCatalog, exec task.ExecutionContext, record task.Record) error {
	core, err := r.core()
	if err != nil {
		return err
	}
	return core.HandleTaskWake(ctx, env, conn, target, catalog, exec, record)
}

// MaintainHistory keeps session history compacted. Without a core there is no
// history to maintain, so it is a no-op rather than a failure.
func (r *Runtime) MaintainHistory(ctx context.Context, key session.AgentSessionKey, currentTime *memory.GameTimeSnapshot) {
	core, err := r.core()
	if err != nil {
		return
	}
	core.MaintainHistory(ctx, key, currentTime)
}

// HistoryStore is the memory backend the gateway publishes task results into. It
// outlives any single core, so it is available before the core is ready.
func (r *Runtime) HistoryStore() memory.HistoryStore { return r.store }

// Layout is the resolved data root layout.
func (r *Runtime) Layout() dataroot.Layout { return r.layout }

// AgentConfig is the resolved agent configuration, with every runtime-owned path
// already interpreted against the data root.
func (r *Runtime) AgentConfig() agent.Config { return r.agentCfg }

// ModelConfigPath is where the agent core expects the model configuration.
func (r *Runtime) ModelConfigPath() string { return r.modelPath }

// AgentConfigPath is where the Runtime read the agent configuration from.
func (r *Runtime) AgentConfigPath() string { return r.agentPath }

// State reports whether the agent core is serving turns.
func (r *Runtime) State() State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// Ready reports whether the agent core can serve turns. Downstream components that
// consume durable state, such as the task dispatcher, must not act while it is false.
func (r *Runtime) Ready() bool { return r.State() == StateReady }

// Blocked reports a configuration problem that configuring a model cannot resolve.
// A caller about to do work whose only purpose is to make the core ready can use
// it to fail immediately, instead of spending that work on a process that will
// refuse the result.
func (r *Runtime) Blocked() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.blocked
}

// Reason explains a state that is not StateReady.
func (r *Runtime) Reason() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.reason
}

// Close releases the trace recorder.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorder == nil {
		return nil
	}
	recorder := r.recorder
	r.recorder = nil
	return recorder.Close(context.Background())
}

func (r *Runtime) core() (*agent.Loop, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.loop == nil {
		return nil, fmt.Errorf("agent core is not ready: %s", r.reason)
	}
	return r.loop, nil
}

func resolveConfigPath(env dataroot.Env, name, root, fallback string) string {
	if value := strings.TrimSpace(env.Getenv(name)); value != "" {
		return dataroot.ResolvePath(root, value)
	}
	return dataroot.ResolvePath(root, fallback)
}

func resolveConfigPaths(root string, cfg agent.Config) agent.Config {
	cfg.MemoryStore.Root = dataroot.ResolvePath(root, cfg.MemoryStore.Root)
	cfg.Task.DBPath = dataroot.ResolvePath(root, cfg.Task.DBPath)
	if strings.TrimSpace(cfg.DefinitionCatalogRoot) != "" {
		cfg.DefinitionCatalogRoot = dataroot.ResolvePath(root, cfg.DefinitionCatalogRoot)
	}
	return cfg
}

func modelReason(path string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("model configuration not found at %s", path)
	}
	return fmt.Sprintf("model configuration at %s is unusable: %v", path, err)
}
