// Package bootstrap owns configuration commits, runtime publication and shutdown.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/config"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/gateway"
	"gameagent/runtime/internal/idgen"
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
	modelSecretFile = "model.key"
)

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

type State string

const (
	StateNeedsConfiguration State = "needs_configuration"
	StateBlocked            State = "blocked"
	StateReady              State = "ready"
	StateReconfiguring      State = "reconfiguring"
)

type Game struct {
	ID    string  `json:"id"`
	Title *string `json:"title"`
}

type Snapshot struct {
	State               State                    `json:"state"`
	Ready               bool                     `json:"ready"`
	Reason              string                   `json:"reason,omitempty"`
	ReasonCode          string                   `json:"-"`
	LoadedGame          *Game                    `json:"loaded_game"`
	ConfiguredGame      *Game                    `json:"configured_game"`
	RestartRequired     bool                     `json:"restart_required"`
	Model               *llm.ConfigSummary       `json:"model,omitempty"`
	ModelError          string                   `json:"model_error,omitempty"`
	AgentConfigPath     string                   `json:"agent_config_path"`
	Adapters            []gateway.ConnectionInfo `json:"adapters"`
	ConnectionCount     int                      `json:"connection_count"`
	LastConnectionError *ConnectionError         `json:"last_connection_error"`
}

type runtimeBundle struct {
	config    agent.Config
	catalog   definition.Catalog
	provider  model.Provider
	loop      *agent.Loop
	history   memory.HistoryStore
	gateway   *gateway.Server
	taskStore *task.SQLiteStore
}

// setupMu owns all slow preparation and commits. mu protects only the published
// snapshot; readers never wait for model or asset IO.
type Runtime struct {
	protocolv1alpha2.UnimplementedGameAgentGatewayServer
	setupMu             sync.Mutex
	mu                  sync.RWMutex
	layout              dataroot.Layout
	modelPath           string
	overridePath        string
	recorder            trace.Recorder
	snapshot            Snapshot
	bundle              *runtimeBundle
	streams             *gateway.StreamGroup
	closed              bool
	lastConnectionError *ConnectionError
	// Test seams exercise the publication boundary and cleanup failures.
	testBeforePublish func() error
	testCleanup       func(*runtimeBundle) error
	pendingCleanup    *runtimeBundle
}

func Open(explicitRoot string, env dataroot.Env) (*Runtime, error) {
	root, err := dataroot.Resolve(explicitRoot, env)
	if err != nil {
		return nil, err
	}
	layout := dataroot.New(root)
	if err := layout.Ensure(); err != nil {
		return nil, err
	}
	recorder, err := trace.NewJSONLRecorder(layout.TracePath(), trace.JSONLRecorderOptions{})
	if err != nil {
		return nil, fmt.Errorf("open trace storage: %w", err)
	}
	r := &Runtime{layout: layout, recorder: recorder, streams: &gateway.StreamGroup{}}
	r.modelPath = resolveConfigPath(env, llm.ConfigEnvName, root, layout.ConfigPath(modelConfigFile))
	if value := strings.TrimSpace(env.Getenv(agent.ConfigEnvName)); value != "" {
		r.overridePath = dataroot.ResolvePath(root, value)
	}
	r.snapshot.State = StateNeedsConfiguration
	_ = r.Configure()
	return r, nil
}

