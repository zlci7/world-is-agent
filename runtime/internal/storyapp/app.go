package storyapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/secret"
)

func Open(ctx context.Context, options Options) (*App, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root := strings.TrimSpace(options.DataRoot)
	if root == "" {
		return nil, fmt.Errorf("storyapp: data root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	appRoot := filepath.Join(root, "story-app")
	userID := strings.TrimSpace(options.UserID)
	if userID == "" {
		userID = LocalUserID
	}
	if err := os.MkdirAll(filepath.Join(appRoot, "worlds", userID), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(appRoot, "config"), 0o755); err != nil {
		return nil, err
	}
	db, err := openAppDB(filepath.Join(appRoot, "app.db"))
	if err != nil {
		return nil, err
	}
	app := &App{root: appRoot, dataRoot: root, appDB: db, userID: userID, worlds: make(map[string]*worldRuntime), runs: make(map[string]*runRuntime), logger: options.Logger, closed: make(chan struct{})}
	if options.ModelConfigPath != "" {
		app.modelPath = options.ModelConfigPath
	} else if value := strings.TrimSpace(os.Getenv("WIA_MODEL_CONFIG")); value != "" {
		app.modelPath = value
	} else {
		app.modelPath = filepath.Join(appRoot, "config", "model.json")
	}
	if options.Generator != nil {
		app.generator = options.Generator
		app.modelInfo = ModelInfo{Provider: "test", Model: "injected", Configured: true, Source: "injected"}
	} else {
		app.loadModelConfig(options.AllowFake)
	}
	if err := app.markInterrupted(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	select {
	case <-a.closed:
	default:
		close(a.closed)
	}
	a.runsMu.Lock()
	active := make([]*runRuntime, 0, len(a.runs))
	for _, r := range a.runs {
		r.Cancel()
		active = append(active, r)
	}
	a.runsMu.Unlock()
	for _, r := range active {
		select {
		case <-r.Done:
		case <-time.After(5 * time.Second):
		}
	}
	if a.appDB != nil {
		return a.appDB.Close()
	}
	return nil
}

func (a *App) loadModelConfig(allowFake bool) {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	a.generator = nil
	a.modelInfo = ModelInfo{}
	a.modelError = ""
	config, err := llm.LoadConfig(a.modelPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			a.modelError = "model configuration could not be read"
		}
		return
	}
	if (config.Provider == "" || config.Provider == "fake") && !allowFake {
		a.modelError = "model_not_configured: a real provider is required"
		return
	}
	provider, _, err := llm.NewProviderFromConfigFile(a.modelPath)
	if err != nil {
		a.modelError = err.Error()
		return
	}
	generator, ok := provider.(model.TextGenerator)
	if !ok {
		a.modelError = "text_generation_unsupported: configured provider does not support text generation"
		return
	}
	a.generator = generator
	a.modelInfo = ModelInfo{Provider: config.Provider, Model: config.Model, Configured: true, Source: "config"}
}

func (a *App) Logger() Logger          { return a.logger }
func (a *App) DataRoot() string        { return a.dataRoot }
func (a *App) ModelConfigPath() string { return a.modelPath }

func (a *App) Status(ctx context.Context) (Status, error) {
	a.modelMu.RLock()
	info, modelErr, ready := a.modelInfo, a.modelError, a.generator != nil
	a.modelMu.RUnlock()
	activeID, revision, err := a.activeWorldState(ctx)
	if err != nil {
		return Status{}, err
	}
	var active *WorldSummary
	if activeID != "" {
		summary, err := a.worldSummary(ctx, activeID)
		if err != nil && !errors.Is(err, ErrWorldNotFound) {
			return Status{}, err
		} else if err == nil {
			active = &summary
		}
	}
	return Status{Ready: ready, Model: info, ModelError: modelErr, UserID: a.userID, ActiveWorld: active, ActiveRevision: revision, DataRoot: a.dataRoot, ModelConfigPath: a.modelPath}, nil
}

func (a *App) Games() []GameSummary { return []GameSummary{lanternDefinition().Summary} }

func (a *App) CreateWorld(ctx context.Context, name, mode, playerName, playerProfile string, activate bool) (WorldSummary, error) {
	def := lanternDefinition()
	if mode == "" {
		mode = def.Summary.DefaultMode
	}
	if mode != "open" && mode != "guided" {
		return WorldSummary{}, ErrInvalidRequest
	}
	name = cleanText(name)
	if name == "" {
		name = "暮灯镇 · 新存档"
	}
	playerName = cleanText(playerName)
	if playerName == "" {
		playerName = "旅人"
	}
	playerProfile = cleanText(playerProfile)
	if playerProfile == "" {
		playerProfile = "一个正在寻找答案的旅人。"
	}
	worldID := newID("world")
	path := a.worldPath(worldID)
	store, err := openWorldDB(path)
	if err != nil {
		return WorldSummary{}, err
	}
	if err := initializeWorld(ctx, store, a.userID, worldID, def, mode, playerName, playerProfile); err != nil {
		store.db.Close()
		_ = os.Remove(path)
		return WorldSummary{}, err
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('name',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, name); err != nil {
		store.db.Close()
		return WorldSummary{}, err
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('updated_at',?)`, nowText()); err != nil {
		store.db.Close()
		return WorldSummary{}, err
	}
	store.db.Close()
	now := nowText()
	if _, err := a.appDB.ExecContext(ctx, `INSERT INTO worlds(user_id,game_id,world_id,name,path,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, a.userID, GameID, worldID, name, path, "ready", now, now); err != nil {
		return WorldSummary{}, err
	}
	if activate {
		if err := a.activate(ctx, worldID, 0); err != nil {
			return WorldSummary{}, err
		}
	}
	return a.worldSummary(ctx, worldID)
}

func (a *App) ListWorlds(ctx context.Context) ([]WorldSummary, error) {
	rows, err := a.appDB.QueryContext(ctx, `SELECT world_id,name,status,updated_at FROM worlds WHERE user_id=? AND game_id=? ORDER BY updated_at DESC`, a.userID, GameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WorldSummary
	for rows.Next() {
		var id, name, status, updated string
		if err := rows.Scan(&id, &name, &status, &updated); err != nil {
			return nil, err
		}
		summary, err := a.worldSummary(ctx, id)
		if err != nil {
			return nil, err
		}
		summary.Name = name
		summary.Status = status
		summary.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, summary)
	}
	return result, rows.Err()
}

func (a *App) worldSummary(ctx context.Context, worldID string) (WorldSummary, error) {
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return WorldSummary{}, err
	}
	if status != "ready" {
		return WorldSummary{}, ErrWorldNotReady
	}
	store, err := openWorldDB(path)
	if err != nil {
		return WorldSummary{}, err
	}
	defer store.db.Close()
	snapshot, err := loadWorldSnapshot(ctx, store, 1)
	if err != nil {
		return WorldSummary{}, err
	}
	snapshot.Summary.Status = status
	return snapshot.Summary, nil
}

func (a *App) ReadWorld(ctx context.Context, worldID string, limit int) (worldSnapshot, error) {
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return worldSnapshot{}, err
	}
	if status != "ready" {
		return worldSnapshot{}, ErrWorldNotReady
	}
	store, err := openWorldDB(path)
	if err != nil {
		return worldSnapshot{}, err
	}
	defer store.db.Close()
	return loadWorldSnapshot(ctx, store, limit)
}

func (a *App) ReadMessages(ctx context.Context, worldID string, limit int) ([]Message, error) {
	snap, err := a.ReadWorld(ctx, worldID, limit)
	return snap.Messages, err
}
func (a *App) ReadCharacters(ctx context.Context, worldID string) ([]Character, error) {
	snap, err := a.ReadWorld(ctx, worldID, 1)
	return snap.Characters, err
}
func (a *App) ReadMemories(ctx context.Context, worldID, recipient string) ([]Memory, error) {
	snap, err := a.ReadWorld(ctx, worldID, 1)
	return snap.Memories[recipient], err
}
func (a *App) ReadPerceptions(ctx context.Context, worldID, recipient string) ([]Perception, error) {
	snap, err := a.ReadWorld(ctx, worldID, 1)
	return snap.Perceptions[recipient], err
}

func (a *App) activeWorldState(ctx context.Context) (string, int64, error) {
	var id string
	var revision int64
	err := a.appDB.QueryRowContext(ctx, `SELECT active_world_id,active_revision FROM user_play_state WHERE user_id=?`, a.userID).Scan(&id, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return id, revision, err
}

func (a *App) activeWorldStateExact(ctx context.Context) (string, int64, error) {
	var id string
	var rev int64
	err := a.appDB.QueryRowContext(ctx, `SELECT active_world_id,active_revision FROM user_play_state WHERE user_id=?`, a.userID).Scan(&id, &rev)
	return id, rev, err
}

func (a *App) activeWorld(ctx context.Context) (string, int64, error) {
	return a.activeWorldStateExact(ctx)
}

func (a *App) worldRecord(ctx context.Context, worldID string) (string, string, error) {
	var path, status string
	err := a.appDB.QueryRowContext(ctx, `SELECT path,status FROM worlds WHERE user_id=? AND game_id=? AND world_id=?`, a.userID, GameID, worldID).Scan(&path, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrWorldNotFound
	}
	return path, status, err
}

func (a *App) worldPath(worldID string) string {
	return filepath.Join(a.root, "worlds", a.userID, GameID, worldID, "world.db")
}

func (a *App) markInterrupted(ctx context.Context) error {
	rows, err := a.appDB.QueryContext(ctx, `SELECT path FROM worlds WHERE user_id=? AND game_id=? AND status='ready'`, a.userID, GameID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		store, err := openWorldDB(path)
		if err != nil {
			return err
		}
		if err := markRunInterrupted(ctx, store.db); err != nil {
			store.db.Close()
			return err
		}
		store.db.Close()
	}
	return nil
}

func (a *App) worldRuntimeFor(id string) *worldRuntime {
	a.worldMu.Lock()
	defer a.worldMu.Unlock()
	if w := a.worlds[id]; w != nil {
		return w
	}
	w := &worldRuntime{worldID: id}
	a.worlds[id] = w
	return w
}

func (a *App) isActive(ctx context.Context, worldID string, revision int64) bool {
	id, rev, err := a.activeWorldState(ctx)
	return err == nil && id == worldID && rev == revision
}

func (a *App) hashRun(req RunRequest) string {
	data, _ := json.Marshal(struct{ Input, Addressee string }{req.Input, req.AddresseeID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func newID(prefix string) string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}

// ConfigureModel verifies a real provider before publishing its configuration.
// Credentials are kept in the runtime secrets directory and never returned to the client.
func (a *App) ConfigureModel(ctx context.Context, request ModelConfigRequest) (Status, error) {
	provider := strings.TrimSpace(request.Provider)
	if provider != "openai" && provider != "deepseek" {
		return Status{}, ErrInvalidRequest
	}
	if strings.TrimSpace(request.APIKey) == "" {
		return Status{}, ErrInvalidRequest
	}
	configDir := filepath.Dir(a.modelPath)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return Status{}, err
	}
	secretPath := filepath.Join(a.root, "secrets", "model.key")
	if err := secret.Write(secretPath, request.APIKey); err != nil {
		return Status{}, err
	}
	tmpKey := filepath.Join(a.root, "secrets", "model.pending.key")
	_ = secret.Write(tmpKey, request.APIKey)
	tmpConfig := filepath.Join(configDir, "model.pending.json")
	window := llm.DefaultWindowLimits(provider, request.Model)
	config := llm.Config{Provider: provider, Model: strings.TrimSpace(request.Model), BaseURL: strings.TrimRight(strings.TrimSpace(request.BaseURL), "/"), APIKey: "file:../secrets/model.pending.key", WindowLimits: window}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(tmpConfig, data, 0o600); err != nil {
		return Status{}, err
	}
	providerClient, _, err := llm.NewProviderFromConfigFile(tmpConfig)
	if err != nil {
		_ = os.Remove(tmpConfig)
		_ = os.Remove(tmpKey)
		return Status{}, fmt.Errorf("model_not_configured: %w", err)
	}
	generator, ok := providerClient.(model.TextGenerator)
	if !ok {
		_ = os.Remove(tmpConfig)
		_ = os.Remove(tmpKey)
		return Status{}, errors.New("text_generation_unsupported")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := generator.GenerateText(probeCtx, model.TextRequest{System: "Reply with one short word.", Input: "ready", MaxInputTokens: 1024, MaxOutputTokens: 64}); err != nil {
		_ = os.Remove(tmpConfig)
		_ = os.Remove(tmpKey)
		return Status{}, fmt.Errorf("model_not_configured: provider verification failed")
	}
	if err := secret.Write(secretPath, request.APIKey); err != nil {
		return Status{}, err
	}
	final := llm.Config{Provider: provider, Model: strings.TrimSpace(request.Model), BaseURL: strings.TrimRight(strings.TrimSpace(request.BaseURL), "/"), APIKey: "file:../secrets/model.key", WindowLimits: window}
	finalData, _ := json.Marshal(final)
	if err := os.WriteFile(a.modelPath, finalData, 0o600); err != nil {
		return Status{}, err
	}
	_ = os.Remove(tmpConfig)
	_ = os.Remove(tmpKey)
	a.modelMu.Lock()
	a.generator = generator
	a.modelInfo = ModelInfo{Provider: provider, Model: final.Model, Configured: true, Source: "config"}
	a.modelError = ""
	a.modelMu.Unlock()
	return a.Status(ctx)
}
