package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/task"
)

func TestCoordinatorOwnsTaskStoreFromInitializationThroughShutdown(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			root := t.TempDir()
			env := dataroot.Env{GOOS: "linux", Getenv: func(name string) string {
				if name == "GAMEAGENT_AGENT_CONFIG" {
					return "config/custom.json"
				}
				return ""
			}, HomeDir: func() (string, error) { return root, nil }}
			layout := dataroot.New(root)
			if err := layout.Ensure(); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(root, "data", "tasks", "owned.sqlite")
			profile := fmt.Sprintf(`{"definition_catalog_root":"config/games","task":{"enabled":%t,"db_path":%q}}`, enabled, filepath.ToSlash(db))
			if err := os.WriteFile(layout.ConfigPath("custom.json"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			core, err := bootstrap.Open(root, env)
			if err != nil {
				t.Fatal(err)
			}
			defer core.Close()
			if _, err := os.Stat(db); !os.IsNotExist(err) {
				t.Fatalf("shell opened task DB: %v", err)
			}
			if err := os.WriteFile(core.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := core.SelectGame("rimworld"); err != nil {
				t.Fatal(err)
			}
			if !core.Ready() {
				t.Fatal(core.Reason())
			}
			if enabled {
				if store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: db}); err == nil {
					store.Close()
					t.Fatal("coordinator did not own task DB")
				}
			} else if _, err := os.Stat(db); !os.IsNotExist(err) {
				t.Fatalf("disabled task DB: %v", err)
			}
			if err := core.Close(); err != nil {
				t.Fatal(err)
			}
			if enabled {
				store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: db})
				if err != nil {
					t.Fatal(err)
				}
				store.Close()
			}
		})
	}
}
