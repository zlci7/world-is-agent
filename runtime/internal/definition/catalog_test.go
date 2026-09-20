package definition_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gameagent/runtime/internal/definition"
)

func TestNewCatalogFindsScopedDefinitionsWithTrimmedKeys(t *testing.T) {
	catalog, err := definition.NewCatalog(
		[]definition.GameDefinition{
			{
				SchemaVersion:        " v1alpha1 ",
				GameID:               " stardew-valley ",
				Title:                "Stardew Valley",
				Summary:              "A farming life sim.",
				WorldRules:           []string{"Each season has 28 days."},
				Lore:                 []string{"Pelican Town is a small rural town."},
				NarrativeConstraints: []string{"Stay in character."},
				SourceVersion:        "fixture",
			},
		},
		[]definition.AgentDefinition{
			{
				SchemaVersion:      "v1alpha1",
				GameID:             "stardew-valley",
				DefinitionID:       " npc:Abigail ",
				Identity:           "Abigail is a Stardew Valley villager.",
				Personality:        []string{"adventurous"},
				SpeechStyle:        []string{"direct"},
				Preferences:        []string{"amethyst"},
				BehaviorGuidelines: []string{"Respond briefly."},
				SourceVersion:      "fixture",
			},
		},
	)
	if err != nil {
		t.Fatalf("NewCatalog returned error: %v", err)
	}

	game, ok := catalog.FindGame(" stardew-valley ")
	if !ok {
		t.Fatal("FindGame did not find stardew-valley")
	}
	if game.GameID != "stardew-valley" {
		t.Fatalf("GameID = %q, want stardew-valley", game.GameID)
	}
	if len(game.WorldRules) != 1 || game.WorldRules[0] != "Each season has 28 days." {
		t.Fatalf("WorldRules = %+v", game.WorldRules)
	}

	agent, ok := catalog.FindAgent("stardew-valley", " npc:Abigail ")
	if !ok {
		t.Fatal("FindAgent did not find npc:Abigail")
	}
	if agent.DefinitionID != "npc:Abigail" {
		t.Fatalf("DefinitionID = %q, want npc:Abigail", agent.DefinitionID)
	}
	if len(agent.BehaviorGuidelines) != 1 || agent.BehaviorGuidelines[0] != "Respond briefly." {
		t.Fatalf("BehaviorGuidelines = %+v", agent.BehaviorGuidelines)
	}
	if _, ok := catalog.FindGame("Stardew-Valley"); ok {
		t.Fatal("FindGame should compare game_id case-sensitively")
	}
	if _, ok := catalog.FindAgent("stardew-valley", "NPC:Abigail"); ok {
		t.Fatal("FindAgent should compare definition_id case-sensitively")
	}
}

func TestNewCatalogRejectsDuplicateGameID(t *testing.T) {
	_, err := definition.NewCatalog(
		[]definition.GameDefinition{
			{SchemaVersion: "v1alpha1", GameID: "stardew-valley"},
			{SchemaVersion: "v1alpha1", GameID: " stardew-valley "},
		},
		nil,
	)
	if err == nil {
		t.Fatal("NewCatalog returned nil error, want duplicate game_id error")
	}
	if !strings.Contains(err.Error(), "duplicate game_id") {
		t.Fatalf("error = %v, want duplicate game_id", err)
	}
}

func TestNewCatalogRejectsDuplicateAgentDefinitionWithinGame(t *testing.T) {
	_, err := definition.NewCatalog(
		nil,
		[]definition.AgentDefinition{
			{SchemaVersion: "v1alpha1", GameID: "game-a", DefinitionID: "npc:Abigail"},
			{SchemaVersion: "v1alpha1", GameID: "game-a", DefinitionID: " npc:Abigail "},
		},
	)
	if err == nil {
		t.Fatal("NewCatalog returned nil error, want duplicate agent definition error")
	}
	if !strings.Contains(err.Error(), "duplicate agent definition") {
		t.Fatalf("error = %v, want duplicate agent definition", err)
	}
}