func (r *Runtime) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := r.snapshot
	result.LoadedGame = cloneGame(result.LoadedGame)
	result.ConfiguredGame = cloneGame(result.ConfiguredGame)
	if result.Model != nil {
		copy := *result.Model
		result.Model = &copy
	}
	result.Adapters = []gateway.ConnectionInfo{}
	if result.Ready && r.bundle != nil {
		result.Adapters = r.bundle.gateway.Connections()
	}
	result.ConnectionCount = len(result.Adapters)
	if r.lastConnectionError != nil {
		copy := *r.lastConnectionError
		result.LastConnectionError = &copy
	}
	return result
}
func cloneGame(game *Game) *Game {
	if game == nil {
		return nil
	}
	result := *game
	if game.Title != nil {
		title := *game.Title
		result.Title = &title
	}
	return &result
}
func shippedGame(id string) (*Game, error) {
	games, err := config.Games()
	if err != nil {
		return nil, err
	}
	for _, game := range games {
		if game.ID == id {
			title := game.Title
			return &Game{ID: id, Title: &title}, nil
		}
	}
	return nil, &config.Error{Code: "invalid_game", Err: fmt.Errorf("unknown game %q", id)}
}
func (r *Runtime) publishFailure(next Snapshot, state State, code string, err error) error {
	next.State, next.Ready, next.ReasonCode, next.Reason = state, false, code, err.Error()
	r.mu.Lock()
	r.snapshot = next
	r.mu.Unlock()
	return err
}

type initializationError struct {
	err    error
	unsafe bool
}

func (e *initializationError) Error() string { return e.err.Error() }
func (e *initializationError) Unwrap() error { return e.err }
func errorCode(err error, fallback string) string {
	var e *config.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return fallback
}
func (r *Runtime) readSelection() (*Game, error) {
	data, err := os.ReadFile(r.layout.ConfigPath("active-game.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &config.Error{Code: "game_not_selected", Err: errors.New("choose a game to configure this Runtime")}
		}
		return nil, &config.Error{Code: "storage_unavailable", Err: err}
	}
	var document struct {
		GameID string `json:"game_id"`
	}
	if err := json.Unmarshal(data, &document); err != nil || strings.TrimSpace(document.GameID) == "" {
		return nil, &config.Error{Code: "game_selection_invalid", Err: errors.New("the game selection is invalid; choose a game again")}
	}
	game, err := shippedGame(document.GameID)
	if err != nil {
		return &Game{ID: document.GameID}, err
	}
	return game, nil
}

func (r *Runtime) Configure() error {
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	return r.configureLocked()
}
func (r *Runtime) configureLocked() error {
	r.mu.RLock()
	installed, closed, blocked, reason := r.bundle != nil, r.closed, r.snapshot.State == StateBlocked, r.snapshot.Reason
	r.mu.RUnlock()
	if closed || blocked {
		return errors.New(reason)
	}
	if installed {
		return nil
	}
	game, err := r.readSelection()
	next := Snapshot{ConfiguredGame: game, AgentConfigPath: r.overridePath}
	summary, summaryErr := llm.DescribeConfig(r.modelPath)
	if summaryErr == nil {
		next.Model = &summary
	} else {
		next.ModelError = summaryErr.Error()
	}
	fail := func(state State, code string, err error) error { return r.publishFailure(next, state, code, err) }
	if err != nil {
		code := errorCode(err, "storage_unavailable")
		state := StateNeedsConfiguration
		if code == "storage_unavailable" {
			state = StateBlocked
		}
		return fail(state, code, err)
	}
	if err := config.AssetsStatus(r.layout.ConfigDir(), game.ID); err != nil {
		code := errorCode(err, "profile_invalid")
		state := StateNeedsConfiguration
		if code == "storage_unavailable" {
			state = StateBlocked
		}
		return fail(state, code, err)
	}
	path := r.layout.ConfigPath(filepath.Join("games", game.ID, agentConfigFile))
	if r.overridePath != "" {
		path = r.overridePath
	}
	next.AgentConfigPath = path
	cfg, err := strictAgentConfig(path)
	if err != nil {
		code, state := "profile_invalid", StateNeedsConfiguration
		if r.overridePath != "" {
			code, state = "override_configuration_invalid", StateBlocked
		}
		return fail(state, code, err)
	}
	cfg = resolveConfigPaths(r.layout.Root(), cfg)
	if err := r.validateOverrideRoot(cfg); err != nil {
		return fail(StateBlocked, "override_configuration_invalid", err)
	}
	catalog, err := definition.LoadGameCatalogFromDir(cfg.DefinitionCatalogRoot, game.ID)
	if err != nil {
		return fail(StateNeedsConfiguration, "profile_invalid", err)
	}
	provider, _, err := llm.NewProviderFromConfigFile(r.modelPath)
	if err != nil {
		return fail(StateNeedsConfiguration, "model_configuration_required", errors.New(modelReason(r.modelPath, err)))
	}
	candidate, err := r.prepareBundle(cfg, catalog, provider)
	if err == nil && r.testBeforePublish != nil {
		if err = r.testBeforePublish(); err != nil {
			err = r.abortCandidate(candidate, err)
		}
	}
	if err != nil {
		state := StateNeedsConfiguration
		var failure *initializationError
		if errors.As(err, &failure) && failure.unsafe {
			state = StateBlocked
		}
		return fail(state, "initialization_failed", err)
	}
	next.LoadedGame = cloneGame(game)
	next.State, next.Ready = StateReady, true
	r.mu.Lock()
	r.bundle, r.snapshot = candidate, next
	r.mu.Unlock()
	return nil
}

