package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/model"
)

func TestSQLiteSearchLiteralOriginalFields(t *testing.T) {
	ctx := context.Background()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	search, ok := any(s).(HistorySearchStore)
	if !ok {
		t.Fatal("SQLite history does not implement indexed search")
	}
	b := testHistoryBatch("literal")
	b.Event.Facts[0].Text = "\u9493\u9c7c\u65f6\uff0c7319 LinUS"
	b.Terminal.Reason = "unsearchableterminal"
	b.Steps = []HistoryStep{{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{Name: "unsearchabletool", Arguments: map[string]any{"note": "\u9493\u9c7c"}}}}}}
	source, err := s.AppendHistory(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.BeginHistorySnapshot(ctx, b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseHistorySnapshot(snap)
	page, err := search.SearchHistory(ctx, HistorySearchRequest{Snapshot: snap, Query: "\u9493\u9c7c 7319 LINUS", Limits: HistorySearchLimits{Bytes: 4096}})
	if err != nil || len(page.Matches) != 2 {
		t.Fatalf("literal search: %+v, %v", page, err)
	}
	first := page.Matches[0]
	if first.Source.ID != source.ID || first.Source.Batch != nil || first.Score != 3 || first.Field.Text != b.Event.Facts[0].Text || first.Field.ActorID != "player" || first.Span != (HistoryTextSpan{Path: "/event/facts/0/Text", End: 14}) {
		t.Fatalf("verified original: %+v", first)
	}
	if page.Matches[1].Score != 1 || page.Matches[1].Field.Kind != "tool_argument" || page.Matches[1].Field.ActorID != "" || page.More || page.Bytes <= source.Bytes {
		t.Fatalf("field attribution or repeated source read: %+v", page)
	}
	for _, query := range []string{"unsearchableterminal", "unsearchabletool", "' OR 1=1 --"} {
		page, err := search.SearchHistory(ctx, HistorySearchRequest{Snapshot: snap, Query: query})
		if err != nil || len(page.Matches) != 0 {
			t.Fatalf("query %q: %+v, %v", query, page, err)
		}
	}
}

func sqliteSearcher(t *testing.T, s *SQLiteHistoryStore) HistorySearchStore {
	t.Helper()
	search, ok := any(s).(HistorySearchStore)
	if !ok {
		t.Fatal("SQLite history does not implement indexed search")
	}
	return search
}

func sqliteRebuild(t *testing.T, s *SQLiteHistoryStore, request HistoryRebuildRequest) (HistoryMaintenanceResult, error) {
	t.Helper()
	rebuild, ok := any(s).(interface {
		RebuildHistoryIndex(context.Context, HistoryRebuildRequest) (HistoryMaintenanceResult, error)
	})
	if !ok {
		t.Fatal("SQLite history does not implement index rebuild")
	}
	return rebuild.RebuildHistoryIndex(context.Background(), request)
}

func sqliteSearchFixture(t *testing.T, options ...HistoryStoreOption) (*SQLiteHistoryStore, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{}, options...)
	b := testHistoryBatch("fixture")
	snap, err := s.BeginHistorySnapshot(context.Background(), b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	s.ReleaseHistorySnapshot(snap)
	return s, historyTestDB(t, root, b)
}

func sqliteSearchAppend(t *testing.T, s *SQLiteHistoryStore, b HistoryBatch) HistorySource {
	t.Helper()
	source, err := s.AppendHistory(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func sqliteSearchSnapshot(t *testing.T, s *SQLiteHistoryStore) HistorySnapshot {
	t.Helper()
	snap, err := s.BeginHistorySnapshot(context.Background(), testHistoryBatch("").Owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.ReleaseHistorySnapshot(snap) })
	return snap
}

func searchExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSearchScopeWatermarkAndFutureCandidates(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	search := sqliteSearcher(t, s)
	b := testHistoryBatch("old")
	b.Event.Facts[0].Text = "7319"
	b.Event.GameTime = &GameTimeSnapshot{Tick: 1, PresentFields: gameTimeTick}
	old := sqliteSearchAppend(t, s, b)
	for i := 0; i < 9; i++ {
		b := testHistoryBatch(fmt.Sprintf("future-%d", i))
		b.Event.GameTime = &GameTimeSnapshot{Tick: 20, PresentFields: gameTimeTick}
		sqliteSearchAppend(t, s, b)
	}
	unknown := sqliteSearchAppend(t, s, testHistoryBatch("unknown"))
	for _, dimension := range []string{"game", "world", "entity"} {
		b := testHistoryBatch(dimension)
		switch dimension {
		case "game":
			b.Owner.GameID = "other-game"
		case "world":
			b.Owner.WorldID = "other-world"
		case "entity":
			b.Owner.EntityID = "other-entity"
		}
		sqliteSearchAppend(t, s, b)
	}
	snap := sqliteSearchSnapshot(t, s)
	sqliteSearchAppend(t, s, testHistoryBatch("after-snapshot"))
	request := HistorySearchRequest{Snapshot: snap, Query: "7319", CurrentTime: &GameTimeSnapshot{Tick: 5, PresentFields: gameTimeTick}}
	page, err := search.SearchHistory(context.Background(), request)
	if err != nil || len(page.Matches) != 2 || page.Matches[0].Source.ID != unknown.ID || page.Matches[1].Source.ID != old.ID || page.Scanned != 11 || page.More || !slices.Contains(page.Diagnostics, "time_hidden") || !slices.Contains(page.Diagnostics, "time_unknown") {
		t.Fatalf("snapshot/future: %+v %v", page, err)
	}
	request.Limits.ScanCandidates = 5
	page, err = search.SearchHistory(context.Background(), request)
	if err != nil || page.Scanned != 5 || len(page.Matches) != 1 || !page.More || page.NextOffset != 5 || !slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("scan bound: %+v %v", page, err)
	}
	request.Offset = 5
	page, err = search.SearchHistory(context.Background(), request)
	if err != nil || page.Scanned != 0 || len(page.Matches) != 0 || !slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("total scan bound: %+v %v", page, err)
	}
	s.ReleaseHistorySnapshot(snap)
	if _, err := search.SearchHistory(context.Background(), request); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("released snapshot: %v", err)
	}
}

