package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
)

func TestRetrievalConfigDefaultsAndRoundTrip(t *testing.T) {
	cfg := (agent.Config{}).WithDefaults()
	if !cfg.Retrieval.EnabledValue() || cfg.Retrieval.QueryMaxChars != 256 || cfg.Retrieval.QueryMaxTerms != 32 || cfg.Retrieval.ScanLimit != 512 || cfg.Retrieval.TimeoutMS != 500 || cfg.Retrieval.MaxReadBytes != 32<<20 || cfg.Retrieval.Limit != 5 || cfg.Retrieval.MaxTokens != 1024 || cfg.RetentionDays != 0 {
		t.Fatalf("retrieval defaults: %+v retention=%d", cfg.Retrieval, cfg.RetentionDays)
	}
	if cfg.HistoryIndex != memory.DefaultHistoryIndexLimits() || cfg.HistoryMaintenance != memory.DefaultHistoryMaintenanceLimits() {
		t.Fatalf("maintenance/index defaults: %+v %+v", cfg.HistoryIndex, cfg.HistoryMaintenance)
	}
	raw := `{"history_retrieval_enabled":false,"history_query_max_chars":100,"history_query_max_terms":8,"history_search_scan_limit":64,"history_search_timeout_ms":250,"history_search_max_read_bytes":1048576,"history_retrieval_limit":3,"max_retrieved_history_tokens":512,"retention_days":14,"history_index":{"max_source_text_bytes":4096,"max_source_fragments":32,"max_source_term_associations":128},"history_maintenance":{"max_sources":4,"max_bytes":2097152,"timeout_ms":200,"lock_timeout_ms":20}}`
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := agent.LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.RetrievalConfig{Enabled: boolPtr(false), QueryMaxChars: 100, QueryMaxTerms: 8, ScanLimit: 64, TimeoutMS: 250, MaxReadBytes: 1 << 20, Limit: 3, MaxTokens: 512}
	if !reflect.DeepEqual(loaded.Retrieval, want) || loaded.RetentionDays != 14 || loaded.HistoryIndex.TextBytes != 4096 || loaded.HistoryMaintenance.LockTimeoutMS != 20 {
		t.Fatalf("loaded config: %+v", loaded)
	}
	encoded, err := json.Marshal(loaded.Retrieval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := agent.LoadConfigFile(path)
	if err != nil || !reflect.DeepEqual(roundTrip.Retrieval, want) {
		t.Fatalf("round trip: %+v %v", roundTrip.Retrieval, err)
	}
}

func TestRetrievalConfigRejectsNegativeAndOverflowBounds(t *testing.T) {
	for _, field := range []string{"history_query_max_chars", "history_query_max_terms", "history_search_scan_limit", "history_search_timeout_ms", "history_search_max_read_bytes", "history_retrieval_limit", "max_retrieved_history_tokens", "retention_days"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(path, []byte(`{"`+field+`":-1}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := agent.LoadConfigFile(path); err == nil {
				t.Fatalf("accepted negative %s", field)
			}
		})
	}
	for _, raw := range []string{
		`{"history_index":{"max_source_text_bytes":-1}}`, `{"history_index":{"max_source_fragments":-1}}`, `{"history_index":{"max_source_term_associations":-1}}`,
		`{"history_maintenance":{"max_sources":-1}}`, `{"history_maintenance":{"max_bytes":-1}}`, `{"history_maintenance":{"timeout_ms":-1}}`, `{"history_maintenance":{"lock_timeout_ms":-1}}`,
		`{"history_maintenance":{"timeout_ms":10,"lock_timeout_ms":20}}`, `{"history_search_timeout_ms":9223372036854775807}`, `{"history_maintenance":{"timeout_ms":9223372036854775807}}`, `{"retention_days":9223372036854775807}`,
		`{"history_search_scan_limit":9223372036854775807}`, `{"history_index":{"max_source_term_associations":9223372036854775807}}`, `{"history_maintenance":{"max_sources":9223372036854775807}}`, `{"retention_days":106752}`,
	} {
		path := filepath.Join(t.TempDir(), "agent.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := agent.LoadConfigFile(path); err == nil {
			t.Fatalf("accepted invalid config: %s", raw)
		}
	}
	if err := (agent.Config{}).WithDefaults().Validate(); err != nil {
		t.Fatal(err)
	}
	maximumRetention := agent.DefaultConfig()
	maximumRetention.RetentionDays = 106751
	if err := maximumRetention.Validate(); err != nil {
		t.Fatalf("maximum safe retention rejected: %v", err)
	}
}