func strictAgentConfig(path string) (agent.Config, error) {
	if _, err := os.Stat(path); err != nil {
		return agent.Config{}, err
	}
	return agent.LoadConfigFile(path)
}

func (r *Runtime) validateOverrideRoot(cfg agent.Config) error {
	if r.overridePath == "" || cfg.DefinitionCatalogRoot == filepath.Join(r.layout.ConfigDir(), "games") {
		return nil
	}
	info, err := os.Stat(cfg.DefinitionCatalogRoot)
	if err != nil {
		return fmt.Errorf("override definition catalog root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("override definition catalog root %s must be a directory", cfg.DefinitionCatalogRoot)
	}
	return nil
}

func (r *Runtime) rejectOverride(err error) error {
	next := r.Snapshot()
	if !next.Ready {
		r.publishFailure(next, StateBlocked, "override_configuration_invalid", err)
	}
	return &config.Error{Code: "override_configuration_invalid", Err: err}
}

func (r *Runtime) prepareBundle(cfg agent.Config, catalog definition.Catalog, provider model.Provider) (*runtimeBundle, error) {
	if err := os.MkdirAll(cfg.MemoryStore.Root, 0o755); err != nil {
		return nil, err
	}
	history := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{
		Root: cfg.MemoryStore.Root, BusyTimeout: cfg.MemoryStore.BusyTimeout,
		MaxRecordsPerEntity:           cfg.MemoryStore.MaxRecordsPerEntity,
		MaxProjectionBatchesPerEntity: cfg.MemoryStore.MaxProjectionBatchesPerEntity,
	}, cfg.History, memory.WithHistoryIndexLimits(cfg.HistoryIndex))
	candidate := &runtimeBundle{config: cfg, catalog: catalog, provider: provider, history: history}
	candidate.loop = agent.NewLoop(provider, r.recorder, cfg, agent.WithDefinitionCatalog(catalog), agent.WithHistoryStore(history))
	options := []gateway.ServerOption{gateway.WithConnectionReady(func() { r.mu.Lock(); r.lastConnectionError = nil; r.mu.Unlock() })}
	if cfg.Task.Enabled {
		storeOptions := cfg.Task.StoreOptions
		storeOptions.Path = cfg.Task.DBPath
		store, err := task.OpenSQLiteStore(context.Background(), storeOptions)
		if err != nil {
			return nil, err
		}
		candidate.taskStore = store
		options = append(options, gateway.WithTaskService(task.NewService(store)), gateway.WithTaskResultHistory(history))
	}
	candidate.gateway = gateway.NewServer(candidate.loop, options...)
	if cfg.Task.Enabled {
		dispatch := task.DispatcherConfig{ScanInterval: cfg.Task.ScanInterval, BatchSize: cfg.Task.DispatchBatch, RetryMin: cfg.Task.RetryMin, RetryMax: cfg.Task.RetryMax}
		if err := candidate.gateway.StartTaskDispatcher(context.Background(), dispatch, nil, nil); err != nil {
			return nil, r.abortCandidate(candidate, err)
		}
	}
	return candidate, nil
}