func TestSQLiteSearchRanksAllFieldsAndOffset(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	search := sqliteSearcher(t, s)
	b := testHistoryBatch("rank")
	b.Event.Facts = []SourceContextFact{{Text: "alpha beta"}, {Text: "alpha beta"}, {Text: "alpha"}, {Text: "alpha"}, {Text: "alpha"}, {Text: "alpha"}, {Text: "alpha"}}
	old := sqliteSearchAppend(t, s, b)
	b = testHistoryBatch("new")
	b.Event.Facts[0].Text = "alpha"
	newer := sqliteSearchAppend(t, s, b)
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "ALPHA beta alpha"}
	page, err := search.SearchHistory(context.Background(), request)
	if err != nil || len(page.Matches) != 8 || page.More || page.NextOffset != 8 || page.Bytes <= old.Bytes+newer.Bytes || page.Bytes > DefaultHistorySearchLimits().Bytes {
		t.Fatalf("candidate page: %+v %v", page, err)
	}
	if page.Matches[0].Score != 2 || page.Matches[0].Field.Path != "/event/facts/0/Text" || page.Matches[1].Field.Path != "/event/facts/1/Text" || page.Matches[2].Source.ID != newer.ID || !reflect.DeepEqual(page.Matches[0].MatchedTerms, []string{"alpha", "beta"}) {
		t.Fatalf("ranking: %+v", page.Matches)
	}
	request.Offset = 2
	request.Limits.ScanCandidates = 4
	page, err = search.SearchHistory(context.Background(), request)
	if err != nil || page.Scanned != 2 || len(page.Matches) != 2 || page.Matches[0].Source.ID != newer.ID || page.NextOffset != 4 || !page.More {
		t.Fatalf("offset: %+v %v", page, err)
	}
}

