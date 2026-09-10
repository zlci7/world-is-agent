package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryConfigDefaultsAndExplicitOverrides(t *testing.T) {
	cfg := Config{}.WithDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.History.MaxBatchBytes != 8<<20 || cfg.History.ScanRecords != 512 || cfg.History.SummarySources != 16384 ||
		cfg.Compaction.KeepRecentTokens != 20000 || cfg.Compaction.MaxSummaryTokens != 2048 ||
		cfg.Compaction.MaxInputTokens != 32768 || cfg.Compaction.MaxResponseBytes != 1<<20 ||
		cfg.Compaction.TimeoutMS != 10000 || cfg.Compaction.RetryCooldownTurns != 3 || !cfg.Compaction.EnabledValue() {
		t.Fatalf("unexpected defaults: history=%+v compaction=%+v", cfg.History, cfg.Compaction)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"history":{"max_batch_bytes":1024,"page_records":2},"compaction_enabled":false,"keep_recent_tokens":1234,"compaction_timeout_ms":500}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Compaction.EnabledValue() || loaded.Compaction.KeepRecentTokens != 1234 || loaded.Compaction.TimeoutMS != 500 || loaded.History.MaxBatchBytes != 1024 || loaded.History.PageRecords != 2 {
		t.Fatalf("overrides lost: %+v", loaded)
	}
}

func TestHistoryConfigRejectsInvalidLimits(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.History.MaxBatchBytes = -1 },
		func(c *Config) { c.History.PageRecords = c.History.ScanRecords + 1 },
		func(c *Config) { c.Compaction.KeepRecentTokens = -1 },
		func(c *Config) { c.Compaction.MaxSummaryTokens = -1 },
		func(c *Config) { c.Compaction.MaxInputTokens = -1 },
		func(c *Config) { c.Compaction.MaxResponseBytes = -1 },
		func(c *Config) { c.Compaction.TimeoutMS = -1 },
		func(c *Config) { c.Compaction.RetryCooldownTurns = -1 },
		func(c *Config) { c.Compaction.MaxInputTokens = c.Compaction.MaxSummaryTokens },
	} {
		cfg := DefaultConfig()
		mutate(&cfg)
		if err := cfg.WithDefaults().Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}
