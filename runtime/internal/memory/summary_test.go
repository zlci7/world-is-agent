package memory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gameagent/runtime/internal/session"
)

type summaryTestStore interface {
	HistoryStore
	SummaryStore
}

func summaryBackends(t *testing.T, limits HistoryLimits, run func(*testing.T, summaryTestStore)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, limits))
	})
	t.Run("memory", func(t *testing.T) {
		run(t, NewInMemoryHistoryStore(limits))
	})
}

func summaryTestTime(hour int32) *GameTimeSnapshot {
	return &GameTimeSnapshot{Year: 1, Season: 1, Day: 1, Hour: hour, PresentFields: gameTimeCalendarFields}
}

func summaryTestSource(t *testing.T, s HistoryStore, turn string, hour int32) HistorySource {
	t.Helper()
	b := testHistoryBatch(turn)
	b.Event.GameTime = summaryTestTime(hour)
	source, err := s.AppendHistory(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func summaryTestSnapshot(t *testing.T, s HistoryStore, owner session.AgentSessionKey) HistorySnapshot {
	t.Helper()
	snap, err := s.BeginHistorySnapshot(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.ReleaseHistorySnapshot(snap) })
	return snap
}

func summaryTestRef(source HistorySource) HistorySourceRef {
	return HistorySourceRef{ID: source.ID, Sequence: source.Sequence, Fingerprint: source.Fingerprint}
}

func summaryTestCommit(t *testing.T, s summaryTestStore, parent string, sources ...HistorySource) SummaryCheckpoint {
	t.Helper()
	snap := summaryTestSnapshot(t, s, sources[0].Owner)
	request := SummaryCommit{Snapshot: snap, ExpectedRevision: snap.SummaryRevision, ParentID: parent, Version: "test-v1", Text: "The player does not like rain."}
	for _, source := range sources {
		request.Sources = append(request.Sources, summaryTestRef(source))
	}
	checkpoint, err := s.CommitSummary(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func summaryTestRead(t *testing.T, s summaryTestStore, owner session.AgentSessionKey, current *GameTimeSnapshot, limits SummaryReadLimits) SummaryRead {
	t.Helper()
	result, err := s.ReadSummary(context.Background(), summaryTestSnapshot(t, s, owner), current, limits)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func summaryHasDiagnostic(read SummaryRead, want string) bool {
	for _, diagnostic := range read.Diagnostics {
		if diagnostic == want {
			return true
		}
	}
	return false
}

func TestSummaryCanonicalCumulativeNoncontiguousCoverage(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		first := summaryTestSource(t, s, "first", 8)
		_ = summaryTestSource(t, s, "uncovered", 9)
		third := summaryTestSource(t, s, "third", 10)
		parent := summaryTestCommit(t, s, "", third, third)
		child := summaryTestCommit(t, s, parent.ID, first, third, first)
		want := []HistorySourceRef{summaryTestRef(first), summaryTestRef(third)}
		if !reflect.DeepEqual(child.Sources, want) || child.ParentID != parent.ID || child.Revision <= parent.Revision || child.ID == parent.ID {
			t.Fatalf("cumulative coverage: %+v", child)
		}
		if len(child.Times) != 2 || child.Times[0].Value.Hour != 8 || child.Times[1].Value.Hour != 10 {
			t.Fatalf("source times: %+v", child.Times)
		}
		read := summaryTestRead(t, s, first.Owner, summaryTestTime(10), SummaryReadLimits{})
		if read.Checkpoint == nil || !reflect.DeepEqual(*read.Checkpoint, child) || len(read.Diagnostics) != 0 {
			t.Fatalf("read: %+v", read)
		}
	})
}

func TestSummaryInheritsFutureTimesAndFallsBack(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		early := summaryTestSource(t, s, "early", 8)
		visible := summaryTestCommit(t, s, "", early)
		future := summaryTestSource(t, s, "future", 12)
		hidden := summaryTestCommit(t, s, visible.ID, future)
		rollback := summaryTestSource(t, s, "rollback", 9)
		latest := summaryTestCommit(t, s, hidden.ID, rollback)
		read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{})
		if read.Checkpoint == nil || read.Checkpoint.ID != visible.ID || !summaryHasDiagnostic(read, "summary_time_hidden") {
			t.Fatalf("future inheritance/fallback: %+v", read)
		}
		read = summaryTestRead(t, s, early.Owner, summaryTestTime(12), SummaryReadLimits{})
		if read.Checkpoint == nil || read.Checkpoint.ID != latest.ID {
			t.Fatalf("time catches up: %+v", read)
		}
	})
}

