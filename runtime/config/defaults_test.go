package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testdata/v0.1.0 is copied byte-for-byte from runtime/config/games/stardew-valley/
// at tag v0.1.0, commit 5657635e68ef54a4b98aae98e2af8ca84004ef81.

func TestGamesDiscoversEmbeddedProfiles(t *testing.T) {
	games, err := Games()
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("Games count = %d, want 2", len(games))
	}
	for _, game := range games {
		if game.ID == "" || game.Title == "" {
			t.Fatalf("invalid game: %+v", game)
		}
	}
}

func TestAssetsStatusDistinguishesMissingAndInvalid(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	assertCode(t, AssetsStatus(configDir, "stardew-valley"), CodeProfileAssetsMissing)
	path := filepath.Join(configDir, "games", "stardew-valley", "agent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertCode(t, AssetsStatus(configDir, "stardew-valley"), CodeProfileInvalid)
}

func TestPrepareGameValidatesThenCommitsOnlyMissingAssets(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	customPath := filepath.Join(configDir, "games", "rimworld", "definitions", "game.json")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatal(err)
	}
	shipped, err := defaults.ReadFile("games/rimworld/definitions/game.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customPath, shipped, 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareGame(configDir, "rimworld")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if prepared.Game.ID != "rimworld" {
		t.Fatalf("game = %+v", prepared.Game)
	}
	if _, ok := prepared.Catalog.FindGame("rimworld"); !ok {
		t.Fatal("candidate catalog missing selected game")
	}
	before, _ := os.ReadFile(customPath)
	if err := prepared.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(customPath)
	if string(after) != string(before) {
		t.Fatal("commit replaced existing file")
	}
	if err := AssetsStatus(configDir, "rimworld"); err != nil {
		t.Fatalf("AssetsStatus: %v", err)
	}
}

