package agent

import "fmt"

type CompactionConfig struct {
	Enabled            *bool `json:"compaction_enabled"`
	KeepRecentTokens   int   `json:"keep_recent_tokens"`
	MaxSummaryTokens   int   `json:"max_summary_tokens"`
	MaxInputTokens     int   `json:"max_summary_input_tokens"`
	MaxResponseBytes   int   `json:"max_summary_response_bytes"`
	TimeoutMS          int   `json:"compaction_timeout_ms"`
	RetryCooldownTurns int   `json:"compaction_retry_cooldown_turns"`
}

func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{Enabled: boolPtr(true), KeepRecentTokens: 20000, MaxSummaryTokens: 2048,
		MaxInputTokens: 32768, MaxResponseBytes: 1 << 20, TimeoutMS: 10000, RetryCooldownTurns: 3}
}
func (c CompactionConfig) WithDefaults() CompactionConfig {
	d := DefaultCompactionConfig()
	if c.Enabled == nil {
		c.Enabled = d.Enabled
	}
	if c.KeepRecentTokens == 0 {
		c.KeepRecentTokens = d.KeepRecentTokens
	}
	if c.MaxSummaryTokens == 0 {
		c.MaxSummaryTokens = d.MaxSummaryTokens
	}
	if c.MaxInputTokens == 0 {
		c.MaxInputTokens = d.MaxInputTokens
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = d.MaxResponseBytes
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = d.TimeoutMS
	}
	if c.RetryCooldownTurns == 0 {
		c.RetryCooldownTurns = d.RetryCooldownTurns
	}
	return c
}
func (c CompactionConfig) Validate() error {
	for name, value := range map[string]int{
		"keep_recent_tokens": c.KeepRecentTokens, "max_summary_tokens": c.MaxSummaryTokens,
		"max_summary_input_tokens": c.MaxInputTokens, "max_summary_response_bytes": c.MaxResponseBytes,
		"compaction_timeout_ms": c.TimeoutMS, "compaction_retry_cooldown_turns": c.RetryCooldownTurns,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.MaxInputTokens <= c.MaxSummaryTokens {
		return fmt.Errorf("max_summary_input_tokens must exceed max_summary_tokens")
	}
	return nil
}
func (c CompactionConfig) EnabledValue() bool { return c.Enabled == nil || *c.Enabled }