func TestSQLiteSearchRejectsCorruptIndex(t *testing.T) {
	for _, mutation := range []struct{ name, query string }{
		{"text", `UPDATE history_fragments SET original_text='forged 7319'`},
		{"actor", `UPDATE history_fragments SET actor_id='forged'`},
		{"kind", `UPDATE history_fragments SET field_kind='forged'`},
		{"start", `UPDATE history_fragments SET start_rune=1`},
		{"end", `UPDATE history_fragments SET end_rune=1`},
		{"path", `UPDATE history_fragments SET field_path='/forged'`},
		{"term", `UPDATE history_terms SET term='unicorn' WHERE term='7319'`},
		{"fingerprint", `UPDATE history_index_sources SET content_fingerprint='forged'`},
		{"signature", `UPDATE history_index_sources SET index_signature='old-version'`},
		{"missing", `DELETE FROM history_index_sources`},
		{"raw", `UPDATE session_history SET payload_json='{}',payload_bytes=2`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			s, db := sqliteSearchFixture(t)
			sqliteSearchAppend(t, s, testHistoryBatch("corrupt"))
			searchExec(t, db, mutation.query)
			query := "7319"
			if mutation.name == "term" {
				query = "unicorn"
			}
			page, err := sqliteSearcher(t, s).SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: query})
			if err != nil || len(page.Matches) != 0 || !slices.Contains(page.Diagnostics, "scan_incomplete") {
				t.Fatalf("corruption: %+v %v", page, err)
			}
		})
	}
}

func TestSQLiteSearchRawByteBoundariesAndPagination(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	search := sqliteSearcher(t, s)
	first := sqliteSearchAppend(t, s, testHistoryBatch("same-length-1"))
	second := sqliteSearchAppend(t, s, testHistoryBatch("same-length-2"))
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"}
	headBytes := sqliteSearchFixtureHeadBytes(t, second)
	field := HistoryTextField{Path: "/event/facts/0/Text", ActorID: "player", Kind: "utterance", Text: testHistoryBatch("").Event.Facts[0].Text}
	outputBytes := headBytes + len(field.Path) + len(field.Text) + len(field.ActorID) + len(field.Kind) + 24 + len("7319")
	firstPageBytes := 2*40 + len(second.ID) + second.Bytes + headBytes + len(field.Path) + outputBytes
	for _, delta := range []int{-1, 0, 1} {
		request.Limits.Bytes = firstPageBytes + delta
		page, err := search.SearchHistory(context.Background(), request)
		if err != nil || page.Bytes > request.Limits.Bytes || (delta < 0 && len(page.Matches) != 0) || (delta >= 0 && (len(page.Matches) != 1 || page.Bytes != firstPageBytes || page.NextOffset != 1 || !page.More)) {
			t.Fatalf("bytes delta=%d: %+v %v", delta, page, err)
		}
	}
	request.Offset = 1
	request.Limits.Bytes = firstPageBytes - 40
	page, err := search.SearchHistory(context.Background(), request)
	if err != nil || len(page.Matches) != 1 || page.Matches[0].Source.ID != first.ID || page.More || page.NextOffset != 2 {
		t.Fatalf("resume: %+v %v", page, err)
	}
	searchExec(t, db, `UPDATE session_history SET payload_json=?,payload_bytes=1 WHERE source_id=?`, strings.Repeat("x", 4096), first.ID)
	request.Limits.Bytes = 10
	page, err = search.SearchHistory(context.Background(), request)
	if err != nil || page.Bytes != 0 || len(page.Matches) != 0 || !slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("actual blob bytes must be checked before raw fetch: %+v %v", page, err)
	}
}

func sqliteSearchFixtureHeadBytes(t *testing.T, source HistorySource) int {
	t.Helper()
	times, err := json.Marshal(source.Times)
	if err != nil {
		t.Fatal(err)
	}
	return len(source.ID) + len(source.BatchKey) + len(source.Owner.GameID) + len(source.Owner.WorldID) + len(source.Owner.EntityID) + len(HistoryKindTerminal) + len(source.Fingerprint) + len(times) + len(source.Availability) + len(source.LegacyMemoryID) + 32
}

