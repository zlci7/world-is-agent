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
	"google.golang.org/grpc"
)

// openTestRuntime gives the gateway the same shape it gets in production: a
// runtime whose configuration came from a data root.
func openTestRuntime(t *testing.T, agentConfig string) *bootstrap.Runtime {
	t.Helper()
	root := t.TempDir()
	layout := dataroot.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.ConfigPath("agent.json"), []byte(agentConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := bootstrap.Open(root, dataroot.Env{
		GOOS:    "linux",
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return "", os.ErrNotExist },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	return runtime
}

func TestTaskRuntimeConfigStartupAndShutdown(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "tasks.sqlite")
			core := openTestRuntime(t, fmt.Sprintf(`{"task":{"enabled":%t,"db_path":%q}}`, enabled, filepath.ToSlash(dbPath)))
			if got := core.AgentConfig().Task.DBPath; got != filepath.Clean(dbPath) {
				t.Fatalf("resolved task db path = %q, want %q", got, filepath.Clean(dbPath))
			}

			process, err := newGatewayRuntime(context.Background(), core)
			if err != nil {
				t.Fatal(err)
			}
			if enabled {
				if process.gateway.WorldRegistry() == nil {
					t.Fatal("enabled gateway missing Service registry")
				}
				if _, err := os.Stat(dbPath); err != nil {
					t.Fatal(err)
				}
				if store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: dbPath}); err == nil {
					store.Close()
					t.Fatal("process did not own SQLite store")
				}
			} else {
				if process.gateway.WorldRegistry() != nil {
					t.Fatal("disabled registry installed")
				}
				if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
					t.Fatalf("disabled process touched DB: %v", err)
				}
			}
			if err := process.shutdown(context.Background(), grpc.NewServer()); err != nil {
				t.Fatal(err)
			}
			if enabled {
				store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: dbPath})
				if err != nil {
					t.Fatalf("shutdown retained store ownership: %v", err)
				}
				store.Close()
			}
		})
	}
}
