package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"gameagent/runtime/internal/model"
)

func TestInMemoryHistoryCopiesPayloadTimesAndSummaries(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryHistoryStore(HistoryLimits{})
	b := testHistoryBatch("first")
	b.Event.GameTime = summaryTestTime(8)
	b.Steps = []HistoryStep{{Index: 1, Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call", Name: "speak", Arguments: map[string]any{"nested": map[string]any{"text": "original"}}}}}}}
	first, err := s.AppendHistory(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	b.Event.Facts[0].Text = "mutated"
	b.Event.GameTime.Hour = 12
	b.Steps[0].Decision.ToolCalls[0].Arguments["nested"].(map[string]any)["text"] = "mutated"
	first.Batch.Event.Facts[0].Text = "also mutated"
	first.Times[0].Value.Hour = 20
	got, err := s.ReadHistorySource(ctx, b.Owner, first.ID)
	if err != nil || got.Batch.Event.GameTime.Hour != 8 || got.Times[0].Value.Hour != 8 || got.Batch.Event.Facts[0].Text != "I do not like rain. The number is 7319." || got.Batch.Steps[0].Decision.ToolCalls[0].Arguments["nested"].(map[string]any)["text"] != "original" {
		t.Fatalf("payload alias: %+v %v", got, err)
	}
	checkpoint := summaryTestCommit(t, s, "", got)
	id := checkpoint.ID
	checkpoint.Sources[0].Fingerprint = "mutated"
	checkpoint.Times[0].Value.Hour = 20
	read := summaryTestRead(t, s, b.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.ID != id || read.Checkpoint.Sources[0].Fingerprint != got.Fingerprint || read.Checkpoint.Times[0].Value.Hour != 8 {
		t.Fatalf("committed summary alias: %+v", read)
	}
	read.Checkpoint.Times[0].Value.Hour = 20
	read.Checkpoint.Sources[0].Fingerprint = "mutated"
	read = summaryTestRead(t, s, b.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.Times[0].Value.Hour != 8 || read.Checkpoint.Sources[0].Fingerprint != got.Fingerprint {
		t.Fatalf("read summary alias: %+v", read)
	}
}

func TestInMemoryHistoryEquivalentRetriesAndConflicts(t *testing.T) {
	s := NewInMemoryHistoryStore(HistoryLimits{})
	b := testHistoryBatch("first")
	first, err := s.AppendHistory(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AppendHistory(context.Background(), b)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("retry: %+v %v", again, err)
	}
	b.Terminal.Reason = "different"
	if _, err := s.AppendHistory(context.Background(), b); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("conflict: %v", err)
	}
	page, err := s.ReadHistorySnapshot(context.Background(), summaryTestSnapshot(t, s, b.Owner), 0, HistoryReadLimits{})
	if err != nil || len(page.Sources) != 1 || page.Sources[0].Fingerprint != first.Fingerprint {
		t.Fatalf("conflict changed history: %+v %v", page, err)
	}
}

func TestInMemoryHistorySnapshotPaginationAndBounds(t *testing.T) {
	s := NewInMemoryHistoryStore(HistoryLimits{PageRecords: 1})
	first := summaryTestSource(t, s, "first", 8)
	second := summaryTestSource(t, s, "second", 9)
	snap := summaryTestSnapshot(t, s, first.Owner)
	_ = summaryTestSource(t, s, "later", 10)
	page, err := s.ReadHistorySnapshot(context.Background(), snap, 0, HistoryReadLimits{Records: 100})
	if err != nil || len(page.Sources) != 1 || page.Sources[0].ID != second.ID || !page.More || page.Scanned != 1 || page.NextBefore != second.Sequence {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page.Sources[0].Batch.Event.GameTime.Hour = 20
	page, err = s.ReadHistorySnapshot(context.Background(), snap, page.NextBefore, HistoryReadLimits{})
	if err != nil || len(page.Sources) != 1 || page.Sources[0].ID != first.ID || page.More {
		t.Fatalf("second page: %+v %v", page, err)
	}
	page, err = s.ReadHistorySnapshot(context.Background(), snap, 0, HistoryReadLimits{Bytes: 1})
	if err != nil || len(page.Sources) != 0 || page.Scanned != 1 || !page.More || len(page.Diagnostics) != 1 {
		t.Fatalf("oversized source: %+v %v", page, err)
	}
	forged := snap
	forged.Watermark++
	if _, err := s.ReadHistorySnapshot(context.Background(), forged, 0, HistoryReadLimits{}); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("forged snapshot: %v", err)
	}
	s.ReleaseHistorySnapshot(forged)
	if _, err := s.ReadHistorySnapshot(context.Background(), snap, 0, HistoryReadLimits{}); err != nil {
		t.Fatal(err)
	}
	s.ReleaseHistorySnapshot(snap)
	if _, err := s.ReadHistorySnapshot(context.Background(), snap, 0, HistoryReadLimits{}); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("released snapshot: %v", err)
	}
}

func TestInMemoryHistoryRejectsInvalidBatchesAndCancellation(t *testing.T) {
	s := NewInMemoryHistoryStore(HistoryLimits{MaxBatchBytes: 1})
	b := testHistoryBatch("first")
	if _, err := s.AppendHistory(context.Background(), b); !errors.Is(err, ErrHistoryCapacity) {
		t.Fatalf("batch bytes: %v", err)
	}
	s = NewInMemoryHistoryStore(HistoryLimits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AppendHistory(ctx, b); !errors.Is(err, context.Canceled) {
		t.Fatalf("append cancellation: %v", err)
	}
	if _, err := s.BeginHistorySnapshot(ctx, b.Owner); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancellation: %v", err)
	}
	if _, err := s.ReadHistorySource(ctx, b.Owner, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation: %v", err)
	}
	source := summaryTestSource(t, s, "valid", 8)
	snap := summaryTestSnapshot(t, s, source.Owner)
	if _, err := s.ReadSummary(ctx, snap, nil, SummaryReadLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("summary cancellation: %v", err)
	}
	if _, err := s.CommitSummary(ctx, SummaryCommit{Snapshot: snap, Version: "v1", Text: "Valid.", Sources: []HistorySourceRef{summaryTestRef(source)}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("publication cancellation: %v", err)
	}
	b.Owner.EntityID = ""
	if _, err := s.AppendHistory(context.Background(), b); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("invalid owner: %v", err)
	}
}

func TestInMemoryHistoryRetainedHeadsValidateSummaryTimes(t *testing.T) {
	s := NewInMemoryHistoryStore(HistoryLimits{})
	early := summaryTestSource(t, s, "early", 8)
	parent := summaryTestCommit(t, s, "", early)
	future := summaryTestSource(t, s, "future", 12)
	child := summaryTestCommit(t, s, parent.ID, future)
	s.mu.Lock()
	for id, source := range s.sources {
		source.Batch = nil
		source.Availability = HistoryPruned
		s.sources[id] = source
	}
	s.mu.Unlock()
	read := summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, "summary_time_hidden") {
		t.Fatalf("pruned future: %+v", read)
	}
	read = summaryTestRead(t, s, early.Owner, summaryTestTime(12), SummaryReadLimits{})
	if read.Checkpoint == nil || !reflect.DeepEqual(*read.Checkpoint, child) {
		t.Fatalf("pruned provenance: %+v", read)
	}
	page, err := s.ReadHistorySnapshot(context.Background(), summaryTestSnapshot(t, s, early.Owner), 0, HistoryReadLimits{})
	if err != nil || len(page.Sources) != 0 || page.Scanned != 2 || page.More || len(page.Diagnostics) != 2 {
		t.Fatalf("pruned page: %+v %v", page, err)
	}
	rollback := summaryTestSource(t, s, "rollback", 9)
	_ = summaryTestCommit(t, s, child.ID, rollback)
	read = summaryTestRead(t, s, early.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID {
		t.Fatalf("inherited pruned time: %+v", read)
	}
}

func TestInMemoryHistorySidecarIsIsolatedAndDoesNotCreateFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	s := NewInMemoryHistoryStore(HistoryLimits{})
	first := summaryTestSource(t, s, "first", 8)
	_ = summaryTestCommit(t, s, "", first)
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
		if _, err := s.ReadHistorySource(context.Background(), b.Owner, first.ID); !errors.Is(err, ErrHistoryNotFound) {
			t.Fatalf("%s source isolation: %v", field, err)
		}
		page, err := s.ReadHistorySnapshot(context.Background(), summaryTestSnapshot(t, s, b.Owner), 0, HistoryReadLimits{})
		if err != nil || len(page.Sources) != 1 || page.Sources[0].ID != other.ID {
			t.Fatalf("%s snapshot isolation: %+v %v", field, page, err)
		}
	}
	files, err := os.ReadDir(".")
	if err != nil || len(files) != 0 {
		t.Fatalf("sidecar filesystem effects: %v %v", files, err)
	}
}

func TestInMemoryHistoryCoverageManifestRejectsMutation(t *testing.T) {
	for _, mutation := range []string{"partial deletion", "count", "fingerprint", "times", "consistent manifest with false times", "pruned head times", "reference replacement", "manifest bytes"} {
		t.Run(mutation, func(t *testing.T) {
			s := NewInMemoryHistoryStore(HistoryLimits{})
			early := summaryTestSource(t, s, "early", 8)
			parent := summaryTestCommit(t, s, "", early)
			future := summaryTestSource(t, s, "future", 12)
			child := summaryTestCommit(t, s, parent.ID, future)
			rollback := summaryTestSource(t, s, "rollback", 9)
			fingerprint, timesJSON := summaryTestManifest(t, child.Sources, child.Times)
			if child.CoverageFingerprint != fingerprint || child.sourceCount != 2 || child.coverageTimesJSON != timesJSON {
				t.Fatalf("in-memory manifest: %+v", child)
			}
			corrupted := cloneSummaryCheckpoint(child)
			limits := SummaryReadLimits{}
			diagnostic := "summary_coverage_invalid"
			switch mutation {
			case "partial deletion":
				corrupted.Sources = corrupted.Sources[:1]
			case "count":
				corrupted.sourceCount = 1
			case "fingerprint":
				corrupted.CoverageFingerprint = strings.Repeat("0", 64)
			case "times":
				corrupted.coverageTimesJSON = "[]"
			case "consistent manifest with false times":
				corrupted.Times[1].Value.Hour = 9
				corrupted.CoverageFingerprint, corrupted.coverageTimesJSON = summaryTestManifest(t, corrupted.Sources, corrupted.Times)
			case "pruned head times":
				s.mu.Lock()
				source := s.sources[future.ID]
				source.Batch = nil
				source.Availability = HistoryPruned
				source.Times = cloneHistoryTimes(source.Times)
				source.Times[0].Value.Hour = 9
				s.sources[future.ID] = source
				s.mu.Unlock()
			case "reference replacement":
				corrupted.Sources[1] = summaryTestRef(rollback)
			case "manifest bytes":
				corrupted.coverageTimesJSON = "[" + strings.Repeat(" ", 4096)
				limits.Bytes = 2000
				diagnostic = "summary_scan_incomplete"
			}
			s.mu.Lock()
			s.summaries[child.ID] = corrupted
			s.mu.Unlock()
			read := summaryTestRead(t, s, early.Owner, summaryTestTime(12), limits)
			if read.Checkpoint == nil || read.Checkpoint.ID != parent.ID || !summaryHasDiagnostic(read, diagnostic) {
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

func TestInMemoryHistoryManifestTimesRemainIndependentOfReturnedCheckpoint(t *testing.T) {
	s := NewInMemoryHistoryStore(HistoryLimits{})
	source := summaryTestSource(t, s, "source", 8)
	checkpoint := summaryTestCommit(t, s, "", source)
	fingerprint := checkpoint.CoverageFingerprint
	checkpoint.Times[0].Value.Hour = 12
	checkpoint.CoverageFingerprint = "changed"
	read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint == nil || read.Checkpoint.CoverageFingerprint != fingerprint || read.Checkpoint.Times[0].Value.Hour != 8 {
		t.Fatalf("coverage manifest alias: %+v", read)
	}
	var persistedTimes []HistoryTime
	if err := json.Unmarshal([]byte(read.Checkpoint.coverageTimesJSON), &persistedTimes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedTimes, read.Checkpoint.Times) {
		t.Fatalf("coverage times diverged: %+v", read.Checkpoint)
	}
}

func TestInMemoryHistoryRejectsInvalidSummaryHeadAvailability(t *testing.T) {
	s := NewInMemoryHistoryStore(HistoryLimits{})
	source := summaryTestSource(t, s, "source", 8)
	checkpoint := summaryTestCommit(t, s, "", source)
	s.mu.Lock()
	head := s.sources[source.ID]
	head.Availability = "invalid"
	s.sources[source.ID] = head
	s.mu.Unlock()
	read := summaryTestRead(t, s, source.Owner, summaryTestTime(10), SummaryReadLimits{})
	if read.Checkpoint != nil || !summaryHasDiagnostic(read, "summary_coverage_invalid") {
		t.Fatalf("invalid source head availability: %+v", read)
	}
	request := SummaryCommit{Snapshot: summaryTestSnapshot(t, s, source.Owner), ExpectedRevision: checkpoint.Revision, ParentID: checkpoint.ID, Version: "v1", Text: "Valid.", Sources: []HistorySourceRef{summaryTestRef(source)}}
	if _, err := s.CommitSummary(context.Background(), request); !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("invalid head inherited: %v", err)
	}
}
