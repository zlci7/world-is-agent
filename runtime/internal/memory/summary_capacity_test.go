package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type summaryCapacityFixture struct {
	checkpoint    SummaryCheckpoint
	payloadBytes  int64
	metadataBytes int
}

func TestSummaryCapacitySourceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		fits  bool
	}{
		{"below", 16383, true},
		{"equal", 16384, true},
		{"above", 16385, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summaryBackends(t, HistoryLimits{}, func(t *testing.T, store summaryTestStore) {
				fixture := newSummaryCapacityFixture(t, store, tc.count, 0)
				snapshot := summaryTestSnapshot(t, store, fixture.checkpoint.Owner)
				// A persisted fixture keeps read timing independent of publication success.
				t.Run("read", func(t *testing.T) {
					read, err := summaryCapacityRead(t, store, snapshot, summaryTestTime(10))
					if err != nil {
						t.Fatalf("ReadSummary with %d sources: %v", tc.count, err)
					}
					if tc.fits {
						summaryCapacityRequireRead(t, read, fixture.checkpoint)
					} else if read.Checkpoint != nil || (!summaryHasDiagnostic(read, "summary_scan_incomplete") && !summaryHasDiagnostic(read, "summary_coverage_invalid")) {
						t.Fatalf("over-capacity checkpoint admitted: checkpoint=%t diagnostics=%v", read.Checkpoint != nil, read.Diagnostics)
					}
				})
				t.Run("commit", func(t *testing.T) {
					request := SummaryCommit{Snapshot: snapshot, ExpectedRevision: snapshot.SummaryRevision, Version: "capacity-v1", Text: "Capacity summary.", Sources: fixture.checkpoint.Sources}
					checkpoint, err := summaryCapacityCommit(t, store, request)
					if !tc.fits {
						if !errors.Is(err, ErrHistoryCapacity) || checkpoint.ID != "" {
							t.Fatalf("over-capacity publication: id=%q err=%v", checkpoint.ID, err)
						}
						if after := summaryTestSnapshot(t, store, snapshot.Owner); after.SummaryRevision != snapshot.SummaryRevision {
							t.Fatalf("rejected publication advanced revision from %d to %d", snapshot.SummaryRevision, after.SummaryRevision)
						}
						return
					}
					if err != nil {
						t.Fatalf("CommitSummary with %d sources: %v", tc.count, err)
					}
					summaryCapacityRequireCoverage(t, checkpoint, fixture.checkpoint)
					if checkpoint.ID == fixture.checkpoint.ID || checkpoint.Revision <= snapshot.SummaryRevision {
						t.Fatalf("publication did not create a new revision: id=%q revision=%d", checkpoint.ID, checkpoint.Revision)
					}
				})
			})
		})
	}
}

func TestSummaryCapacityPayloadExceeds32MiB(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, store summaryTestStore) {
		fixture := newSummaryCapacityFixture(t, store, 9, 4<<20)
		if fixture.payloadBytes <= 32<<20 || fixture.metadataBytes >= 32<<20 {
			t.Fatalf("fixture must exceed payload budget while metadata fits: payload=%d metadata=%d", fixture.payloadBytes, fixture.metadataBytes)
		}
		snapshot := summaryTestSnapshot(t, store, fixture.checkpoint.Owner)
		t.Run("read", func(t *testing.T) {
			read, err := summaryCapacityRead(t, store, snapshot, summaryTestTime(10))
			if err != nil {
				t.Fatal(err)
			}
			summaryCapacityRequireRead(t, read, fixture.checkpoint)
		})
		t.Run("commit_inherited_coverage", func(t *testing.T) {
			request := SummaryCommit{Snapshot: snapshot, ExpectedRevision: snapshot.SummaryRevision, ParentID: fixture.checkpoint.ID, Version: "capacity-v1", Text: "Capacity summary."}
			checkpoint, err := summaryCapacityCommit(t, store, request)
			if err != nil {
				t.Fatal(err)
			}
			summaryCapacityRequireCoverage(t, checkpoint, fixture.checkpoint)
			if checkpoint.ParentID != fixture.checkpoint.ID || checkpoint.Revision <= snapshot.SummaryRevision {
				t.Fatalf("inherited publication: parent=%q revision=%d", checkpoint.ParentID, checkpoint.Revision)
			}
			read, err := summaryCapacityRead(t, store, summaryTestSnapshot(t, store, snapshot.Owner), summaryTestTime(10))
			if err != nil {
				t.Fatal(err)
			}
			summaryCapacityRequireRead(t, read, checkpoint)
		})
	})
}