func TestNewCatalogAllowsSameDefinitionIDAcrossGames(t *testing.T) {
	catalog, err := definition.NewCatalog(
		nil,
		[]definition.AgentDefinition{
			{SchemaVersion: "v1alpha1", GameID: "game-a", DefinitionID: "npc:Guide", Identity: "Guide A"},
			{SchemaVersion: "v1alpha1", GameID: "game-b", DefinitionID: "npc:Guide", Identity: "Guide B"},
		},
	)
	if err != nil {
		t.Fatalf("NewCatalog returned error: %v", err)
	}

	agentA, ok := catalog.FindAgent("game-a", "npc:Guide")
	if !ok {
		t.Fatal("FindAgent did not find game-a npc:Guide")
	}
	agentB, ok := catalog.FindAgent("game-b", "npc:Guide")
	if !ok {
		t.Fatal("FindAgent did not find game-b npc:Guide")
	}
	if agentA.Identity != "Guide A" || agentB.Identity != "Guide B" {
		t.Fatalf("agents crossed game scope: game-a=%q game-b=%q", agentA.Identity, agentB.Identity)
	}
}

func TestNewCatalogRejectsMissingRequiredIdentityFields(t *testing.T) {
	tests := []struct {
		name   string
		games  []definition.GameDefinition
		agents []definition.AgentDefinition
		want   string
	}{
		{
			name:  "game schema_version",
			games: []definition.GameDefinition{{GameID: "game-a"}},
			want:  "schema_version",
		},
		{
			name:  "game game_id",
			games: []definition.GameDefinition{{SchemaVersion: "v1alpha1"}},
			want:  "game_id",
		},
		{
			name:   "agent schema_version",
			agents: []definition.AgentDefinition{{GameID: "game-a", DefinitionID: "npc:Abigail"}},
			want:   "schema_version",
		},
		{
			name:   "agent game_id",
			agents: []definition.AgentDefinition{{SchemaVersion: "v1alpha1", DefinitionID: "npc:Abigail"}},
			want:   "game_id",
		},
		{
			name:   "agent definition_id",
			agents: []definition.AgentDefinition{{SchemaVersion: "v1alpha1", GameID: "game-a"}},
			want:   "definition_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := definition.NewCatalog(tt.games, tt.agents)
			if err == nil {
				t.Fatal("NewCatalog returned nil error, want validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestNewCatalogRejectsUnsupportedSchemaVersion(t *testing.T) {
	_, err := definition.NewCatalog(
		[]definition.GameDefinition{{SchemaVersion: "v9", GameID: "game-a"}},
		nil,
	)
	if err == nil {
		t.Fatal("NewCatalog returned nil error, want unsupported schema error")
	}
	if !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("error = %v, want unsupported schema_version", err)
	}
}

func TestLoadCatalogFromDirLoadsStaticDefinitionFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, " stardew-valley", "definitions", "game.json"), `{
  "schema_version": "v1alpha1",
  "game_id": "stardew-valley",
  "title": "Stardew Valley",
  "summary": "A farming life sim.",
  "world_rules": ["Each season has 28 days."],
  "lore": ["Pelican Town is a small rural town."],
  "narrative_constraints": ["Stay grounded in Stardew Valley."],
  "source_version": "test"
}`)
	writeFile(t, filepath.Join(root, " stardew-valley", "definitions", "abigail.json"), `{
  "schema_version": "v1alpha1",
  "game_id": "stardew-valley",
  "definition_id": "npc:Abigail",
  "identity": "Abigail is a villager in Pelican Town.",
  "personality": ["adventurous"],
  "speech_style": ["brief"],
  "preferences": ["amethyst"],
  "behavior_guidelines": ["Stay in character."],
  "source_version": "test"
}`)

	catalog, err := definition.LoadCatalogFromDir(root)
	if err != nil {
		t.Fatalf("LoadCatalogFromDir returned error: %v", err)
	}
	if _, ok := catalog.FindGame("stardew-valley"); !ok {
		t.Fatal("FindGame did not find loaded game definition")
	}
	if _, ok := catalog.FindAgent("stardew-valley", "npc:Abigail"); !ok {
		t.Fatal("FindAgent did not find loaded agent definition")
	}
}

