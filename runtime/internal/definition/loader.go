package definition

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadCatalogFromDir(root string) (Catalog, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Catalog{}, fmt.Errorf("read definition root: %w", err)
	}

	var games []GameDefinition
	var agents []AgentDefinition
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		gameID := strings.TrimSpace(entry.Name())
		gameDir := filepath.Join(root, entry.Name())
		definitionsDir := filepath.Join(gameDir, "definitions")

		game, hasGame, err := loadGameDefinition(definitionsDir, gameID)
		if err != nil {
			return Catalog{}, err
		}
		if hasGame {
			games = append(games, game)
		}

		loadedAgents, err := loadAgentDefinitions(definitionsDir, gameID)
		if err != nil {
			return Catalog{}, err
		}
		agents = append(agents, loadedAgents...)
	}

	return NewCatalog(games, agents)
}

// LoadGameCatalogFromDir loads one required game beneath a multi-game catalog
// root. Other game directories are outside this selection and are not read.
func LoadGameCatalogFromDir(root, gameID string) (Catalog, error) {
	gameID = strings.TrimSpace(gameID)
	if gameID == "" || filepath.Base(gameID) != gameID || gameID == "." || gameID == ".." {
		return Catalog{}, fmt.Errorf("invalid game_id %q", gameID)
	}
	definitionsDir := filepath.Join(root, gameID, "definitions")
	game, found, err := loadGameDefinition(definitionsDir, gameID)
	if err != nil {
		return Catalog{}, err
	}
	if !found {
		return Catalog{}, fmt.Errorf("required game definition %s is missing", filepath.Join(definitionsDir, "game.json"))
	}
	agents, err := loadSelectedAgentDefinitions(definitionsDir, gameID)
	if err != nil {
		return Catalog{}, err
	}
	if len(agents) == 0 {
		return Catalog{}, fmt.Errorf("required agent definition is missing from %s", definitionsDir)
	}
	return NewCatalog([]GameDefinition{game}, agents)
}

func loadSelectedAgentDefinitions(definitionsDir, pathGameID string) ([]AgentDefinition, error) {
	var agents []AgentDefinition
	err := filepath.WalkDir(definitionsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == filepath.Join(definitionsDir, "game.json") || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read agent definition %s: %w", path, err)
		}
		var agent AgentDefinition
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("parse agent definition %s: %w", path, err)
		}
		if trimmed := strings.TrimSpace(agent.GameID); trimmed != "" && trimmed != pathGameID {
			return fmt.Errorf("scope mismatch for agent definition %s: path game_id %q file game_id %q", path, pathGameID, trimmed)
		}
		agents = append(agents, agent)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read agent definitions %s: %w", definitionsDir, err)
	}
	return agents, nil
}

func loadGameDefinition(definitionsDir string, pathGameID string) (GameDefinition, bool, error) {
	path := filepath.Join(definitionsDir, "game.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return GameDefinition{}, false, nil
		}
		return GameDefinition{}, false, fmt.Errorf("read game definition %s: %w", path, err)
	}

	var game GameDefinition
	if err := json.Unmarshal(data, &game); err != nil {
		return GameDefinition{}, false, fmt.Errorf("parse game definition %s: %w", path, err)
	}
	if trimmed := strings.TrimSpace(game.GameID); trimmed != "" && trimmed != pathGameID {
		return GameDefinition{}, false, fmt.Errorf("scope mismatch for game definition %s: path game_id %q file game_id %q", path, pathGameID, trimmed)
	}
	return game, true, nil
}

func loadAgentDefinitions(agentsDir string, pathGameID string) ([]AgentDefinition, error) {
	info, err := os.Stat(agentsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent definitions %s: %w", agentsDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("read agent definitions %s: not a directory", agentsDir)
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, fmt.Errorf("read agent definitions %s: %w", agentsDir, err)
	}

	agents := make([]AgentDefinition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "game.json" || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read agent definition %s: %w", path, err)
		}

		var agent AgentDefinition
		if err := json.Unmarshal(data, &agent); err != nil {
			return nil, fmt.Errorf("parse agent definition %s: %w", path, err)
		}
		if trimmed := strings.TrimSpace(agent.GameID); trimmed != "" && trimmed != pathGameID {
			return nil, fmt.Errorf("scope mismatch for agent definition %s: path game_id %q file game_id %q", path, pathGameID, trimmed)
		}
		agents = append(agents, agent)
	}
	return agents, nil
}