func (r *Runtime) SelectGame(id string) error {
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	if err := r.gameSetupAllowed(); err != nil {
		return err
	}
	if _, err := shippedGame(id); err != nil {
		return err
	}
	prepared, err := config.PrepareGame(r.layout.ConfigDir(), id)
	if err != nil {
		return err
	}
	defer prepared.Close()
	cfg := prepared.AgentConfig
	if r.overridePath != "" {
		cfg, err = strictAgentConfig(r.overridePath)
		if err != nil {
			return r.rejectOverride(err)
		}
	}
	cfg = resolveConfigPaths(r.layout.Root(), cfg)
	if err := r.validateOverrideRoot(cfg); err != nil {
		return r.rejectOverride(err)
	}
	if cfg.DefinitionCatalogRoot != filepath.Join(r.layout.ConfigDir(), "games") {
		if _, err := definition.LoadGameCatalogFromDir(cfg.DefinitionCatalogRoot, id); err != nil {
			return &config.Error{Code: "profile_invalid", Err: err}
		}
	}
	if err := prepared.CommitAssets(); err != nil {
		return err
	}
	// Another process may have created a valid but different file while assets
	// were prepared. Validate the effective on-disk content before selection.
	actualPath := r.layout.ConfigPath(filepath.Join("games", id, agentConfigFile))
	if r.overridePath != "" {
		actualPath = r.overridePath
	}
	actual, err := strictAgentConfig(actualPath)
	if err != nil {
		return &config.Error{Code: "profile_invalid", Err: err}
	}
	actual = resolveConfigPaths(r.layout.Root(), actual)
	if _, err := definition.LoadGameCatalogFromDir(actual.DefinitionCatalogRoot, id); err != nil {
		return &config.Error{Code: "profile_invalid", Err: err}
	}
	data, _ := json.Marshal(struct {
		GameID string `json:"game_id"`
	}{id})
	commit := func() error { return config.WriteFile(r.layout.ConfigPath("active-game.json"), append(data, '\n')) }
	r.mu.RLock()
	installed := r.bundle != nil
	r.mu.RUnlock()
	if !installed {
		if err := commit(); err != nil {
			return &config.Error{Code: "game_setup_failed", Err: err}
		}
		_ = r.configureLocked()
		return nil
	}
	catalog, err := definition.LoadGameCatalogFromDir(actual.DefinitionCatalogRoot, id)
	if err != nil {
		return err
	}
	provider, _, err := llm.NewProviderFromConfigFile(r.modelPath)
	if err != nil {
		return err
	}
	summary, err := llm.DescribeConfig(r.modelPath)
	if err != nil {
		return err
	}
	game, _ := shippedGame(id)
	return r.replaceLocked(actual, catalog, provider, Snapshot{ConfiguredGame: game, AgentConfigPath: actualPath, Model: &summary}, commit)
}
func (r *Runtime) gameSetupAllowed() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.snapshot.State == StateBlocked {
		return &config.Error{Code: "setup_blocked", Err: errors.New(r.snapshot.Reason)}
	}
	return nil
}