func TestSummaryCapacityMissingHeadPreservesManifestAndExcludesCheckpoint(t *testing.T) {
	summaryBackends(t, HistoryLimits{}, func(t *testing.T, store summaryTestStore) {
		ctx := context.Background()
		early := summaryTestSource(t, store, "capacity-early", 8)
		future := summaryTestSource(t, store, "capacity-future", 12)
		checkpoint := summaryTestCommit(t, store, "", early, future)
		snapshot := summaryTestSnapshot(t, store, early.Owner)
		read, err := summaryCapacityRead(t, store, snapshot, summaryTestTime(12))
		if err != nil {
			t.Fatal(err)
		}
		summaryCapacityRequireRead(t, read, checkpoint)
		switch backend := store.(type) {
		case *SQLiteHistoryStore:
			db := historyTestDB(t, backend.base.options.Root, testHistoryBatch("capacity-early"))
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Only this test-directory connection bypasses the FK to inject a missing head.
			if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
				t.Fatal(err)
			}
			result, err := conn.ExecContext(ctx, "DELETE FROM session_history WHERE game_id=? AND world_id=? AND entity_id=? AND source_id=?", future.Owner.GameID, future.Owner.WorldID, future.Owner.EntityID, future.ID)
			if err != nil {
				t.Fatal(err)
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				t.Fatalf("deleted heads=%d err=%v", count, err)
			}
			var count int
			var fingerprint, timesJSON string
			if err := conn.QueryRowContext(ctx, "SELECT source_count,coverage_fingerprint,coverage_times_json FROM context_summaries WHERE summary_id=?", checkpoint.ID).Scan(&count, &fingerprint, &timesJSON); err != nil {
				t.Fatal(err)
			}
			wantFingerprint, wantTimes := summaryTestManifest(t, checkpoint.Sources, checkpoint.Times)
			refs, err := readSQLiteSummaryReferences(ctx, conn, checkpoint.ID, &summaryScanBudget{sources: 16384, bytes: 32 << 20})
			if err != nil || count != 2 || fingerprint != wantFingerprint || timesJSON != wantTimes || !reflect.DeepEqual(refs, checkpoint.Sources) {
				t.Fatalf("head deletion changed independent coverage: count=%d refs=%d err=%v", count, len(refs), err)
			}
		case *InMemoryHistoryStore:
			backend.mu.Lock()
			delete(backend.sources, future.ID)
			preserved := cloneSummaryCheckpoint(backend.summaries[checkpoint.ID])
			backend.mu.Unlock()
			if !reflect.DeepEqual(preserved, checkpoint) {
				t.Fatal("head deletion changed independent coverage")
			}
		}
		if _, err := store.ReadHistorySource(ctx, future.Owner, future.ID); !errors.Is(err, ErrHistoryNotFound) {
			t.Fatalf("missing head fixture: %v", err)
		}
		for _, hour := range []int32{10, 12} {
			read, err := summaryCapacityRead(t, store, snapshot, summaryTestTime(hour))
			if err != nil || read.Checkpoint != nil || !summaryHasDiagnostic(read, "summary_coverage_invalid") {
				t.Fatalf("missing head at hour %d: checkpoint=%t diagnostics=%v err=%v", hour, read.Checkpoint != nil, read.Diagnostics, err)
			}
		}
	})
}

