// Package config owns the profiles and definition assets shipped with Runtime.
package config

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/definition"
)

const (
	gamesDir                 = "games"
	profileFile              = "agent.json"
	CodeInvalidGame          = "invalid_game"
	CodeProfileAssetsMissing = "profile_assets_missing"
	CodeProfileInvalid       = "profile_invalid"
	CodeMigrationConflict    = "migration_conflict"
	CodeStorageUnavailable   = "storage_unavailable"
)

//go:embed all:games legacy-profiles.json
var defaults embed.FS

type Game struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Error struct {
	Code string
	Path string
	Err  error
}

func (e *Error) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("%s at %s: %v", e.Code, e.Path, e.Err)
}
func (e *Error) Unwrap() error { return e.Err }

type stagedAsset struct {
	target string
	data   []byte
}
type PreparedGame struct {
	Game            Game
	AgentConfig     agent.Config
	Catalog         definition.Catalog
	AgentConfigPath string
	stageDir        string
	assets          []stagedAsset
	configDir       string
	gameIDs         map[string]bool
}

func Games() ([]Game, error) {
	matches, err := fs.Glob(defaults, "games/*/agent.json")
	if err != nil {
		return nil, err
	}
	games := make([]Game, 0, len(matches))
	for _, profile := range matches {
		id := strings.Split(profile, "/")[1]
		data, err := defaults.ReadFile("games/" + id + "/definitions/game.json")
		if err != nil {
			return nil, fmt.Errorf("read shipped game %s: %w", id, err)
		}
		var gameDef definition.GameDefinition
		if err := json.Unmarshal(data, &gameDef); err != nil {
			return nil, fmt.Errorf("parse shipped game %s: %w", id, err)
		}
		if strings.TrimSpace(gameDef.GameID) != id || strings.TrimSpace(gameDef.Title) == "" {
			return nil, fmt.Errorf("invalid shipped game %s", id)
		}
		games = append(games, Game{ID: id, Title: strings.TrimSpace(gameDef.Title)})
	}
	sort.Slice(games, func(i, j int) bool { return games[i].ID < games[j].ID })
	return games, nil
}

func findGame(gameID string) (Game, error) {
	if gameID == "" || filepath.Base(gameID) != gameID || gameID == "." || gameID == ".." {
		return Game{}, &Error{Code: CodeInvalidGame, Err: fmt.Errorf("unknown game_id %q", gameID)}
	}
	games, err := Games()
	if err != nil {
		return Game{}, &Error{Code: CodeProfileInvalid, Err: err}
	}
	for _, game := range games {
		if game.ID == gameID {
			return game, nil
		}
	}
	return Game{}, &Error{Code: CodeInvalidGame, Err: fmt.Errorf("unknown game_id %q", gameID)}
}

func AssetsStatus(configDir, gameID string) error {
	if _, err := findGame(gameID); err != nil {
		return err
	}
	for _, path := range []string{configDir, filepath.Join(configDir, "games"), filepath.Join(configDir, "games", gameID)} {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return &Error{Code: CodeStorageUnavailable, Path: path, Err: errors.New("expected a directory")}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return &Error{Code: CodeStorageUnavailable, Path: path, Err: err}
		}
	}
	profilePath := filepath.Join(configDir, "games", gameID, profileFile)
	if _, err := os.Stat(profilePath); err == nil {
		if err := validateAgentProfile(profilePath); err != nil {
			return &Error{Code: CodeProfileInvalid, Path: profilePath, Err: err}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return &Error{Code: CodeStorageUnavailable, Path: profilePath, Err: err}
	}
	for _, asset := range shippedAssets(gameID) {
		if _, err := os.Stat(filepath.Join(configDir, filepath.FromSlash(asset))); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return &Error{Code: CodeProfileAssetsMissing, Path: asset, Err: err}
			}
			return &Error{Code: CodeStorageUnavailable, Path: asset, Err: err}
		}
	}
	if _, err := definition.LoadGameCatalogFromDir(filepath.Join(configDir, "games"), gameID); err != nil {
		return &Error{Code: CodeProfileInvalid, Path: filepath.Join(configDir, "games", gameID, "definitions"), Err: err}
	}
	return nil
}

