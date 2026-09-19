package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
	"gameagent/runtime/internal/trace"
)

var preparationOwner = session.AgentSessionKey{GameID: "fake-game", WorldID: "world", EntityID: "agent"}

func TestHistoryCompactionRequestDeclaresSourceAuthority(t *testing.T) {
	store := newPreparationStore()
	source := appendPreparationSource(t, store, "authority", "I said the parcel arrived.", preparationTime(8))
	req, selected, err := historyCompactionRequest(context.Background(), nil, []memory.HistorySource{source}, memory.DefaultHistoryLimits(), DefaultCompactionConfig())
	if err != nil || len(selected) != 1 {
		t.Fatalf("build summary request: selected=%d err=%v", len(selected), err)
	}
	for _, rule := range []string{
		"event.facts[].ActorEntityID identifies the actor of that fact only",
		"attribute speech to its speaker, choices to the chooser, commands to the issuer, and interactions to the participant",
		"steps[].decision records the model's intent for the agent owner.EntityID",
		"steps[].executions[].action_result records the actual game receipt",
		"runtime_result and runtime_error record Runtime assessments",
		"terminal.status describes the Turn lifecycle, not action success",
	} {
		if !strings.Contains(req.System, rule) {
			t.Errorf("summary request does not declare source authority: %s", rule)
		}
	}
}

type preparationStore struct {
	*memory.InMemoryHistoryStore
	pages         []memory.HistoryReadLimits
	summaryLimits []memory.SummaryReadLimits
	deadlines     []time.Duration
	pageError     error
	summaryError  error
	beginError    error
	failPage      int
	releases      int
	afterBegin    func()
	afterPage     func()
	commits       []memory.SummaryCommit
}

func newPreparationStore() *preparationStore {
	return &preparationStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
}

func (s *preparationStore) BeginHistorySnapshot(ctx context.Context, owner session.AgentSessionKey) (memory.HistorySnapshot, error) {
	if s.beginError != nil {
		return memory.HistorySnapshot{}, s.beginError
	}
	snapshot, err := s.InMemoryHistoryStore.BeginHistorySnapshot(ctx, owner)
	if s.afterBegin != nil {
		s.afterBegin()
	}
	return snapshot, err
}

func (s *preparationStore) ReleaseHistorySnapshot(snapshot memory.HistorySnapshot) {
	s.releases++
	s.InMemoryHistoryStore.ReleaseHistorySnapshot(snapshot)
}

func (s *preparationStore) ReadHistorySnapshot(ctx context.Context, snapshot memory.HistorySnapshot, before int64, limits memory.HistoryReadLimits) (memory.HistoryPage, error) {
	s.pages = append(s.pages, limits)
	deadline, ok := ctx.Deadline()
	if !ok {
		return memory.HistoryPage{}, errors.New("missing history deadline")
	}
	s.deadlines = append(s.deadlines, time.Until(deadline))
	if s.pageError != nil && (s.failPage == 0 || s.failPage == len(s.pages)) {
		return memory.HistoryPage{}, s.pageError
	}
	page, err := s.InMemoryHistoryStore.ReadHistorySnapshot(ctx, snapshot, before, limits)
	if s.afterPage != nil {
		s.afterPage()
	}
	return page, err
}

func (s *preparationStore) ReadSummary(ctx context.Context, snapshot memory.HistorySnapshot, current *memory.GameTimeSnapshot, limits memory.SummaryReadLimits) (memory.SummaryRead, error) {
	s.summaryLimits = append(s.summaryLimits, limits)
	if s.summaryError != nil {
		return memory.SummaryRead{}, s.summaryError
	}
	return s.InMemoryHistoryStore.ReadSummary(ctx, snapshot, current, limits)
}

func (s *preparationStore) CommitSummary(ctx context.Context, commit memory.SummaryCommit) (memory.SummaryCheckpoint, error) {
	s.commits = append(s.commits, commit)
	return s.InMemoryHistoryStore.CommitSummary(ctx, commit)
}

type preparationTrace struct {
	events []trace.Event
	onEmit func(trace.EventName)
}

func (r *preparationTrace) Emit(name trace.EventName, data trace.EventData) {
	r.events = append(r.events, trace.Event{Event: name, Fields: data.Fields})
	if r.onEmit != nil {
		r.onEmit(name)
	}
}
func (r *preparationTrace) Complete(trace.EventData)                    {}
func (r *preparationTrace) Fail(string, string, error, trace.EventData) {}

func (r *preparationTrace) hasDiagnostic(want string) bool {
	for _, event := range r.events {
		diagnostics, _ := event.Fields["diagnostics"].([]string)
		for _, diagnostic := range diagnostics {
			if diagnostic == want {
				return true
			}
		}
	}
	return false
}

func preparationTime(hour int32) *memory.GameTimeSnapshot {
	return &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 1, Hour: hour}
}