func TestSQLiteSearchQueryAndRequestLimits(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	search := sqliteSearcher(t, s)
	sqliteSearchAppend(t, s, testHistoryBatch("limits"))
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"}
	for _, query := range []string{"", "!?", "\u9493"} {
		request.Query = query
		page, err := search.SearchHistory(context.Background(), request)
		if err != nil || page.Scanned != 0 || !slices.Contains(page.Diagnostics, "no_valid_query") {
			t.Fatalf("no query: %+v %v", page, err)
		}
	}
	request.Query = "missing 7319"
	request.Limits.QueryTerms = 1
	page, err := search.SearchHistory(context.Background(), request)
	if err != nil || len(page.Matches) != 0 || !slices.Contains(page.Diagnostics, "no_match") {
		t.Fatalf("query terms: %+v %v", page, err)
	}
	request.Query = "7319"
	request.Limits.QueryChars = 3
	page, err = search.SearchHistory(context.Background(), request)
	if err != nil || len(page.Matches) != 0 {
		t.Fatalf("query chars: %+v %v", page, err)
	}
	for _, limits := range []HistorySearchLimits{{QueryChars: -1}, {QueryTerms: -1}, {ScanCandidates: -1}, {Bytes: -1}, {TimeoutMS: -1}} {
		request.Limits = limits
		if _, err := search.SearchHistory(context.Background(), request); err == nil {
			t.Fatalf("negative limits accepted: %+v", limits)
		}
	}
	request.Limits = HistorySearchLimits{}
	request.Offset = -1
	if _, err := search.SearchHistory(context.Background(), request); err == nil {
		t.Fatal("negative offset accepted")
	}
	request.Offset = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := search.SearchHistory(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestSQLiteSearchExhaustedOffsetDoesNotInventMoreCandidates(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	sqliteSearchAppend(t, s, testHistoryBatch("single"))
	request := HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319", Offset: 10, Limits: HistorySearchLimits{ScanCandidates: 5}}
	page, err := s.SearchHistory(context.Background(), request)
	if err != nil || page.More || page.NextOffset != 5 || page.Scanned != 0 || page.Bytes != 0 {
		t.Fatalf("exhausted candidate range: %+v %v", page, err)
	}
}

func TestSQLiteSearchCapacityNoOpAndChangedLimits(t *testing.T) {
	s, db := sqliteSearchFixture(t, WithHistoryIndexLimits(HistoryIndexLimits{TextBytes: 4}))
	b := testHistoryBatch("capacity")
	b.Event.Facts[0].Text = "7319 x"
	source := sqliteSearchAppend(t, s, b)
	var status string
	if err := db.QueryRow(`SELECT status FROM history_index_sources WHERE source_id=?`, source.ID).Scan(&status); err != nil || status != "capacity_exceeded" {
		t.Fatalf("capacity status: %s %v", status, err)
	}
	for _, table := range []string{"history_fragments", "history_terms"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial capacity index: %s %d %v", table, count, err)
		}
	}
	page, err := sqliteSearcher(t, s).SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"})
	if err != nil || len(page.Matches) != 0 || !slices.Contains(page.Diagnostics, "index_capacity_exceeded") || !slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("incomplete capacity: %+v %v", page, err)
	}
	searchExec(t, db, `CREATE TRIGGER reject_reindex BEFORE INSERT ON history_index_sources BEGIN SELECT RAISE(ABORT,'unexpected reindex'); END`)
	result, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: b.Owner, Force: true})
	if err != nil || result.Processed != 0 || result.Bytes != 0 || result.More {
		t.Fatalf("known impossible force no-op: %+v %v", result, err)
	}
	searchExec(t, db, `DROP TRIGGER reject_reindex`)
	s.indexLimits.TextBytes = 6
	result, err = sqliteRebuild(t, s, HistoryRebuildRequest{Owner: b.Owner})
	if err != nil || result.Processed != 1 || result.Bytes != source.Bytes {
		t.Fatalf("changed capacity: %+v %v", result, err)
	}
	raw, err := s.ReadHistorySource(context.Background(), b.Owner, source.ID)
	if err != nil || !reflect.DeepEqual(raw, source) {
		t.Fatalf("rebuild changed raw source: %+v %v", raw, err)
	}
	page, err = sqliteSearcher(t, s).SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"})
	if err != nil || len(page.Matches) != 1 || slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("rebuilt search: %+v %v", page, err)
	}
}

func TestSQLiteSearchWriterUsesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	writer, ok := any(s).(interface {
		indexHistorySource(context.Context, *sql.Tx, HistorySource) error
	})
	if !ok {
		t.Fatal("SQLite history has no transactional index writer")
	}
	b := testHistoryBatch("writer")
	conn, closeConn, err := s.openHistoryConn(ctx, b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	data, key, fp, err := CanonicalHistoryBatch(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		if fail {
			if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_term BEFORE INSERT ON history_terms BEGIN SELECT RAISE(ABORT,'term failure'); END`); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := beginSQLiteWriteTransaction(ctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		source, err := insertHistorySource(ctx, tx, b, data, key, fp, time.Unix(100, 0), "")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		err = writer.indexHistorySource(ctx, tx, source)
		if (err != nil) != fail || (fail && !strings.Contains(err.Error(), "term failure")) {
			_ = tx.Rollback()
			t.Fatalf("writer error: %v (fail=%v)", err, fail)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("writer took ownership of transaction: %v", err)
		}
		for _, table := range []string{"session_history", "history_index_sources", "history_fragments", "history_terms"} {
			var count int
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rollback %s: count=%d error=%v", table, count, err)
			}
		}
	}
}

func TestSQLiteSearchRebuildBoundedProgressAndIdempotence(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	owner := testHistoryBatch("").Owner
	if _, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	var sources []HistorySource
	for i := 0; i < 3; i++ {
		sources = append(sources, sqliteSearchAppend(t, s, testHistoryBatch(fmt.Sprintf("rebuild-%d", i))))
	}
	searchExec(t, db, `DELETE FROM history_terms`)
	searchExec(t, db, `DELETE FROM history_fragments`)
	searchExec(t, db, `DELETE FROM history_index_sources`)
	request := HistoryRebuildRequest{Owner: owner, Limits: HistoryMaintenanceLimits{Sources: 1}}
	for i, source := range sources {
		result, err := sqliteRebuild(t, s, request)
		if err != nil || result.Processed != 1 || result.Bytes != source.Bytes || result.NextAfter != source.Sequence || result.More != (i < 2) || !reflect.DeepEqual(result.SourceIDs, []string{source.ID}) {
			t.Fatalf("rebuild page %d: %+v %v", i, result, err)
		}
		request.AfterSequence = result.NextAfter
	}
	result, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: owner})
	if err != nil || result.Processed != 0 || result.More {
		t.Fatalf("ready no-op: %+v %v", result, err)
	}
	for i := 0; i < 2; i++ {
		result, err = sqliteRebuild(t, s, HistoryRebuildRequest{Owner: owner, Force: true})
		if err != nil || result.Processed != 3 || result.More {
			t.Fatalf("force: %+v %v", result, err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM history_fragments`).Scan(&count); err != nil || count != 3 {
			t.Fatalf("duplicate fragments: %d %v", count, err)
		}
		for _, source := range sources {
			raw, err := s.ReadHistorySource(context.Background(), owner, source.ID)
			if err != nil || !reflect.DeepEqual(raw, source) {
				t.Fatalf("source altered: %+v %v", raw, err)
			}
		}
	}
}