func TestSummaryOlderVisibleParentUsesCurrentPublicationRevision(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		early := summaryTestSource(t, s, "early", 8)
		parent := summaryTestCommit(t, s, "", early)
		future := summaryTestSource(t, s, "future", 12)
		head := summaryTestCommit(t, s, parent.ID, future)
		rollback := summaryTestSource(t, s, "rollback", 9)
		snap := summaryTestSnapshot(t, s, early.Owner)
		read, err := s.ReadSummary(context.Background(), snap, summaryTestTime(10), SummaryReadLimits{})
		if err != nil || read.Checkpoint == nil || read.Checkpoint.ID != parent.ID {
			t.Fatalf("selected parent: %+v %v", read, err)
		}
		request := SummaryCommit{Snapshot: snap, ExpectedRevision: head.Revision, ParentID: parent.ID, Version: "v1", Text: "Rollback history.", Sources: []HistorySourceRef{summaryTestRef(rollback)}}
		child, err := s.CommitSummary(context.Background(), request)
		if err != nil || child.Revision <= head.Revision || !reflect.DeepEqual(child.Sources, []HistorySourceRef{summaryTestRef(early), summaryTestRef(rollback)}) {
			t.Fatalf("publish from older parent: %+v %v", child, err)
		}
		if _, err := s.CommitSummary(context.Background(), request); !errors.Is(err, ErrSummaryConflict) {
			t.Fatalf("stale publication: %v", err)
		}
		request.Snapshot = summaryTestSnapshot(t, s, early.Owner)
		if _, err := s.CommitSummary(context.Background(), request); !errors.Is(err, ErrSummaryConflict) {
			t.Fatalf("expected revision differs from captured revision: %v", err)
		}
		read, err = s.ReadSummary(context.Background(), snap, summaryTestTime(12), SummaryReadLimits{})
		if err != nil || read.Checkpoint == nil || read.Checkpoint.ID != head.ID {
			t.Fatalf("frozen summary revision: %+v %v", read, err)
		}
	})
}

func TestSummaryOwnerSnapshotAndSourceValidation(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		first := summaryTestSource(t, s, "first", 8)
		snap := summaryTestSnapshot(t, s, first.Owner)
		later := summaryTestSource(t, s, "later", 9)
		request := SummaryCommit{Snapshot: snap, Version: "v1", Text: "Original.", Sources: []HistorySourceRef{summaryTestRef(first)}}
		for name, change := range map[string]func(*SummaryCommit){
			"missing source":  func(r *SummaryCommit) { r.Sources[0].ID = "missing" },
			"fingerprint":     func(r *SummaryCommit) { r.Sources[0].Fingerprint = "forged" },
			"sequence":        func(r *SummaryCommit) { r.Sources[0].Sequence++ },
			"after watermark": func(r *SummaryCommit) { r.Sources[0] = summaryTestRef(later) },
			"conflicting duplicate": func(r *SummaryCommit) {
				other := r.Sources[0]
				other.Fingerprint = "forged"
				r.Sources = append(r.Sources, other)
			},
			"missing parent": func(r *SummaryCommit) { r.ParentID = "missing" },
			"fake snapshot":  func(r *SummaryCommit) { r.Snapshot.Watermark++ },
			"empty coverage": func(r *SummaryCommit) { r.Sources = nil },
		} {
			t.Run(name, func(t *testing.T) {
				r := request
				r.Sources = append([]HistorySourceRef(nil), request.Sources...)
				change(&r)
				if _, err := s.CommitSummary(context.Background(), r); err == nil {
					t.Fatal("invalid publication accepted")
				}
			})
		}
		valid, err := s.CommitSummary(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"game", "world", "entity"} {
			b := testHistoryBatch("other")
			switch field {
			case "game":
				b.Owner.GameID = "other"
			case "world":
				b.Owner.WorldID = "other"
			case "entity":
				b.Owner.EntityID = "other"
			}
			other, err := s.AppendHistory(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			otherSnap := summaryTestSnapshot(t, s, other.Owner)
			read, err := s.ReadSummary(context.Background(), otherSnap, nil, SummaryReadLimits{})
			if err != nil || read.Checkpoint != nil {
				t.Fatalf("%s isolation: %+v %v", field, read, err)
			}
			r := SummaryCommit{Snapshot: otherSnap, ParentID: valid.ID, Version: "v1", Text: "Other.", Sources: []HistorySourceRef{summaryTestRef(other)}}
			if _, err := s.CommitSummary(context.Background(), r); err == nil {
				t.Fatalf("%s parent isolation", field)
			}
			r.ParentID = ""
			r.Sources = []HistorySourceRef{summaryTestRef(first)}
			if _, err := s.CommitSummary(context.Background(), r); err == nil {
				t.Fatalf("%s source isolation", field)
			}
			r.Sources = []HistorySourceRef{summaryTestRef(other)}
			if _, err := s.CommitSummary(context.Background(), r); err != nil {
				t.Fatalf("%s owner revision isolation: %v", field, err)
			}
		}
		s.ReleaseHistorySnapshot(snap)
		if _, err := s.ReadSummary(context.Background(), snap, nil, SummaryReadLimits{}); !errors.Is(err, ErrInvalidHistory) {
			t.Fatalf("released snapshot: %v", err)
		}
	})
}