func appendPreparationSource(t *testing.T, store memory.HistoryStore, turn, text string, at *memory.GameTimeSnapshot) memory.HistorySource {
	t.Helper()
	batch := memory.HistoryBatch{
		Owner: preparationOwner, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: turn,
		Event: memory.HistoryEvent{ID: "event-" + turn, Type: "generic.input", GameTime: at,
			Facts: []memory.SourceContextFact{{ActorEntityID: "player", Kind: "utterance", Text: text}}},
		Terminal: memory.HistoryTerminal{Status: "completed"},
	}
	source, err := store.AppendHistory(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func preparationLoop(store *preparationStore) *Loop {
	cfg := DefaultConfig()
	cfg.Compaction.Enabled = boolPtr(false)
	return &Loop{config: cfg, historyStore: store, summaryStore: store}
}

func preparationIDs(sources []memory.HistorySource) []string {
	ids := make([]string, len(sources))
	for i, source := range sources {
		ids[i] = source.ID
	}
	return ids
}

func TestPrepareHistoryFiltersFutureBeforeSelectionAcrossPages(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "first", "visible", preparationTime(8))
	second := appendPreparationSource(t, store, "second", "also visible", preparationTime(9))
	for i := 0; i < 130; i++ {
		appendPreparationSource(t, store, fmt.Sprintf("future-%d", i), "future", preparationTime(12))
	}
	loop := preparationLoop(store)
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 1000)
	defer prepared.Release()
	if prepared.Input == nil || !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{first.ID, second.ID}) {
		t.Fatalf("visible durable order lost: %+v", prepared.Input)
	}
	if len(store.pages) != 3 || !r.hasDiagnostic("history_time_hidden") || r.hasDiagnostic("history_scan_incomplete") {
		t.Fatalf("future paging diagnostics: pages=%v trace=%+v", store.pages, r.events)
	}
}

func TestPrepareHistoryKnownFutureIsHiddenAlongsideUnknown(t *testing.T) {
	store := newPreparationStore()
	unknown := appendPreparationSource(t, store, "unknown", "unknown is visible", nil)
	batch := *unknown.Batch
	batch.TurnID = "mixed"
	batch.Observations = []memory.HistoryObservation{{Step: 1, GameTime: preparationTime(12)}}
	if _, err := store.AppendHistory(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	r := &preparationTrace{}
	prepared := preparationLoop(store).prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 1000)
	defer prepared.Release()
	if !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{unknown.ID}) || !r.hasDiagnostic("history_time_unknown") || !r.hasDiagnostic("history_time_hidden") {
		t.Fatalf("per-source time check lost: input=%+v events=%+v", prepared.Input, r.events)
	}
}

func TestPrepareHistoryClampsReadLimitsAndReportsIncompleteScan(t *testing.T) {
	store := newPreparationStore()
	for i := 0; i < 515; i++ {
		appendPreparationSource(t, store, fmt.Sprintf("future-%d", i), "future", preparationTime(12))
	}
	loop := preparationLoop(store)
	loop.config.History = memory.HistoryLimits{PageRecords: 1000, PageBytes: 64 << 20, ScanRecords: 1000, ScanBytes: 64 << 20, ReadTimeoutMS: 10000, SummarySources: 20000, SummaryCheckpoints: 1000}
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 100)
	defer prepared.Release()
	if prepared.Input == nil || len(prepared.Input.Sources) != 0 || len(store.pages) != 8 || !r.hasDiagnostic("history_scan_incomplete") {
		t.Fatalf("scan must stop at 512: pages=%v input=%+v events=%+v", store.pages, prepared.Input, r.events)
	}
	for i, limits := range store.pages {
		if limits.Records != 64 || limits.Bytes > 8<<20 || store.deadlines[i] <= 0 {
			t.Fatalf("unbounded page: limits=%+v deadline=%s", limits, store.deadlines[i])
		}
		// The configured budget is 10000ms, so a deadline at or under the default
		// is the clamp. Comparing against the default rather than a literal keeps
		// this a statement about the clamp instead of about its current value.
		if store.deadlines[i] > time.Duration(memory.DefaultHistoryLimits().ReadTimeoutMS)*time.Millisecond {
			t.Fatalf("page deadline %s is not clamped to the default read timeout", store.deadlines[i])
		}
	}
	if !reflect.DeepEqual(store.summaryLimits, []memory.SummaryReadLimits{{Sources: 16384, Checkpoints: 64, Bytes: 32 << 20}}) {
		t.Fatalf("summary limits: %+v", store.summaryLimits)
	}
}

