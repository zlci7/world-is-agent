package memory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type summarySQLCall struct {
	query string
	args  []any
}

func TestResolveSummaryTimesRejectsCancellationBeforeReadingHeads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	access := summaryAccess{sources: func(context.Context, []string, *summaryScanBudget) ([]HistorySource, error) {
		called = true
		return nil, errors.New("unexpected head read")
	}}
	_, err := resolveSummaryTimes(ctx, HistorySnapshot{}, []HistorySourceRef{{ID: "source"}}, access, &summaryScanBudget{bytes: 1024})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled resolver read heads: called=%t err=%v", called, err)
	}
}

type summarySQLRecorder struct {
	sqliteConnector
	calls []summarySQLCall
}

func (s *summarySQLRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	s.calls = append(s.calls, summarySQLCall{query, args})
	return s.sqliteConnector.QueryRowContext(ctx, query, args...)
}

func (s *summarySQLRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	s.calls = append(s.calls, summarySQLCall{query, args})
	return s.sqliteConnector.QueryContext(ctx, query, args...)
}

func TestSQLiteSummaryHeadBatchUsesSourcePointLookups(t *testing.T) {
	ctx := context.Background()
	store := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	fixture := newSummaryCapacityFixture(t, store, 256, 0)
	ids := make([]string, len(fixture.checkpoint.Sources))
	for i, ref := range fixture.checkpoint.Sources {
		ids[len(ids)-1-i] = ref.ID
	}
	snapshot := summaryTestSnapshot(t, store, fixture.checkpoint.Owner)
	conn, closeConn, err := store.openHistoryConn(ctx, fixture.checkpoint.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	recorder := &summarySQLRecorder{sqliteConnector: conn}
	heads, err := readSQLiteSummaryHeadBatch(ctx, recorder, snapshot, ids, &summaryScanBudget{bytes: 1 << 20})
	if err != nil || len(heads) != len(ids) || heads[0].ID != ids[0] || heads[len(heads)-1].ID != ids[len(ids)-1] {
		t.Fatalf("source order changed: %+v %v", heads, err)
	}
	if len(recorder.calls) != 2 {
		t.Fatalf("SQL calls per bounded batch = %d, want 2", len(recorder.calls))
	}
	for _, call := range recorder.calls {
		rows, err := conn.QueryContext(ctx, "EXPLAIN QUERY PLAN "+call.query, call.args...)
		if err != nil {
			t.Fatal(err)
		}
		pointLookup := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			t.Log(detail)
			if strings.Contains(detail, "source_id=?") {
				pointLookup = true
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil || !pointLookup {
			t.Fatalf("source batch did not use bounded point lookups: %v", err)
		}
	}
}
