package agent

import (
	"fmt"
	"math"
	"time"

	"gameagent/runtime/internal/memory"
)

type RetrievalConfig struct {
	Enabled       *bool `json:"history_retrieval_enabled"`
	QueryMaxChars int   `json:"history_query_max_chars"`
	QueryMaxTerms int   `json:"history_query_max_terms"`
	ScanLimit     int   `json:"history_search_scan_limit"`
	TimeoutMS     int   `json:"history_search_timeout_ms"`
	MaxReadBytes  int   `json:"history_search_max_read_bytes"`
	Limit         int   `json:"history_retrieval_limit"`
	MaxTokens     int   `json:"max_retrieved_history_tokens"`
}

func DefaultRetrievalConfig() RetrievalConfig {
	l := memory.DefaultHistorySearchLimits()
	return RetrievalConfig{boolPtr(true), l.QueryChars, l.QueryTerms, l.ScanCandidates, l.TimeoutMS, l.Bytes, 5, 1024}
}

func (c RetrievalConfig) EnabledValue() bool { return c.Enabled == nil || *c.Enabled }

func (c RetrievalConfig) WithDefaults() RetrievalConfig {
	d := DefaultRetrievalConfig()
	if c.Enabled == nil {
		c.Enabled = d.Enabled
	}
	for _, field := range []struct {
		value    *int
		fallback int
	}{
		{&c.QueryMaxChars, d.QueryMaxChars}, {&c.QueryMaxTerms, d.QueryMaxTerms}, {&c.ScanLimit, d.ScanLimit},
		{&c.TimeoutMS, d.TimeoutMS}, {&c.MaxReadBytes, d.MaxReadBytes}, {&c.Limit, d.Limit}, {&c.MaxTokens, d.MaxTokens},
	} {
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	return c
}

func (c RetrievalConfig) searchLimits() memory.HistorySearchLimits {
	return memory.HistorySearchLimits{QueryChars: c.QueryMaxChars, QueryTerms: c.QueryMaxTerms, ScanCandidates: c.ScanLimit, TimeoutMS: c.TimeoutMS, Bytes: c.MaxReadBytes}
}

func (c RetrievalConfig) Validate() error {
	if err := c.searchLimits().Validate(); err != nil {
		return err
	}
	if c.Limit <= 0 || c.MaxTokens <= 0 {
		return fmt.Errorf("history retrieval limit and token budget must be positive")
	}
	if c.ScanLimit == math.MaxInt {
		return fmt.Errorf("history_search_scan_limit must allow pagination lookahead")
	}
	if int64(c.TimeoutMS) > math.MaxInt64/int64(time.Millisecond) {
		return fmt.Errorf("history_search_timeout_ms exceeds duration range")
	}
	return nil
}

func (c Config) validateHistoryRuntimeBounds() error {
	if err := c.HistoryIndex.Validate(); err != nil {
		return err
	}
	if c.HistoryIndex.TermAssociations == math.MaxInt {
		return fmt.Errorf("history index association limit must allow capacity lookahead")
	}
	if err := c.HistoryMaintenance.Validate(); err != nil {
		return err
	}
	if c.HistoryMaintenance.Sources == math.MaxInt {
		return fmt.Errorf("history maintenance source limit must allow pagination lookahead")
	}
	if int64(c.HistoryMaintenance.TimeoutMS) > math.MaxInt64/int64(time.Millisecond) {
		return fmt.Errorf("history maintenance timeout exceeds duration range")
	}
	if c.RetentionDays < 0 || int64(c.RetentionDays) > math.MaxInt64/int64(24*time.Hour) {
		return fmt.Errorf("retention_days must be nonnegative and fit a duration")
	}
	return nil
}