func TestSummaryOutputAndInputBoundsAreAtomic(t *testing.T) {
	summaryBackends(t, HistoryLimits{SummarySources: 2}, func(t *testing.T, s summaryTestStore) {
		source := summaryTestSource(t, s, "first", 8)
		parent := summaryTestCommit(t, s, "", source)
		snap := summaryTestSnapshot(t, s, source.Owner)
		request := SummaryCommit{Snapshot: snap, ExpectedRevision: parent.Revision, ParentID: parent.ID, Version: "v1", Text: "Valid.", Sources: []HistorySourceRef{summaryTestRef(source)}}
		for name, change := range map[string]func(*SummaryCommit){
			"empty":                func(r *SummaryCommit) { r.Text = "" },
			"whitespace":           func(r *SummaryCommit) { r.Text = " \n\t" },
			"version":              func(r *SummaryCommit) { r.Version = " " },
			"default token limit":  func(r *SummaryCommit) { r.Text = strings.Repeat("x", 8193) },
			"explicit token limit": func(r *SummaryCommit) { r.Text = "abcdefghijk"; r.MaxOutputTokens = 2 },
			"CJK token limit":      func(r *SummaryCommit) { r.Text = "\u96e8\u5929\u597d"; r.MaxOutputTokens = 2 },
			"input refs": func(r *SummaryCommit) {
				r.Sources = []HistorySourceRef{summaryTestRef(source), summaryTestRef(source), summaryTestRef(source)}
			},
		} {
			t.Run(name, func(t *testing.T) {
				r := request
				change(&r)
				if _, err := s.CommitSummary(context.Background(), r); err == nil {
					t.Fatal("invalid output/input accepted")
				}
			})
		}
		read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{})
		if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID {
			t.Fatalf("failed commits changed published coverage: %+v", read)
		}
		request.Text = strings.Repeat("x", 8192)
		if _, err := s.CommitSummary(context.Background(), request); err != nil {
			t.Fatalf("exact default token bound: %v", err)
		}
	})
}

func TestSummaryCoverageBudgetAppliesAcrossCandidateAttempts(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		early := summaryTestSource(t, s, "early", 8)
		parent := summaryTestCommit(t, s, "", early)
		future := summaryTestSource(t, s, "future", 12)
		_ = summaryTestCommit(t, s, parent.ID, future)
		for _, tc := range []struct {
			name       string
			limits     SummaryReadLimits
			wantParent bool
			incomplete bool
		}{
			{"complete fallback", SummaryReadLimits{Sources: 3}, true, false},
			{"cumulative exhaustion", SummaryReadLimits{Sources: 2}, false, true},
			{"oversized candidate fallback", SummaryReadLimits{Sources: 1}, true, true},
			{"checkpoint exhaustion", SummaryReadLimits{Checkpoints: 1}, false, true},
			{"byte exhaustion", SummaryReadLimits{Bytes: 1}, false, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), tc.limits)
				if (read.Checkpoint != nil) != tc.wantParent || (read.Checkpoint != nil && read.Checkpoint.ID != parent.ID) || summaryHasDiagnostic(read, "summary_scan_incomplete") != tc.incomplete {
					t.Fatalf("bounded coverage: %+v", read)
				}
			})
		}
	})
}

func TestSummaryReferenceBytesAllowSmallerCheckpointFallback(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		early := summaryTestSource(t, s, "early", 8)
		parent := summaryTestCommit(t, s, "", early)
		var sources []HistorySource
		for i := 0; i < 16; i++ {
			sources = append(sources, summaryTestSource(t, s, fmt.Sprintf("future-%d", i), 12))
		}
		_ = summaryTestCommit(t, s, "", sources...)
		read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{Bytes: 2000})
		if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_scan_incomplete") {
			t.Fatalf("reference byte fallback: %+v", read)
		}
	})
}

func TestSummaryInvalidReferencesConsumeCoverageBudget(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		early := summaryTestSource(t, s, "early", 8)
		parent := summaryTestCommit(t, s, "", early)
		later := summaryTestSource(t, s, "later", 9)
		child := summaryTestCommit(t, s, parent.ID, later)
		switch backend := s.(type) {
		case *SQLiteHistoryStore:
			db := historyTestDB(t, backend.base.options.Root, testHistoryBatch("early"))
			if _, err := db.Exec("UPDATE summary_sources SET source_sequence=source_sequence+100 WHERE summary_id=? AND source_id=?", child.ID, later.ID); err != nil {
				t.Fatal(err)
			}
		case *InMemoryHistoryStore:
			backend.mu.Lock()
			corrupted := cloneSummaryCheckpoint(backend.summaries[child.ID])
			corrupted.Sources[1].Sequence += 100
			backend.summaries[child.ID] = corrupted
			backend.mu.Unlock()
		}
		read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{Sources: 2})
		if read.Checkpoint != nil || !summaryHasDiagnostic(read, "summary_scan_incomplete") || !summaryHasDiagnostic(read, "summary_coverage_invalid") {
			t.Fatalf("invalid references escaped scan budget: %+v", read)
		}
		read = summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{Sources: 3})
		if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID {
			t.Fatalf("validated fallback with sufficient budget: %+v", read)
		}
	})
}

