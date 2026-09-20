package bootstrap

import (
	"errors"
	"gameagent/runtime/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gameagent/runtime/internal/dataroot"
)

func openPublicationRuntime(t *testing.T) *Runtime {
	t.Helper()
	root := t.TempDir()
	r, err := Open(root, dataroot.Env{GOOS: "linux", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if err := os.WriteFile(r.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCandidateRemainsUnpublishedAndReadersDoNotWaitForPreparation(t *testing.T) {
	r := openPublicationRuntime(t)
	reached, release := make(chan struct{}), make(chan struct{})
	r.testBeforePublish = func() error { close(reached); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- r.SelectGame("stardew-valley") }()
	<-reached
	read := make(chan Snapshot, 1)
	go func() { read <- r.Snapshot() }()
	select {
	case snapshot := <-read:
		if snapshot.Ready || snapshot.LoadedGame != nil || r.HistoryStore() != nil {
			t.Errorf("partially published: %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Error("status read waited for slow preparation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(); !got.Ready || got.LoadedGame.ID != "stardew-valley" || r.HistoryStore() == nil {
		t.Fatalf("publication: %+v", got)
	}
}

func TestFailedCandidateCleansTaskOwnershipAndCanRetry(t *testing.T) {
	r := openPublicationRuntime(t)
	r.testBeforePublish = func() error { return errors.New("injected preparation failure") }
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatalf("persisted selection must return status: %v", err)
	}
	if got := r.Snapshot(); got.Ready || got.LoadedGame != nil || got.State != StateNeedsConfiguration || got.ReasonCode != "initialization_failed" {
		t.Fatalf("failure: %+v", got)
	}
	r.testBeforePublish = nil
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if !r.Ready() {
		t.Fatal("cleaned candidate retained task store ownership: " + r.Reason())
	}
}

func TestUncertainCandidateCleanupBlocksFurtherSetup(t *testing.T) {
	r := openPublicationRuntime(t)
	r.testBeforePublish = func() error { return errors.New("injected failure") }
	r.testCleanup = func(*runtimeBundle) error { return errors.New("injected cleanup uncertainty") }
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(); got.State != StateBlocked || got.LoadedGame != nil {
		t.Fatalf("unsafe: %+v", got)
	}
	r.testBeforePublish, r.testCleanup = nil, nil
	if err := r.Configure(); err == nil {
		t.Fatal("unsafe failure retried")
	}
	if err := r.SelectGame("rimworld"); err == nil {
		t.Fatal("unsafe failure allowed selection")
	}
}

func TestConcurrentGameAndModelCommitsKeepLoadedProfileAndSavedSelectionCoherent(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, dataroot.Env{GOOS: "linux", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		if err := r.ApplyModelConfiguration(ModelSetup{Provider: "fake", APIKey: "local-test"}); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer group.Done()
		if err := r.SelectGame("stardew-valley"); err != nil {
			t.Error(err)
		}
	}()
	group.Wait()
	got := r.Snapshot()
	if !got.Ready || got.ConfiguredGame.ID != "stardew-valley" || got.RestartRequired != (got.LoadedGame.ID != got.ConfiguredGame.ID) {
		t.Fatalf("concurrent: %+v", got)
	}
	if r.AgentConfig().Task.Enabled != (got.LoadedGame.ID == "stardew-valley") {
		t.Fatal("loaded policy and identity differ")
	}
	cfg, err := llm.LoadConfig(r.ModelConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.ModelConfigPath()), strings.TrimPrefix(cfg.APIKey, "file:"))); err != nil {
		t.Fatal(err)
	}
}
