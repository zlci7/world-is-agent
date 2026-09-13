package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/task"
	"google.golang.org/grpc"
)

func TestTaskRuntimeConfigStartupAndShutdown(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			cfg := agent.DefaultTaskConfig()
			cfg.Enabled = enabled
			cfg.DBPath = filepath.Join(t.TempDir(), "tasks.sqlite")
			process, err := newGatewayRuntime(context.Background(), nil, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if enabled {
				if process.gateway.WorldRegistry() == nil {
					t.Fatal("enabled gateway missing Service registry")
				}
				if _, err := os.Stat(cfg.DBPath); err != nil {
					t.Fatal(err)
				}
				if store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: cfg.DBPath}); err == nil {
					store.Close()
					t.Fatal("process did not own SQLite store")
				}
			} else {
				if process.gateway.WorldRegistry() != nil {
					t.Fatal("disabled registry installed")
				}
				if _, err := os.Stat(cfg.DBPath); !os.IsNotExist(err) {
					t.Fatalf("disabled process touched DB: %v", err)
				}
			}
			if err := process.shutdown(context.Background(), grpc.NewServer()); err != nil {
				t.Fatal(err)
			}
			if enabled {
				store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: cfg.DBPath})
				if err != nil {
					t.Fatalf("shutdown retained store ownership: %v", err)
				}
				store.Close()
			}
		})
	}
}