func PrepareGame(configDir, gameID string) (*PreparedGame, error) {
	game, err := findGame(gameID)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp("", "gameagent-profile-*")
	if err != nil {
		return nil, &Error{Code: CodeStorageUnavailable, Err: err}
	}
	p := &PreparedGame{Game: game, stageDir: stage, configDir: configDir, gameIDs: map[string]bool{}}
	fail := func(err error) (*PreparedGame, error) { _ = p.Close(); return nil, err }
	if err := p.stageGame(configDir, gameID, nil); err != nil {
		return fail(err)
	}
	if err := p.stageLegacy(configDir); err != nil {
		return fail(err)
	}
	p.AgentConfigPath = filepath.Join(stage, "games", gameID, profileFile)
	if err := validateAgentProfile(p.AgentConfigPath); err != nil {
		return fail(&Error{Code: CodeProfileInvalid, Path: p.AgentConfigPath, Err: err})
	}
	p.AgentConfig, err = agent.LoadConfigFile(p.AgentConfigPath)
	if err != nil {
		return fail(&Error{Code: CodeProfileInvalid, Path: p.AgentConfigPath, Err: err})
	}
	p.Catalog, err = definition.LoadGameCatalogFromDir(filepath.Join(stage, "games"), gameID)
	if err != nil {
		return fail(&Error{Code: CodeProfileInvalid, Path: filepath.Join(stage, "games", gameID, "definitions"), Err: err})
	}
	return p, nil
}

func (p *PreparedGame) stageGame(configDir, gameID string, profileOverride []byte) error {
	p.gameIDs[gameID] = true
	sourceRoot := filepath.Join(configDir, "games", gameID)
	if err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(configDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		stagePath := filepath.Join(p.stageDir, relative)
		if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(stagePath, data, FileMode)
	}); err != nil {
		return &Error{Code: CodeStorageUnavailable, Path: sourceRoot, Err: err}
	}
	for _, path := range shippedAssets(gameID) {
		data, err := defaults.ReadFile(path)
		if err != nil {
			return &Error{Code: CodeProfileInvalid, Path: path, Err: err}
		}
		target := filepath.Join(configDir, filepath.FromSlash(path))
		if existing, err := os.ReadFile(target); err == nil {
			data = existing
		} else if !errors.Is(err, fs.ErrNotExist) {
			return &Error{Code: CodeStorageUnavailable, Path: target, Err: err}
		} else {
			p.assets = append(p.assets, stagedAsset{target: target, data: append([]byte(nil), data...)})
		}
		if profileOverride != nil && strings.HasSuffix(filepath.ToSlash(path), "/agent.json") {
			data = profileOverride
			p.replaceAsset(target, data)
		}
		stagePath := filepath.Join(p.stageDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(stagePath), 0755); err != nil {
			return &Error{Code: CodeStorageUnavailable, Path: stagePath, Err: err}
		}
		if err := os.WriteFile(stagePath, data, FileMode); err != nil {
			return &Error{Code: CodeStorageUnavailable, Path: stagePath, Err: err}
		}
	}
	return nil
}

func (p *PreparedGame) replaceAsset(target string, data []byte) {
	for i := range p.assets {
		if p.assets[i].target == target {
			p.assets[i].data = append([]byte(nil), data...)
			return
		}
	}
	p.assets = append(p.assets, stagedAsset{target: target, data: append([]byte(nil), data...)})
}

func (p *PreparedGame) stageLegacy(configDir string) error {
	active := filepath.Join(configDir, "active-game.json")
	if _, err := os.Stat(active); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return &Error{Code: CodeStorageUnavailable, Path: active, Err: err}
	}
	rootProfile := filepath.Join(configDir, "agent.json")
	data, err := os.ReadFile(rootProfile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &Error{Code: CodeStorageUnavailable, Path: rootProfile, Err: err}
	}
	var raw struct {
		DefinitionCatalogRoot string `json:"definition_catalog_root"`
	}
	resolvedCatalog := raw.DefinitionCatalogRoot
	if json.Unmarshal(data, &raw) == nil {
		resolvedCatalog = raw.DefinitionCatalogRoot
	}
	if !filepath.IsAbs(resolvedCatalog) {
		resolvedCatalog = filepath.Join(filepath.Dir(configDir), resolvedCatalog)
	}
	if filepath.Clean(resolvedCatalog) != filepath.Join(configDir, "games") {
		return &Error{Code: CodeMigrationConflict, Path: rootProfile, Err: errors.New("legacy profile origin cannot be identified")}
	}
	if err := validateAgentProfile(rootProfile); err != nil {
		return &Error{Code: CodeMigrationConflict, Path: rootProfile, Err: err}
	}
	legacy, err := legacyProfile()
	if err != nil {
		return &Error{Code: CodeProfileInvalid, Err: err}
	}
	legacyID := legacy.GameID
	for name, expected := range legacy.IdentityFiles {
		path := filepath.Join(configDir, "games", legacyID, "definitions", name)
		data, err := os.ReadFile(path)
		var actual map[string]json.RawMessage
		if err != nil || json.Unmarshal(data, &actual) != nil || actual == nil {
			return &Error{Code: CodeMigrationConflict, Path: path, Err: errors.New("legacy release identity is missing or invalid")}
		}
		for field, want := range expected {
			var got string
			if json.Unmarshal(actual[field], &got) != nil || got != want {
				return &Error{Code: CodeMigrationConflict, Path: path, Err: fmt.Errorf("legacy release identity field %s does not match", field)}
			}
		}
	}
	target := filepath.Join(configDir, "games", legacyID, profileFile)
	if _, err := os.Stat(target); err == nil {
		if err := validateAgentProfile(target); err != nil {
			return &Error{Code: CodeProfileInvalid, Path: target, Err: err}
		}
		data = nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return &Error{Code: CodeStorageUnavailable, Path: target, Err: err}
	}
	// Existing valid targets remain authoritative; staging fills missing release assets.
	if err := p.stageGame(configDir, legacyID, data); err != nil {
		return err
	}
	if err := validateAgentProfile(filepath.Join(p.stageDir, "games", legacyID, profileFile)); err != nil {
		return &Error{Code: CodeProfileInvalid, Path: rootProfile, Err: err}
	}
	if _, err := definition.LoadGameCatalogFromDir(filepath.Join(p.stageDir, "games"), legacyID); err != nil {
		return &Error{Code: CodeProfileInvalid, Path: filepath.Join(configDir, "games", legacyID), Err: err}
	}
	return nil
}

