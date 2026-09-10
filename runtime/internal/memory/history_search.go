package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"gameagent/runtime/internal/session"
)

const RetrievalSchemaVersion = "phase8_3_history_v1"
const HistoryIndexVersion = "literal_v1"

type HistoryIndexLimits struct {
	TextBytes        int `json:"max_source_text_bytes"`
	Fragments        int `json:"max_source_fragments"`
	TermAssociations int `json:"max_source_term_associations"`
}

func DefaultHistoryIndexLimits() HistoryIndexLimits {
	return HistoryIndexLimits{256 << 10, 1024, 16384}
}
func (l HistoryIndexLimits) WithDefaults() HistoryIndexLimits {
	d := DefaultHistoryIndexLimits()
	if l.TextBytes == 0 {
		l.TextBytes = d.TextBytes
	}
	if l.Fragments == 0 {
		l.Fragments = d.Fragments
	}
	if l.TermAssociations == 0 {
		l.TermAssociations = d.TermAssociations
	}
	return l
}
func (l HistoryIndexLimits) Validate() error {
	if l.TextBytes <= 0 || l.Fragments <= 0 || l.TermAssociations <= 0 {
		return fmt.Errorf("history index limits must be positive")
	}
	return nil
}
func (l HistoryIndexLimits) signature() string {
	return fmt.Sprintf("%s:%d:%d:%d", HistoryIndexVersion, l.TextBytes, l.Fragments, l.TermAssociations)
}

type HistorySearchLimits struct {
	QueryChars     int `json:"query_max_chars"`
	QueryTerms     int `json:"query_max_terms"`
	ScanCandidates int `json:"scan_limit"`
	Bytes          int `json:"max_read_bytes"`
	TimeoutMS      int `json:"timeout_ms"`
}

func DefaultHistorySearchLimits() HistorySearchLimits {
	return HistorySearchLimits{256, 32, 512, 32 << 20, 500}
}
func (l HistorySearchLimits) WithDefaults() HistorySearchLimits {
	d := DefaultHistorySearchLimits()
	for _, p := range []struct {
		v *int
		d int
	}{{&l.QueryChars, d.QueryChars}, {&l.QueryTerms, d.QueryTerms}, {&l.ScanCandidates, d.ScanCandidates}, {&l.Bytes, d.Bytes}, {&l.TimeoutMS, d.TimeoutMS}} {
		if *p.v == 0 {
			*p.v = p.d
		}
	}
	return l
}
func (l HistorySearchLimits) Validate() error {
	for _, v := range []int{l.QueryChars, l.QueryTerms, l.ScanCandidates, l.Bytes, l.TimeoutMS} {
		if v <= 0 {
			return fmt.Errorf("history search limits must be positive")
		}
	}
	return nil
}

// Source is a verified header. Field and Span refer to the unchanged canonical string.
type HistoryMatch struct {
	Source       HistorySource
	Field        HistoryTextField
	Span         HistoryTextSpan
	MatchedTerms []string
	Score        int
}
type HistorySearchRequest struct {
	Snapshot    HistorySnapshot
	CurrentTime *GameTimeSnapshot
	Query       string
	Limits      HistorySearchLimits
	Offset      int
}
type HistorySearchPage struct {
	Matches     []HistoryMatch
	NextOffset  int
	More        bool
	Scanned     int
	Bytes       int
	Diagnostics []string
}
type HistorySearchStore interface {
	SearchHistory(context.Context, HistorySearchRequest) (HistorySearchPage, error)
}

type HistoryMaintenanceLimits struct {
	Sources       int `json:"max_sources"`
	Bytes         int `json:"max_bytes"`
	TimeoutMS     int `json:"timeout_ms"`
	LockTimeoutMS int `json:"lock_timeout_ms"`
}

func DefaultHistoryMaintenanceLimits() HistoryMaintenanceLimits {
	return HistoryMaintenanceLimits{16, 8 << 20, 500, 100}
}
func (l HistoryMaintenanceLimits) WithDefaults() HistoryMaintenanceLimits {
	d := DefaultHistoryMaintenanceLimits()
	for _, p := range []struct {
		v *int
		d int
	}{{&l.Sources, d.Sources}, {&l.Bytes, d.Bytes}, {&l.TimeoutMS, d.TimeoutMS}, {&l.LockTimeoutMS, d.LockTimeoutMS}} {
		if *p.v == 0 {
			*p.v = p.d
		}
	}
	return l
}
func (l HistoryMaintenanceLimits) Validate() error {
	for _, v := range []int{l.Sources, l.Bytes, l.TimeoutMS, l.LockTimeoutMS} {
		if v <= 0 {
			return fmt.Errorf("history maintenance limits must be positive")
		}
	}
	if l.LockTimeoutMS > l.TimeoutMS {
		return fmt.Errorf("maintenance lock timeout exceeds task timeout")
	}
	return nil
}

type HistoryRebuildRequest struct {
	Owner         session.AgentSessionKey
	AfterSequence int64
	Force         bool
	Limits        HistoryMaintenanceLimits
}
type HistoryPruneRequest struct {
	Owner            session.AgentSessionKey
	AfterSequence    int64
	Now              time.Time
	RetentionDays    int
	CurrentTime      *GameTimeSnapshot
	KeepRecentTokens int
	Limits           HistoryMaintenanceLimits
}
type HistoryMaintenanceResult struct {
	Processed   int
	Bytes       int
	ReadSources int
	ReadBytes   int
	NextAfter   int64
	More        bool
	SourceIDs   []string
	Diagnostics []string
}
type HistoryMaintenanceStore interface {
	RebuildHistoryIndex(context.Context, HistoryRebuildRequest) (HistoryMaintenanceResult, error)
	PruneHistory(context.Context, HistoryPruneRequest) (HistoryMaintenanceResult, error)
}

// Query input is bounded before tokenization; punctuation and scripts separate tokens.
func HistoryQueryTerms(text string, maxChars, maxTerms int) []string {
	if maxChars <= 0 || maxTerms <= 0 {
		return nil
	}
	var terms []string
	seen := map[string]bool{}
	add := func(term string) {
		if term != "" && !seen[term] && len(terms) < maxTerms {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	var latin strings.Builder
	var previous rune
	count := 0
	flush := func() { add(latin.String()); latin.Reset() }
	for _, r := range text {
		if count >= maxChars || len(terms) >= maxTerms {
			break
		}
		count++
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			if previous != 0 {
				add(string([]rune{previous, r}))
			}
			previous = r
		case unicode.Is(unicode.Latin, r) || unicode.IsDigit(r):
			previous = 0
			latin.WriteRune(unicode.ToLower(r))
		default:
			previous = 0
			flush()
		}
	}
	flush()
	return terms
}

func historyIndexFields(batch HistoryBatch, limits HistoryIndexLimits) ([]HistoryTextField, string) {
	fields := HistoryTextFields(batch)
	if len(fields) > limits.Fragments {
		return nil, "capacity_exceeded"
	}
	bytes, associations := 0, 0
	for _, f := range fields {
		if len(f.Text) > limits.TextBytes-bytes {
			return nil, "capacity_exceeded"
		}
		bytes += len(f.Text)
		terms := HistoryQueryTerms(f.Text, len(f.Text), limits.TermAssociations+1)
		associations += len(terms)
		if associations > limits.TermAssociations {
			return nil, "capacity_exceeded"
		}
	}
	return fields, "ready"
}
