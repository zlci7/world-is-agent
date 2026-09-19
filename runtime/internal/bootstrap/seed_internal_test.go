package bootstrap

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gameagent/runtime/internal/dataroot"
)

// A root whose shipped configuration could not be prepared must not reach ready,
// even once a valid model configuration exists. Otherwise the user is shown a
// ready Runtime that is running the fallback agent configuration: no definitions,
// a smaller step budget, and nothing later in the flow that would notice.
func TestAFailedSeedBlocksTheCoreEvenWithAValidModelConfiguration(t *testing.T) {
	original := seedDefaults
	seedDefaults = func(string, bool) (bool, error) {
		return false, errors.New("shipped configuration is unusable")
	}
	defer func() { seedDefaults = original }()

	root := t.TempDir()
	env := dataroot.Env{
		GOOS:    "linux",
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return root, nil },
	}
	runtime, err := Open(root, env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	// Blocking, not refusing to run: the client still has to be able to say what
	// went wrong.
	if runtime.State() != StateNeedsConfiguration {
		t.Fatalf("state = %q, want %q", runtime.State(), StateNeedsConfiguration)
	}
	if runtime.Ready() {
		t.Fatal("a root that could not be seeded reported ready")
	}
	if !strings.Contains(runtime.Reason(), "shipped configuration") {
		t.Fatalf("reason = %q, want it to name the initialization failure", runtime.Reason())
	}

	// A perfectly good model configuration must not clear it.
	if err := runtime.ApplyModelConfiguration(ModelSetup{Provider: "deepseek", APIKey: "sk-valid"}); err == nil {
		t.Fatal("a valid model configuration configured a core on a root with no shipped configuration")
	}
	if runtime.Ready() {
		t.Fatal("the core became ready after a failed seed")
	}
	if _, err := filepath.Abs(root); err != nil {
		t.Fatalf("root: %v", err)
	}
}
