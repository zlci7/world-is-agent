package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gameagent/runtime/internal/definition"
)

func TestSeedWritesTheShippedTreeIntoAFreshRoot(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")

	seeded, err := Seed(configDir, false)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if !seeded {
		t.Fatal("a fresh root must be seeded")
	}

	// The anchor is the shipped profile, not the development baseline: the
	// baseline loads no definitions.
	agentPath := filepath.Join(configDir, agentFile)
	written, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("read seeded %s: %v", agentFile, err)
	}
	expected, err := shippedProfile()
	if err != nil {
		t.Fatalf("shippedProfile: %v", err)
	}
	if string(written) != string(expected) {
		t.Fatalf("seeded %s is not the shipped profile", agentFile)
	}

	var profile struct {
		DefinitionCatalogRoot string `json:"definition_catalog_root"`
	}
	if err := json.Unmarshal(written, &profile); err != nil {
		t.Fatalf("parse seeded %s: %v", agentFile, err)
	}
	if profile.DefinitionCatalogRoot == "" {
		t.Fatal("the seeded profile must point at a definition catalog, or no definition is loaded")
	}

	// The seeded catalog root must resolve inside the seeded root, and load. The
	// game id comes from the shipped tree rather than a constant, so this test
	// stays free of game-specific identifiers.
	catalogRoot := filepath.Join(root, filepath.FromSlash(profile.DefinitionCatalogRoot))
	catalog, err := definition.LoadCatalogFromDir(catalogRoot)
	if err != nil {
		t.Fatalf("load seeded catalog at %s: %v", catalogRoot, err)
	}
	gameIDs, err := fs.Glob(defaults, gamesDir+"/*/"+definitionsDir+"/game.json")
	if err != nil || len(gameIDs) == 0 {
		t.Fatalf("shipped tree has no game definition: %v", err)
	}
	gameID := filepath.Base(filepath.Dir(filepath.Dir(gameIDs[0])))
	if _, ok := catalog.FindGame(gameID); !ok {
		t.Fatalf("the seeded catalog does not contain game %q", gameID)
	}
}

// A game directory's other files belong to the development tree. Only
// definitions have a reader in a data root, so only definitions are written.
func TestSeedWritesDefinitionsAndNothingElse(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")

	if _, err := Seed(configDir, false); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	expected := 0
	err := fs.WalkDir(defaults, gamesDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && containsDefinitions(path) {
			expected++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk shipped tree: %v", err)
	}

	written := 0
	err = filepath.WalkDir(configDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Base(path) == agentFile && filepath.Dir(path) == configDir {
			return nil
		}
		written++
		return nil
	})
	if err != nil {
		t.Fatalf("walk seeded tree: %v", err)
	}
	if written != expected {
		t.Fatalf("seeded %d files besides the anchor, want %d definitions", written, expected)
	}
}

func TestSeedDoesNothingWhenTheAnchorExists(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// A user's own configuration, including one this package would not ship.
	own := []byte(`{"max_steps": 9}`)
	if err := os.WriteFile(filepath.Join(configDir, agentFile), own, 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	seeded, err := Seed(configDir, false)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if seeded {
		t.Fatal("an existing anchor must stop seeding entirely")
	}

	after, err := os.ReadFile(filepath.Join(configDir, agentFile))
	if err != nil {
		t.Fatalf("read anchor: %v", err)
	}
	if string(after) != string(own) {
		t.Fatal("seeding replaced an existing configuration")
	}
	if _, err := os.Stat(filepath.Join(configDir, gamesDir)); !os.IsNotExist(err) {
		t.Fatal("seeding wrote definitions next to an existing configuration")
	}
}

func TestSeedIsSkippedEntirelyForAnExplicitConfiguration(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")

	seeded, err := Seed(configDir, true)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if seeded {
		t.Fatal("an explicit configuration must skip seeding")
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatal("a skipped seed created files")
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")

	if _, err := Seed(configDir, false); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	before := snapshot(t, configDir)

	seeded, err := Seed(configDir, false)
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if seeded {
		t.Fatal("the second seed must report that it wrote nothing")
	}
	if after := snapshot(t, configDir); !sameSnapshot(before, after) {
		t.Fatalf("the second seed changed the tree:\nbefore %v\nafter  %v", before, after)
	}
}

// The anchor is written last on purpose, so an interrupted seed is retried
// instead of leaving a root that looks configured but is not.
func TestSeedResumesWhenOnlyTheDefinitionsWereWritten(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	if err := seedDefinitions(configDir); err != nil {
		t.Fatalf("seedDefinitions: %v", err)
	}

	seeded, err := Seed(configDir, false)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if !seeded {
		t.Fatal("a root with definitions but no anchor is not configured yet")
	}
	if _, err := os.Stat(filepath.Join(configDir, agentFile)); err != nil {
		t.Fatalf("stat anchor after resume: %v", err)
	}
}

// A release that ships more than one game profile has no rule for choosing, and
// guessing would hand the user a configuration nobody validated.
func TestSeedRequiresExactlyOneShippedProfile(t *testing.T) {
	if _, err := shippedProfile(); err != nil {
		t.Fatalf("the shipped tree must contain exactly one profile: %v", err)
	}
}

func containsDefinitions(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/"+definitionsDir+"/")
}

func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		paths = append(paths, relative+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

func sameSnapshot(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
