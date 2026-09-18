package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/agent"
)

func TestLoadConfigFileLoadsPromptConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.json")
	data := []byte(`{
  "turn_timeout_ms": 1000,
  "llm_timeout_ms": 2000,
  "observe_timeout_ms": 3000,
  "action_timeout_ms": 4000,
  "action_start_timeout_ms": 5000,
  "async_action_timeout_ms": 6000,
  "memory_enabled": true,
  "recent_memory_limit": 7,
  "max_recent_memory_tokens": 2048,
  "max_steps": 5,
  "max_tool_calls_per_step": 3,
  "max_tool_calls_per_turn": 9,
  "max_async_actions_per_turn": 2,
  "max_parallel_tool_calls": 2,
  "max_tool_result_output_tokens": 4096,
  "max_tool_result_output_depth": 3,
  "max_tool_result_output_fields": 16,
  "max_tool_result_output_array_items": 8,
  "memory_store": {
    "kind": "sqlite",
    "root": "runtime/.custom/memory",
    "busy_timeout_ms": 1234,
    "max_records_per_entity": 11,
    "max_projection_batches_per_entity": 44
  },
  "definition_catalog_root": "runtime/config/games",
  "prompt": {
    "language": "Simplified Chinese",
    "npc_style": "quiet mountain hermit",
    "max_speak_chars": 42,
    "tool_instruction": "Use exactly one available tool."
  }
}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := agent.LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.TurnTimeout != time.Second {
		t.Fatalf("expected turn timeout 1s, got %s", cfg.TurnTimeout)
	}
	if cfg.ActionStartTimeout != 5*time.Second {
		t.Fatalf("expected action start timeout 5s, got %s", cfg.ActionStartTimeout)
	}
	if cfg.AsyncActionTimeout != 6*time.Second {
		t.Fatalf("expected async action timeout 6s, got %s", cfg.AsyncActionTimeout)
	}
	if cfg.Prompt.Language != "Simplified Chinese" {
		t.Fatalf("expected language from config, got %q", cfg.Prompt.Language)
	}
	if cfg.Prompt.NPCStyle != "quiet mountain hermit" {
		t.Fatalf("expected npc style from config, got %q", cfg.Prompt.NPCStyle)
	}
	if cfg.Prompt.MaxSpeakChars != 42 {
		t.Fatalf("expected max speak chars 42, got %d", cfg.Prompt.MaxSpeakChars)
	}
	if cfg.Prompt.ToolInstruction != "Use exactly one available tool." {
		t.Fatalf("expected tool instruction from config, got %q", cfg.Prompt.ToolInstruction)
	}
	if !cfg.MemoryEnabledValue() {
		t.Fatal("expected memory enabled from config")
	}
	if cfg.RecentMemoryLimit != 7 {
		t.Fatalf("expected recent memory limit 7, got %d", cfg.RecentMemoryLimit)
	}
	if cfg.MaxRecentMemoryTokens != 2048 {
		t.Fatalf("expected max recent memory tokens 2048, got %d", cfg.MaxRecentMemoryTokens)
	}
	if cfg.MaxSteps != 5 {
		t.Fatalf("expected max steps 5, got %d", cfg.MaxSteps)
	}
	if cfg.MaxToolCallsPerStep != 3 {
		t.Fatalf("expected max tool calls per step 3, got %d", cfg.MaxToolCallsPerStep)
	}
	if cfg.MaxToolCallsPerTurn != 9 {
		t.Fatalf("expected max tool calls per turn 9, got %d", cfg.MaxToolCallsPerTurn)
	}
	if cfg.MaxAsyncActionsPerTurn != 2 {
		t.Fatalf("expected max async actions per turn 2, got %d", cfg.MaxAsyncActionsPerTurn)
	}
	if cfg.MaxParallelToolCalls != 2 {
		t.Fatalf("expected max parallel tool calls 2, got %d", cfg.MaxParallelToolCalls)
	}
	if cfg.MaxToolResultOutputTokens != 4096 {
		t.Fatalf("expected max tool result output tokens 4096, got %d", cfg.MaxToolResultOutputTokens)
	}
	if cfg.MaxToolResultOutputDepth != 3 {
		t.Fatalf("expected max tool result output depth 3, got %d", cfg.MaxToolResultOutputDepth)
	}
	if cfg.MaxToolResultOutputFields != 16 {
		t.Fatalf("expected max tool result output fields 16, got %d", cfg.MaxToolResultOutputFields)
	}
	if cfg.MaxToolResultOutputArrayItems != 8 {
		t.Fatalf("expected max tool result output array items 8, got %d", cfg.MaxToolResultOutputArrayItems)
	}
	if cfg.MemoryStore.Kind != "sqlite" {
		t.Fatalf("expected memory store kind sqlite, got %q", cfg.MemoryStore.Kind)
	}
	if cfg.MemoryStore.Root != "runtime/.custom/memory" {
		t.Fatalf("expected memory store root from config, got %q", cfg.MemoryStore.Root)
	}
	if cfg.MemoryStore.BusyTimeout != 1234*time.Millisecond {
		t.Fatalf("expected memory store busy timeout 1234ms, got %s", cfg.MemoryStore.BusyTimeout)
	}
	if cfg.MemoryStore.MaxRecordsPerEntity != 11 {
		t.Fatalf("expected max records per entity 11, got %d", cfg.MemoryStore.MaxRecordsPerEntity)
	}
	if cfg.MemoryStore.MaxProjectionBatchesPerEntity != 44 {
		t.Fatalf("expected max projection batches per entity 44, got %d", cfg.MemoryStore.MaxProjectionBatchesPerEntity)
	}
	if cfg.DefinitionCatalogRoot != "runtime/config/games" {
		t.Fatalf("expected definition catalog root from config, got %q", cfg.DefinitionCatalogRoot)
	}
}

func TestLoadConfigFileDefaultsMemoryEnabledWhenFieldOmitted(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.json")
	data := []byte(`{
  "turn_timeout_ms": 1000,
  "prompt": {
    "language": "Simplified Chinese"
  }
}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := agent.LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if !cfg.MemoryEnabledValue() {
		t.Fatal("expected memory to be enabled by default when memory_enabled is omitted")
	}
}

