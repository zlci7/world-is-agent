package bootstrap

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/task"
)

func TestTaskConsoleReadsRaceInstanceReplacementWithoutPublishingStaleData(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, dataroot.Env{GOOS: "windows", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = os.WriteFile(r.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	q := task.ConsoleQuery{InstanceID: r.Snapshot().InstanceID, GameID: "stardew-valley"}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				page, err := r.ReadTaskWorlds(context.Background(), q)
				if err == nil {
					if page.InstanceID != q.InstanceID || page.GameID != q.GameID {
						t.Error("query acquired another instance identity")
					}
				} else if !errors.Is(err, ErrConsoleRuntimeChanged) && !errors.Is(err, ErrConsoleRuntimeNotReady) {
					t.Errorf("query error: %v", err)
					return
				}
			}
		}()
	}
	if err = r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if _, err = r.ReadTaskWorlds(context.Background(), q); !errors.Is(err, ErrConsoleRuntimeChanged) {
		t.Fatalf("retired scope: %v", err)
	}
}