func TestPrepareHistoryHonorsSmallerRecordAndByteLimits(t *testing.T) {
	store := newPreparationStore()
	for i := 0; i < 6; i++ {
		appendPreparationSource(t, store, fmt.Sprintf("turn-%d", i), "payload", preparationTime(8))
	}
	loop := preparationLoop(store)
	loop.config.History = memory.HistoryLimits{PageRecords: 2, PageBytes: 1200, ScanRecords: 3, ScanBytes: 1500, SummarySources: 7, SummaryCheckpoints: 2, ReadTimeoutMS: 100}
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 5000)
	defer prepared.Release()
	if len(store.pages) != 2 || store.pages[1].Records != 1 || store.pages[1].Bytes >= 1200 || len(prepared.Input.Sources) > 3 || !r.hasDiagnostic("history_scan_incomplete") {
		t.Fatalf("smaller aggregate limits ignored: pages=%v input=%+v", store.pages, prepared.Input)
	}
	if store.summaryLimits[0] != (memory.SummaryReadLimits{Sources: 7, Checkpoints: 2, Bytes: 1500}) {
		t.Fatalf("smaller summary limits ignored: %v", store.summaryLimits)
	}
}

func TestPrepareHistorySnapshotIsFrozenAndLeaseReleaseIsIdempotent(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "first", "initial", preparationTime(8))
	store.afterBegin = func() { appendPreparationSource(t, store, "later", "concurrent", preparationTime(9)) }
	prepared := preparationLoop(store).prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 1000)
	if !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{first.ID}) || prepared.Snapshot.Watermark != first.Sequence || store.releases != 0 {
		t.Fatalf("snapshot was not held: %+v releases=%d", prepared, store.releases)
	}
	if _, err := store.InMemoryHistoryStore.ReadHistorySnapshot(context.Background(), prepared.Snapshot, 0, memory.HistoryReadLimits{}); err != nil {
		t.Fatal(err)
	}
	prepared.Release()
	prepared.Release()
	var absent *preparedHistory
	absent.Release()
	if store.releases != 1 {
		t.Fatalf("release count=%d", store.releases)
	}
	if _, err := store.InMemoryHistoryStore.ReadHistorySnapshot(context.Background(), prepared.Snapshot, 0, memory.HistoryReadLimits{}); err == nil {
		t.Fatal("lease still active after release")
	}
}

func TestPrepareHistoryReadFailuresKeepBoundedInputAndSafeDiagnostics(t *testing.T) {
	for _, stage := range []string{"snapshot", "summary", "page", "partial_page", "timeout"} {
		t.Run(stage, func(t *testing.T) {
			store := newPreparationStore()
			appendPreparationSource(t, store, "first", "private source text", preparationTime(8))
			latest := appendPreparationSource(t, store, "latest", "private source text", preparationTime(9))
			failure := errors.New("private raw error")
			want, count := "history_read_failed", 0
			switch stage {
			case "snapshot":
				store.beginError, want = failure, "history_snapshot_failed"
			case "summary":
				store.summaryError, want, count = failure, "summary_read_failed", 2
			case "page":
				store.pageError = failure
			case "partial_page":
				store.pageError, store.failPage, count = failure, 2, 1
			case "timeout":
				store.pageError, want = context.DeadlineExceeded, "history_read_timeout"
			}
			loop := preparationLoop(store)
			loop.config.History.PageRecords = 1
			r := &preparationTrace{}
			prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 777)
			defer prepared.Release()
			if prepared.Input == nil || prepared.Input.MaxTokens != 777 || len(prepared.Input.Sources) != count || !r.hasDiagnostic(want) {
				t.Fatalf("fail-open input: %+v events=%+v", prepared.Input, r.events)
			}
			if stage == "partial_page" && (prepared.Input.Sources[0].ID != latest.ID || !r.hasDiagnostic("history_scan_incomplete")) {
				t.Fatal("partial page discarded or not diagnosed")
			}
			if strings.Contains(fmt.Sprint(r.events), "private") {
				t.Fatal("trace leaked source or error text")
			}
		})
	}
}

type preparationGenerator struct {
	requests []model.TextRequest
	generate func(context.Context, model.TextRequest) (model.TextResponse, error)
}

func (g *preparationGenerator) GenerateText(ctx context.Context, request model.TextRequest) (model.TextResponse, error) {
	g.requests = append(g.requests, request)
	if g.generate != nil {
		return g.generate(ctx, request)
	}
	return model.TextResponse{Text: "The player reported progress. Completion remains unconfirmed."}, nil
}

func preparationRef(source memory.HistorySource) memory.HistorySourceRef {
	return memory.HistorySourceRef{ID: source.ID, Sequence: source.Sequence, Fingerprint: source.Fingerprint}
}