func TestPrepareGameRejectsUnknownAndInvalidExistingAssets(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	_, err := PrepareGame(configDir, "../rimworld")
	assertCode(t, err, CodeInvalidGame)
	path := filepath.Join(configDir, "games", "rimworld", "agent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = PrepareGame(configDir, "rimworld")
	assertCode(t, err, CodeProfileInvalid)
	data, _ := os.ReadFile(path)
	if string(data) != "{" {
		t.Fatal("invalid existing asset was overwritten")
	}
}

func TestPrepareGameStagesRecognizedLegacyProfileForOriginalGame(t *testing.T) {
	configDir, legacy := legacyFixture(t)
	legacy = bytes.Replace(legacy, []byte(`"enabled": true`), []byte(`"enabled": false`), 1)
	legacy = bytes.Replace(legacy, []byte(`"turn_timeout_ms": 270000`), []byte(`"turn_timeout_ms": 123456`), 1)
	legacy = bytes.Replace(legacy, []byte(`自然、简短、符合 Stardew Valley NPC 的语气`), []byte(`自定义角色语气`), 1)
	if err := os.WriteFile(filepath.Join(configDir, "agent.json"), legacy, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareGame(configDir, "rimworld")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(configDir, "games", "stardew-valley", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(legacy) {
		t.Fatal("legacy bytes were not preserved")
	}
}

func TestLegacyRejectsExternalCatalogAndConflictingIdentity(t *testing.T) {
	for _, mutate := range []func(string){
		func(dir string) {
			profile, _ := os.ReadFile(filepath.Join(dir, "agent.json"))
			profile = bytes.Replace(profile, []byte(`config/games`), []byte(filepath.ToSlash(filepath.Join(t.TempDir(), "games"))), 1)
			_ = os.WriteFile(filepath.Join(dir, "agent.json"), profile, 0644)
		},
		func(dir string) {
			path := filepath.Join(dir, "games", "stardew-valley", "definitions", "game.json")
			data, _ := os.ReadFile(path)
			data = bytes.Replace(data, []byte(`"game_id": "stardew-valley"`), []byte(`"game_id": "rimworld"`), 1)
			_ = os.WriteFile(path, data, 0644)
		},
	} {
		dir, _ := legacyFixture(t)
		mutate(dir)
		_, err := PrepareGame(dir, "rimworld")
		assertCode(t, err, CodeMigrationConflict)
	}
}

func legacyFixture(t *testing.T) (string, []byte) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config")
	profile, err := os.ReadFile(filepath.Join("testdata", "v0.1.0", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	for source, target := range map[string]string{
		filepath.Join("testdata", "v0.1.0", "agent.json"):                                  filepath.Join(dir, "agent.json"),
		filepath.Join("testdata", "v0.1.0", "definitions", "game.json"):                    filepath.Join(dir, "games", "stardew-valley", "definitions", "game.json"),
		filepath.Join("testdata", "v0.1.0", "definitions", "archetype-town-villager.json"): filepath.Join(dir, "games", "stardew-valley", "definitions", "archetype-town-villager.json"),
	} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, profile
}

func TestPrepareGameRejectsUnrecognizedLegacyRoot(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "agent.json"), []byte(`{"definition_catalog_root":"elsewhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareGame(configDir, "rimworld")
	assertCode(t, err, CodeMigrationConflict)
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestLegacyRequiresOriginalGameIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"definition_catalog_root":"config/games"}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareGame(dir, "rimworld")
	assertCode(t, err, CodeMigrationConflict)
}

func TestMinimalGameIDDoesNotIdentifyALegacyRelease(t *testing.T) {
	dir, _ := legacyFixture(t)
	path := filepath.Join(dir, "games", "stardew-valley", "definitions", "game.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"v1alpha1","game_id":"stardew-valley"}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareGame(dir, "rimworld")
	assertCode(t, err, CodeMigrationConflict)
}

func TestLegacyRequiresReleasedArchetypeIdentity(t *testing.T) {
	dir, _ := legacyFixture(t)
	path := filepath.Join(dir, "games", "stardew-valley", "definitions", "archetype-town-villager.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareGame(dir, "rimworld")
	assertCode(t, err, CodeMigrationConflict)
}

func TestPrepareGameRejectsNonObjectProfiles(t *testing.T) {
	for _, content := range []string{"null", "[]"} {
		t.Run(content, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config")
			path := filepath.Join(dir, "games", "rimworld", "agent.json")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := PrepareGame(dir, "rimworld")
			assertCode(t, err, CodeProfileInvalid)
		})
	}
}

func TestDamagedActiveGameSkipsLegacyMigration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active-game.json"), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"definition_catalog_root":"external"}`), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareGame(dir, "rimworld")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
}

func TestLegacyExistingTargetRepairsMissingDefinitions(t *testing.T) {
	dir, _ := legacyFixture(t)
	path := filepath.Join(dir, "games", "stardew-valley", "definitions", "game.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	game, _ := defaults.ReadFile("games/stardew-valley/definitions/game.json")
	profile, _ := defaults.ReadFile("games/stardew-valley/agent.json")
	authoritative := bytes.Replace(profile, []byte(`"max_steps": 5`), []byte(`"max_steps": 2`), 1)
	for path, data := range map[string][]byte{path: game, filepath.Join(dir, "agent.json"): profile, filepath.Join(dir, "games", "stardew-valley", "agent.json"): profile} {
		if path == filepath.Join(dir, "games", "stardew-valley", "agent.json") {
			data = authoritative
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := PrepareGame(dir, "rimworld")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	if err := AssetsStatus(dir, "stardew-valley"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "games", "stardew-valley", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, authoritative) {
		t.Fatal("existing legacy target was not authoritative")
	}
}

func TestConcurrentAssetCreationIsPreservedAndRevalidated(t *testing.T) {
	dir := t.TempDir()
	p, err := PrepareGame(dir, "rimworld")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	path := filepath.Join(dir, "games", "rimworld", "agent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	assertCode(t, p.CommitAssets(), CodeProfileInvalid)
	data, _ := os.ReadFile(path)
	if string(data) != "{" {
		t.Fatal("concurrent writer overwritten")
	}
}