func TestSQLiteSearchRebuildBytesAndSQLRollback(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	first := sqliteSearchAppend(t, s, testHistoryBatch("bytes-1"))
	second := sqliteSearchAppend(t, s, testHistoryBatch("bytes-2"))
	searchExec(t, db, `DELETE FROM history_index_sources`)
	readLimit := 2*40 + len(first.ID) + sqliteSearchFixtureHeadBytes(t, first) + first.Bytes
	request := HistoryRebuildRequest{Owner: first.Owner, Limits: HistoryMaintenanceLimits{Bytes: readLimit}}
	result, err := sqliteRebuild(t, s, request)
	if err != nil || result.Processed != 1 || result.Bytes != first.Bytes || result.ReadBytes != readLimit || result.ReadSources != 1 || result.NextAfter != first.Sequence || !result.More {
		t.Fatalf("maintenance byte boundary: %+v %v", result, err)
	}
	request.AfterSequence = result.NextAfter
	result, err = sqliteRebuild(t, s, request)
	if err != nil || result.Processed != 1 || result.NextAfter != second.Sequence || result.More {
		t.Fatalf("resume maintenance: %+v %v", result, err)
	}
	searchExec(t, db, `DELETE FROM history_index_sources`)
	searchExec(t, db, `DELETE FROM history_terms`)
	searchExec(t, db, `DELETE FROM history_fragments`)
	searchExec(t, db, `CREATE TRIGGER fail_second_index BEFORE INSERT ON history_index_sources WHEN NEW.source_id='`+second.ID+`' BEGIN SELECT RAISE(ABORT,'rebuild failure'); END`)
	result, err = sqliteRebuild(t, s, HistoryRebuildRequest{Owner: first.Owner})
	if err == nil || !strings.Contains(err.Error(), "rebuild failure") || result.Processed != 0 || len(result.SourceIDs) != 0 || result.ReadSources != 2 || result.ReadBytes <= first.Bytes+second.Bytes {
		t.Fatalf("rebuild transaction error: %+v %v", result, err)
	}
	for _, table := range []string{"history_index_sources", "history_fragments", "history_terms"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial rebuild %s: %d %v", table, count, err)
		}
	}
	for _, source := range []HistorySource{first, second} {
		if raw, err := s.ReadHistorySource(context.Background(), first.Owner, source.ID); err != nil || !reflect.DeepEqual(raw, source) {
			t.Fatalf("rollback raw: %+v %v", raw, err)
		}
	}
}

func TestSQLiteSearchPrunedSourcesCannotResurrect(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	b := testHistoryBatch("pruned")
	source := sqliteSearchAppend(t, s, b)
	searchExec(t, db, `UPDATE session_history SET payload_json=NULL,availability='pruned' WHERE source_id=?`, source.ID)
	result, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: b.Owner, Force: true})
	if err != nil || result.Processed != 0 || result.Bytes != 0 || result.More {
		t.Fatalf("rebuild pruned: %+v %v", result, err)
	}
	retry, err := s.AppendHistory(context.Background(), b)
	if err != nil || retry.Batch != nil || retry.Availability != HistoryPruned || retry.ID != source.ID || !retry.CreatedAt.Equal(source.CreatedAt) || retry.Fingerprint != source.Fingerprint || retry.Sequence != source.Sequence {
		t.Fatalf("retry resurrected raw: %+v %v", retry, err)
	}
	page, err := sqliteSearcher(t, s).SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"})
	if err != nil || len(page.Matches) != 0 || page.Scanned != 0 || !slices.Contains(page.Diagnostics, "no_match") || slices.Contains(page.Diagnostics, "source_pruned") || slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("pruned stale terms: %+v %v", page, err)
	}
}