func newSummaryCapacityFixture(t *testing.T, store summaryTestStore, count, textBytes int) summaryCapacityFixture {
	t.Helper()
	ctx := context.Background()
	started := time.Now()
	var limits HistoryLimits
	var tx *sql.Tx
	owner := testHistoryBatch("capacity").Owner
	switch backend := store.(type) {
	case *SQLiteHistoryStore:
		limits = backend.limits
		conn, closeConn, err := backend.openHistoryConn(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		defer closeConn()
		tx, err = beginSQLiteWriteTransaction(ctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
	case *InMemoryHistoryStore:
		limits = backend.limits
	default:
		t.Fatalf("unsupported capacity backend %T", store)
	}
	if limits != DefaultHistoryLimits() || limits.SummarySources != 16384 || limits.ScanBytes != 32<<20 || limits.ReadTimeoutMS != 2000 || limits.WriteTimeoutMS != 5000 {
		t.Fatalf("capacity fixture requires unchanged default limits: %+v", limits)
	}
	fixture := summaryCapacityFixture{checkpoint: SummaryCheckpoint{ID: "capacity-fixture", Owner: owner, Version: "capacity-v1", Text: "Capacity summary.", CreatedAt: time.Unix(100, 0).UTC()}}
	text := strings.Repeat("x", textBytes)
	for i := 0; i < count; i++ {
		batch := testHistoryBatch(fmt.Sprintf("capacity-%05d", i))
		batch.Event.GameTime = summaryTestTime(8)
		if textBytes > 0 {
			batch.Event.Facts[0].Text = text
		}
		var source HistorySource
		var err error
		if tx != nil {
			data, key, fingerprint, canonicalErr := CanonicalHistoryBatch(batch, limits.MaxBatchBytes)
			if canonicalErr != nil {
				t.Fatal(canonicalErr)
			}
			source, err = insertHistorySource(ctx, tx, batch, data, key, fingerprint, fixture.checkpoint.CreatedAt, "")
		} else {
			source, err = store.AppendHistory(ctx, batch)
		}
		if err != nil {
			t.Fatalf("source %d: %v", i, err)
		}
		if source.Sequence != int64(i+1) || source.Batch == nil || source.Availability != HistoryAvailable {
			t.Fatalf("source %d is not a complete canonical history source", i)
		}
		fixture.checkpoint.Sources = append(fixture.checkpoint.Sources, summaryTestRef(source))
		fixture.checkpoint.Times = append(fixture.checkpoint.Times, cloneHistoryTimes(source.Times)...)
		fixture.payloadBytes += int64(source.Bytes)
		timesJSON, err := json.Marshal(source.Times)
		if err != nil {
			t.Fatal(err)
		}
		fixture.metadataBytes += len(source.ID)*2 + len(source.Fingerprint)*2 + len(source.Availability) + len(timesJSON) + 16
	}
	checkpoint := &fixture.checkpoint
	checkpoint.sourceCount = len(checkpoint.Sources)
	checkpoint.CoverageFingerprint, checkpoint.coverageTimesJSON = summaryTestManifest(t, checkpoint.Sources, checkpoint.Times)
	fixture.metadataBytes += summaryHeaderBytes(*checkpoint)
	if fixture.metadataBytes >= limits.ScanBytes {
		t.Fatalf("fixture metadata exceeds scan budget: %d >= %d", fixture.metadataBytes, limits.ScanBytes)
	}
	if tx != nil {
		result, err := tx.ExecContext(ctx, `INSERT INTO context_summaries(summary_id,game_id,world_id,entity_id,parent_id,generation_version,summary_text,source_count,coverage_fingerprint,coverage_times_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, checkpoint.ID, owner.GameID, owner.WorldID, owner.EntityID, checkpoint.ParentID, checkpoint.Version, checkpoint.Text, checkpoint.sourceCount, checkpoint.CoverageFingerprint, checkpoint.coverageTimesJSON, checkpoint.CreatedAt.UnixNano())
		if err != nil {
			t.Fatal(err)
		}
		checkpoint.Revision, err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		statement, err := tx.PrepareContext(ctx, `INSERT INTO summary_sources(summary_id,source_id,source_sequence,content_fingerprint) VALUES(?,?,?,?)`)
		if err != nil {
			t.Fatal(err)
		}
		defer statement.Close()
		for _, ref := range checkpoint.Sources {
			if _, err := statement.ExecContext(ctx, checkpoint.ID, ref.ID, ref.Sequence, ref.Fingerprint); err != nil {
				t.Fatal(err)
			}
		}
		if err := commitSQLiteTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
	} else {
		backend := store.(*InMemoryHistoryStore)
		backend.mu.Lock()
		world := historyWorldKey{game: owner.GameID, world: owner.WorldID}
		backend.revisions[world]++
		checkpoint.Revision = backend.revisions[world]
		backend.summaries[checkpoint.ID] = cloneSummaryCheckpoint(*checkpoint)
		backend.summaryOrder[owner] = append(backend.summaryOrder[owner], checkpoint.ID)
		backend.mu.Unlock()
	}
	t.Logf("fixture sources=%d payload_bytes=%d metadata_bytes=%d setup_elapsed=%s", count, fixture.payloadBytes, fixture.metadataBytes, time.Since(started))
	return fixture
}

func summaryCapacityRead(t *testing.T, store SummaryStore, snapshot HistorySnapshot, current *GameTimeSnapshot) (SummaryRead, error) {
	t.Helper()
	started := time.Now()
	read, err := store.ReadSummary(context.Background(), snapshot, current, SummaryReadLimits{})
	elapsed := time.Since(started)
	count := 0
	if read.Checkpoint != nil {
		count = len(read.Checkpoint.Sources)
	}
	t.Logf("ReadSummary elapsed=%s read_timeout=%dms returned_sources=%d diagnostics=%v err=%v", elapsed, DefaultHistoryLimits().ReadTimeoutMS, count, read.Diagnostics, err)
	return read, err
}

func summaryCapacityCommit(t *testing.T, store SummaryStore, request SummaryCommit) (SummaryCheckpoint, error) {
	t.Helper()
	started := time.Now()
	checkpoint, err := store.CommitSummary(context.Background(), request)
	t.Logf("CommitSummary elapsed=%s default_timeout=5s new_sources=%d inherited=%t returned_sources=%d err=%v", time.Since(started), len(request.Sources), request.ParentID != "", len(checkpoint.Sources), err)
	return checkpoint, err
}

func summaryCapacityRequireRead(t *testing.T, read SummaryRead, want SummaryCheckpoint) {
	t.Helper()
	if read.Checkpoint == nil || read.Checkpoint.ID != want.ID || len(read.Diagnostics) != 0 {
		t.Fatalf("expected complete checkpoint %q: checkpoint=%t diagnostics=%v", want.ID, read.Checkpoint != nil, read.Diagnostics)
	}
	summaryCapacityRequireCoverage(t, *read.Checkpoint, want)
}

func summaryCapacityRequireCoverage(t *testing.T, got, want SummaryCheckpoint) {
	t.Helper()
	if got.ID == "" || got.Owner != want.Owner || got.Text != want.Text || got.Version != want.Version || got.CoverageFingerprint != want.CoverageFingerprint || !reflect.DeepEqual(got.Sources, want.Sources) || !reflect.DeepEqual(got.Times, want.Times) {
		t.Fatalf("coverage differs: id=%q sources=%d want_sources=%d times=%d want_times=%d", got.ID, len(got.Sources), len(want.Sources), len(got.Times), len(want.Times))
	}
}