func TestConfigWithDefaultsPreservesExplicitMemoryDisabled(t *testing.T) {
	cfg := (agent.Config{
		MemoryEnabled: boolPtr(false),
	}).WithDefaults()

	if cfg.MemoryEnabledValue() {
		t.Fatal("expected explicit memory disabled to survive WithDefaults")
	}
}

func TestConfigWithDefaultsFillsPromptConfig(t *testing.T) {
	cfg := (agent.Config{}).WithDefaults()

	if cfg.Prompt.Language == "" {
		t.Fatal("expected default prompt language")
	}
	if cfg.Prompt.NPCStyle == "" {
		t.Fatal("expected default npc style")
	}
	if cfg.Prompt.MaxSpeakChars <= 0 {
		t.Fatalf("expected positive default max speak chars, got %d", cfg.Prompt.MaxSpeakChars)
	}
	if cfg.Prompt.ToolInstruction == "" {
		t.Fatal("expected default tool instruction")
	}
	if !cfg.MemoryEnabledValue() {
		t.Fatal("expected memory to be enabled by default")
	}
	if cfg.RecentMemoryLimit <= 0 {
		t.Fatalf("expected positive default recent memory limit, got %d", cfg.RecentMemoryLimit)
	}
}

func TestConfigWithDefaultsFillsSQLiteMemoryStoreConfig(t *testing.T) {
	cfg := (agent.Config{RecentMemoryLimit: 30}).WithDefaults()

	if cfg.MemoryStore.Kind != "sqlite" {
		t.Fatalf("default memory store kind = %q, want sqlite", cfg.MemoryStore.Kind)
	}
	// The default is relative to the Runtime data root, so it must stay relative.
	if cfg.MemoryStore.Root != "data/memory" || filepath.IsAbs(cfg.MemoryStore.Root) {
		t.Fatalf("default memory store root = %q, want the data-root relative default", cfg.MemoryStore.Root)
	}
	if cfg.MemoryStore.BusyTimeout != 5*time.Second {
		t.Fatalf("default memory store busy timeout = %s, want 5s", cfg.MemoryStore.BusyTimeout)
	}
	if cfg.MemoryStore.MaxRecordsPerEntity != 100 {
		t.Fatalf("default max records per entity = %d, want persistent recent-memory default 100", cfg.MemoryStore.MaxRecordsPerEntity)
	}
	if cfg.MemoryStore.MaxProjectionBatchesPerEntity != 400 {
		t.Fatalf("default max projection batches per entity = %d, want 400", cfg.MemoryStore.MaxProjectionBatchesPerEntity)
	}
}