func TestSQLiteSearchIndexCapacityBoundaries(t *testing.T) {
	for _, dimension := range []string{"text", "fragments", "terms"} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/%d", dimension, delta), func(t *testing.T) {
				limits := DefaultHistoryIndexLimits()
				switch dimension {
				case "text":
					limits.TextBytes = 9 + delta
				case "fragments":
					limits.Fragments = 2 + delta
				case "terms":
					limits.TermAssociations = 3 + delta
				}
				s, db := sqliteSearchFixture(t, WithHistoryIndexLimits(limits))
				b := testHistoryBatch("boundary")
				b.Event.Facts = []SourceContextFact{{Text: "a a b"}, {Text: "7319"}}
				source := sqliteSearchAppend(t, s, b)
				var status string
				if err := db.QueryRow(`SELECT status FROM history_index_sources WHERE source_id=?`, source.ID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if (status == "capacity_exceeded") != (delta < 0) {
					t.Fatalf("boundary status: %s", status)
				}
				var fragments, terms int
				if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM history_fragments),(SELECT COUNT(*) FROM history_terms)`).Scan(&fragments, &terms); err != nil || (delta < 0 && (fragments != 0 || terms != 0)) || (delta >= 0 && (fragments != 2 || terms != 3)) {
					t.Fatalf("index rows: %d %d %v", fragments, terms, err)
				}
			})
		}
	}
}

func TestSQLiteSearchReadsSnapshotWhileWriterIsActive(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	source := sqliteSearchAppend(t, s, testHistoryBatch("reader"))
	snap := sqliteSearchSnapshot(t, s)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE session_history SET created_at=created_at WHERE source_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	page, err := sqliteSearcher(t, s).SearchHistory(context.Background(), HistorySearchRequest{Snapshot: snap, Query: "7319", Limits: HistorySearchLimits{TimeoutMS: 250}})
	if err != nil || len(page.Matches) != 1 || page.Matches[0].Source.ID != source.ID {
		t.Fatalf("reader blocked on writer: %+v %v", page, err)
	}
}

func TestSQLiteSearchConcurrentAppendKeepsSnapshot(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	search := sqliteSearcher(t, s)
	first := sqliteSearchAppend(t, s, testHistoryBatch("frozen"))
	snap := sqliteSearchSnapshot(t, s)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 12; i++ {
			if _, err := s.AppendHistory(context.Background(), testHistoryBatch(fmt.Sprintf("concurrent-%d", i))); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 12; i++ {
		page, err := search.SearchHistory(context.Background(), HistorySearchRequest{Snapshot: snap, Query: "7319"})
		if err != nil || len(page.Matches) != 1 || page.Matches[0].Source.ID != first.ID || page.Scanned != 1 || page.More {
			t.Errorf("concurrent search: %+v %v", page, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSearchRebuildMaintenanceLimits(t *testing.T) {
	s, db := sqliteSearchFixture(t)
	source := sqliteSearchAppend(t, s, testHistoryBatch("maintenance"))
	searchExec(t, db, `DELETE FROM history_index_sources`)
	result, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: source.Owner, Limits: HistoryMaintenanceLimits{Bytes: source.Bytes - 1}})
	if err != nil || result.Bytes != 0 || len(result.SourceIDs) != 0 || !slices.Contains(result.Diagnostics, "scan_incomplete") {
		t.Fatalf("oversized raw read: %+v %v", result, err)
	}
	for _, limits := range []HistoryMaintenanceLimits{{Sources: -1}, {Bytes: -1}, {TimeoutMS: -1}, {LockTimeoutMS: -1}, {TimeoutMS: 10, LockTimeoutMS: 11}} {
		if _, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: source.Owner, Limits: limits}); err == nil {
			t.Fatalf("invalid maintenance limits accepted: %+v", limits)
		}
	}
	if _, err := sqliteRebuild(t, s, HistoryRebuildRequest{Owner: source.Owner, AfterSequence: -1}); err == nil {
		t.Fatal("negative maintenance cursor accepted")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE session_history SET created_at=created_at`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err = sqliteRebuild(t, s, HistoryRebuildRequest{Owner: source.Owner, Limits: HistoryMaintenanceLimits{LockTimeoutMS: 20, TimeoutMS: 1000}})
	if err == nil || time.Since(start) >= 800*time.Millisecond || result.Processed != 0 {
		t.Fatalf("maintenance lock wait: %+v %v (%v)", result, err, time.Since(start))
	}
}

func TestSQLiteSearchAppendSQLFailurePreservesAtomicity(t *testing.T) {
	for _, table := range []string{"history_index_sources", "history_fragments", "history_terms"} {
		t.Run(table, func(t *testing.T) {
			s, db := sqliteSearchFixture(t)
			searchExec(t, db, "CREATE TRIGGER reject_append BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'append index failure'); END")
			_, err := s.AppendHistory(context.Background(), testHistoryBatch("failing-append"))
			if err == nil || !strings.Contains(err.Error(), "append index failure") {
				t.Fatalf("append error: %v", err)
			}
			for _, table := range []string{"session_history", "history_index_sources", "history_fragments", "history_terms"} {
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial append %s: %d %v", table, count, err)
				}
			}
		})
	}
}