func TestSummaryConfiguredAndHardBounds(t *testing.T) {
	summaryBackends(t, HistoryLimits{SummarySources: 20000, SummaryCheckpoints: 1000}, func(t *testing.T, s summaryTestStore) {
		source := summaryTestSource(t, s, "source", 12)
		request := SummaryCommit{Snapshot: summaryTestSnapshot(t, s, source.Owner), Version: "v1", Text: "Valid.", Sources: make([]HistorySourceRef, 16385)}
		for i := range request.Sources {
			request.Sources[i] = summaryTestRef(source)
		}
		if _, err := s.CommitSummary(context.Background(), request); !errors.Is(err, ErrHistoryCapacity) {
			t.Fatalf("hard source bound: %v", err)
		}
		early := summaryTestSource(t, s, "early", 8)
		_ = summaryTestCommit(t, s, "", early)
		for i := 0; i < 64; i++ {
			_ = summaryTestCommit(t, s, "", source)
		}
		read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{Sources: 20000, Checkpoints: 1000})
		if read.Checkpoint != nil || !summaryHasDiagnostic(read, "summary_scan_incomplete") {
			t.Fatalf("hard checkpoint scan bound: %+v", read)
		}
	})
	summaryBackends(t, HistoryLimits{SummaryCheckpoints: 1}, func(t *testing.T, s summaryTestStore) {
		early := summaryTestSource(t, s, "early", 8)
		parent := summaryTestCommit(t, s, "", early)
		future := summaryTestSource(t, s, "future", 12)
		_ = summaryTestCommit(t, s, parent.ID, future)
		read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{Checkpoints: 1000})
		if read.Checkpoint != nil || !summaryHasDiagnostic(read, "summary_scan_incomplete") {
			t.Fatalf("configured checkpoint scan bound: %+v", read)
		}
	})
}

func TestSummaryInheritedReferenceConflictAndCapacity(t *testing.T) {
	summaryBackends(t, HistoryLimits{SummarySources: 2}, func(t *testing.T, s summaryTestStore) {
		first := summaryTestSource(t, s, "first", 8)
		second := summaryTestSource(t, s, "second", 9)
		parent := summaryTestCommit(t, s, "", first, second)
		third := summaryTestSource(t, s, "third", 10)
		request := SummaryCommit{Snapshot: summaryTestSnapshot(t, s, first.Owner), ExpectedRevision: parent.Revision, ParentID: parent.ID, Version: "v1", Text: "Valid.", Sources: []HistorySourceRef{summaryTestRef(third)}}
		if _, err := s.CommitSummary(context.Background(), request); !errors.Is(err, ErrHistoryCapacity) {
			t.Fatalf("cumulative capacity: %v", err)
		}
		request.Sources[0] = summaryTestRef(first)
		request.Sources[0].Fingerprint = "forged"
		if _, err := s.CommitSummary(context.Background(), request); !errors.Is(err, ErrSummaryConflict) {
			t.Fatalf("inherited fingerprint conflict: %v", err)
		}
		request.Sources = nil
		if checkpoint, err := s.CommitSummary(context.Background(), request); err != nil || !reflect.DeepEqual(checkpoint.Sources, parent.Sources) {
			t.Fatalf("parent-only coverage: %+v %v", checkpoint, err)
		}
	})
}

func TestSummaryUnknownIsDiagnosedAndCannotMaskFuture(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		b := testHistoryBatch("unknown")
		source, err := s.AppendHistory(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		parent := summaryTestCommit(t, s, "", source)
		read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{})
		if read.Checkpoint == nil || !summaryHasDiagnostic(read, "summary_time_unknown") {
			t.Fatalf("unknown: %+v", read)
		}
		future := summaryTestSource(t, s, "future", 12)
		_ = summaryTestCommit(t, s, parent.ID, future)
		read = summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{})
		if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_time_hidden") || !summaryHasDiagnostic(read, "summary_time_unknown") {
			t.Fatalf("mixed unknown/future: %+v", read)
		}
	})
}

func TestSummaryConcurrentPublicationHasOneWinner(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		source := summaryTestSource(t, s, "first", 8)
		request := SummaryCommit{Snapshot: summaryTestSnapshot(t, s, source.Owner), Version: "v1", Text: "Original.", Sources: []HistorySourceRef{summaryTestRef(source)}}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := s.CommitSummary(context.Background(), request)
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		wins, conflicts := 0, 0
		for err := range errs {
			if err == nil {
				wins++
			} else if errors.Is(err, ErrSummaryConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if wins != 1 || conflicts != 1 {
			t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
		}
	})
}