func TestLoadConfigFileRejectsUnsupportedMemoryStoreKind(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.json")
	data := []byte(`{
  "memory_store": {
    "kind": "sqltie"
  }
}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := agent.LoadConfigFile(configPath)
	if err == nil {
		t.Fatal("LoadConfigFile returned nil error, want unsupported memory_store.kind error")
	}
	if !strings.Contains(err.Error(), `unsupported memory_store.kind "sqltie"`) {
		t.Fatalf("LoadConfigFile error = %v, want unsupported memory_store.kind", err)
	}
}

func TestConfigValidatesMemoryCapacityAfterDefaults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recent    int
		records   int
		batches   int
		wantError bool
	}{
		{"explicit conflict", 1, 3, 2, true},
		{"default records conflict", 1, 0, 2, true},
		{"recent limit raises records", 4, 2, 3, true},
		{"equal capacity", 2, 2, 2, false},
		{"default batch capacity", 1, 30, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := (agent.Config{
				RecentMemoryLimit: tc.recent,
				MemoryStore: agent.MemoryStoreConfig{
					MaxRecordsPerEntity:           tc.records,
					MaxProjectionBatchesPerEntity: tc.batches,
				},
			}).WithDefaults()
			err := cfg.Validate()
			if (err != nil) != tc.wantError {
				t.Fatalf("Validate() = %v, want error %t", err, tc.wantError)
			}
			if err != nil && (!strings.Contains(err.Error(), "max_projection_batches_per_entity") || !strings.Contains(err.Error(), "max_records_per_entity")) {
				t.Fatalf("validation error lacks capacity fields: %v", err)
			}
		})
	}
}

func TestLoadConfigFileRejectsConflictingMemoryCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	data := []byte(`{"memory_store":{"max_records_per_entity":30,"max_projection_batches_per_entity":29}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadConfigFile(path); err == nil {
		t.Fatal("LoadConfigFile accepted conflicting memory capacities")
	}
}

func TestDefaultToolInstructionDoesNotNameGameSpecificTools(t *testing.T) {
	cfg := agent.DefaultConfig()
	instruction := cfg.Prompt.ToolInstruction

	for _, forbidden := range gameSpecificToolInstructionTerms() {
		if strings.Contains(instruction, forbidden) {
			t.Fatalf("default tool instruction should not mention game-specific term %q: %q", forbidden, instruction)
		}
	}
}

func TestDefaultToolInstructionKeepsGenericToolGuidance(t *testing.T) {
	cfg := agent.DefaultConfig()
	instruction := cfg.Prompt.ToolInstruction

	for _, want := range []string{"available tools", "descriptions", "input schemas", "settle"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("expected default tool instruction to mention %q, got %q", want, instruction)
		}
	}
}

func TestBundledToolInstructionDoesNotNameGameSpecificTools(t *testing.T) {
	cfg, err := agent.LoadConfigFile(filepath.Join("..", "..", "config", "agent.json"))
	if err != nil {
		t.Fatalf("load bundled config: %v", err)
	}

	for _, forbidden := range gameSpecificToolInstructionTerms() {
		if strings.Contains(cfg.Prompt.ToolInstruction, forbidden) {
			t.Fatalf("bundled tool instruction should not mention game-specific term %q: %q", forbidden, cfg.Prompt.ToolInstruction)
		}
	}
}

func TestBundledStardewConfigEnablesDefinitionCatalogRoot(t *testing.T) {
	cfg, err := agent.LoadConfigFile(filepath.Join("..", "..", "config", "games", "stardew-valley", "agent.json"))
	if err != nil {
		t.Fatalf("load bundled Stardew config: %v", err)
	}

	// The bundled catalog root is relative to the Runtime data root.
	if filepath.ToSlash(cfg.DefinitionCatalogRoot) != "config/games" {
		t.Fatalf("DefinitionCatalogRoot = %q, want the data-root relative config/games", cfg.DefinitionCatalogRoot)
	}
	if filepath.IsAbs(cfg.DefinitionCatalogRoot) {
		t.Fatalf("DefinitionCatalogRoot = %q, want a relative path", cfg.DefinitionCatalogRoot)
	}
}

func TestStardewTaskExecutionBudget(t *testing.T) {
	cfg, err := agent.LoadConfigFile(filepath.Join("..", "..", "config", "games", "stardew-valley", "agent.json"))
	if err != nil {
		t.Fatalf("load bundled Stardew config: %v", err)
	}
	if cfg.AsyncActionTimeout != 190*time.Second {
		t.Fatalf("AsyncActionTimeout = %s, want 190s", cfg.AsyncActionTimeout)
	}
	if cfg.TurnTimeout != 270*time.Second {
		t.Fatalf("TurnTimeout = %s, want 270s", cfg.TurnTimeout)
	}
	defaults := agent.DefaultConfig()
	if defaults.AsyncActionTimeout != 45*time.Second || defaults.TurnTimeout != 90*time.Second {
		t.Fatalf("generic timeout defaults changed: async=%s turn=%s", defaults.AsyncActionTimeout, defaults.TurnTimeout)
	}
	// 5 steps: a mail turn writes a letter, speaks and emotes; at 3 the model ran
	// out of steps before it could settle, and the turn was reported as failed.
	if cfg.MaxSteps != 5 || cfg.MaxAsyncActionsPerTurn != 1 {
		t.Fatalf("model execution limits changed: steps=%d async=%d", cfg.MaxSteps, cfg.MaxAsyncActionsPerTurn)
	}
}

func gameSpecificToolInstructionTerms() []string {
	return []string{
		"present_dialogue",
		"player_said_to_npc",
		"reply_options",
		"allow_free_text",
		"speak",
		"emote",
		"face_player",
	}
}

func TestConfigLoadsPhase5Budgets(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.json")
	data := []byte(`{
  "max_steps": 5,
  "max_tool_calls_per_step": 4,
  "max_tool_calls_per_turn": 8,
  "max_parallel_tool_calls": 3,
  "max_tool_result_output_tokens": 1234,
  "max_tool_result_output_depth": 5,
  "max_tool_result_output_fields": 32,
  "max_tool_result_output_array_items": 12
}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := agent.LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.MaxSteps != 5 ||
		cfg.MaxToolCallsPerStep != 4 ||
		cfg.MaxToolCallsPerTurn != 8 ||
		cfg.MaxParallelToolCalls != 3 ||
		cfg.MaxToolResultOutputTokens != 1234 ||
		cfg.MaxToolResultOutputDepth != 5 ||
		cfg.MaxToolResultOutputFields != 32 ||
		cfg.MaxToolResultOutputArrayItems != 12 {
		t.Fatalf("phase5 budgets not loaded: %+v", cfg)
	}
}

func TestConfigDefaultsPhase5BudgetsWhenMissingZeroOrNegative(t *testing.T) {
	cfg := (agent.Config{
		MaxSteps:                      -1,
		MaxToolCallsPerStep:           0,
		MaxToolCallsPerTurn:           -2,
		MaxAsyncActionsPerTurn:        0,
		MaxParallelToolCalls:          0,
		MaxToolResultOutputTokens:     -1,
		MaxToolResultOutputDepth:      0,
		MaxToolResultOutputFields:     -1,
		MaxToolResultOutputArrayItems: 0,
	}).WithDefaults()

	if cfg.MaxSteps != 3 {
		t.Fatalf("MaxSteps = %d, want 3", cfg.MaxSteps)
	}
	if cfg.MaxToolCallsPerStep != 4 {
		t.Fatalf("MaxToolCallsPerStep = %d, want 4", cfg.MaxToolCallsPerStep)
	}
	if cfg.MaxToolCallsPerTurn != 6 {
		t.Fatalf("MaxToolCallsPerTurn = %d, want 6", cfg.MaxToolCallsPerTurn)
	}
	if cfg.MaxAsyncActionsPerTurn != 1 {
		t.Fatalf("MaxAsyncActionsPerTurn = %d, want 1", cfg.MaxAsyncActionsPerTurn)
	}
	if cfg.MaxParallelToolCalls != 4 {
		t.Fatalf("MaxParallelToolCalls = %d, want 4", cfg.MaxParallelToolCalls)
	}
	if cfg.MaxToolResultOutputTokens != 8192 {
		t.Fatalf("MaxToolResultOutputTokens = %d, want 8192", cfg.MaxToolResultOutputTokens)
	}
	if cfg.MaxToolResultOutputDepth != 4 {
		t.Fatalf("MaxToolResultOutputDepth = %d, want 4", cfg.MaxToolResultOutputDepth)
	}
	if cfg.MaxToolResultOutputFields != 64 {
		t.Fatalf("MaxToolResultOutputFields = %d, want 64", cfg.MaxToolResultOutputFields)
	}
	if cfg.MaxToolResultOutputArrayItems != 32 {
		t.Fatalf("MaxToolResultOutputArrayItems = %d, want 32", cfg.MaxToolResultOutputArrayItems)
	}
	if cfg.ActionStartTimeout != 3*time.Second {
		t.Fatalf("ActionStartTimeout = %s, want 3s", cfg.ActionStartTimeout)
	}
	if cfg.AsyncActionTimeout != 45*time.Second {
		t.Fatalf("AsyncActionTimeout = %s, want 45s", cfg.AsyncActionTimeout)
	}
	if cfg.TurnTimeout != 90*time.Second {
		t.Fatalf("TurnTimeout = %s, want 90s", cfg.TurnTimeout)
	}
}

func TestLoadConfigFileLoadsPhase74BudgetConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.json")
	data := []byte(`{
  "max_request_tokens": 10000,
  "max_system_tokens": 1000,
  "max_user_message_tokens": 8000,
  "max_definition_tokens": 1200,
  "max_observation_tokens": 2200,
  "max_event_tokens": 900,
  "max_context_facts_tokens": 700,
  "max_recent_memory_tokens": 333,
  "max_transcript_tokens": 444,
  "max_tool_count": 5,
  "max_tool_description_tokens": 88,
  "max_tool_schema_tokens": 99,
  "max_total_tool_schema_tokens": 222
}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := agent.LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.MaxRequestTokens != 10000 ||
		cfg.MaxSystemTokens != 1000 ||
		cfg.MaxUserMessageTokens != 8000 ||
		cfg.MaxDefinitionTokens != 1200 ||
		cfg.MaxObservationTokens != 2200 ||
		cfg.MaxEventTokens != 900 ||
		cfg.MaxContextFactsTokens != 700 ||
		cfg.MaxRecentMemoryTokens != 333 ||
		cfg.MaxTranscriptTokens != 444 ||
		cfg.MaxToolCount != 5 ||
		cfg.MaxToolDescriptionTokens != 88 ||
		cfg.MaxToolSchemaTokens != 99 ||
		cfg.MaxTotalToolSchemaTokens != 222 {
		t.Fatalf("phase7.4 budget config not loaded: %+v", cfg)
	}
}

func TestLoadConfigFileRejectsRemovedByteBudgetFields(t *testing.T) {
	cases := []struct {
		name string
		json string
		key  string
		want string
	}{
		{
			name: "request bytes",
			json: `{"max_request_bytes": 10000}`,
			key:  "max_request_bytes",
			want: "max_request_tokens",
		},
		{
			name: "memory context size limit",
			json: `{"memory_context_size_limit": 777}`,
			key:  "memory_context_size_limit",
			want: "max_recent_memory_tokens",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(configPath, []byte(tc.json), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := agent.LoadConfigFile(configPath)
			if err == nil {
				t.Fatal("LoadConfigFile returned nil error, want removed byte budget field rejection")
			}
			message := err.Error()
			if !strings.Contains(message, tc.key) || !strings.Contains(message, tc.want) {
				t.Fatalf("LoadConfigFile error = %q, want to mention %q and %q", message, tc.key, tc.want)
			}
		})
	}
}

func TestDefaultConfigUsesPhase74EstimatedTokenBudget(t *testing.T) {
	cfg := agent.DefaultConfig()

	if cfg.MaxRequestTokens != 65536 {
		t.Fatalf("MaxRequestTokens = %d, want 65536 estimated tokens", cfg.MaxRequestTokens)
	}
	if cfg.MaxSystemTokens != 8192 {
		t.Fatalf("MaxSystemTokens = %d, want 8192 estimated tokens", cfg.MaxSystemTokens)
	}
	if cfg.MaxUserMessageTokens != 49152 {
		t.Fatalf("MaxUserMessageTokens = %d, want 49152 estimated tokens", cfg.MaxUserMessageTokens)
	}
}

func TestConfigPhase6DefaultTurnTimeoutCoversAsyncBudget(t *testing.T) {
	cfg := agent.DefaultConfig()
	worstCase := cfg.ObserveTimeout +
		time.Duration(cfg.MaxSteps)*cfg.LLMTimeout +
		cfg.ActionStartTimeout +
		cfg.AsyncActionTimeout +
		cfg.ObserveTimeout +
		cfg.LLMTimeout +
		cfg.ActionTimeout

	if cfg.TurnTimeout <= worstCase {
		t.Fatalf("TurnTimeout = %s, want greater than worst case %s", cfg.TurnTimeout, worstCase)
	}
}

func boolPtr(v bool) *bool {
	return &v
}