func TestLoadCatalogFromDirLoadsBundledStardewDefinitions(t *testing.T) {
	catalog, err := definition.LoadCatalogFromDir(filepath.Join("..", "..", "config", "games"))
	if err != nil {
		t.Fatalf("LoadCatalogFromDir returned error: %v", err)
	}

	game, ok := catalog.FindGame("stardew-valley")
	if !ok {
		t.Fatal("FindGame did not find bundled stardew-valley definition")
	}
	if game.GameID != "stardew-valley" {
		t.Fatalf("GameID = %q, want stardew-valley", game.GameID)
	}
	if game.Title == "" || game.Summary == "" {
		t.Fatalf("bundled Stardew game definition should include title and summary: %+v", game)
	}

	for _, definitionID := range []string{
		"npc:Abigail",
		"npc:Alex",
		"npc:Caroline",
		"npc:Clint",
		"npc:Demetrius",
		"npc:Dwarf",
		"npc:Elliott",
		"npc:Emily",
		"npc:Evelyn",
		"npc:George",
		"npc:Gus",
		"npc:Haley",
		"npc:Harvey",
		"npc:Jas",
		"npc:Jodi",
		"npc:Kent",
		"npc:Krobus",
		"npc:Leah",
		"npc:Lewis",
		"npc:Linus",
		"npc:Marnie",
		"npc:Maru",
		"npc:Pam",
		"npc:Penny",
		"npc:Pierre",
		"npc:Robin",
		"npc:Sam",
		"npc:Sandy",
		"npc:Sebastian",
		"npc:Shane",
		"npc:Vincent",
		"npc:Willy",
		"npc:Wizard",
		"archetype:town_villager",
	} {
		agent, ok := catalog.FindAgent("stardew-valley", definitionID)
		if !ok {
			t.Fatalf("FindAgent did not find bundled %s definition", definitionID)
		}
		if agent.GameID != "stardew-valley" {
			t.Fatalf("%s GameID = %q, want stardew-valley", definitionID, agent.GameID)
		}
		if agent.DefinitionID != definitionID {
			t.Fatalf("DefinitionID = %q, want %q", agent.DefinitionID, definitionID)
		}
		if agent.Identity == "" {
			t.Fatalf("%s identity is empty", definitionID)
		}
		if definitionID != "archetype:town_villager" {
			if len(agent.Personality) == 0 {
				t.Fatalf("%s personality is empty", definitionID)
			}
			if len(agent.SpeechStyle) == 0 {
				t.Fatalf("%s speech_style is empty", definitionID)
			}
			if len(agent.BehaviorGuidelines) == 0 {
				t.Fatalf("%s behavior_guidelines is empty", definitionID)
			}
			if agent.SourceVersion != "manual-valleytalk-inspired-draft" {
				t.Fatalf("%s SourceVersion = %q, want manual-valleytalk-inspired-draft", definitionID, agent.SourceVersion)
			}
		}
	}
}

func TestLoadCatalogFromDirRejectsMissingConfiguredRoot(t *testing.T) {
	_, err := definition.LoadCatalogFromDir(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("LoadCatalogFromDir returned nil error, want missing configured root error")
	}
	if !strings.Contains(err.Error(), "read definition root") {
		t.Fatalf("error = %v, want read definition root", err)
	}
}

func TestLoadCatalogFromDirRejectsMalformedJSON(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "stardew-valley", "definitions", "game.json"), `{`)

	_, err := definition.LoadCatalogFromDir(root)
	if err == nil {
		t.Fatal("LoadCatalogFromDir returned nil error, want malformed JSON error")
	}
	if !strings.Contains(err.Error(), "parse game definition") {
		t.Fatalf("error = %v, want parse game definition", err)
	}
}

func TestLoadCatalogFromDirRejectsPathScopeMismatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "stardew-valley", "definitions", "game.json"), `{
  "schema_version": "v1alpha1",
  "game_id": "other-game"
}`)

	_, err := definition.LoadCatalogFromDir(root)
	if err == nil {
		t.Fatal("LoadCatalogFromDir returned nil error, want scope mismatch error")
	}
	if !strings.Contains(err.Error(), "scope mismatch") {
		t.Fatalf("error = %v, want scope mismatch", err)
	}
}