func commitPreparationSummary(t *testing.T, store *memory.InMemoryHistoryStore, parent, text string, sources ...memory.HistorySource) memory.SummaryCheckpoint {
	t.Helper()
	snapshot, err := store.BeginHistorySnapshot(context.Background(), preparationOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	refs := make([]memory.HistorySourceRef, len(sources))
	for i, source := range sources {
		refs[i] = preparationRef(source)
	}
	checkpoint, err := store.CommitSummary(context.Background(), memory.SummaryCommit{
		Snapshot: snapshot, ExpectedRevision: snapshot.SummaryRevision, ParentID: parent,
		Version: "test_summary", Text: text, Sources: refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func enablePreparationCompaction(loop *Loop, generator *preparationGenerator) {
	loop.config.Compaction.Enabled = boolPtr(true)
	loop.config.Compaction.KeepRecentTokens = 1
	loop.config.Compaction.MaxSummaryTokens = 64
	loop.summaryGenerator = generator
}

func TestPrepareHistoryExcludesOnlyExactNoncontiguousCoverage(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "first", "first", preparationTime(8))
	second := appendPreparationSource(t, store, "second", "second", preparationTime(8))
	third := appendPreparationSource(t, store, "third", "third", preparationTime(8))
	fourth := appendPreparationSource(t, store, "fourth", "fourth", preparationTime(8))
	parent := commitPreparationSummary(t, store.InMemoryHistoryStore, "", "First and third.", first, third)
	prepared := preparationLoop(store).prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 1000)
	defer prepared.Release()
	if prepared.Input.Summary == nil || prepared.Input.Summary.ID != parent.ID || !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{second.ID, fourth.ID}) {
		t.Fatalf("coverage must be IDs, not a sequence range: %+v", prepared.Input)
	}
}

func TestPrepareHistoryCompactionKeepsNewestWholeUnitAndCommitsNewRefsOnly(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "first", "first", preparationTime(8))
	second := appendPreparationSource(t, store, "second", strings.Repeat("middle complete payload ", 40), preparationTime(8))
	third := appendPreparationSource(t, store, "third", "third", preparationTime(8))
	newest := appendPreparationSource(t, store, "newest", strings.Repeat("newest complete payload ", 100), preparationTime(8))
	parent := commitPreparationSummary(t, store.InMemoryHistoryStore, "", "First and third.", first, third)
	g := &preparationGenerator{}
	loop := preparationLoop(store)
	enablePreparationCompaction(loop, g)
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(g.requests) != 1 || len(store.commits) != 1 || prepared.Input.Summary == nil || prepared.Input.Summary.ID == parent.ID {
		t.Fatalf("compaction missing: calls=%d commits=%v input=%+v", len(g.requests), store.commits, prepared.Input)
	}
	request := store.commits[0]
	if request.ParentID != parent.ID || request.ExpectedRevision != parent.Revision || request.Version != "phase8_2_summary_v1" || request.MaxOutputTokens != 64 || !reflect.DeepEqual(request.Sources, []memory.HistorySourceRef{preparationRef(second)}) {
		t.Fatalf("commit must carry new exact refs: %+v", request)
	}
	if !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{newest.ID}) || !reflect.DeepEqual(prepared.Input.Sources[0].Batch, newest.Batch) {
		t.Fatal("newest complete unit was compacted or cropped during preparation")
	}
	if fields := r.events[len(r.events)-1].Fields; fields["covered_source_count"] != 3 || fields["uncovered_source_count"] != 1 {
		t.Fatalf("final coverage counts do not include successful compaction: %+v", fields)
	}
	data, _, _, err := memory.CanonicalHistoryBatch(*second.Batch, 8<<20)
	if err != nil || !strings.Contains(g.requests[0].Input, string(data)) || !strings.Contains(g.requests[0].Input, parent.Text) || strings.Contains(g.requests[0].Input, newest.Batch.TurnID) {
		t.Fatal("generator did not receive exactly the old summary and complete older source")
	}
}

func TestPrepareHistoryCompactionMayExtendOlderVisibleParent(t *testing.T) {
	store := newPreparationStore()
	early := appendPreparationSource(t, store, "early", "early", preparationTime(8))
	parent := commitPreparationSummary(t, store.InMemoryHistoryStore, "", "Early events.", early)
	future := appendPreparationSource(t, store, "future", "future", preparationTime(12))
	head := commitPreparationSummary(t, store.InMemoryHistoryStore, parent.ID, "Early and future events.", future)
	rollback := appendPreparationSource(t, store, "rollback", "rollback", preparationTime(9))
	newest := appendPreparationSource(t, store, "newest", "newest", preparationTime(10))
	g := &preparationGenerator{}
	loop := preparationLoop(store)
	enablePreparationCompaction(loop, g)
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(store.commits) != 1 || store.commits[0].ParentID != parent.ID || store.commits[0].ExpectedRevision != head.Revision || prepared.Input.Summary == nil || prepared.Input.Summary.Revision <= head.Revision {
		t.Fatalf("visible older parent was not independently CAS-published: commits=%+v input=%+v", store.commits, prepared.Input)
	}
	if !reflect.DeepEqual(prepared.Input.Summary.Sources, []memory.HistorySourceRef{preparationRef(early), preparationRef(rollback)}) || !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{newest.ID}) || !r.hasDiagnostic("summary_time_hidden") {
		t.Fatalf("rollback visibility or coverage leaked: %+v", prepared.Input)
	}
}

