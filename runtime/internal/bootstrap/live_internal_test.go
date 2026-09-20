package bootstrap

import (
	"context"
	"errors"
	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type silentRuntimeStream struct {
	protocol.GameAgentGateway_ConnectServer
	ctx               context.Context
	entered, finished chan struct{}
}

func (s *silentRuntimeStream) Context() context.Context { return s.ctx }
func (s *silentRuntimeStream) Recv() (*protocol.AdapterMessage, error) {
	close(s.entered)
	<-s.ctx.Done()
	close(s.finished)
	return nil, s.ctx.Err()
}
func TestRuntimeRetiresStreamsBeforeHello(t *testing.T) {
	for _, closeRuntime := range []bool{false, true} {
		t.Run(map[bool]string{false: "switch", true: "close"}[closeRuntime], func(t *testing.T) {
			r := openPublicationRuntime(t)
			if err := r.SelectGame("rimworld"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &silentRuntimeStream{ctx: ctx, entered: make(chan struct{}), finished: make(chan struct{})}
			rpc := make(chan error, 1)
			go func() { defer cancel(); rpc <- r.Connect(stream) }()
			<-stream.entered
			done := make(chan error, 1)
			go func() {
				if closeRuntime {
					done <- r.Close()
				} else {
					done <- r.SelectGame("stardew-valley")
				}
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("silent Hello stream blocked retirement")
			}
			<-rpc
			select {
			case <-stream.finished:
			default:
				t.Fatal("retirement left Recv running")
			}
		})
	}
}
func TestFailedModelReplacementRetainsCredentialAndRestoresReady(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyModelConfiguration(ModelSetup{Provider: "fake", Model: "first", APIKey: "first-test-key"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.ModelConfigPath())
	cfg, _ := llm.LoadConfig(r.ModelConfigPath())
	firstPath := filepath.Join(filepath.Dir(r.ModelConfigPath()), strings.TrimPrefix(cfg.APIKey, "file:"))
	r.testBeforePublish = func() error { return errors.New("injected candidate failure") }
	if err := r.ApplyModelConfiguration(ModelSetup{Provider: "fake", Model: "second", APIKey: "second-test-key"}); err == nil {
		t.Fatal("failed preparation accepted")
	}
	after, _ := os.ReadFile(r.ModelConfigPath())
	key, _ := os.ReadFile(firstPath)
	if string(after) != string(before) || strings.TrimSpace(string(key)) != "first-test-key" || !r.Ready() || r.Snapshot().Model.Model != "first" {
		t.Fatal("failed model replacement changed committed configuration")
	}
	entries, _ := os.ReadDir(r.Layout().SecretsDir())
	if len(entries) != 1 {
		t.Fatal("failed credential remains")
	}
	r.testBeforePublish = nil
	if err := r.ApplyModelConfiguration(ModelSetup{Provider: "fake", Model: "third", APIKey: "third-test-key"}); err != nil {
		t.Fatal("rollback retained task database ownership", err)
	}
}
func TestConfigurationCommitFailureRestoresPreviousBundle(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	old := r.bundle
	previous := r.Snapshot()
	before, _ := os.ReadFile(r.layout.ConfigPath("active-game.json"))
	r.setupMu.Lock()
	err := r.replaceLocked(old.config, old.catalog, old.provider, previous, func() error { return errors.New("commit failed") })
	r.setupMu.Unlock()
	after, _ := os.ReadFile(r.layout.ConfigPath("active-game.json"))
	if err == nil || !r.Ready() || r.bundle == old || string(after) != string(before) {
		t.Fatal("failed commit did not restore Ready with unchanged disk")
	}
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentCloseAndConfigurationRetiresEveryGeneration(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if i%3 == 0 {
				_ = r.Close()
			} else if i%3 == 1 {
				_ = r.SelectGame("stardew-valley")
			} else {
				_ = r.ApplyModelConfiguration(ModelSetup{Provider: "fake", APIKey: "test-key"})
			}
		}(i)
	}
	group.Wait()
	if r.Ready() || r.State() != StateBlocked {
		t.Fatal("shutdown lost serialization")
	}
	if err := r.SelectGame("rimworld"); err == nil {
		t.Fatal("shutdown permitted replacement")
	}
}

func TestLivePublicationReportsReconfiguringAndRestoresOnFailure(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	r.testBeforePublish = func() error { close(reached); <-release; return errors.New("candidate failure") }
	done := make(chan error, 1)
	go func() { done <- r.SelectGame("stardew-valley") }()
	<-reached
	if got := r.Snapshot(); got.State != StateReconfiguring || got.Ready || r.HistoryStore() != nil {
		t.Error("replacement published before completion")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("failure hidden")
	}
	if got := r.Snapshot(); !got.Ready || got.LoadedGame.ID != "rimworld" || got.ConfiguredGame.ID != "rimworld" || got.RestartRequired {
		t.Fatal("previous game not restored")
	}
	r.testBeforePublish = nil
}
func TestUncertainLiveCandidateCleanupBlocksAndPreservesDisk(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.layout.ConfigPath("active-game.json"))
	r.testBeforePublish = func() error { return errors.New("candidate failure") }
	r.testCleanup = func(*runtimeBundle) error { return errors.New("cleanup uncertainty") }
	if err := r.SelectGame("stardew-valley"); err == nil {
		t.Fatal("uncertainty hidden")
	}
	after, _ := os.ReadFile(r.layout.ConfigPath("active-game.json"))
	if r.State() != StateBlocked || r.Ready() || string(before) != string(after) {
		t.Fatal("unsafe rollback published Ready or committed candidate")
	}
}