func TestLoadCatalogFromDirRejectsInvalidAgentFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "malformed json",
			content: `{`,
			want:    "parse agent definition",
		},
		{
			name: "scope mismatch",
			content: `{
  "schema_version": "v1alpha1",
  "game_id": "other-game",
  "definition_id": "npc:Abigail"
}`,
			want: "scope mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "stardew-valley", "definitions", "game.json"), `{
  "schema_version": "v1alpha1",
  "game_id": "stardew-valley"
}`)
			writeFile(t, filepath.Join(root, "stardew-valley", "definitions", "abigail.json"), tt.content)

			_, err := definition.LoadCatalogFromDir(root)
			if err == nil {
				t.Fatal("LoadCatalogFromDir returned nil error, want agent file validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadCatalogFromDirIgnoresNestedAgentDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "game-a", "definitions", "game.json"), `{
  "schema_version": "v1alpha1",
  "game_id": "game-a"
}`)
	writeFile(t, filepath.Join(root, "game-a", "definitions", "agents", "nested.json"), `{
  "schema_version": "v1alpha1",
  "game_id": "game-a",
  "definition_id": "npc:Nested"
}`)

	catalog, err := definition.LoadCatalogFromDir(root)
	if err != nil {
		t.Fatalf("LoadCatalogFromDir returned error: %v", err)
	}
	if _, ok := catalog.FindAgent("game-a", "npc:Nested"); ok {
		t.Fatal("FindAgent found nested agent definition, want flat definitions only")
	}
}

func TestLoadGameCatalogFromDirRequiresOnlySelectedGame(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "selected", "definitions", "game.json"), `{"schema_version":"v1alpha1","game_id":"selected","title":"Selected"}`)
	writeFile(t, filepath.Join(root, "selected", "definitions", "agent.json"), `{"schema_version":"v1alpha1","game_id":"selected","definition_id":"agent"}`)
	writeFile(t, filepath.Join(root, "broken", "definitions", "game.json"), `{`)

	catalog, err := definition.LoadGameCatalogFromDir(root, "selected")
	if err != nil {
		t.Fatalf("LoadGameCatalogFromDir: %v", err)
	}
	if _, ok := catalog.FindGame("selected"); !ok {
		t.Fatal("selected game is missing")
	}
}

func TestLoadGameCatalogFromDirRequiresGameAndAgentDefinitions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "selected", "definitions", "game.json"), `{"schema_version":"v1alpha1","game_id":"selected"}`)
	if _, err := definition.LoadGameCatalogFromDir(root, "selected"); err == nil || !strings.Contains(err.Error(), "agent definition") {
		t.Fatalf("missing agents error = %v", err)
	}
	os.Remove(filepath.Join(root, "selected", "definitions", "game.json"))
	if _, err := definition.LoadGameCatalogFromDir(root, "selected"); err == nil || !strings.Contains(err.Error(), "game.json") {
		t.Fatalf("missing game error = %v", err)
	}
}

func TestLoadGameCatalogFromDirLoadsNestedDefinitions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "selected", "definitions", "game.json"), `{"schema_version":"v1alpha1","game_id":"selected"}`)
	writeFile(t, filepath.Join(root, "selected", "definitions", "characters", "nested.json"), `{"schema_version":"v1alpha1","game_id":"selected","definition_id":"nested"}`)
	catalog, err := definition.LoadGameCatalogFromDir(root, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.FindAgent("selected", "nested"); !ok {
		t.Fatal("nested definition was not loaded")
	}
}

func TestLoadGameCatalogFromDirRejectsInvalidNestedDefinition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "selected", "definitions", "game.json"), `{"schema_version":"v1alpha1","game_id":"selected"}`)
	writeFile(t, filepath.Join(root, "selected", "definitions", "nested", "invalid.json"), `{`)
	if _, err := definition.LoadGameCatalogFromDir(root, "selected"); err == nil || !strings.Contains(err.Error(), "invalid.json") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadGameCatalogFromDirRejectsDuplicateNestedDefinition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "selected", "definitions", "game.json"), `{"schema_version":"v1alpha1","game_id":"selected"}`)
	definitionJSON := `{"schema_version":"v1alpha1","game_id":"selected","definition_id":"duplicate"}`
	writeFile(t, filepath.Join(root, "selected", "definitions", "one.json"), definitionJSON)
	writeFile(t, filepath.Join(root, "selected", "definitions", "nested", "two.json"), definitionJSON)
	if _, err := definition.LoadGameCatalogFromDir(root, "selected"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v", err)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