func TestPrepareHistoryCompactionFailurePreservesOldInput(t *testing.T) {
	for _, failure := range []string{"generator", "empty", "oversized", "bytes", "timeout", "cas"} {
		t.Run(failure, func(t *testing.T) {
			store := newPreparationStore()
			first := appendPreparationSource(t, store, "first", "original", preparationTime(8))
			parent := commitPreparationSummary(t, store.InMemoryHistoryStore, "", "Old verified summary.", first)
			older := appendPreparationSource(t, store, "older", "private older source", preparationTime(8))
			newest := appendPreparationSource(t, store, "newest", "private newest source", preparationTime(8))
			g := &preparationGenerator{generate: func(ctx context.Context, request model.TextRequest) (model.TextResponse, error) {
				switch failure {
				case "generator":
					return model.TextResponse{}, errors.New("private provider failure")
				case "empty":
					return model.TextResponse{Text: "  "}, nil
				case "oversized":
					return model.TextResponse{Text: strings.Repeat("x", request.MaxOutputTokens*4+4)}, nil
				case "bytes":
					return model.TextResponse{Text: strings.Repeat("x", request.MaxResponseBytes+1)}, nil
				case "timeout":
					<-ctx.Done()
					return model.TextResponse{}, ctx.Err()
				case "cas":
					commitPreparationSummary(t, store.InMemoryHistoryStore, parent.ID, "Other writer.", older)
				}
				return model.TextResponse{Text: "private generated text"}, nil
			}}
			loop := preparationLoop(store)
			enablePreparationCompaction(loop, g)
			loop.config.Compaction.TimeoutMS = 15
			if failure == "bytes" {
				loop.config.Compaction.MaxResponseBytes = 8
			}
			r := &preparationTrace{}
			prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 200)
			defer prepared.Release()
			if len(g.requests) != 1 || prepared.Input.Summary == nil || !reflect.DeepEqual(*prepared.Input.Summary, parent) || !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{older.ID, newest.ID}) {
				t.Fatalf("failed compaction changed frozen input: calls=%d input=%+v", len(g.requests), prepared.Input)
			}
			if strings.Contains(fmt.Sprint(r.events), "private") || !r.hasDiagnostic("compaction_failed") {
				t.Fatalf("unsafe or missing failure diagnostic: %+v", r.events)
			}
		})
	}
}

func TestPrepareHistoryCompactionCooldownRequiresThreeTurnsAndNewData(t *testing.T) {
	store := newPreparationStore()
	appendPreparationSource(t, store, "older", "older", preparationTime(8))
	appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
	g := &preparationGenerator{generate: func(context.Context, model.TextRequest) (model.TextResponse, error) {
		return model.TextResponse{}, errors.New("failed")
	}}
	loop := preparationLoop(store)
	enablePreparationCompaction(loop, g)
	prepare := func(budget int) {
		loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), budget).Release()
	}
	prepare(200)
	if len(g.requests) != 1 {
		t.Fatal("first pressure turn must attempt")
	}
	appendPreparationSource(t, store, "fresh", "fresh uncovered", preparationTime(8))
	for i := 0; i < 3; i++ {
		prepare(200)
		if len(g.requests) != 1 {
			t.Fatalf("retry during skipped subsequent turn %d", i+1)
		}
	}
	prepare(100000)
	if len(g.requests) != 1 {
		t.Fatal("new data without pressure retried")
	}
	prepare(200)
	if len(g.requests) != 2 {
		t.Fatal("retry missing after exactly three turns and fresh data")
	}
	for i := 0; i < 5; i++ {
		prepare(200)
	}
	if len(g.requests) != 2 {
		t.Fatal("unchanged failed watermark retried")
	}
}

func TestPrepareHistoryCompactionNeverGeneratesAfterParentCancellation(t *testing.T) {
	for _, cancelAt := range []string{"before_snapshot", "after_snapshot", "after_page"} {
		t.Run(cancelAt, func(t *testing.T) {
			store := newPreparationStore()
			appendPreparationSource(t, store, "older", "older", preparationTime(8))
			appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAt == "before_snapshot" {
				cancel()
			} else if cancelAt == "after_snapshot" {
				store.afterBegin = cancel
			} else {
				store.afterPage = cancel
			}
			g := &preparationGenerator{}
			loop := preparationLoop(store)
			enablePreparationCompaction(loop, g)
			prepared := loop.prepareHistory(ctx, nil, preparationOwner, preparationTime(10), 200)
			prepared.Release()
			if len(g.requests) != 0 || len(store.commits) != 0 {
				t.Fatal("cancelled parent generated or committed")
			}
		})
	}
}

