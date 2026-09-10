package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/trace"
)

func (l *Loop) prepareRetrieval(ctx context.Context, tracer trace.TurnTracer, snapshot memory.HistorySnapshot, currentTime *memory.GameTimeSnapshot, event *protocol.GameEvent) *agentcontext.RetrievedHistoryInput {
	config := l.config.Retrieval.WithDefaults()
	if !l.config.MemoryEnabledValue() || !config.EnabledValue() {
		return nil
	}
	input := &agentcontext.RetrievedHistoryInput{Snapshot: snapshot, MaxTokens: config.MaxTokens, Limit: config.Limit}
	started := time.Now()
	scanned, bytesRead := 0, 0
	add := func(code string) {
		if !slices.Contains(input.Diagnostics, code) {
			input.Diagnostics = append(input.Diagnostics, code)
		}
	}
	defer func() {
		emitHistoryEvent(tracer, "history_retrieved", trace.Fields{
			"diagnostics": append([]string(nil), input.Diagnostics...), "candidate_count": len(input.Matches),
			"search_scanned": scanned, "search_bytes_read": bytesRead, "search_scan_limit": config.ScanLimit,
			"search_byte_limit": config.MaxReadBytes, "snapshot_watermark": snapshot.Watermark,
			"snapshot_summary_revision": snapshot.SummaryRevision, "elapsed_ms": time.Since(started).Milliseconds(),
		})
	}()
	store, ok := l.historyStore.(memory.HistorySearchStore)
	if !ok {
		add("search_unavailable")
		return input
	}
	if snapshot.LeaseID == "" {
		add("search_snapshot_unavailable")
		return input
	}
	query := historyEventQuery(event, config.QueryMaxChars)
	if len(memory.HistoryQueryTerms(query, config.QueryMaxChars, config.QueryMaxTerms)) == 0 {
		add("search_no_valid_query")
		return input
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutMS)*time.Millisecond)
	defer cancel()
	offset := 0
	for {
		limits := config.searchLimits()
		limits.Bytes -= bytesRead
		remainingCandidates := config.ScanLimit - scanned
		if remainingCandidates <= 0 || limits.Bytes <= 0 {
			add("search_scan_incomplete")
			break
		}
		if err := ctx.Err(); err != nil {
			add(searchReadErrorCode(err))
			add("search_scan_incomplete")
			break
		}
		page, err := store.SearchHistory(ctx, memory.HistorySearchRequest{Snapshot: snapshot, CurrentTime: currentTime, Query: query, Limits: limits, Offset: offset})
		for _, code := range page.Diagnostics {
			code = strings.TrimPrefix(code, "search_")
			switch code {
			case "no_match":
				// A candidate page can be empty while a later page still matches.
			case "no_valid_query", "scan_incomplete", "index_not_ready", "index_capacity_exceeded", "time_hidden", "time_unknown", "source_invalid", "source_pruned", "read_failed":
				add("search_" + code)
			default:
				add("search_store_diagnostic_unknown")
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		validPage := page.Scanned >= len(page.Matches) && page.Scanned <= remainingCandidates && page.Bytes >= 0 && page.Bytes <= limits.Bytes &&
			(!page.More || (page.Scanned > 0 && page.NextOffset > offset && page.NextOffset-offset == page.Scanned))
		if validPage {
			// Validated usage includes work completed before a page failure.
			scanned += page.Scanned
			bytesRead += page.Bytes
		} else {
			add("search_page_invalid")
		}
		if err != nil {
			add(searchReadErrorCode(err))
		}
		if !validPage || err != nil {
			add("search_scan_incomplete")
			break
		}
		for _, match := range page.Matches {
			source := match.Source
			if source.Owner != snapshot.Owner || source.ID == "" || source.Sequence <= 0 || source.Sequence > snapshot.Watermark || source.Availability == memory.HistoryPruned {
				add("search_source_invalid")
				add("search_scan_incomplete")
				continue
			}
			visible, unknown := memory.HistoryVisibility(source.Times, currentTime)
			if unknown {
				add("search_time_unknown")
			}
			if !visible {
				add("search_time_hidden")
				continue
			}
			input.Matches = append(input.Matches, match)
		}
		if !page.More {
			break
		}
		offset = page.NextOffset
	}
	if len(input.Matches) == 0 && len(input.Diagnostics) == 0 {
		add("search_no_match")
	}
	return input
}

// Bound the join itself so a large fact never creates an unbounded query copy.
func historyEventQuery(event *protocol.GameEvent, maxChars int) string {
	var text strings.Builder
	count := 0
	for _, fact := range event.GetContextFacts() {
		if fact.GetText() == "" {
			continue
		}
		if count > 0 && count < maxChars {
			text.WriteByte(' ')
			count++
		}
		for _, r := range fact.GetText() {
			if count >= maxChars {
				return text.String()
			}
			text.WriteRune(r)
			count++
		}
		if count >= maxChars {
			break
		}
	}
	return text.String()
}

func searchReadErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "search_read_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "search_read_cancelled"
	}
	return "search_read_failed"
}
