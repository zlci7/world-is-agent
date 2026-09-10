package memory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHistoryPruneRejectsBudgetFallbackCheckpoint(t *testing.T) {
	s, sources, created := pruneFixture(t)
	ctx := context.Background()
	snap, err := s.BeginHistorySnapshot(ctx, sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CommitSummary(ctx, SummaryCommit{Snapshot: snap, ExpectedRevision: snap.SummaryRevision, Version: "alternate", Text: strings.Repeat("expanded summary ", 600), MaxOutputTokens: 10000, Sources: []HistorySourceRef{summaryTestRef(sources[2])}})
	s.ReleaseHistorySnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	req := pruneRequest(sources, created.Add(48*time.Hour))
	req.Limits.Bytes = 4096
	got, err := s.PruneHistory(ctx, req)
	if err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("unproven current summary allowed deletion: %+v %v", got, err)
	}
}

func TestHistoryPruneLeaseWaitIsBounded(t *testing.T) {
	s, sources, created := pruneFixture(t)
	req := pruneRequest(sources, created.Add(48*time.Hour))
	req.Limits.TimeoutMS = 500
	req.Limits.LockTimeoutMS = 20
	s.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := s.PruneHistory(context.Background(), req); done <- err }()
	select {
	case err := <-done:
		db := historyTestDB(t, s.base.options.Root, *sources[0].Batch)
		_, writeErr := db.Exec("UPDATE session_history SET created_at=created_at WHERE source_id=?", sources[0].ID)
		s.mu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lease timeout: %v", err)
		}
		if writeErr != nil {
			t.Fatalf("writer not released after lock failure: %v", writeErr)
		}
	case <-time.After(200 * time.Millisecond):
		s.mu.Unlock()
		<-done
		t.Fatal("lease wait ignored lock deadline")
	}
}

func TestHistoryPruneAccountsForProofBytes(t *testing.T) {
	s, sources, created := pruneFixture(t)
	req := pruneRequest(sources, created.Add(48*time.Hour))
	req.Limits.Bytes = 2000
	got, err := s.PruneHistory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadBytes == 0 || got.ReadBytes > req.Limits.Bytes {
		t.Fatalf("proof bytes not accounted: %+v", got)
	}
	if got.Bytes > got.ReadBytes || got.ReadSources > 16 {
		t.Fatalf("unbounded original reads: %+v", got)
	}
	if len(got.SourceIDs) == 2 {
		t.Fatalf("summary+heads+two originals exceed total budget: %+v", got)
	}
}

func TestHistoryPruneBoundsOriginalReadsWithLargerCoverage(t *testing.T) {
	ctx := context.Background()
	created := time.Unix(1700000000, 0).UTC()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir(), Now: func() time.Time { return created }}, HistoryLimits{})
	var sources []HistorySource
	var refs []HistorySourceRef
	for i := 0; i < 40; i++ {
		source := summaryTestSource(t, s, fmt.Sprintf("bounded-%d", i), 8)
		sources = append(sources, source)
		if i < 32 {
			refs = append(refs, summaryTestRef(source))
		}
	}
	snap, err := s.BeginHistorySnapshot(ctx, sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CommitSummary(ctx, SummaryCommit{Snapshot: snap, ExpectedRevision: snap.SummaryRevision, Version: "test", Text: "Covered history.", Sources: refs})
	s.ReleaseHistorySnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	req := pruneRequest(sources, created.Add(48*time.Hour))
	for iteration := 0; iteration < 2; iteration++ {
		got, err := s.PruneHistory(ctx, req)
		if err != nil || len(got.SourceIDs) != 16 || got.ReadSources != 16 || got.Processed != 16 || got.ReadBytes <= got.Bytes || got.ReadBytes > 8<<20 {
			t.Fatalf("batch%d: %+v %v", iteration, got, err)
		}
		req.AfterSequence = got.NextAfter
	}
}