func TestPrepareHistoryCompactionPressureThresholdAndZeroBudget(t *testing.T) {
	for _, mode := range []string{"below", "at", "zero", "single", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			store := newPreparationStore()
			older := appendPreparationSource(t, store, "older", "older", preparationTime(8))
			sources := []memory.HistorySource{older}
			if mode != "single" {
				sources = append(sources, appendPreparationSource(t, store, "newest", "newest", preparationTime(8)))
			}
			tokens := agentcontext.EstimateHistoryInputTokens(agentcontext.HistoryInput{Sources: sources})
			budget := tokens * 10 / 9
			if mode == "below" {
				budget += 2
			}
			if mode == "zero" {
				budget = 0
			}
			g := &preparationGenerator{}
			loop := preparationLoop(store)
			enablePreparationCompaction(loop, g)
			if mode == "disabled" {
				loop.config.Compaction.Enabled = boolPtr(false)
			}
			prepared := loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), budget)
			defer prepared.Release()
			want := 0
			if mode == "at" {
				want = 1
			}
			if len(g.requests) != want || prepared.Input == nil {
				t.Fatalf("pressure decision calls=%d want=%d tokens=%d budget=%d", len(g.requests), want, tokens, budget)
			}
			if mode == "disabled" && len(prepared.Input.Sources) != 2 {
				t.Fatal("disabled compaction stopped reads")
			}
		})
	}
}

func TestPrepareHistoryCompactionBoundsCompleteFramedRequest(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "first", "first", preparationTime(8))
	parent := commitPreparationSummary(t, store.InMemoryHistoryStore, "", strings.Repeat("prior summary ", 25), first)
	older := appendPreparationSource(t, store, "older", strings.Repeat("quoted \" text ", 20), preparationTime(8))
	newest := appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
	data, _, _, err := memory.CanonicalHistoryBatch(*older.Batch, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	g := &preparationGenerator{}
	loop := preparationLoop(store)
	enablePreparationCompaction(loop, g)
	loop.config.Compaction.MaxInputTokens = tokenestimate.EstimateText(string(data)) + tokenestimate.EstimateText(parent.Text) + 1
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(g.requests) != 0 || len(store.commits) != 0 || !r.hasDiagnostic("compaction_input_budget_exceeded") {
		t.Fatalf("system and escaped message framing were omitted from input budget: calls=%d events=%+v", len(g.requests), r.events)
	}
	if prepared.Input.Summary == nil || prepared.Input.Summary.ID != parent.ID || !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{older.ID, newest.ID}) {
		t.Fatal("non-fitting complete request changed coverage")
	}
}

func TestPrepareHistoryCompactionSelectsEarliestFittingWholePrefixOnce(t *testing.T) {
	store := newPreparationStore()
	var sources []memory.HistorySource
	for i := 0; i < 5; i++ {
		sources = append(sources, appendPreparationSource(t, store, fmt.Sprintf("turn-%d", i), strings.Repeat("source payload ", 100), preparationTime(8)))
	}
	g := &preparationGenerator{generate: func(ctx context.Context, req model.TextRequest) (model.TextResponse, error) {
		if _, err := model.ValidateTextRequest(req); err != nil {
			t.Errorf("complete request is oversized: %v", err)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("generator deadline exceeds ten seconds")
		}
		return model.TextResponse{Text: "A bounded prefix of older events."}, nil
	}}
	loop := preparationLoop(store)
	enablePreparationCompaction(loop, g)
	loop.config.Compaction.MaxInputTokens = 1800
	loop.config.Compaction.TimeoutMS = 100000
	prepared := loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(g.requests) != 1 || len(store.commits) != 1 {
		t.Fatalf("attempt count: calls=%d commits=%d", len(g.requests), len(store.commits))
	}
	refs := store.commits[0].Sources
	if len(refs) == 0 || len(refs) >= len(sources)-1 {
		t.Fatalf("fixture must select a strict prefix: refs=%+v", refs)
	}
	for i, ref := range refs {
		if ref != preparationRef(sources[i]) {
			t.Fatal("compaction skipped or split an older source")
		}
		data, _, _, err := memory.CanonicalHistoryBatch(*sources[i].Batch, 8<<20)
		if err != nil || !strings.Contains(g.requests[0].Input, string(data)) {
			t.Fatal("partial source reached generator")
		}
	}
	if !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), preparationIDs(sources[len(refs):])) {
		t.Fatal("unselected sources acquired coverage")
	}
}

