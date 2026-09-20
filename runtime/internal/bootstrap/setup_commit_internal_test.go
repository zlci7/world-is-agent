package bootstrap

import (
	"fmt"
	"gameagent/runtime/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gameagent/runtime/internal/dataroot"
)

func TestConcurrentModelConfigurationsCommitCoherently(t *testing.T) {
	const contenders = 8

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
	if err := runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	results := make([]error, contenders)
	for i := 0; i < contenders; i++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			// The fake provider needs neither a key nor a window, so every one of
			// these submissions can reach the install step. The credential is what
			// tells them apart on disk.
			results[index] = runtime.ApplyModelConfiguration(ModelSetup{
				Provider: "fake",
				Model:    fmt.Sprintf("stub-model-%d", index),
				APIKey:   fmt.Sprintf("sk-stub-%d", index),
			})
		}(i)
	}
	start.Done()
	done.Wait()

	var winners []int
	for index, err := range results {
		if err == nil {
			winners = append(winners, index)
		}
	}
	if len(winners) != contenders {
		t.Fatalf("%d of %d concurrent commits succeeded, want every serialized commit: %v", len(winners), contenders, results)
	}

	// The invariant is not which submission won but that the two files agree:
	// the credential on disk is the one the configuration on disk was written
	// with. A submission that wrote after losing the race leaves a credential
	// belonging to a different model than the one that got installed.
	written, err := os.ReadFile(runtime.ModelConfigPath())
	if err != nil {
		t.Fatalf("read the model configuration: %v", err)
	}
	cfg, err := llm.LoadConfig(runtime.ModelConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(filepath.Dir(runtime.ModelConfigPath()), strings.TrimPrefix(cfg.APIKey, "file:")))
	if err != nil {
		t.Fatalf("read the stored credential: %v", err)
	}
	key := strings.TrimSpace(string(stored))

	matched := -1
	for index := 0; index < contenders; index++ {
		if key != fmt.Sprintf("sk-stub-%d", index) {
			continue
		}
		matched = index
		if !strings.Contains(string(written), fmt.Sprintf("stub-model-%d", index)) {
			t.Fatalf("the stored credential belongs to submission %d but the configuration on disk does not:\n%s", index, written)
		}
	}
	if matched < 0 {
		t.Fatalf("the stored credential %q belongs to no submission", key)
	}
}