func TestRetentionTailUsesVisibleHeadersAndConservativeTokenFloor(t *testing.T) {
	s, sources, _ := pruneFixture(t)
	ctx := context.Background()
	futureBatch := testHistoryBatch("future-large")
	futureBatch.Event.GameTime = summaryTestTime(20)
	futureBatch.Event.Facts[0].Text = strings.Repeat("x", 20000)
	if _, err := s.AppendHistory(ctx, futureBatch); err != nil {
		t.Fatal(err)
	}
	snap, err := s.BeginHistorySnapshot(ctx, sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseHistorySnapshot(snap)
	conn, closeConn, err := s.openHistoryConn(ctx, snap.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	tail, bytes, proven, err := s.retentionTail(ctx, conn, snap, summaryTestTime(12), 1000, 8<<20)
	if err != nil || !proven || len(tail) != 4 || bytes >= 20000 {
		t.Fatalf("future changed visible tail: %v bytes=%d proven=%t %v", tail, bytes, proven, err)
	}
	for _, source := range sources {
		if !tail[source.ID] {
			t.Fatal("visible original not protected")
		}
	}
}

func TestMaintenanceInitializedDatabaseKeepsShortLockTimeout(t *testing.T) {
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir()}, HistoryLimits{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot, err := s.BeginHistorySnapshot(ctx, testHistoryBatch("new").Owner)
	if err != nil {
		t.Fatal(err)
	}
	s.ReleaseHistorySnapshot(snapshot)
	limits := DefaultHistoryMaintenanceLimits()
	conn, closeConn, err := s.openMaintenanceConn(ctx, testHistoryBatch("new").Owner, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn()
	var busy int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != limits.LockTimeoutMS {
		t.Fatalf("busy=%d want=%d err=%v", busy, limits.LockTimeoutMS, err)
	}
}

func pruneFixture(t *testing.T) (*SQLiteHistoryStore, []HistorySource, time.Time) {
	t.Helper()
	created := time.Unix(1700000000, 0).UTC()
	s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: t.TempDir(), Now: func() time.Time { return created }}, HistoryLimits{})
	var sources []HistorySource
	for _, turn := range []string{"covered-a", "covered-b", "uncovered", "latest"} {
		sources = append(sources, summaryTestSource(t, s, turn, 8))
	}
	snap, err := s.BeginHistorySnapshot(context.Background(), sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CommitSummary(context.Background(), SummaryCommit{Snapshot: snap, ExpectedRevision: snap.SummaryRevision, Version: "test", Text: "A verified summary.", Sources: []HistorySourceRef{summaryTestRef(sources[0]), summaryTestRef(sources[1])}})
	s.ReleaseHistorySnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	return s, sources, created
}

func pruneRequest(sources []HistorySource, now time.Time) HistoryPruneRequest {
	return HistoryPruneRequest{Owner: sources[0].Owner, Now: now, RetentionDays: 1, CurrentTime: summaryTestTime(12), KeepRecentTokens: 1}
}

func TestHistoryPruneAgeCoverageAndRetry(t *testing.T) {
	s, sources, created := pruneFixture(t)
	ctx := context.Background()
	req := pruneRequest(sources, created.Add(48*time.Hour))
	req.RetentionDays = 0
	if got, err := s.PruneHistory(ctx, req); err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("disabled: %+v %v", got, err)
	}
	req.RetentionDays = 1
	for _, age := range []time.Duration{24*time.Hour - time.Nanosecond, 24 * time.Hour} {
		req.Now = created.Add(age)
		if got, err := s.PruneHistory(ctx, req); err != nil || len(got.SourceIDs) != 0 {
			t.Fatalf("age %s: %+v %v", age, got, err)
		}
	}
	req.Now = created.Add(24*time.Hour + time.Nanosecond)
	got, err := s.PruneHistory(ctx, req)
	if err != nil || !reflect.DeepEqual(got.SourceIDs, []string{sources[0].ID, sources[1].ID}) {
		t.Fatalf("eligible: %+v %v", got, err)
	}
	for i, source := range sources {
		read, err := s.ReadHistorySource(ctx, source.Owner, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if read.Availability != HistoryPruned || read.Batch != nil {
				t.Fatal("pruned raw survived")
			}
		} else if read.Batch == nil {
			t.Fatal("protected raw deleted")
		}
	}
	retry, err := s.AppendHistory(ctx, *sources[0].Batch)
	if err != nil || retry.Availability != HistoryPruned || !retry.CreatedAt.Equal(created) {
		t.Fatalf("retry resurrected: %+v %v", retry, err)
	}
	changed, _ := CloneHistoryBatch(*sources[0].Batch)
	changed.Event.Facts[0].Text = "changed"
	if _, err := s.AppendHistory(ctx, changed); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("conflict: %v", err)
	}
	snap, err := s.BeginHistorySnapshot(ctx, sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseHistorySnapshot(snap)
	read, err := s.ReadSummary(ctx, snap, summaryTestTime(7), SummaryReadLimits{})
	if err != nil || read.Checkpoint != nil || !summaryHasDiagnostic(read, "summary_time_hidden") {
		t.Fatalf("pruning bypassed cumulative future filter: %+v %v", read, err)
	}
}

func TestHistoryPruneProtectsSnapshotAndTail(t *testing.T) {
	s, sources, created := pruneFixture(t)
	ctx := context.Background()
	req := pruneRequest(sources, created.Add(48*time.Hour))
	snap, err := s.BeginHistorySnapshot(ctx, sources[0].Owner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PruneHistory(ctx, req)
	if err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("active snapshot: %+v %v", got, err)
	}
	// Read through another connection while the lease protects the original sources.
	if page, err := s.ReadHistorySnapshot(ctx, snap, 0, HistoryReadLimits{}); err != nil || len(page.Sources) != 4 {
		t.Fatalf("snapshot lost data: %+v %v", page, err)
	}
	s.ReleaseHistorySnapshot(snap)
	req.KeepRecentTokens = 20000
	if got, err = s.PruneHistory(ctx, req); err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("tail: %+v %v", got, err)
	}
	req.KeepRecentTokens = 1
	if got, err = s.PruneHistory(ctx, req); err != nil || len(got.SourceIDs) != 2 {
		t.Fatalf("released: %+v %v", got, err)
	}
}

