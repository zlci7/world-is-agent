package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
)

type retrievalTestStore struct {
	*memory.InMemoryHistoryStore
	requests  []memory.HistorySearchRequest
	deadlines []time.Time
	search    func(memory.HistorySearchRequest) (memory.HistorySearchPage, error)
}

func (s *retrievalTestStore) SearchHistory(ctx context.Context, req memory.HistorySearchRequest) (memory.HistorySearchPage, error) {
	s.requests = append(s.requests, req)
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, deadline)
	return s.search(req)
}

func TestPrepareRetrievalBoundedQueryAndAggregatePages(t *testing.T) {
	store := &retrievalTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	source := appendPreparationSource(t, store, "old", "fishing 7319", preparationTime(8))
	field := memory.HistoryTextFields(*source.Batch)[0]
	match := memory.HistoryMatch{Source: source, Field: field, Span: memory.HistoryTextSpan{Path: field.Path, End: utf8.RuneCountInString(field.Text)}}
	store.search = func(req memory.HistorySearchRequest) (memory.HistorySearchPage, error) {
		if req.Offset == 0 {
			return memory.HistorySearchPage{Scanned: 5, Bytes: 50, NextOffset: 5, More: true}, nil
		}
		return memory.HistorySearchPage{Matches: []memory.HistoryMatch{match}, Scanned: 1, Bytes: 10}, nil
	}
	cfg := DefaultConfig()
	cfg.Retrieval.QueryMaxChars, cfg.Retrieval.ScanLimit, cfg.Retrieval.MaxReadBytes = 12, 6, 100
	l := &Loop{config: cfg, historyStore: store}
	snapshot, err := store.BeginHistorySnapshot(context.Background(), preparationOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	event := &protocol.GameEvent{ContextFacts: []*protocol.ContextFact{nil, {Text: "fishing"}, {Text: strings.Repeat("7319", 1<<18)}}}
	result := l.prepareRetrieval(context.Background(), &preparationTrace{}, snapshot, preparationTime(9), event)
	if len(result.Matches) != 1 || len(store.requests) != 2 {
		t.Fatalf("matches=%d requests=%d", len(result.Matches), len(store.requests))
	}
	first, second := store.requests[0], store.requests[1]
	if first.Query != "fishing 7319" || second.Query != first.Query || first.Snapshot != snapshot || second.Snapshot != snapshot || second.Limits.ScanCandidates != 6 || second.Limits.Bytes != 50 {
		t.Fatalf("unbounded or changing query: %+v %+v", first, second)
	}
	if store.deadlines[0].IsZero() || !store.deadlines[0].Equal(store.deadlines[1]) || time.Until(store.deadlines[0]) > 500*time.Millisecond {
		t.Fatalf("query deadline changed: %v", store.deadlines)
	}
}

func TestPrepareRetrievalFailureAndInvalidPagesAreDiagnosed(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		page             memory.HistorySearchPage
		err              error
	}{
		{"read", "search_read_failed", memory.HistorySearchPage{}, errors.New("offline")},
		{"timeout", "search_read_timeout", memory.HistorySearchPage{}, context.DeadlineExceeded},
		{"count", "search_page_invalid", memory.HistorySearchPage{Scanned: 513}, nil},
		{"bytes", "search_page_invalid", memory.HistorySearchPage{Bytes: -1}, nil},
		{"progress", "search_page_invalid", memory.HistorySearchPage{Scanned: 1, More: true}, nil},
		{"budget", "search_scan_incomplete", memory.HistorySearchPage{Scanned: 512, More: true, NextOffset: 512}, nil},
		{"store coverage", "search_index_not_ready", memory.HistorySearchPage{Diagnostics: []string{"index_not_ready", "scan_incomplete"}}, nil},
		{"store read", "search_read_failed", memory.HistorySearchPage{Diagnostics: []string{"read_failed"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &retrievalTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{}), search: func(memory.HistorySearchRequest) (memory.HistorySearchPage, error) { return tc.page, tc.err }}
			l := &Loop{config: DefaultConfig(), historyStore: store}
			tracer := &preparationTrace{}
			result := l.prepareRetrieval(context.Background(), tracer, memory.HistorySnapshot{Owner: preparationOwner, LeaseID: "lease", Watermark: 1}, nil, &protocol.GameEvent{ContextFacts: []*protocol.ContextFact{{Text: "fishing"}}})
			if result == nil || len(store.requests) != 1 || !tracer.hasDiagnostic(tc.diagnostic) {
				t.Fatalf("result=%+v requests=%d trace=%+v", result, len(store.requests), tracer.events)
			}
		})
	}
}