func validateAgentProfile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("agent profile must be a JSON object")
	}
	_, err = agent.LoadConfigFile(path)
	return err
}

type legacyProfileIdentity struct {
	GameID        string                       `json:"game_id"`
	IdentityFiles map[string]map[string]string `json:"identity_files"`
}

func legacyProfile() (legacyProfileIdentity, error) {
	data, err := defaults.ReadFile("legacy-profiles.json")
	if err != nil {
		return legacyProfileIdentity{}, err
	}
	var m map[string]legacyProfileIdentity
	if err := json.Unmarshal(data, &m); err != nil {
		return legacyProfileIdentity{}, err
	}
	identity := m["v0.1.0"]
	if len(identity.IdentityFiles) == 0 {
		return identity, errors.New("legacy identity conditions are missing")
	}
	_, err = findGame(identity.GameID)
	return identity, err
}

func (p *PreparedGame) CommitAssets() error {
	for _, asset := range p.assets {
		if err := os.MkdirAll(filepath.Dir(asset.target), 0755); err != nil {
			return &Error{Code: CodeStorageUnavailable, Path: asset.target, Err: err}
		}
		f, err := os.CreateTemp(filepath.Dir(asset.target), "."+filepath.Base(asset.target)+".*")
		if err != nil {
			return &Error{Code: CodeStorageUnavailable, Path: asset.target, Err: err}
		}
		temporary := f.Name()
		_, writeErr := f.Write(asset.data)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(temporary)
			return &Error{Code: CodeStorageUnavailable, Path: asset.target, Err: writeErr}
		}
		if closeErr != nil {
			_ = os.Remove(temporary)
			return &Error{Code: CodeStorageUnavailable, Path: asset.target, Err: closeErr}
		}
		if err := os.Chmod(temporary, FileMode); err != nil {
			_ = os.Remove(temporary)
			return &Error{Code: CodeStorageUnavailable, Path: asset.target, Err: err}
		}
		linkErr := os.Link(temporary, asset.target)
		_ = os.Remove(temporary)
		if errors.Is(linkErr, fs.ErrExist) {
			continue
		}
		if linkErr != nil {
			return &Error{Code: CodeStorageUnavailable, Path: asset.target, Err: linkErr}
		}
	}
	for gameID := range p.gameIDs {
		if err := AssetsStatus(p.configDir, gameID); err != nil {
			return err
		}
	}
	return nil
}
func (p *PreparedGame) Close() error {
	if p == nil || p.stageDir == "" {
		return nil
	}
	err := os.RemoveAll(p.stageDir)
	p.stageDir = ""
	return err
}

func shippedAssets(gameID string) []string {
	var paths []string
	_ = fs.WalkDir(defaults, "games/"+gameID, func(path string, e fs.DirEntry, err error) error {
		relative := strings.TrimPrefix(path, "games/"+gameID+"/")
		if err == nil && !e.IsDir() && (relative == profileFile || strings.HasPrefix(relative, "definitions/")) {
			paths = append(paths, path)
		}
		return err
	})
	sort.Strings(paths)
	return paths
}