// ModelSetupAllowed is checked before HTTP reads a credential and again under
// the coordinator write lock before persisting it.
func (r *Runtime) ModelSetupAllowed() error {
	snapshot := r.Snapshot()
	if snapshot.State == StateBlocked {
		return &config.Error{Code: "setup_blocked", Err: errors.New(snapshot.Reason)}
	}
	if snapshot.Ready {
		return nil
	}
	if snapshot.ReasonCode != "model_configuration_required" && snapshot.ReasonCode != "initialization_failed" {
		return &config.Error{Code: snapshot.ReasonCode, Err: errors.New(snapshot.Reason)}
	}
	return nil
}
func (r *Runtime) ApplyModelConfiguration(setup ModelSetup) error {
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	if err := r.ModelSetupAllowed(); err != nil {
		return err
	}
	if strings.TrimSpace(setup.Provider) == "" {
		return errors.New("a model provider is required")
	}
	if strings.TrimSpace(setup.APIKey) == "" {
		return errors.New("an API key is required")
	}
	game, err := r.readSelection()
	if err != nil {
		return err
	}
	cfg, catalog, path, err := r.loadGame(game.ID)
	if err != nil {
		return err
	}
	secretPath := filepath.Join(r.layout.SecretsDir(), idgen.New("model")+".key")
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(secretPath)
		}
	}()
	if err := secret.Write(secretPath, setup.APIKey); err != nil {
		return fmt.Errorf("store the API key: %w", err)
	}

	reference, err := filepath.Rel(filepath.Dir(r.modelPath), secretPath)
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
	stagePath := r.modelPath + "." + idgen.New("candidate")
	defer os.Remove(stagePath)
	if err := config.WriteFile(stagePath, append(document, '\n')); err != nil {
		return err
	}
	provider, _, err := llm.NewProviderFromConfigFile(stagePath)
	if err != nil {
		return err
	}
	summary, err := llm.DescribeConfig(stagePath)
	if err != nil {
		return err
	}
	next := Snapshot{ConfiguredGame: game, AgentConfigPath: path, Model: &summary}
	err = r.replaceLocked(cfg, catalog, provider, next, func() error {
		if err := config.WriteFile(r.modelPath, append(document, '\n')); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return err
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

func (r *Runtime) HistoryStore() memory.HistoryStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.bundle == nil {
		return nil
	}
	return r.bundle.history
}
func (r *Runtime) Layout() dataroot.Layout { return r.layout }
func (r *Runtime) AgentConfig() agent.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.bundle == nil {
		return agent.Config{}
	}
	return r.bundle.config
}
func (r *Runtime) ModelConfigPath() string { return r.modelPath }
func (r *Runtime) AgentConfigPath() string { return r.Snapshot().AgentConfigPath }
func (r *Runtime) State() State            { return r.Snapshot().State }
func (r *Runtime) Ready() bool             { r.mu.RLock(); defer r.mu.RUnlock(); return r.snapshot.Ready }
func (r *Runtime) Reason() string          { return r.Snapshot().Reason }
func (r *Runtime) Close() error {
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.snapshot.Ready = false
	r.snapshot.State, r.snapshot.ReasonCode, r.snapshot.Reason = StateBlocked, "initialization_failed", "Runtime is shutting down"
	bundle := r.bundle
	r.mu.Unlock()
	var result error
	r.streams.Close()
	if bundle != nil {
		result = closeBundle(bundle)
	}
	if r.pendingCleanup != nil {
		result = errors.Join(result, closeBundle(r.pendingCleanup))
	}
	return errors.Join(result, r.recorder.Close(context.Background()))
}
func (r *Runtime) core() (*agent.Loop, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.snapshot.Ready || r.bundle == nil {
		return nil, fmt.Errorf("agent core is not ready: %s", r.snapshot.Reason)
	}
	return r.bundle.loop, nil
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
func closeBundle(bundle *runtimeBundle) error {
	var result error
	if bundle.gateway != nil {
		result = bundle.gateway.Close(context.Background())
	}
	if bundle.taskStore != nil {
		result = errors.Join(result, bundle.taskStore.Close())
	}
	return result
}
func (r *Runtime) abortCandidate(candidate *runtimeBundle, cause error) error {
	cleanup := closeBundle(candidate)
	if r.testCleanup != nil {
		cleanup = errors.Join(cleanup, r.testCleanup(candidate))
	}
	if cleanup != nil {
		r.pendingCleanup = candidate
	}
	return &initializationError{err: errors.Join(cause, cleanup), unsafe: cleanup != nil}
}
