package memory

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSQLiteSearchDefaultIndexCapacityBoundaries(t *testing.T) {
	for _, dimension := range []string{"text", "fragments", "terms"} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/%d", dimension, delta), func(t *testing.T) {
				s, db := sqliteSearchFixture(t)
				b := testHistoryBatch("default-capacity")
				wantFragments, wantTerms := 1, 1
				switch dimension {
				case "text":
					b.Event.Facts[0].Text = strings.Repeat("a", (256<<10)+delta)
				case "fragments":
					wantFragments, wantTerms = 1024+delta, 1024+delta
					b.Event.Facts = make([]SourceContextFact, wantFragments)
					for i := range b.Event.Facts {
						b.Event.Facts[i].Text = "7319"
					}
				case "terms":
					wantTerms = 16384 + delta
					var text strings.Builder
					for i := 0; i < wantTerms; i++ {
						fmt.Fprintf(&text, "term%05d ", i)
					}
					b.Event.Facts[0].Text = text.String()
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				start := time.Now()
				source, err := s.AppendHistory(ctx, b)
				elapsed := time.Since(start)
				if err != nil || elapsed >= 5*time.Second {
					t.Fatalf("default write deadline: elapsed=%v error=%v", elapsed, err)
				}
				t.Logf("append elapsed=%v raw_bytes=%d", elapsed, source.Bytes)
				var status, fingerprint, signature string
				if err := db.QueryRow(`SELECT status,content_fingerprint,index_signature FROM history_index_sources WHERE source_id=?`, source.ID).Scan(&status, &fingerprint, &signature); err != nil {
					t.Fatal(err)
				}
				wantStatus := "ready"
				if delta > 0 {
					wantStatus, wantFragments, wantTerms = "capacity_exceeded", 0, 0
				}
				if status != wantStatus || fingerprint != source.Fingerprint || signature == "" {
					t.Fatalf("index state: %s %s %s", status, fingerprint, signature)
				}
				var fragments, terms int
				if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM history_fragments),(SELECT COUNT(*) FROM history_terms)`).Scan(&fragments, &terms); err != nil || fragments != wantFragments || terms != wantTerms {
					t.Fatalf("index rows: got %d/%d, want %d/%d, %v", fragments, terms, wantFragments, wantTerms, err)
				}
				raw, err := s.ReadHistorySource(context.Background(), source.Owner, source.ID)
				if err != nil || raw.Batch == nil || !reflect.DeepEqual(raw.Batch.Event.Facts, b.Event.Facts) || raw.Fingerprint != source.Fingerprint || raw.Availability != HistoryAvailable {
					t.Fatalf("complete raw preservation: %v", err)
				}
			})
		}
	}
}

func TestSQLiteSearchDefaultScanCapacityBoundaries(t *testing.T) {
	for _, count := range []int{511, 512, 513} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, _ := sqliteSearchFixture(t)
			b := testHistoryBatch("scan-capacity")
			b.Event.Facts = make([]SourceContextFact, count)
			for i := range b.Event.Facts {
				b.Event.Facts[i] = SourceContextFact{Text: "7319", ActorEntityID: "player", Kind: "utterance"}
			}
			source := sqliteSearchAppend(t, s, b)
			start := time.Now()
			page, err := s.SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), Query: "7319"})
			t.Logf("search elapsed=%v scanned=%d bytes=%d", time.Since(start), page.Scanned, page.Bytes)
			if err != nil || page.Scanned != min(512, count) || len(page.Matches) != min(512, count) || page.Bytes <= source.Bytes || page.Bytes > DefaultHistorySearchLimits().Bytes || page.NextOffset != min(512, count) || page.More != (count > 512) || slices.Contains(page.Diagnostics, "scan_incomplete") != (count > 512) {
				t.Fatalf("default scan: scanned=%d matches=%d bytes=%d next=%d more=%t diagnostics=%v error=%v", page.Scanned, len(page.Matches), page.Bytes, page.NextOffset, page.More, page.Diagnostics, err)
			}
		})
	}
}

func TestSQLiteSearchFutureCandidatesBeyondHistoryPage(t *testing.T) {
	s, _ := sqliteSearchFixture(t)
	b := testHistoryBatch("older-visible")
	b.Event.GameTime = &GameTimeSnapshot{Tick: 1, PresentFields: gameTimeTick}
	older := sqliteSearchAppend(t, s, b)
	for i := 0; i < 70; i++ {
		b := testHistoryBatch(fmt.Sprintf("newer-future-%d", i))
		b.Event.GameTime = &GameTimeSnapshot{Tick: 100, PresentFields: gameTimeTick}
		sqliteSearchAppend(t, s, b)
	}
	page, err := s.SearchHistory(context.Background(), HistorySearchRequest{Snapshot: sqliteSearchSnapshot(t, s), CurrentTime: &GameTimeSnapshot{Tick: 10, PresentFields: gameTimeTick}, Query: "7319"})
	if err != nil || len(page.Matches) != 1 || page.Matches[0].Source.ID != older.ID || page.Scanned != 71 || page.More || !slices.Contains(page.Diagnostics, "time_hidden") || slices.Contains(page.Diagnostics, "scan_incomplete") {
		t.Fatalf("future candidates: scanned=%d matches=%d diagnostics=%v error=%v", page.Scanned, len(page.Matches), page.Diagnostics, err)
	}
}