func TestPrepareHistoryCompactionReportsExhaustedCoverageCapacity(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "first", "first", preparationTime(8))
	parent := commitPreparationSummary(t, store.InMemoryHistoryStore, "", "First.", first)
	appendPreparationSource(t, store, "older", "older", preparationTime(8))
	appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
	loop := preparationLoop(store)
	g := &preparationGenerator{}
	enablePreparationCompaction(loop, g)
	loop.config.History.SummarySources = 1
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(g.requests) != 0 || !r.hasDiagnostic("compaction_coverage_limit") || prepared.Input.Summary == nil || prepared.Input.Summary.ID != parent.ID || len(prepared.Input.Sources) != 2 {
		t.Fatalf("coverage cap misdiagnosed: input=%+v events=%+v", prepared.Input, r.events)
	}
}

func TestPrepareHistoryFrozenInputFitsFinalContextWithoutChangingCoverage(t *testing.T) {
	store := newPreparationStore()
	appendPreparationSource(t, store, "older", "An older report.", preparationTime(8))
	newest := appendPreparationSource(t, store, "newest", strings.Repeat("newest complete content ", 1200), preparationTime(9))
	loop := preparationLoop(store)
	g := &preparationGenerator{}
	enablePreparationCompaction(loop, g)
	prepared := loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 900)
	defer prepared.Release()
	if prepared.Input.Summary == nil || len(prepared.Input.Sources) != 1 || prepared.Input.Sources[0].ID != newest.ID {
		t.Fatal("compaction fixture did not prepare summary and whole newest unit")
	}
	target := &protocol.EntityRef{EntityId: preparationOwner.EntityID, EntityType: "agent"}
	input := agentcontext.BuildInput{
		SessionKey: preparationOwner, CanonicalTarget: target, AgentDescriptor: definition.NewAgentInstanceDescriptor(preparationOwner, target),
		RuntimePolicy:  "Use the current observation.",
		Event:          &protocol.GameEvent{EventId: "current-event", WorldId: preparationOwner.WorldID, TargetEntityId: preparationOwner.EntityID},
		Observation:    &protocol.Observation{WorldId: preparationOwner.WorldID, EntityId: preparationOwner.EntityID},
		History:        prepared.Input,
		RecentMemories: []memory.Record{{MemoryID: "legacy", SourceContextFacts: []memory.SourceContextFact{{Text: "legacy duplicate"}}}},
	}
	budget := agentcontext.DefaultBudgetConfig()
	budget.MaxRequestTokens, budget.MaxUserMessageTokens = 2600, 2000
	engine := agentcontext.NewEngine(budget)
	for step := 0; step < 2; step++ {
		result, err := engine.Build(input)
		if err != nil {
			t.Fatal(err)
		}
		request, err := agentcontext.NewRenderer().Render(result.Projection)
		if err != nil {
			t.Fatal(err)
		}
		size, err := agentcontext.EstimateRequestTokens(request)
		if err != nil || agentcontext.RequestEstimatedTokensExceedBudget(size, result.Report.EffectiveBudget) || result.Report.History.EstimatedTokens > prepared.Input.MaxTokens {
			t.Fatalf("final display exceeded budgets: size=%+v history=%+v err=%v", size, result.Report.History, err)
		}
		if len(result.Projection.RecentMemory) != 0 || strings.Contains(request.Messages[0].Content, "legacy duplicate") {
			t.Fatal("history input failed to suppress legacy Recent")
		}
		if !reflect.DeepEqual(prepared.Input.Sources[0].Batch, newest.Batch) || len(g.requests) != 1 || len(store.summaryLimits) != 1 {
			t.Fatal("final fitting changed frozen input or repeated preparation")
		}
		input.Transcript = []model.Message{{Role: model.RoleAssistant, Content: strings.Repeat("current causal context ", 20)}}
	}
}

func TestPrepareHistoryCompactionTailTargetUsesEffectiveBudget(t *testing.T) {
	for _, tc := range []struct{ budget, keep, summary, want int }{
		{1000, 250, 64, 250}, {997, 10000, 64, 633}, {101, 50, 64, 6}, {100, 50, 100, 0},
	} {
		t.Run(fmt.Sprint(tc.budget), func(t *testing.T) {
			store := newPreparationStore()
			for i := 0; i < 4; i++ {
				appendPreparationSource(t, store, fmt.Sprintf("turn-%d", i), "source", preparationTime(8))
			}
			loop := preparationLoop(store)
			g := &preparationGenerator{}
			enablePreparationCompaction(loop, g)
			loop.config.Compaction.KeepRecentTokens = tc.keep
			loop.config.Compaction.MaxSummaryTokens = tc.summary
			r := &preparationTrace{}
			prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), tc.budget)
			defer prepared.Release()
			if len(g.requests) != 1 {
				t.Fatal("pressure fixture did not compact")
			}
			for _, event := range r.events {
				if event.Event == "compaction_started" && event.Fields["tail_target_tokens"] != tc.want {
					t.Fatalf("tail target=%v want=%d", event.Fields["tail_target_tokens"], tc.want)
				}
			}
		})
	}
}