func TestHistoryPruneRollbackAndFutureSummary(t *testing.T) {
	s, sources, created := pruneFixture(t)
	ctx := context.Background()
	req := pruneRequest(sources, created.Add(48*time.Hour))
	req.CurrentTime = summaryTestTime(7)
	if got, err := s.PruneHistory(ctx, req); err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("future summary: %+v %v", got, err)
	}
	req.CurrentTime = nil
	if got, err := s.PruneHistory(ctx, req); err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("unknown time: %+v %v", got, err)
	}
	req.CurrentTime = summaryTestTime(12)
	db := historyTestDB(t, s.base.options.Root, *sources[0].Batch)
	if _, err := db.Exec("CREATE TRIGGER fail_prune BEFORE UPDATE OF payload_json ON session_history BEGIN SELECT RAISE(ABORT,'test prune'); END"); err != nil {
		t.Fatal(err)
	}
	failed, err := s.PruneHistory(ctx, req)
	if err == nil {
		t.Fatal("write must fail")
	}
	if failed.ReadSources != 1 || failed.ReadBytes <= sources[0].Bytes {
		t.Fatalf("rollback lost admitted read usage: %+v", failed)
	}
	if failed.Processed != 0 || failed.Bytes != 0 || len(failed.SourceIDs) != 0 || failed.NextAfter != req.AfterSequence {
		t.Fatalf("rollback reported committed progress: %+v", failed)
	}
	for _, source := range sources {
		read, err := s.ReadHistorySource(ctx, source.Owner, source.ID)
		if err != nil || read.Availability != HistoryAvailable {
			t.Fatalf("partial cleanup: %+v %v", read, err)
		}
	}
}

