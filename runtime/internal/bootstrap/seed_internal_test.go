package bootstrap

import (
	"gameagent/runtime/internal/dataroot"
	"os"
	"path/filepath"
	"testing"
)

func TestMissingProfileAssetsNeverUseDefaultConfig(t *testing.T) {
	root := t.TempDir()
	env := dataroot.Env{GOOS: "linux", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }}
	r, err := Open(root, env)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.WriteFile(filepath.Join(root, "config", "active-game.json"), []byte(`{"game_id":"rimworld"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Configure(); err == nil {
		t.Fatal("missing assets configured a core")
	}
	if got := r.Snapshot(); got.Ready || got.LoadedGame != nil || got.ReasonCode != "profile_assets_missing" {
		t.Fatalf("%+v", got)
	}
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	if !r.Ready() {
		t.Fatal(r.Reason())
	}
}
