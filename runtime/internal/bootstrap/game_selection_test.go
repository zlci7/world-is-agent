package bootstrap_test

import (
	"os"
	"path/filepath"
	"testing"

	"gameagent/runtime/internal/bootstrap"
)

func TestSelectionAppliesImmediatelyAndRestartRetainsGame(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "model.json", validModelConfig)
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Snapshot(); got.ReasonCode != "game_not_selected" || got.LoadedGame != nil {
		t.Fatalf("fresh: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "games")); !os.IsNotExist(err) {
		t.Fatalf("startup seeded assets: %v", err)
	}
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(); !got.Ready || got.LoadedGame.ID != "rimworld" || r.AgentConfig().Task.Enabled {
		t.Fatalf("first selection: %+v", got)
	}
	originalHistory := r.HistoryStore()
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(); !got.Ready || got.RestartRequired || got.LoadedGame.ID != "stardew-valley" || got.ConfiguredGame.ID != "stardew-valley" {
		t.Fatalf("next selection: %+v", got)
	}
	if r.HistoryStore() == originalHistory || !r.AgentConfig().Task.Enabled {
		t.Fatal("selection did not replace live components")
	}
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().RestartRequired {
		t.Fatal("selecting loaded game must clear restart")
	}
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got := next.Snapshot(); !got.Ready || got.LoadedGame.ID != "stardew-valley" || !next.AgentConfig().Task.Enabled || got.RestartRequired {
		t.Fatalf("restarted: %+v", got)
	}
}

func TestBrokenSelectionCanBeReplacedAndFailedSelectionPreservesReady(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "active-game.json", "{")
	writeConfig(t, root, "model.json", validModelConfig)
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Snapshot(); got.ReasonCode != "game_selection_invalid" || got.ConfiguredGame != nil {
		t.Fatalf("invalid: %+v", got)
	}
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config", "active-game.json")
	before, _ := os.ReadFile(path)
	for _, id := range []string{"../outside", "missing", ""} {
		if err := r.SelectGame(id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) || !r.Ready() {
		t.Fatal("failed selection altered live state")
	}
}

func TestGameMustBeValidBeforeModelSecretIsWritten(t *testing.T) {
	root := t.TempDir()
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "fake", APIKey: "must-not-write"}); err == nil {
		t.Fatal("model accepted before game")
	}
	if _, err := os.Stat(filepath.Join(root, "secrets", "model.key")); !os.IsNotExist(err) {
		t.Fatalf("credential written: %v", err)
	}
}

func TestFailedSelectionWritePreservesPublishedStateAndCanRetry(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "model.json", validModelConfig)
	r, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	path := filepath.Join(root, "config", "active-game.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.SelectGame("rimworld"); err == nil {
		t.Fatal("selection replaced a directory")
	}
	if got := r.Snapshot(); got.Ready || got.ConfiguredGame != nil || got.ReasonCode != "game_not_selected" {
		t.Fatalf("failed commit changed published state: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "games", "rimworld", "agent.json")); err != nil {
		t.Fatal("partial assets not reusable", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	if !r.Ready() {
		t.Fatal(r.Reason())
	}
}