func TestHistoryPruneLegacyAndIndexAreAtomic(t *testing.T) {
	for _, failTable := range []string{"recent_records", "history_terms", "history_fragments", "session_history", ""} {
		t.Run(failTable, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			created := time.Unix(1700000000, 0).UTC()
			batch := testHistoryBatch("legacy-prune")
			record := Record{MemoryID: "old-memory", SessionKey: batch.Owner, ProjectionKind: ProjectionKindSettledTurn, ProjectionVersion: ProjectionVersionRecentV2, SourceTurnID: batch.TurnID, SourceEventID: batch.Event.ID, EventType: batch.Event.Type, CreatedAt: created, GameTime: summaryTestTime(8), SourceContextFacts: batch.Event.Facts}
			record.ProjectionBatchKey, _ = BuildProjectionBatchKey(record.SessionKey, record.SourceTurnID, record.SourceEventID, record.ProjectionKind, record.ProjectionVersion)
			old := NewSQLiteMemoryStore(SQLiteStoreOptions{Root: root, Now: func() time.Time { return created }})
			if err := old.Append(ctx, record); err != nil {
				t.Fatal(err)
			}
			s := NewSQLiteHistoryStore(SQLiteStoreOptions{Root: root, Now: func() time.Time { return created }}, HistoryLimits{})
			snap, err := s.BeginHistorySnapshot(ctx, batch.Owner)
			if err != nil {
				t.Fatal(err)
			}
			page, err := s.ReadHistorySnapshot(ctx, snap, 0, HistoryReadLimits{})
			s.ReleaseHistorySnapshot(snap)
			if err != nil || len(page.Sources) != 1 {
				t.Fatalf("legacy: %+v %v", page, err)
			}
			source := page.Sources[0]
			latest := summaryTestSource(t, s, "latest", 9)
			snap, err = s.BeginHistorySnapshot(ctx, batch.Owner)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CommitSummary(ctx, SummaryCommit{Snapshot: snap, ExpectedRevision: snap.SummaryRevision, Version: "test", Text: "Prior statement.", Sources: []HistorySourceRef{summaryTestRef(source)}})
			s.ReleaseHistorySnapshot(snap)
			if err != nil {
				t.Fatal(err)
			}
			db := historyTestDB(t, root, batch)
			if _, err = db.Exec(`INSERT INTO history_index_sources VALUES(?,?,?,'ready')`, source.ID, source.Fingerprint, s.indexLimits.signature()); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO history_fragments VALUES(?,'/legacy/SourceContextFacts/0/Text','player','utterance',?,0,?)`, source.ID, record.SourceContextFacts[0].Text, len([]rune(record.SourceContextFacts[0].Text))); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO history_terms VALUES(?,?,?,?,?,?)`, batch.Owner.GameID, batch.Owner.WorldID, batch.Owner.EntityID, "7319", source.ID, "/legacy/SourceContextFacts/0/Text"); err != nil {
				t.Fatal(err)
			}
			if failTable != "" {
				action := "DELETE"
				if failTable == "session_history" {
					action = "UPDATE OF payload_json"
				}
				if _, err = db.Exec("CREATE TRIGGER fail_cleanup BEFORE " + action + " ON " + failTable + " BEGIN SELECT RAISE(ABORT,'test atomic'); END"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.PruneHistory(ctx, pruneRequest([]HistorySource{source}, created.Add(48*time.Hour)))
			want := 1
			if failTable == "" {
				want = 0
				if err != nil || len(result.SourceIDs) != 1 {
					t.Fatalf("cleanup: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("SQL failure expected")
			}
			for _, table := range []string{"recent_records", "history_fragments", "history_terms", "history_index_sources"} {
				var count int
				query := "SELECT COUNT(*) FROM " + table
				var args []any
				if table != "recent_records" {
					query += " WHERE source_id=?"
					args = []any{source.ID}
				}
				if err := db.QueryRow(query, args...).Scan(&count); err != nil || count != want {
					t.Fatalf("%s: count=%d want%d err=%v", table, count, want, err)
				}
			}
			var credentials int
			if err := db.QueryRow("SELECT COUNT(*) FROM recent_projection_batches").Scan(&credentials); err != nil || credentials != 1 {
				t.Fatal("credential lost")
			}
			got, err := s.ReadHistorySource(ctx, source.Owner, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (got.Availability == HistoryPruned) != (want == 0) {
				t.Fatal("availability not atomic")
			}
			if got, err := s.ReadHistorySource(ctx, latest.Owner, latest.ID); err != nil || got.Batch == nil {
				t.Fatal("latest lost")
			}
		})
	}
}