func TestSQLiteSummaryPersistsAndValidatesPrunedHistoryTimes(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1234, 567).UTC()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root, Now: func() time.Time { return now }}, HistoryLimits{})
	early := summaryTestSource(t, s, "early", 8)
	parent := summaryTestCommit(t, s, "", early)
	future := summaryTestSource(t, s, "future", 12)
	child := summaryTestCommit(t, s, parent.ID, future)
	if !child.CreatedAt.Equal(now) {
		t.Fatalf("configured clock: %v", child.CreatedAt)
	}
	db := historyTestDB(t, root, testHistoryBatch("early"))
	if _, err := db.Exec("UPDATE session_history SET payload_json=NULL,availability='pruned'"); err != nil {
		t.Fatal(err)
	}
	reopened := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	read := summaryTestRead(t, reopened, early.Owner, summaryTestTime(12), SummaryReadLimits{})
	if read.Checkpoint == nil || !reflect.DeepEqual(*read.Checkpoint, child) {
		t.Fatalf("reopen: %+v", read)
	}
	read = summaryTestRead(t, reopened, early.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_time_hidden") {
		t.Fatalf("pruned future still hidden: %+v", read)
	}
	rollback := summaryTestSource(t, reopened, "rollback", 9)
	_ = summaryTestCommit(t, reopened, child.ID, rollback)
	read = summaryTestRead(t, reopened, early.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID {
		t.Fatalf("inherit pruned future: %+v", read)
	}
}

func TestSQLiteSummaryInvalidCoverageCannotReturnCheckpoint(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE summary_sources SET content_fingerprint='forged' WHERE summary_id=?",
		"UPDATE summary_sources SET source_sequence=source_sequence+100 WHERE summary_id=?",
		"UPDATE summary_sources SET source_id='missing' WHERE summary_id=?",
		"DELETE FROM summary_sources WHERE summary_id=?",
	} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
			source := summaryTestSource(t, s, "first", 8)
			parent := summaryTestCommit(t, s, "", source)
			child := summaryTestCommit(t, s, parent.ID, source)
			db := historyTestDB(t, root, testHistoryBatch("first"))
			if _, err := db.Exec(mutation, child.ID); err != nil {
				t.Fatal(err)
			}
			read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{})
			if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_coverage_invalid") {
				t.Fatalf("invalid coverage: %+v", read)
			}
		})
	}
}

func summaryTestManifest(t *testing.T, refs []HistorySourceRef, times []HistoryTime) (string, string) {
	t.Helper()
	type sourceRef struct {
		ID          string `json:"id"`
		Sequence    int64  `json:"sequence"`
		Fingerprint string `json:"fingerprint"`
	}
	manifest := struct {
		Sources []sourceRef   `json:"sources"`
		Times   []HistoryTime `json:"times"`
	}{Times: times}
	for _, ref := range refs {
		manifest.Sources = append(manifest.Sources, sourceRef{ID: ref.ID, Sequence: ref.Sequence, Fingerprint: ref.Fingerprint})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	timesJSON, err := json.Marshal(times)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), string(timesJSON)
}

