package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/model"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSQLiteSearchSharedPathReadBudget(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	b := testHistoryBatch("path-amplification")
	b.Event.Facts = nil
	values := make([]string, 128)
	for i := range values {
		values[i] = "7319"
	}
	b.Steps = []HistoryStep{{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call", Name: "tool", Arguments: map[string]any{strings.Repeat("k", 16<<10): values}}}}}}
	source := sqliteSearchAppend(t, s, b)
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319", Limits: HistorySearchLimits{Bytes: 64 << 10}}
	page, err := s.SearchHistory(context.Background(), request)
	returnedBytes := 0
	for _, match := range page.Matches {
		returnedBytes += len(match.Field.Path) + len(match.Field.Text) + len(match.Field.Kind) + len(match.Field.ActorID)
	}
	t.Logf("raw=%d returned_fields=%d matches=%d read=%d diagnostics=%v", source.Bytes, returnedBytes, len(page.Matches), page.Bytes, page.Diagnostics)
	if err != nil || page.Bytes > request.Limits.Bytes || returnedBytes+source.Bytes > page.Bytes || len(page.Matches) >= 128 || !page.More || !slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("shared read budget: matches=%d returned=%d bytes=%d diagnostics=%v error=%v", len(page.Matches), returnedBytes, page.Bytes, page.Diagnostics, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM history_index_sources WHERE source_id=?`, source.ID).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("valid source indexing changed: %s %v", status, err)
	}
	raw, err := s.ReadHistorySource(context.Background(), source.Owner, source.ID)
	if err != nil || raw.Batch == nil || raw.Fingerprint != source.Fingerprint {
		t.Fatalf("raw lost: %v", err)
	}
}

func TestSQLiteSearchDeadlineIncludesLeaseWait(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	sqliteSearchAppend(t, s, testHistoryBatch("lease-contention"))
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319", Limits: HistorySearchLimits{TimeoutMS: 20}}
	s.mu.Lock()
	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.mu.Unlock()
		close(released)
	}()
	t.Cleanup(func() { <-released })
	start := time.Now()
	page, err := s.SearchHistory(context.Background(), request)
	elapsed := time.Since(start)
	t.Logf("lease wait elapsed=%v error=%v", elapsed, err)
	if elapsed > 80*time.Millisecond || !errors.Is(err, context.DeadlineExceeded) || len(page.Matches) != 0 {
		t.Fatalf("deadline excludes lease wait: elapsed=%v error=%v", elapsed, err)
	}
}

func TestSQLiteSearchRebuildAccountsMetadata(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	source := sqliteSearchAppend(t, s, testHistoryBatch("read-accounting"))
	result, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{Owner: source.Owner, Force: true, Limits: HistoryMaintenanceLimits{Bytes: source.Bytes}})
	if err != nil || len(result.SourceIDs) != 0 || result.ReadBytes > source.Bytes || result.ReadSources != 0 || !slices.Contains(result.Diagnostics, "scan_incomplete") {
		t.Fatalf("metadata was outside maintenance budget: %+v %v", result, err)
	}
	result, err = s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{Owner: source.Owner, Force: true})
	if err != nil || result.Processed != 1 || result.Bytes != source.Bytes || result.ReadSources != 1 || result.ReadBytes <= source.Bytes {
		t.Fatalf("maintenance read usage: %+v %v", result, err)
	}
}

func TestSQLiteSearchRebuildAdvancesPastSourceThatCannotFitWithPageMetadata(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	largeBatch := testHistoryBatch("large-pending")
	largeBatch.Event.Facts[0].Text = strings.Repeat("x", 2048)
	large := sqliteSearchAppend(t, s, largeBatch)
	small := sqliteSearchAppend(t, s, testHistoryBatch("small-pending"))
	searchExec(t, db, "DELETE FROM history_index_sources")
	fullSize, err := historySourceReadSize(context.Background(), db, large.Owner, large.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{
		Owner:  large.Owner,
		Limits: HistoryMaintenanceLimits{Bytes: fullSize},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NextAfter < large.Sequence || !slices.Contains(result.Diagnostics, "rebuild_bytes_incomplete") {
		t.Fatalf("unserviceable source did not advance the cursor: %+v", result)
	}
	var indexed int
	if err := db.QueryRow("SELECT COUNT(*) FROM history_index_sources WHERE source_id=?", small.ID).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed == 0 && !result.More {
		t.Fatalf("later source was neither indexed nor left resumable: %+v", result)
	}
	var status, signature string
	if err := db.QueryRow("SELECT status,index_signature FROM history_index_sources WHERE source_id=?", large.ID).Scan(&status, &signature); err != nil || status != "maintenance_exceeded" || signature != historyMaintenanceIndexSignature(s.indexLimits, fullSize) {
		t.Fatalf("maintenance capacity state was not persisted: status=%q signature=%q err=%v", status, signature, err)
	}
	if result.More {
		resumed, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{
			Owner: large.Owner, AfterSequence: result.NextAfter,
			Limits: HistoryMaintenanceLimits{Bytes: fullSize},
		})
		if err != nil || len(resumed.SourceIDs) != 1 || resumed.SourceIDs[0] != small.ID {
			t.Fatalf("later source remained starved: %+v %v", resumed, err)
		}
	}
	repeated, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{
		Owner:  large.Owner,
		Limits: HistoryMaintenanceLimits{Bytes: fullSize},
	})
	if err != nil || repeated.Processed != 0 || repeated.More || slices.Contains(repeated.Diagnostics, "rebuild_bytes_incomplete") {
		t.Fatalf("unchanged maintenance capacity retried a known impossible source: %+v %v", repeated, err)
	}
	retried, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{
		Owner:  large.Owner,
		Limits: HistoryMaintenanceLimits{Bytes: fullSize + 256},
	})
	if err != nil || len(retried.SourceIDs) != 1 || retried.SourceIDs[0] != large.ID {
		t.Fatalf("increased maintenance capacity did not retry the source: %+v %v", retried, err)
	}
}

func TestSQLiteSearchRebuildMaintenanceCapacityMarkerRollsBack(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	batch := testHistoryBatch("marker-rollback")
	batch.Event.Facts[0].Text = strings.Repeat("x", 2048)
	source := sqliteSearchAppend(t, s, batch)
	searchExec(t, db, "DELETE FROM history_index_sources")
	fullSize, err := historySourceReadSize(context.Background(), db, source.Owner, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	searchExec(t, db, `CREATE TRIGGER fail_maintenance_marker BEFORE INSERT ON history_index_sources BEGIN SELECT RAISE(ABORT,'marker failure'); END`)
	result, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{
		Owner:  source.Owner,
		Limits: HistoryMaintenanceLimits{Bytes: fullSize},
	})
	if err == nil || !strings.Contains(err.Error(), "marker failure") || result.Processed != 0 || result.ReadBytes == 0 {
		t.Fatalf("marker failure did not roll back: %+v %v", result, err)
	}
	var statuses int
	if err := db.QueryRow("SELECT COUNT(*) FROM history_index_sources").Scan(&statuses); err != nil || statuses != 0 {
		t.Fatalf("failed marker survived rollback: count=%d err=%v", statuses, err)
	}
	stored, err := s.ReadHistorySource(context.Background(), source.Owner, source.ID)
	if err != nil || stored.Availability != HistoryAvailable || stored.Batch == nil {
		t.Fatalf("failed marker changed original: %+v %v", stored, err)
	}
}

func TestSQLiteSearchRebuildExactReadBoundaryProcessesSource(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	source := sqliteSearchAppend(t, s, testHistoryBatch("exact-read-boundary"))
	searchExec(t, db, "DELETE FROM history_index_sources")
	fullSize, err := historySourceReadSize(context.Background(), db, source.Owner, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	limit := 5*8 + len(source.ID) + fullSize
	result, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{
		Owner:  source.Owner,
		Limits: HistoryMaintenanceLimits{Bytes: limit},
	})
	if err != nil || len(result.SourceIDs) != 1 || result.SourceIDs[0] != source.ID || result.ReadBytes != limit {
		t.Fatalf("exact admitted read did not rebuild: %+v %v", result, err)
	}
}

func TestSQLiteSearchCandidatePreflightUsesNumericHeaders(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	b := testHistoryBatch("candidate-preflight")
	b.Event.Facts = nil
	b.Steps = []HistoryStep{{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{Arguments: map[string]any{strings.Repeat("k", 16<<10): []string{"7319", "7319"}}}}}}}
	sqliteSearchAppend(t, s, b)
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"}
	conn, closeConn, err := s.openHistoryConn(context.Background(), b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	for _, limit := range []int{39, 40, 41} {
		budget := &historySearchReadBudget{limit: limit}
		headers, more, err := s.historySearchCandidates(context.Background(), conn, request, request.Limits.WithDefaults(), []string{"7319"}, budget)
		if err != nil || !more || budget.used > limit || (limit < 40 && len(headers) != 0) || (limit >= 40 && (len(headers) != 1 || budget.used != 40 || headers[0].pathBytes <= 16<<10 || headers[0].termRowID == 0)) {
			t.Fatalf("numeric preflight limit=%d: headers=%+v bytes=%d more=%t error=%v", limit, headers, budget.used, more, err)
		}
	}
}

func TestSQLiteSearchPreflightsSourceHeaders(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	source := sqliteSearchAppend(t, s, testHistoryBatch("source-head"))
	snap := sqliteSearchSnapshot(t, s)
	searchExec(t, db, `UPDATE session_history SET batch_key=? WHERE source_id=?`, strings.Repeat("x", 64<<10), source.ID)
	page, err := s.SearchHistory(context.Background(), HistorySearchRequest{Snapshot: snap, Query: "7319", Limits: HistorySearchLimits{Bytes: 32 << 10}})
	if err != nil || len(page.Matches) != 0 || page.Bytes >= 32<<10 || !slices.Contains(page.Diagnostics, "scan_incomplete") || slices.Contains(page.Diagnostics, "read_failed") {
		t.Fatalf("source head read before reserve: %+v %v", page, err)
	}
	result, err := s.RebuildHistoryIndex(context.Background(), HistoryRebuildRequest{Owner: source.Owner, Force: true, Limits: HistoryMaintenanceLimits{Bytes: 32 << 10}})
	if err != nil || result.ReadSources != 0 || result.ReadBytes >= 32<<10 || len(result.SourceIDs) != 0 || !slices.Contains(result.Diagnostics, "scan_incomplete") {
		t.Fatalf("rebuild head read before reserve: %+v %v", result, err)
	}
}

func TestSQLiteSearchPreservesFullDefaultTextCapacity(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	b := testHistoryBatch("full-original")
	b.Event.Facts[0].Text = "7319 " + strings.Repeat("a", (256<<10)-5)
	source := sqliteSearchAppend(t, s, b)
	page, err := s.SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"})
	if err != nil || len(page.Matches) != 1 || page.Matches[0].Field.Text != b.Event.Facts[0].Text || page.Matches[0].Span.End != 256<<10 || page.Matches[0].Source.Batch != nil || page.Bytes < source.Bytes+(256<<10) || page.More || slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("full original: matches=%d bytes=%d diagnostics=%v error=%v", len(page.Matches), page.Bytes, page.Diagnostics, err)
	}
}

func TestSQLiteSearchFieldLookupMatchesCanonicalExtraction(t *testing.T) {
	b := testHistoryBatch("pointer")
	output, err := structpb.NewStruct(map[string]any{"~/": []any{"result", float64(3)}})
	if err != nil {
		t.Fatal(err)
	}
	b.Steps = []HistoryStep{{
		Decision:   model.ModelDecision{ToolCalls: []model.ToolCall{{Name: "private-name", Arguments: map[string]any{"a/b~c": []any{"hello", map[string]any{"": "world", "0": "number key"}}, "count": 3}}}},
		Executions: []HistoryExecution{{Call: model.ToolCall{Arguments: map[string]any{"x": "execution"}}, ActionResult: &protocol.ActionResult{Output: output}}},
	}}
	b.Legacy = &Record{SourceContextFacts: []SourceContextFact{{Text: "legacy", ActorEntityID: "legacy-actor", Kind: "utterance"}}}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	for _, expected := range HistoryTextFields(b) {
		field, ok := historySearchTextField(root, expected.Path)
		if !ok || !reflect.DeepEqual(field, expected) {
			t.Fatalf("canonical pointer %q: got %+v (%t), want %+v", expected.Path, field, ok, expected)
		}
	}
	for _, path := range []string{"/event/Type", "/event/facts/0/ActorEntityID", "/steps/00/decision/ToolCalls/0/Arguments/a~1b~0c/0", "/steps/0/decision/ToolCalls/0/Name", "/steps/0/decision/ToolCalls/0/Arguments/a~1b~c/0", "/steps/0/decision/ToolCalls/0/Arguments/count", "/steps/0/decision/ToolCalls/0/Arguments/a~1b~0c/01"} {
		if field, ok := historySearchTextField(root, path); ok {
			t.Fatalf("noncanonical or private pointer accepted: %q %+v", path, field)
		}
	}
}

func TestSQLiteSearchExitLeaseGuardUsesRemainingDeadline(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	snap := sqliteSearchSnapshot(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	time.Sleep(30 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Now()
	valid, err := s.validSnapshotContext(ctx, snap)
	if valid || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 60*time.Millisecond {
		t.Fatalf("exit lease guard extended deadline: valid=%t elapsed=%v error=%v", valid, time.Since(start), err)
	}
}