func TestPrepareHistoryCompactionParentDeadlineAndLateResponse(t *testing.T) {
	for _, mode := range []string{"deadline", "late_response"} {
		t.Run(mode, func(t *testing.T) {
			store := newPreparationStore()
			appendPreparationSource(t, store, "older", "older", preparationTime(8))
			appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
			parent, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			g := &preparationGenerator{generate: func(ctx context.Context, request model.TextRequest) (model.TextResponse, error) {
				parentDeadline, _ := parent.Deadline()
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(parentDeadline) {
					t.Error("generator escaped parent deadline")
				}
				if mode == "late_response" {
					cancel()
				} else {
					<-ctx.Done()
				}
				return model.TextResponse{Text: "Late response."}, nil
			}}
			loop := preparationLoop(store)
			enablePreparationCompaction(loop, g)
			prepared := loop.prepareHistory(parent, nil, preparationOwner, preparationTime(10), 200)
			defer prepared.Release()
			if len(g.requests) != 1 || len(store.commits) != 0 || prepared.Input.Summary != nil || len(prepared.Input.Sources) != 2 {
				t.Fatal("late response changed coverage after parent cancellation")
			}
		})
	}
}

func TestPrepareHistoryCompactionCooldownIsScopedByFullOwner(t *testing.T) {
	store := newPreparationStore()
	first := appendPreparationSource(t, store, "older", "older", preparationTime(8))
	appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
	other := preparationOwner
	other.WorldID = "another-world"
	for i := 0; i < 2; i++ {
		batch := *first.Batch
		batch.Owner, batch.TurnID = other, fmt.Sprintf("other-%d", i)
		if _, err := store.AppendHistory(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	loop := preparationLoop(store)
	g := &preparationGenerator{generate: func(context.Context, model.TextRequest) (model.TextResponse, error) {
		return model.TextResponse{}, errors.New("failed")
	}}
	enablePreparationCompaction(loop, g)
	loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 200).Release()
	for i := 0; i < 5; i++ {
		loop.prepareHistory(context.Background(), nil, other, preparationTime(10), 200).Release()
	}
	if len(g.requests) != 2 {
		t.Fatalf("other owner inherited cooldown: calls=%d", len(g.requests))
	}
	appendPreparationSource(t, store, "fresh", "fresh", preparationTime(8))
	for i := 0; i < 3; i++ {
		loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 200).Release()
	}
	if len(g.requests) != 2 {
		t.Fatal("other owner's turns consumed this owner's cooldown")
	}
	loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 200).Release()
	if len(g.requests) != 3 {
		t.Fatal("owner-scoped retry missing")
	}
}

func TestPrepareHistoryOversizedSourceContinuesPagingWithPartialDiagnostic(t *testing.T) {
	store := newPreparationStore()
	older := appendPreparationSource(t, store, "older", "visible older source", preparationTime(8))
	appendPreparationSource(t, store, "oversized", strings.Repeat("large ", 500), preparationTime(8))
	loop := preparationLoop(store)
	loop.config.History.PageRecords, loop.config.History.PageBytes = 1, 1000
	r := &preparationTrace{}
	prepared := loop.prepareHistory(context.Background(), r, preparationOwner, preparationTime(10), 1000)
	defer prepared.Release()
	if len(store.pages) != 2 || !reflect.DeepEqual(preparationIDs(prepared.Input.Sources), []string{older.ID}) || !r.hasDiagnostic("history_source_exceeds_page_bytes") || !r.hasDiagnostic("history_scan_incomplete") {
		t.Fatalf("oversized source suppressed older history or partial diagnostic: pages=%v events=%+v", store.pages, r.events)
	}
	if r.events[len(r.events)-1].Fields["history_scan_complete"] != false {
		t.Fatal("partial source read was reported complete")
	}
}

func TestPrepareHistoryCancellationImmediatelyBeforeGenerationSkipsGenerator(t *testing.T) {
	store := newPreparationStore()
	appendPreparationSource(t, store, "older", "older", preparationTime(8))
	appendPreparationSource(t, store, "newest", "newest", preparationTime(8))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &preparationTrace{onEmit: func(name trace.EventName) {
		if name == "compaction_started" {
			cancel()
		}
	}}
	loop := preparationLoop(store)
	g := &preparationGenerator{}
	enablePreparationCompaction(loop, g)
	prepared := loop.prepareHistory(ctx, r, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(g.requests) != 0 || len(store.commits) != 0 || len(prepared.Input.Sources) != 2 {
		t.Fatalf("generator invoked with cancelled parent: calls=%d commits=%d", len(g.requests), len(store.commits))
	}
}