func TestPrepareRetrievalAccountsForValidFailedPageUsage(t *testing.T) {
	store := &retrievalTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	source := appendPreparationSource(t, store, "failed-page", "fishing 7319", preparationTime(8))
	field := memory.HistoryTextFields(*source.Batch)[0]
	match := memory.HistoryMatch{Source: source, Field: field, Span: memory.HistoryTextSpan{Path: field.Path, End: utf8.RuneCountInString(field.Text)}}
	for _, tc := range []struct {
		name  string
		page  memory.HistorySearchPage
		valid bool
	}{
		{"bounded", memory.HistorySearchPage{Matches: []memory.HistoryMatch{match}, Scanned: 3, Bytes: 4096, NextOffset: 3}, true},
		{"negative count", memory.HistorySearchPage{Scanned: -1, Bytes: 4096}, false},
		{"negative bytes", memory.HistorySearchPage{Scanned: 3, Bytes: -1}, false},
		{"excess count", memory.HistorySearchPage{Scanned: 513, Bytes: 4096}, false},
		{"excess bytes", memory.HistorySearchPage{Scanned: 3, Bytes: (32 << 20) + 1}, false},
		{"invalid progress", memory.HistorySearchPage{Scanned: 3, Bytes: 4096, More: true, NextOffset: 2}, false},
		{"missing scanned matches", memory.HistorySearchPage{Matches: []memory.HistoryMatch{match}, Bytes: 4096}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.search = func(memory.HistorySearchRequest) (memory.HistorySearchPage, error) {
				return tc.page, context.DeadlineExceeded
			}
			l := &Loop{config: DefaultConfig(), historyStore: store}
			tracer := &preparationTrace{}
			result := l.prepareRetrieval(context.Background(), tracer, memory.HistorySnapshot{Owner: preparationOwner, LeaseID: "lease", Watermark: source.Sequence}, preparationTime(9), &protocol.GameEvent{ContextFacts: []*protocol.ContextFact{{Text: "fishing"}}})
			if len(result.Matches) != 0 {
				t.Fatalf("failed-page matches injected: %+v", result.Matches)
			}
			if !tracer.hasDiagnostic("search_read_timeout") || !tracer.hasDiagnostic("search_scan_incomplete") {
				t.Fatalf("failure diagnostics missing: %+v", tracer.events)
			}
			if !tc.valid && !tracer.hasDiagnostic("search_page_invalid") {
				t.Fatalf("malformed failed page not diagnosed: %+v", tracer.events)
			}
			if len(tracer.events) != 1 {
				t.Fatalf("retrieval trace count=%d", len(tracer.events))
			}
			fields := tracer.events[0].Fields
			wantScanned, wantBytes := 0, 0
			if tc.valid {
				wantScanned, wantBytes = tc.page.Scanned, tc.page.Bytes
			}
			if fields["search_scanned"] != wantScanned || fields["search_bytes_read"] != wantBytes {
				t.Fatalf("failed-page usage: scanned=%v bytes=%v, want %d/%d", fields["search_scanned"], fields["search_bytes_read"], wantScanned, wantBytes)
			}
		})
	}
}

func TestRetrievalConstructorPassesIndexLimits(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MemoryStore.Root = t.TempDir()
	cfg.HistoryIndex.TextBytes = 1
	l := NewLoop(nil, nil, cfg)
	appendPreparationSource(t, l.historyStore, "old", "fishing 7319", preparationTime(8))
	snapshot, err := l.historyStore.BeginHistorySnapshot(context.Background(), preparationOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer l.historyStore.ReleaseHistorySnapshot(snapshot)
	tracer := &preparationTrace{}
	result := l.prepareRetrieval(context.Background(), tracer, snapshot, preparationTime(9), &protocol.GameEvent{ContextFacts: []*protocol.ContextFact{{Text: "fishing"}}})
	if len(result.Matches) != 0 || !tracer.hasDiagnostic("search_index_capacity_exceeded") {
		t.Fatalf("constructor ignored configured index capacity: %+v %+v", result, tracer.events)
	}
}

func TestPrepareRetrievalSkipsDisabledUnavailableAndEmptyQuery(t *testing.T) {
	for _, tc := range []struct {
		name, query      string
		disabled, legacy bool
	}{
		{name: "disabled", query: "fishing", disabled: true}, {name: "legacy", query: "fishing", legacy: true}, {name: "empty", query: "!"}, {name: "single han", query: "\u9c7c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &retrievalTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
			cfg := DefaultConfig()
			cfg.Retrieval.Enabled = boolPtr(!tc.disabled)
			l := &Loop{config: cfg, historyStore: store}
			if tc.legacy {
				l.historyStore = nil
			}
			l.prepareRetrieval(context.Background(), &preparationTrace{}, memory.HistorySnapshot{Owner: preparationOwner, LeaseID: "lease"}, nil, &protocol.GameEvent{ContextFacts: []*protocol.ContextFact{{Text: tc.query}}})
			if len(store.requests) != 0 {
				t.Fatalf("query performed for %s", tc.name)
			}
		})
	}
}
