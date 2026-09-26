package storyapp

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"gameagent/runtime/internal/model"
)

const (
	LocalUserID   = "local"
	GameID        = "lantern-dusk"
	SchemaVersion = 1
)

var (
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrWorldNotFound       = errors.New("world not found")
	ErrWorldNotReady       = errors.New("world not ready")
	ErrWorldBusy           = errors.New("world is busy")
	ErrAppBusy             = errors.New("story app is already open for this data root")
	ErrVersionConflict     = errors.New("version conflict")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrModelNotConfigured  = errors.New("model not configured")
	ErrGenerationFailed    = errors.New("generation failed")
	ErrStorageUnavailable  = errors.New("storage unavailable")
	ErrSaveFailed          = errors.New("save failed")
	ErrInvalidRequest      = errors.New("invalid request")
	ErrRunNotFound         = errors.New("run not found")
)

type Options struct {
	DataRoot        string
	ModelConfigPath string
	UserID          string
	Generator       model.TextGenerator
	AllowFake       bool
	Logger          Logger
}

type Logger interface {
	Printf(string, ...any)
}

type App struct {
	root            string
	userID          string
	appDB           *sql.DB
	dataRoot        string
	processLock     *processLock
	processLockOnce sync.Once
	modelPath       string
	modelMu         sync.RWMutex
	modelConfigMu   sync.Mutex
	generator       model.TextGenerator
	modelInfo       ModelInfo
	modelError      string
	worldMu         sync.Mutex
	worlds          map[string]*worldRuntime
	activationMu    sync.Mutex
	runsMu          sync.Mutex
	runs            map[string]*runRuntime
	logger          Logger
	closed          chan struct{}
}

type ModelInfo struct {
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
}

type ModelConfigRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key"`
}

type Status struct {
	Ready           bool          `json:"ready"`
	Model           ModelInfo     `json:"model"`
	ModelError      string        `json:"model_error,omitempty"`
	UserID          string        `json:"user_id"`
	ActiveWorld     *WorldSummary `json:"active_world"`
	ActiveRevision  int64         `json:"active_revision"`
	DataRoot        string        `json:"-"`
	ModelConfigPath string        `json:"-"`
}

type GameSummary struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Modes       []string `json:"modes"`
	DefaultMode string   `json:"default_mode"`
}

type WorldSummary struct {
	GameID       string    `json:"game_id"`
	WorldID      string    `json:"world_id"`
	Name         string    `json:"name"`
	Mode         string    `json:"mode"`
	TurnSeq      int64     `json:"turn_seq"`
	MessageHead  int64     `json:"message_head"`
	EventHead    int64     `json:"event_head"`
	ContextEpoch int64     `json:"context_epoch"`
	Clock        string    `json:"clock"`
	Scene        string    `json:"scene"`
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Character struct {
	EntityID     string `json:"entity_id"`
	DefinitionID string `json:"definition_id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Profile      string `json:"profile"`
	Knowledge    string `json:"knowledge"`
	InScene      bool   `json:"in_scene"`
}

// PublicCharacter is the player-facing character projection. Private role
// material stays inside the story runtime and is never sent through ordinary
// play routes.
type PublicCharacter struct {
	EntityID     string `json:"entity_id"`
	DefinitionID string `json:"definition_id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	InScene      bool   `json:"in_scene"`
}

func PublicCharacterViews(characters []Character) []PublicCharacter {
	views := make([]PublicCharacter, 0, len(characters))
	for _, character := range characters {
		views = append(views, PublicCharacter{
			EntityID: character.EntityID, DefinitionID: character.DefinitionID,
			Name: character.Name, Role: character.Role, InScene: character.InScene,
		})
	}
	return views
}

type Message struct {
	Seq       int64     `json:"seq"`
	MessageID string    `json:"message_id"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	RunID     string    `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Event struct {
	Seq          int64     `json:"seq"`
	EventID      string    `json:"event_id"`
	EventType    string    `json:"event_type"`
	ActorID      string    `json:"actor_id,omitempty"`
	TargetID     string    `json:"target_id,omitempty"`
	Content      string    `json:"content"`
	RunID        string    `json:"run_id"`
	Stage        int       `json:"stage"`
	SceneVersion int64     `json:"scene_version"`
	SourceType   string    `json:"source_type"`
	CreatedAt    time.Time `json:"created_at"`
}

type Perception struct {
	Seq           int64     `json:"seq"`
	RecipientID   string    `json:"recipient_id"`
	SourceEventID string    `json:"source_event_id"`
	SourceType    string    `json:"source_type"`
	Content       string    `json:"content"`
	Stage         int       `json:"stage"`
	SceneVersion  int64     `json:"scene_version"`
	CreatedAt     time.Time `json:"created_at"`
}

type Memory struct {
	Seq           int64     `json:"seq"`
	RecipientID   string    `json:"recipient_id"`
	Kind          string    `json:"kind"`
	Content       string    `json:"content"`
	SourceEventID string    `json:"source_event_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type Run struct {
	RunID            string    `json:"run_id"`
	RequestKey       string    `json:"request_key"`
	RequestHash      string    `json:"request_hash"`
	Input            string    `json:"input"`
	AddresseeID      string    `json:"addressee_id,omitempty"`
	Attempt          int       `json:"attempt"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason,omitempty"`
	Error            string    `json:"error,omitempty"`
	MessageSeq       int64     `json:"message_seq,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	InputID          string    `json:"-"`
	InputSeq         int64     `json:"-"`
	BaseTurnSeq      int64     `json:"-"`
	BaseMessageHead  int64     `json:"-"`
	BaseEventHead    int64     `json:"-"`
	BaseContextEpoch int64     `json:"-"`
	BaseSceneVersion int64     `json:"-"`
}

type SaveOperation struct {
	OperationID   string    `json:"operation_id"`
	RequestKey    string    `json:"request_key"`
	SourceWorldID string    `json:"source_world_id"`
	TargetWorldID string    `json:"target_world_id"`
	TargetName    string    `json:"target_name"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RunRequest struct {
	RequestKey             string `json:"request_key"`
	Input                  string `json:"input"`
	AddresseeID            string `json:"addressee_id,omitempty"`
	ExpectedActiveRevision int64  `json:"expected_active_revision"`
	ExpectedMessageHead    int64  `json:"expected_message_head"`
	ExpectedEventHead      int64  `json:"expected_event_head"`
	ExpectedContextEpoch   int64  `json:"expected_context_epoch"`
	attempt                int
	inputID                string
	inputSeq               int64
	requireBaseline        bool
	expectedTurnSeq        int64
	expectedSceneVersion   int64
}

type runRuntime struct {
	Cancel         context.CancelFunc
	Done           chan struct{}
	WorldID        string
	RunID          string
	ActiveRevision int64
	Generator      model.TextGenerator
}

type worldRuntime struct {
	mu               sync.Mutex
	worldID          string
	savePending      bool
	pendingOperation string
}
