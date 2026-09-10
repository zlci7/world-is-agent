package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"gameagent/runtime/internal/model"
)

func TestHistoryRoundTripPreservesLargeArgumentNumbers(t *testing.T) {
	b := testHistoryBatch("large-number")
	b.Steps = []HistoryStep{{Index: 1, Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call", Name: "generic.record", Arguments: map[string]any{"number": json.Number("9007199254740993")}}}}}}
	cloned, err := CloneHistoryBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	_, _, first, _ := CanonicalHistoryBatch(b, 8<<20)
	_, _, second, _ := CanonicalHistoryBatch(cloned, 8<<20)
	if first != second {
		t.Fatal("cloning changed an original numeric value")
	}
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	source, err := s.AppendHistory(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadHistorySource(context.Background(), b.Owner, source.ID)
	if err != nil || read.Fingerprint != first {
		t.Fatalf("round trip: %+v %v", read, err)
	}
}

func TestHistoryLimitsRejectEveryNegativeCapacity(t *testing.T) {
	defaults := DefaultHistoryLimits()
	v := reflect.ValueOf(defaults)
	for i := 0; i < v.NumField(); i++ {
		copy := defaults
		reflect.ValueOf(&copy).Elem().Field(i).SetInt(-1)
		if err := copy.WithDefaults().Validate(); err == nil {
			t.Fatalf("negative %s accepted", v.Type().Field(i).Name)
		}
	}
	if got := (HistoryLimits{}).WithDefaults(); got != defaults {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestSQLiteHistoryPageCountsAndByteBounds(t *testing.T) {
	ctx := context.Background()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	first, err := s.AppendHistory(ctx, testHistoryBatch("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendHistory(ctx, testHistoryBatch("second"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.BeginHistorySnapshot(ctx, first.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseHistorySnapshot(snapshot)
	p, err := s.ReadHistorySnapshot(ctx, snapshot, 0, HistoryReadLimits{Records: 10, Bytes: second.Bytes})
	if err != nil || len(p.Sources) != 1 || p.Sources[0].ID != second.ID || !p.More || p.NextBefore != second.Sequence {
		t.Fatalf("first page %+v %v", p, err)
	}
	p, err = s.ReadHistorySnapshot(ctx, snapshot, p.NextBefore, HistoryReadLimits{Records: 10, Bytes: first.Bytes})
	if err != nil || len(p.Sources) != 1 || p.Sources[0].ID != first.ID || p.More {
		t.Fatalf("second page %+v %v", p, err)
	}
	p, err = s.ReadHistorySnapshot(ctx, snapshot, 0, HistoryReadLimits{Records: 1, Bytes: second.Bytes - 1})
	if err != nil || len(p.Sources) != 0 || p.Scanned != 1 || !p.More || len(p.Diagnostics) == 0 || p.NextBefore != second.Sequence {
		t.Fatalf("oversized source must advance cursor: %+v %v", p, err)
	}
	s.ReleaseHistorySnapshot(snapshot)
	if _, err = s.ReadHistorySnapshot(ctx, snapshot, 0, HistoryReadLimits{}); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("released snapshot: %v", err)
	}
}

func TestSQLiteHistoryAppendRollbackLeavesNoBatchOrWriter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := testHistoryBatch("first")
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	first, err := s.AppendHistory(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	db := historyTestDB(t, root, b)
	if _, err = db.Exec("CREATE TRIGGER fail_history AFTER INSERT ON session_history BEGIN SELECT RAISE(ABORT,'injected'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendHistory(ctx, testHistoryBatch("failed")); err == nil {
		t.Fatal("append succeeded")
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM session_history").Scan(&count); err != nil || count != 1 {
		t.Fatalf("half batch: %d %v", count, err)
	}
	if _, err = db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadHistorySource(ctx, b.Owner, first.ID); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryVisibilityIncludesFutureAlongsideUnknown(t *testing.T) {
	current := &GameTimeSnapshot{Hour: 10, PresentFields: gameTimeCalendarFields}
	future := &GameTimeSnapshot{Hour: 12, PresentFields: gameTimeCalendarFields}
	visible, unknown := HistoryVisibility([]HistoryTime{{Source: "missing"}, {Source: "event", Value: future}}, current)
	if visible || !unknown {
		t.Fatalf("got visible=%v unknown=%v", visible, unknown)
	}
	visible, unknown = HistoryVisibility([]HistoryTime{{Source: "event", Value: current}}, current)
	if !visible || unknown {
		t.Fatal("equal explicit-zero calendar must be visible")
	}
}