func TestSQLiteSummaryRejectsPartiallyDeletedFutureCoverage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	early := summaryTestSource(t, s, "early", 8)
	future := summaryTestSource(t, s, "future", 12)
	db := historyTestDB(t, root, testHistoryBatch("early"))
	for _, fixture := range []struct {
		id      string
		parent  string
		sources []HistorySource
	}{
		{"manifest-parent", "", []HistorySource{early}},
		{"manifest-child", "manifest-parent", []HistorySource{early, future}},
	} {
		var refs []HistorySourceRef
		var times []HistoryTime
		for _, source := range fixture.sources {
			refs = append(refs, summaryTestRef(source))
			times = append(times, source.Times...)
		}
		fingerprint, timesJSON := summaryTestManifest(t, refs, times)
		if _, err := db.Exec(`INSERT INTO context_summaries(summary_id,game_id,world_id,entity_id,parent_id,generation_version,summary_text,source_count,coverage_fingerprint,coverage_times_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, fixture.id, early.Owner.GameID, early.Owner.WorldID, early.Owner.EntityID, fixture.parent, "v1", "Committed source history.", len(refs), fingerprint, timesJSON, time.Unix(100, 0).UnixNano()); err != nil {
			t.Fatal(err)
		}
		for _, ref := range refs {
			if _, err := db.Exec(`INSERT INTO summary_sources(summary_id,source_id,source_sequence,content_fingerprint) VALUES(?,?,?,?)`, fixture.id, ref.ID, ref.Sequence, ref.Fingerprint); err != nil {
				t.Fatal(err)
			}
		}
	}
	snap := summaryTestSnapshot(t, s, early.Owner)
	read, err := s.ReadSummary(ctx, snap, summaryTestTime(12), SummaryReadLimits{})
	if err != nil || read.Checkpoint == nil || read.Checkpoint.ID != "manifest-child" {
		t.Fatalf("healthy manifest: %+v %v", read, err)
	}
	result, err := db.Exec("DELETE FROM summary_sources WHERE summary_id=? AND source_id=?", "manifest-child", future.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("deleted future link: %d %v", changed, err)
	}
	read, err = s.ReadSummary(ctx, snap, summaryTestTime(10), SummaryReadLimits{})
	if err != nil || read.Checkpoint == nil || read.Checkpoint.ID != "manifest-parent" || !summaryHasDiagnostic(read, "summary_coverage_invalid") {
		t.Fatalf("partial deletion exposed future summary: %+v %v", read, err)
	}
	request := SummaryCommit{Snapshot: snap, ExpectedRevision: snap.SummaryRevision, ParentID: "manifest-child", Version: "v1", Text: "New summary.", Sources: []HistorySourceRef{summaryTestRef(future)}}
	if _, err := s.CommitSummary(ctx, request); err == nil {
		t.Fatal("published from a partially deleted parent")
	}
}

func TestSQLiteSummaryPersistsCanonicalCoverageManifest(t *testing.T) {
	root := t.TempDir()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	first := summaryTestSource(t, s, "first", 8)
	_ = summaryTestSource(t, s, "uncovered", 9)
	last := summaryTestSource(t, s, "last", 12)
	parent := summaryTestCommit(t, s, "", last)
	child := summaryTestCommit(t, s, parent.ID, first, last, first)
	refs := []HistorySourceRef{summaryTestRef(first), summaryTestRef(last)}
	times := append(cloneHistoryTimes(first.Times), last.Times...)
	wantFingerprint, wantTimes := summaryTestManifest(t, refs, times)
	db := historyTestDB(t, root, testHistoryBatch("first"))
	var count int
	var fingerprint, timesJSON string
	if err := db.QueryRow("SELECT source_count,coverage_fingerprint,coverage_times_json FROM context_summaries WHERE summary_id=?", child.ID).Scan(&count, &fingerprint, &timesJSON); err != nil {
		t.Fatal(err)
	}
	if count != 2 || fingerprint != wantFingerprint || timesJSON != wantTimes {
		t.Fatalf("persisted manifest: count=%d fingerprint=%q times=%s", count, fingerprint, timesJSON)
	}
	reopened := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	read := summaryTestRead(t, reopened, first.Owner, summaryTestTime(12), SummaryReadLimits{})
	if read.Checkpoint == nil || !reflect.DeepEqual(*read.Checkpoint, child) || !reflect.DeepEqual(read.Checkpoint.Sources, refs) {
		t.Fatalf("manifest reopen: %+v", read)
	}
}

func TestSQLiteSummaryManifestAndHeadTimeMismatchFallsBack(t *testing.T) {
	for _, mutation := range []string{"count", "invalid count type", "fingerprint", "times", "malformed times", "consistent manifest with false times", "pruned head times", "equal-count reference replacement"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
			early := summaryTestSource(t, s, "early", 8)
			parent := summaryTestCommit(t, s, "", early)
			future := summaryTestSource(t, s, "future", 12)
			child := summaryTestCommit(t, s, parent.ID, future)
			rollback := summaryTestSource(t, s, "rollback", 9)
			db := historyTestDB(t, root, testHistoryBatch("early"))
			var err error
			switch mutation {
			case "count":
				_, err = db.Exec("UPDATE context_summaries SET source_count=1 WHERE summary_id=?", child.ID)
			case "invalid count type":
				_, err = db.Exec("UPDATE context_summaries SET source_count='invalid' WHERE summary_id=?", child.ID)
			case "fingerprint":
				_, err = db.Exec("UPDATE context_summaries SET coverage_fingerprint=? WHERE summary_id=?", strings.Repeat("0", 64), child.ID)
			case "times":
				_, err = db.Exec("UPDATE context_summaries SET coverage_times_json='[]' WHERE summary_id=?", child.ID)
			case "malformed times":
				_, err = db.Exec("UPDATE context_summaries SET coverage_times_json='[' WHERE summary_id=?", child.ID)
			case "consistent manifest with false times":
				times := cloneHistoryTimes(child.Times)
				times[len(times)-1].Value.Hour = 9
				fingerprint, timesJSON := summaryTestManifest(t, child.Sources, times)
				_, err = db.Exec("UPDATE context_summaries SET coverage_fingerprint=?,coverage_times_json=? WHERE summary_id=?", fingerprint, timesJSON, child.ID)
			case "pruned head times":
				times := cloneHistoryTimes(future.Times)
				times[0].Value.Hour = 9
				timesJSON, marshalErr := json.Marshal(times)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				_, err = db.Exec("UPDATE session_history SET payload_json=NULL,availability='pruned',times_json=? WHERE source_id=?", string(timesJSON), future.ID)
			case "equal-count reference replacement":
				_, err = db.Exec("UPDATE summary_sources SET source_id=?,source_sequence=?,content_fingerprint=? WHERE summary_id=? AND source_id=?", rollback.ID, rollback.Sequence, rollback.Fingerprint, child.ID, future.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			read := summaryTestRead(t, s, early.Owner, summaryTestTime(12), SummaryReadLimits{})
			if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_coverage_invalid") {
				t.Fatalf("inconsistent manifest injected: %+v", read)
			}
			request := SummaryCommit{Snapshot: summaryTestSnapshot(t, s, early.Owner), ExpectedRevision: child.Revision, ParentID: child.ID, Version: "v1", Text: "New summary.", Sources: []HistorySourceRef{summaryTestRef(rollback)}}
			if _, err := s.CommitSummary(context.Background(), request); err == nil {
				t.Fatal("inherited invalid parent manifest")
			}
			if snapshot := summaryTestSnapshot(t, s, early.Owner); snapshot.SummaryRevision != child.Revision {
				t.Fatalf("failed validation published revision %d", snapshot.SummaryRevision)
			}
		})
	}
}

func TestSQLiteSummaryManifestBytesAreBoundedBeforeParsing(t *testing.T) {
	root := t.TempDir()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	early := summaryTestSource(t, s, "early", 8)
	parent := summaryTestCommit(t, s, "", early)
	future := summaryTestSource(t, s, "future", 12)
	child := summaryTestCommit(t, s, parent.ID, future)
	db := historyTestDB(t, root, testHistoryBatch("early"))
	if _, err := db.Exec("UPDATE context_summaries SET coverage_times_json=? WHERE summary_id=?", "["+strings.Repeat(" ", 4096), child.ID); err != nil {
		t.Fatal(err)
	}
	read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{Bytes: 2000})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_scan_incomplete") || summaryHasDiagnostic(read, "summary_coverage_invalid") {
		t.Fatalf("manifest parsed outside header budget: %+v", read)
	}
}

func TestSQLiteSummaryManifestPublicationRollsBackWithSources(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var store *SQLiteHistoryStore
	var snapshot HistorySnapshot
	expireSnapshot := false
	store = NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root, Now: func() time.Time {
		if expireSnapshot {
			store.ReleaseHistorySnapshot(snapshot)
		}
		return time.Unix(100, 0).UTC()
	}}, HistoryLimits{})
	early := summaryTestSource(t, store, "early", 8)
	parent := summaryTestCommit(t, store, "", early)
	later := summaryTestSource(t, store, "later", 9)
	snapshot = summaryTestSnapshot(t, store, early.Owner)
	expireSnapshot = true
	request := SummaryCommit{Snapshot: snapshot, ExpectedRevision: parent.Revision, ParentID: parent.ID, Version: "v1", Text: "New summary.", Sources: []HistorySourceRef{summaryTestRef(later)}}
	if _, err := store.CommitSummary(ctx, request); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("inactive publication snapshot: %v", err)
	}
	db := historyTestDB(t, root, testHistoryBatch("early"))
	var summaries, sources, sourceCount int
	var fingerprint, timesJSON string
	if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM context_summaries),(SELECT COUNT(*) FROM summary_sources),source_count,coverage_fingerprint,coverage_times_json FROM context_summaries WHERE summary_id=?", parent.ID).Scan(&summaries, &sources, &sourceCount, &fingerprint, &timesJSON); err != nil {
		t.Fatal(err)
	}
	wantFingerprint, wantTimes := summaryTestManifest(t, parent.Sources, parent.Times)
	if summaries != 1 || sources != 1 || sourceCount != 1 || fingerprint != wantFingerprint || timesJSON != wantTimes {
		t.Fatalf("partial publication retained: summaries=%d sources=%d count=%d fingerprint=%s times=%s", summaries, sources, sourceCount, fingerprint, timesJSON)
	}
	read := summaryTestRead(t, store, early.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID {
		t.Fatalf("failed publication changed checkpoint: %+v", read)
	}
}

func TestSummaryReadUsesOnlySourceHeadBudget(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		batch := testHistoryBatch("large-source")
		batch.Event.GameTime = summaryTestTime(8)
		batch.Event.Facts[0].Text = strings.Repeat("x", 4<<10)
		source, err := s.AppendHistory(context.Background(), batch)
		if err != nil {
			t.Fatal(err)
		}
		if source.Bytes <= 2<<10 {
			t.Fatalf("fixture payload is only %d bytes", source.Bytes)
		}
		checkpoint := summaryTestCommit(t, s, "", source)
		read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{Bytes: 2 << 10})
		if read.Checkpoint == nil || !reflect.DeepEqual(*read.Checkpoint, checkpoint) || len(read.Diagnostics) != 0 {
			t.Fatalf("payload consumed summary metadata budget: %+v", read)
		}
	})
}

func TestSummaryIncrementalCommitUsesOnlySourceHeadBudget(t *testing.T) {
	limits := HistoryLimits{ScanBytes: 8 << 10, PageBytes: 8 << 10}
	summaryBackends(t, limits, func(t *testing.T, s summaryTestStore) {
		var checkpoints []SummaryCheckpoint
		var sources []HistorySource
		var parent string
		payloadBytes := 0
		for i := 0; i < 3; i++ {
			batch := testHistoryBatch(fmt.Sprintf("large-source-%d", i))
			batch.Event.GameTime = summaryTestTime(int32(8 + i))
			batch.Event.Facts[0].Text = strings.Repeat("x", 4<<10)
			source, err := s.AppendHistory(context.Background(), batch)
			if err != nil {
				t.Fatal(err)
			}
			payloadBytes += source.Bytes
			sources = append(sources, source)
			checkpoint := summaryTestCommit(t, s, parent, source)
			if len(checkpoint.Sources) != i+1 {
				t.Fatalf("cumulative coverage: %+v", checkpoint)
			}
			checkpoints = append(checkpoints, checkpoint)
			parent = checkpoint.ID
		}
		if payloadBytes <= limits.ScanBytes {
			t.Fatalf("cumulative fixture %d fits scan budget %d", payloadBytes, limits.ScanBytes)
		}
		read := summaryTestRead(t, s, sources[0].Owner, summaryTestTime(10), SummaryReadLimits{})
		if read.Checkpoint == nil || !reflect.DeepEqual(*read.Checkpoint, checkpoints[2]) || len(read.Diagnostics) != 0 {
			t.Fatalf("cumulative payload limited summary read: %+v", read)
		}
		read = summaryTestRead(t, s, sources[0].Owner, summaryTestTime(9), SummaryReadLimits{})
		if read.Checkpoint == nil || read.Checkpoint.ID != checkpoints[1].ID || !summaryHasDiagnostic(read, "summary_time_hidden") {
			t.Fatalf("heads-only cumulative time fallback: %+v", read)
		}
	})
}

func TestSummaryAccessReturnsIndependentSourceHeads(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, s summaryTestStore) {
		ctx := context.Background()
		source := summaryTestSource(t, s, "source", 8)
		snapshot := summaryTestSnapshot(t, s, source.Owner)
		var access summaryAccess
		switch backend := s.(type) {
		case *SQLiteHistoryStore:
			conn, closeConn, err := backend.openHistoryConn(ctx, source.Owner)
			if err != nil {
				t.Fatal(err)
			}
			defer closeConn()
			access = sqliteSummaryAccess(ctx, conn, snapshot)
		case *InMemoryHistoryStore:
			backend.mu.Lock()
			defer backend.mu.Unlock()
			access = backend.summaryAccessLocked(snapshot)
		}
		heads, err := access.sources(ctx, []string{source.ID}, &summaryScanBudget{sources: 1, bytes: 8 << 20})
		if err != nil {
			t.Fatal(err)
		}
		head := heads[0]
		if head.Batch != nil || head.Bytes != 0 || head.ID != source.ID || head.Sequence != source.Sequence || head.Owner != source.Owner || head.Fingerprint != source.Fingerprint || head.Availability != HistoryAvailable || !reflect.DeepEqual(head.Times, source.Times) {
			t.Fatalf("expected source head, got %+v", head)
		}
		head.Times[0].Value.Hour = 12
		again, err := access.sources(ctx, []string{source.ID}, &summaryScanBudget{sources: 1, bytes: 2 << 10})
		if err != nil || again[0].Times[0].Value.Hour != 8 {
			t.Fatalf("source head time alias: %+v %v", again, err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		budget := &summaryScanBudget{sources: 1, bytes: 2048}
		heads, err = access.sources(cancelled, []string{source.ID}, budget)
		if !errors.Is(err, context.Canceled) || len(heads) != 0 || budget.bytes != 2048 {
			t.Fatalf("cancelled source access did work: heads=%d budget=%d err=%v", len(heads), budget.bytes, err)
		}
	})
}

func TestSQLiteSummaryValidatesHeadsWithoutDecodingPayload(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root}, HistoryLimits{})
	source := summaryTestSource(t, s, "source", 8)
	parent := summaryTestCommit(t, s, "", source)
	db := historyTestDB(t, root, testHistoryBatch("source"))
	if _, err := db.Exec("UPDATE session_history SET payload_json='invalid-json' WHERE source_id=?", source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadHistorySource(ctx, source.Owner, source.ID); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("invalid payload fixture: %v", err)
	}
	read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{Bytes: 2 << 10})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || len(read.Diagnostics) != 0 {
		t.Fatalf("summary reread the original payload: %+v", read)
	}
	child := summaryTestCommit(t, s, parent.ID, source)
	if !reflect.DeepEqual(child.Sources, parent.Sources) || child.CoverageFingerprint != parent.CoverageFingerprint {
		t.Fatalf("head provenance changed: %+v", child)
	}
}
