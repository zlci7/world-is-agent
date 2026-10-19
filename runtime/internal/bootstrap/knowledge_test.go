package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/session"
)

func TestKnowledgeLifetimeAcrossSwitchAndRestart(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	first := r.bundle.knowledge
	if first == nil {
		t.Fatal("knowledge not composed")
	}
	scope := first.Scope(session.AgentSessionKey{GameID: "stardew-valley", WorldID: "save", EntityID: "npc:Robin"}, "npc:Robin", "罗宾")
	before, err := first.Search(context.Background(), scope, "鸡舍")
	if err != nil || len(before.Entries) == 0 {
		t.Fatalf("initial search: %+v %v", before, err)
	}
	if err = r.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	if r.bundle.knowledge != nil {
		t.Fatal("knowledge leaked into another game")
	}
	if _, err = first.Search(context.Background(), scope, "鸡舍"); err == nil {
		t.Fatal("retired connection remained open")
	}
	if err = r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if r.bundle.knowledge == first {
		t.Fatal("reused retired service")
	}
	root := r.layout.Root()
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(root, dataroot.Env{GOOS: "linux", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if !next.Ready() || next.bundle.knowledge == nil {
		t.Fatalf("restart failed: %+v", next.Snapshot())
	}
	after, err := next.bundle.knowledge.Search(context.Background(), scope, "鸡舍")
	if err != nil || after.ContentHash != before.ContentHash || len(after.Entries) == 0 {
		t.Fatalf("rebuilt snapshot: %+v %v", after, err)
	}
}

func TestKnowledgeRollbackRestoresCapturedContent(t *testing.T) {
	r := openPublicationRuntime(t)
	if err := r.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	old := r.bundle
	scope := old.knowledge.Scope(session.AgentSessionKey{GameID: "stardew-valley", WorldID: "save", EntityID: "npc:Robin"}, "npc:Robin", "罗宾")
	path := filepath.Join(r.layout.ConfigDir(), "games", "stardew-valley", "knowledge", "catalog.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, original, 0644) })
	// The candidate is complete before publication fails. Restoration must use
	// the retired instance's validated content, even when source files changed.
	r.testBeforePublish = func() error {
		if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
			return err
		}
		return errors.New("publication failed")
	}
	p, _, err := llm.NewProviderFromConfigFile(r.modelPath)
	if err != nil {
		t.Fatal(err)
	}
	err = r.replaceLocked(old.config, old.catalog, p, r.Snapshot(), func() error { return nil })
	if err == nil || !r.Ready() || r.bundle.knowledge == old.knowledge {
		t.Fatalf("rollback: %v %+v", err, r.Snapshot())
	}
	found, err := r.bundle.knowledge.Search(context.Background(), scope, "鸡舍")
	if err != nil || len(found.Entries) == 0 {
		t.Fatalf("restored knowledge: %+v %v", found, err)
	}
}

func TestEnabledMissingKnowledgeFailsBundlePreparation(t *testing.T) {
	r := openPublicationRuntime(t)
	cfg := agent.DefaultConfig()
	cfg.MemoryStore.Root = t.TempDir()
	cfg.Knowledge.Enabled = true
	cfg.DefinitionCatalogRoot = t.TempDir()
	p, _, err := llm.NewProviderFromConfigFile(r.modelPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.prepareBundle(cfg, definition.Catalog{}, p, "generic"); err == nil {
		t.Fatal("missing knowledge accepted")
	}
}
