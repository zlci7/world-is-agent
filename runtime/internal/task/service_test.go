package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gameagent/runtime/internal/session"
)

func TestCreateWithoutProposal(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, StoreOptions{
		Path: filepath.Join(t.TempDir(), "tasks.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	world := WorldKey{GameID: "fake-game", WorldID: "world-a"}
	clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	head, err := svc.ActivateWorld(ctx, world, "run-a", clock,
		CheckpointRef{Status: "absent", World: world})
	if err != nil {
		t.Fatal(err)
	}
	source := SourceRef{Kind: "internal", EventID: "e", TurnID: "t", CallID: "c"}
	exec := ExecutionContext{
		Owner:   session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "actor"},
		Binding: head.Binding,
		Clock:   clock,
		Source:  source,
	}
	spec := TaskSpec{
		Instruction:    "inspect later",
		ClockID:        clock.ID,
		WakeAt:         200,
		DeadlineAt:     300,
		ResultContract: "authoritative_evidence",
		Source:         source,
	}
	got, err := svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created || got.Task.State != State("waiting") || got.Task.Revision != 1 {
		t.Fatalf("unexpected create: %+v", got)
	}
	if !strings.HasPrefix(got.Task.ID, "task_") {
		t.Fatalf("task id = %q, want task_ prefix", got.Task.ID)
	}
	again, err := svc.Create(ctx, exec, spec, Admission{})
	if err != nil || again.Task.ID != got.Task.ID || !again.Created {
		t.Fatalf("retry: %+v %v", again, err)
	}
}

func TestCreateIdempotencyRetryIgnoresSmallerReopenWriteLimit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	store, err := OpenSQLiteStore(ctx, StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	world, clock := testWorld(), Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	svc := NewService(store)
	head, err := svc.ActivateWorld(ctx, world, "run-a", clock, CheckpointRef{Status: "absent", World: world})
	if err != nil {
		t.Fatal(err)
	}
	exec, spec := createInputs(head, clock, testOwner(), "event-a", "turn-a", "call-a", "equivalence-a")
	created, err := svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLiteStore(ctx, StoreOptions{Path: path, MaxTaskBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again, err := NewService(reopened).Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatalf("exact retry with smaller write limit returned error: %v", err)
	}
	if !reflect.DeepEqual(again, created) {
		t.Fatalf("exact retry = %+v, want immutable %+v", again, created)
	}
}

func TestCreateEquivalenceReuseAfterClockPassesOriginalWake(t *testing.T) {
	ctx := context.Background()
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	svc := NewService(store)
	world, clock := testWorld(), Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	head, err := svc.ActivateWorld(ctx, world, "run-a", clock, CheckpointRef{Status: "absent", World: world})
	if err != nil {
		t.Fatal(err)
	}
	exec, spec := createInputs(head, clock, testOwner(), "event-a", "turn-a", "call-a", "equivalence-a")
	created, err := svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}

	advanced := Clock{ID: clock.ID, Tick: spec.WakeAt + 1, Sequence: 2}
	row, err := store.loadWorldHead(ctx, world)
	if err != nil {
		t.Fatal(err)
	}
	row.Head.Clock = advanced
	if err := store.putWorldHead(ctx, row); err != nil {
		t.Fatal(err)
	}
	exec.Clock = advanced
	exec.Source.EventID, exec.Source.TurnID, exec.Source.CallID = "event-b", "turn-b", "call-b"
	spec.Source = exec.Source
	reused, err := svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatalf("equivalence retry after wake returned error: %v", err)
	}
	if reused.Created || !reflect.DeepEqual(reused.Task, created.Task) {
		t.Fatalf("equivalence retry = %+v, want existing task with Created=false", reused)
	}
}

func TestCreateIdempotencyReturnsImmutableInitialResponseAfterTaskChangesAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	store, err := OpenSQLiteStore(ctx, StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	svc := deterministicTaskService(store)
	world, clock := testWorld(), Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	head, err := svc.ActivateWorld(ctx, world, "run-a", clock, CheckpointRef{Status: checkpointStatusAbsent, World: world})
	if err != nil {
		t.Fatal(err)
	}
	exec, spec := createInputs(head, clock, testOwner(), "event-a", "turn-a", "call-a", "equivalence-a")
	created, err := svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	originalWake, err := loadOnlyWake(t, store, exec.Owner)
	if err != nil {
		t.Fatal(err)
	}

	current, err := store.loadTask(ctx, exec.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.State = StateSucceeded
	current.Revision = 2
	current.NextWakeAt = nil
	terminalEvidence := Evidence{
		FactID: "fact-terminal", TaskID: current.ID, Binding: head.Binding, StartRevision: 1,
		OccurredAt: clock.Tick, Kind: EvidenceKindSatisfied, Source: SourceRef{Kind: SourceKindEnvironment}, Applied: true,
	}
	current.Evidence = []Evidence{terminalEvidence}
	current.Result = &Result{
		ID: "result-terminal", TaskID: current.ID, Revision: current.Revision, State: current.State,
		Reason: EvidenceKindSatisfied, OccurredAt: terminalEvidence.OccurredAt,
		EvidenceRefs: []string{terminalEvidence.FactID}, Source: terminalEvidence.Source,
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET state = ?, revision = ?, next_wake_at = NULL, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		string(current.State), int64(current.Revision), currentJSON,
		exec.Owner.GameID, exec.Owner.WorldID, exec.Owner.EntityID, current.ID); err != nil {
		t.Fatal(err)
	}
	advanced := Clock{ID: clock.ID, Tick: spec.WakeAt + 50, Sequence: 2}
	headRow, err := store.loadWorldHead(ctx, world)
	if err != nil {
		t.Fatal(err)
	}
	headRow.Head.Clock = advanced
	if err := store.putWorldHead(ctx, headRow); err != nil {
		t.Fatal(err)
	}
	if leaked, err := svc.Create(ctx, exec, spec, Admission{}); !errors.Is(err, ErrClockRewound) || !reflect.DeepEqual(leaked, CreateResult{}) {
		t.Fatalf("stale-clock exact retry = %+v, %v; want zero result and ErrClockRewound", leaked, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLiteStore(ctx, StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	exec.Clock = advanced
	again, err := NewService(reopened).Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, created) {
		t.Fatalf("exact retry = %+v, want original %+v", again, created)
	}
	if got := scopedRowCount(t, reopened.db, "tasks", exec.Owner); got != 1 {
		t.Fatalf("task count = %d, want 1", got)
	}
	if got := scopedRowCount(t, reopened.db, "task_wakeups", exec.Owner); got != 1 {
		t.Fatalf("wake count = %d, want 1", got)
	}
	reopenedWake, err := reopened.loadWake(ctx, exec.Owner, originalWake.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopenedWake, originalWake) {
		t.Fatalf("reopened wake = %+v, want %+v", reopenedWake, originalWake)
	}
}

func TestCreateIdempotencyNeverBypassesCurrentBindingAuthority(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*ExecutionContext)
	}{
		{name: "run", mutate: func(exec *ExecutionContext) { exec.Binding.RunID = "old-run" }},
		{name: "generation", mutate: func(exec *ExecutionContext) { exec.Binding.Generation++ }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exec := fixture.exec
			tt.mutate(&exec)
			result, err := fixture.svc.Create(context.Background(), exec, fixture.spec, Admission{})
			if !errors.Is(err, ErrGenerationStale) || !reflect.DeepEqual(result, CreateResult{}) {
				t.Fatalf("stale exact retry = %+v, %v; want zero result and ErrGenerationStale", result, err)
			}
		})
	}
	assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	stored, err := fixture.store.loadTask(context.Background(), fixture.exec.Owner, created.Task.ID)
	if err != nil || !reflect.DeepEqual(stored, created.Task) {
		t.Fatalf("stored task after stale retries = %+v, %v", stored, err)
	}
}

func TestCreateIdempotencyChangedInputConflictsBeforeEquivalence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExecutionContext, *TaskSpec)
	}{
		{name: "instruction", mutate: func(_ *ExecutionContext, spec *TaskSpec) { spec.Instruction = "different instruction" }},
		{name: "contract", mutate: func(_ *ExecutionContext, spec *TaskSpec) { spec.Contract = []byte(`{"expected":"different"}`) }},
		{name: "source facts", mutate: func(exec *ExecutionContext, spec *TaskSpec) {
			exec.Source.Facts = []byte(`[{"kind":"different"}]`)
			spec.Source = exec.Source
		}},
		{name: "source game time", mutate: func(exec *ExecutionContext, spec *TaskSpec) {
			exec.Source.GameTime = []byte(`{"tick":101}`)
			spec.Source = exec.Source
		}},
		{name: "wake timing", mutate: func(_ *ExecutionContext, spec *TaskSpec) { spec.WakeAt = 201 }},
		{name: "deadline timing", mutate: func(_ *ExecutionContext, spec *TaskSpec) { spec.DeadlineAt = 301 }},
		{name: "equivalence", mutate: func(_ *ExecutionContext, spec *TaskSpec) { spec.EquivalenceKey = "equivalence-b" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
			if err != nil {
				t.Fatal(err)
			}
			exec, spec := fixture.exec, fixture.spec
			tt.mutate(&exec, &spec)
			if _, err := fixture.svc.Create(context.Background(), exec, spec, Admission{}); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("Create() error = %v, want ErrIdempotencyConflict", err)
			}
			stored, err := fixture.store.loadTask(context.Background(), fixture.exec.Owner, created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, created.Task) {
				t.Fatalf("stored task changed to %+v, want %+v", stored, created.Task)
			}
			assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
		})
	}
}

func TestCreateEquivalenceReusesOnlySameOwnerNonterminalTask(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	ctx := context.Background()
	created, err := fixture.svc.Create(ctx, fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	current := created.Task
	current.Progress = []byte(`{"status":"still-waiting"}`)
	setTaskStateForCreateTest(t, fixture.store, current, StatePaused, 2)
	current, err = fixture.store.loadTask(ctx, fixture.exec.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}

	exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-b", "turn-b", "call-b")
	reused, err := fixture.svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if reused.Created || !reflect.DeepEqual(reused.Task, current) {
		t.Fatalf("same-owner equivalence = %+v, want existing task with Created=false", reused)
	}
	assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)

	otherEntity := fixture.exec.Owner
	otherEntity.EntityID = "actor-b"
	otherExec, otherSpec := createInputs(fixture.head, fixture.clock, otherEntity, "event-b", "turn-b", "call-b", fixture.spec.EquivalenceKey)
	otherCreated, err := fixture.svc.Create(ctx, otherExec, otherSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if !otherCreated.Created || otherCreated.Task.ID == created.Task.ID {
		t.Fatalf("other entity create = %+v, want independent task", otherCreated)
	}
	assertCreateRowCounts(t, fixture.store, otherEntity, 1, 1)

	otherWorld := WorldKey{GameID: fixture.world.GameID, WorldID: "world-b"}
	otherClock := fixture.clock
	otherHead, err := fixture.svc.ActivateWorld(ctx, otherWorld, "run-b", otherClock, CheckpointRef{Status: checkpointStatusAbsent, World: otherWorld})
	if err != nil {
		t.Fatal(err)
	}
	otherWorldOwner := fixture.exec.Owner
	otherWorldOwner.WorldID = otherWorld.WorldID
	worldExec, worldSpec := createInputs(otherHead, otherClock, otherWorldOwner, "event-b", "turn-b", "call-b", fixture.spec.EquivalenceKey)
	worldCreated, err := fixture.svc.Create(ctx, worldExec, worldSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if !worldCreated.Created || worldCreated.Task.ID == created.Task.ID {
		t.Fatalf("other world create = %+v, want independent task", worldCreated)
	}
	assertCreateRowCounts(t, fixture.store, otherWorldOwner, 1, 1)
}

func TestCreateEquivalenceTerminalAndEmptyKeysAllowIndependentTasks(t *testing.T) {
	t.Run("terminal equivalence", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		ctx := context.Background()
		first, err := fixture.svc.Create(ctx, fixture.exec, fixture.spec, Admission{})
		if err != nil {
			t.Fatal(err)
		}
		setTaskStateForCreateTest(t, fixture.store, first.Task, StateSucceeded, 2)
		exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-b", "turn-b", "call-b")
		second, err := fixture.svc.Create(ctx, exec, spec, Admission{})
		if err != nil {
			t.Fatal(err)
		}
		if !second.Created || second.Task.ID == first.Task.ID {
			t.Fatalf("terminal equivalence create = %+v, want new task", second)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 2, 2)
	})

	t.Run("empty and different equivalence", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		ctx := context.Background()
		fixture.spec.EquivalenceKey = ""
		if _, err := fixture.svc.Create(ctx, fixture.exec, fixture.spec, Admission{}); err != nil {
			t.Fatal(err)
		}
		exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-b", "turn-b", "call-b")
		if result, err := fixture.svc.Create(ctx, exec, spec, Admission{}); err != nil || !result.Created {
			t.Fatalf("second empty-equivalence create = %+v, %v", result, err)
		}
		exec, spec = withCreateCall(fixture.exec, fixture.spec, "event-c", "turn-c", "call-c")
		spec.EquivalenceKey = "different"
		if result, err := fixture.svc.Create(ctx, exec, spec, Admission{}); err != nil || !result.Created {
			t.Fatalf("different-equivalence create = %+v, %v", result, err)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 3, 3)
	})
}

func TestCreateConcurrentExactAndEquivalentCallsConverge(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		results, errs := runConcurrentCreates(24, func(index int) (CreateResult, error) {
			return fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
		})
		for index, err := range errs {
			if err != nil {
				t.Fatalf("Create(%d) error = %v", index, err)
			}
			if !results[index].Created || !reflect.DeepEqual(results[index], results[0]) {
				t.Fatalf("exact result %d = %+v, want immutable %+v", index, results[index], results[0])
			}
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	})

	t.Run("equivalent", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		results, errs := runConcurrentCreates(24, func(index int) (CreateResult, error) {
			identity := fmt.Sprintf("call-%02d", index)
			exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-"+identity, "turn-"+identity, identity)
			return fixture.svc.Create(context.Background(), exec, spec, Admission{})
		})
		createdCount := 0
		ids := map[string]struct{}{}
		for index, err := range errs {
			if err != nil {
				t.Fatalf("Create(%d) error = %v", index, err)
			}
			if results[index].Created {
				createdCount++
			}
			ids[results[index].Task.ID] = struct{}{}
		}
		if createdCount != 1 || len(ids) != 1 {
			t.Fatalf("equivalent results created=%d task_ids=%v, want 1/1", createdCount, ids)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	})
}

func TestActivateWorldFreshAbsentIsAtomicAndConflictsNeverOverwriteHead(t *testing.T) {
	ctx := context.Background()
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	svc := NewService(store)
	world := testWorld()
	clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	const callers = 20
	results := make([]Head, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = svc.ActivateWorld(ctx, world, "run-a", clock, CheckpointRef{Status: "absent", World: world})
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil || !reflect.DeepEqual(results[index], results[0]) {
			t.Fatalf("ActivateWorld(%d) = %+v, %v; want shared head %+v", index, results[index], err, results[0])
		}
	}
	want := Head{
		Binding: Binding{World: world, RunID: "run-a", Generation: 1},
		Clock:   clock,
		Status:  "ready",
	}
	if !reflect.DeepEqual(results[0], want) {
		t.Fatalf("activated head = %+v, want %+v", results[0], want)
	}

	conflicts := []struct {
		name  string
		runID string
		clock Clock
		want  error
	}{
		{name: "different run", runID: "run-b", clock: clock, want: ErrTaskChanged},
		{name: "rewound tick", runID: "run-a", clock: Clock{ID: clock.ID, Tick: 99, Sequence: 1}, want: ErrClockRewound},
		{name: "advanced tick", runID: "run-a", clock: Clock{ID: clock.ID, Tick: 101, Sequence: 1}, want: ErrClockMismatch},
		{name: "rewound sequence", runID: "run-a", clock: Clock{ID: clock.ID, Tick: 100, Sequence: 0}, want: ErrClockRewound},
		{name: "advanced sequence", runID: "run-a", clock: Clock{ID: clock.ID, Tick: 100, Sequence: 2}, want: ErrClockMismatch},
		{name: "different clock", runID: "run-a", clock: Clock{ID: "other.clock", Tick: 100, Sequence: 1}, want: ErrClockMismatch},
	}
	for _, tt := range conflicts {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.ActivateWorld(ctx, world, tt.runID, tt.clock, CheckpointRef{Status: "absent", World: world}); !errors.Is(err, tt.want) {
				t.Fatalf("ActivateWorld() error = %v, want %v", err, tt.want)
			}
			stored, err := store.loadWorldHead(ctx, world)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.Head, want) {
				t.Fatalf("conflict overwrote head to %+v, want %+v", stored.Head, want)
			}
		})
	}
}

func TestActivateWorldRejectsInvalidOrUnsupportedReferencesWithoutWriting(t *testing.T) {
	tests := []struct {
		name  string
		world WorldKey
		runID string
		clock Clock
		ref   CheckpointRef
		want  error
	}{
		{name: "blank world", world: WorldKey{}, runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "absent"}, want: ErrInvalidTaskSpec},
		{name: "blank run", world: testWorld(), runID: " ", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "absent", World: testWorld()}, want: ErrInvalidTaskSpec},
		{name: "negative clock", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: -1}, ref: CheckpointRef{Status: "absent", World: testWorld()}, want: ErrInvalidTaskSpec},
		{name: "absent wrong world", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "absent", World: WorldKey{GameID: "fake-game", WorldID: "world-b"}}, want: ErrCheckpointInvalid},
		{name: "absent checkpoint id", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "absent", World: testWorld(), ID: "checkpoint"}, want: ErrCheckpointInvalid},
		{name: "absent checksum", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "absent", World: testWorld(), Checksum: "sum"}, want: ErrCheckpointInvalid},
		{name: "absent schema", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "absent", World: testWorld(), SchemaVersion: 1}, want: ErrCheckpointInvalid},
		{name: "confirmed unsupported", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "confirmed", World: testWorld(), ID: "checkpoint", SchemaVersion: 1}, want: ErrCheckpointMissing},
		{name: "unconfirmed unsupported", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "unconfirmed", World: testWorld()}, want: ErrCheckpointUnconfirmed},
		{name: "unknown status", world: testWorld(), runID: "run-a", clock: Clock{ID: "clock", Tick: 0}, ref: CheckpointRef{Status: "latest", World: testWorld()}, want: ErrCheckpointInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
			if _, err := NewService(store).ActivateWorld(context.Background(), tt.world, tt.runID, tt.clock, tt.ref); !errors.Is(err, tt.want) {
				t.Fatalf("ActivateWorld() error = %v, want %v", err, tt.want)
			}
			if got := totalRows(t, store.db, "task_world_heads"); got != 0 {
				t.Fatalf("world head count = %d, want 0", got)
			}
		})
	}
}

func TestActivateWorldRejectsMissingHeadWithDurableWorldState(t *testing.T) {
	t.Run("orphan task and wake", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.db.Exec(`DELETE FROM task_world_heads WHERE game_id = ? AND world_id = ?`,
			fixture.world.GameID, fixture.world.WorldID); err != nil {
			t.Fatal(err)
		}

		if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-recreated", fixture.clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world}); !errors.Is(err, ErrWorldNotReady) {
			t.Fatalf("ActivateWorld() error = %v, want ErrWorldNotReady", err)
		}
		if got := totalRows(t, fixture.store.db, "task_world_heads"); got != 0 {
			t.Fatalf("world head count = %d, want 0", got)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	})

	t.Run("checkpoint only", func(t *testing.T) {
		store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
		checkpoint := checkpointStoreFixture("checkpoint-orphan", "save-orphan", []byte(`{"tasks":[]}`))
		if err := store.insertCheckpoint(context.Background(), checkpoint); err != nil {
			t.Fatal(err)
		}
		if _, err := NewService(store).ActivateWorld(context.Background(), checkpoint.World, "run-a", checkpoint.Clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: checkpoint.World}); !errors.Is(err, ErrWorldNotReady) {
			t.Fatalf("ActivateWorld() error = %v, want ErrWorldNotReady", err)
		}
		if got := totalRows(t, store.db, "task_world_heads"); got != 0 {
			t.Fatalf("world head count = %d, want 0", got)
		}
		if got := totalRows(t, store.db, "task_checkpoints"); got != 1 {
			t.Fatalf("checkpoint count = %d, want 1", got)
		}
	})

	t.Run("other world state does not block", func(t *testing.T) {
		store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
		ownerA := testOwner()
		record, wake := taskStoreFixture(ownerA, "task-world-a", "wake-world-a")
		if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
			t.Fatal(err)
		}
		checkpoint := checkpointStoreFixture("checkpoint-world-a", "save-world-a", []byte(`{"tasks":[]}`))
		if err := store.insertCheckpoint(context.Background(), checkpoint); err != nil {
			t.Fatal(err)
		}

		worldB := WorldKey{GameID: ownerA.GameID, WorldID: "world-b"}
		clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
		head, err := NewService(store).ActivateWorld(context.Background(), worldB, "run-b", clock,
			CheckpointRef{Status: checkpointStatusAbsent, World: worldB})
		if err != nil {
			t.Fatalf("ActivateWorld(other world) error = %v", err)
		}
		if head.Binding.World != worldB || head.Binding.Generation != 1 {
			t.Fatalf("other-world head = %+v", head)
		}
	})
}

func TestActivateWorldSerializesResidualStateCheckWithHeadInsert(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	owner := testOwner()
	record, wake := taskStoreFixture(owner, "task-race", "wake-race")
	taskInserted := make(chan struct{})
	allowWakeInsert := make(chan struct{})
	store.testAfterTaskInsert = func(context.Context) error {
		close(taskInserted)
		<-allowWakeInsert
		return nil
	}
	insertDone := make(chan error, 1)
	go func() {
		insertDone <- store.insertTaskAndWake(context.Background(), record, wake)
	}()
	<-taskInserted

	activationStarted := make(chan struct{})
	activationDone := make(chan error, 1)
	go func() {
		close(activationStarted)
		_, err := NewService(store).ActivateWorld(context.Background(), testWorld(), "run-race",
			Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1},
			CheckpointRef{Status: checkpointStatusAbsent, World: testWorld()})
		activationDone <- err
	}()
	<-activationStarted
	close(allowWakeInsert)
	if err := <-insertDone; err != nil {
		t.Fatalf("insertTaskAndWake() error = %v", err)
	}
	store.testAfterTaskInsert = nil
	if err := <-activationDone; !errors.Is(err, ErrWorldNotReady) {
		t.Fatalf("concurrent ActivateWorld() error = %v, want ErrWorldNotReady", err)
	}
	if got := totalRows(t, store.db, "task_world_heads"); got != 0 {
		t.Fatalf("world head count = %d, want 0", got)
	}
	assertCreateRowCounts(t, store, owner, 1, 1)
}

func TestCreatePersistsExactInitialTaskAndWakeWithOpaqueLexemes(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.ID != "task_test_1" || created.Task.Owner != fixture.exec.Owner ||
		created.Task.State != StateWaiting || created.Task.Revision != 1 ||
		created.Task.CreatedAtGameTick != 100 || created.Task.CreatedAtUnixMS != 1_700_000_000_123 ||
		created.Task.NextWakeAt == nil || *created.Task.NextWakeAt != 200 ||
		created.Task.Progress != nil || created.Task.NeedsReconcile || created.Task.PauseReason != "" ||
		created.Task.Result != nil || len(created.Task.Operations) != 0 || len(created.Task.Evidence) != 0 || len(created.Task.Cleanup) != 0 {
		t.Fatalf("created task fields = %+v", created.Task)
	}
	if !bytes.Contains(created.Task.Spec.Source.GameTime, []byte("9007199254740993")) ||
		!bytes.Contains(created.Task.Spec.Source.GameTime, []byte("1.2300")) {
		t.Fatalf("game_time lost numeric lexemes: %s", created.Task.Spec.Source.GameTime)
	}
	if !bytes.Contains(created.Task.Spec.Contract, []byte("1.2500")) {
		t.Fatalf("contract lost numeric lexeme: %s", created.Task.Spec.Contract)
	}
	wake, err := loadOnlyWake(t, fixture.store, fixture.exec.Owner)
	if err != nil {
		t.Fatal(err)
	}
	wantWake := Wake{
		ID:               "wake_test_2",
		TaskID:           created.Task.ID,
		Owner:            fixture.exec.Owner,
		ExpectedRevision: 1,
		DueTick:          200,
		Reason:           "created",
		Status:           "pending",
		Generation:       fixture.head.Binding.Generation,
	}
	if !reflect.DeepEqual(wake, wantWake) {
		t.Fatalf("wake = %+v, want %+v", wake, wantWake)
	}
}

func TestCreateValidationAndAuthorityFailuresWriteNothing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*createFixture, *ExecutionContext, *TaskSpec, *Admission)
		want   error
	}{
		{name: "wrong owner", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) {
			exec.Owner.GameID = "other-game"
		}, want: ErrWorldMismatch},
		{name: "stale run", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) {
			exec.Binding.RunID = "run-old"
		}, want: ErrGenerationStale},
		{name: "stale generation", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) { exec.Binding.Generation = 2 }, want: ErrGenerationStale},
		{name: "clock id", mutate: func(_ *createFixture, exec *ExecutionContext, spec *TaskSpec, _ *Admission) {
			exec.Clock.ID = "other.clock"
			spec.ClockID = exec.Clock.ID
		}, want: ErrClockMismatch},
		{name: "clock tick rewound", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) { exec.Clock.Tick-- }, want: ErrClockRewound},
		{name: "clock tick advanced", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) { exec.Clock.Tick++ }, want: ErrClockMismatch},
		{name: "clock sequence rewound", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) { exec.Clock.Sequence = 0 }, want: ErrClockRewound},
		{name: "clock sequence advanced", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) { exec.Clock.Sequence++ }, want: ErrClockMismatch},
		{name: "spec clock mismatch", mutate: func(_ *createFixture, _ *ExecutionContext, spec *TaskSpec, _ *Admission) {
			spec.ClockID = "other.clock"
		}, want: ErrClockMismatch},
		{name: "wake at now", mutate: func(_ *createFixture, _ *ExecutionContext, spec *TaskSpec, _ *Admission) { spec.WakeAt = 100 }, want: ErrInvalidTaskSpec},
		{name: "wake in past", mutate: func(_ *createFixture, _ *ExecutionContext, spec *TaskSpec, _ *Admission) { spec.WakeAt = 99 }, want: ErrInvalidTaskSpec},
		{name: "wake after deadline", mutate: func(_ *createFixture, _ *ExecutionContext, spec *TaskSpec, _ *Admission) { spec.WakeAt = 301 }, want: ErrInvalidTaskSpec},
		{name: "negative deadline", mutate: func(_ *createFixture, _ *ExecutionContext, spec *TaskSpec, _ *Admission) { spec.DeadlineAt = -1 }, want: ErrInvalidTaskSpec},
		{name: "source mismatch", mutate: func(_ *createFixture, exec *ExecutionContext, _ *TaskSpec, _ *Admission) {
			exec.Source.Facts = []byte(`[]`)
		}, want: ErrSourceInvalid},
		{name: "blank event", mutate: func(_ *createFixture, exec *ExecutionContext, spec *TaskSpec, _ *Admission) {
			exec.Source.EventID = " "
			spec.Source.EventID = " "
		}, want: ErrSourceInvalid},
		{name: "blank turn", mutate: func(_ *createFixture, exec *ExecutionContext, spec *TaskSpec, _ *Admission) {
			exec.Source.TurnID = "\t"
			spec.Source.TurnID = "\t"
		}, want: ErrSourceInvalid},
		{name: "blank call", mutate: func(_ *createFixture, exec *ExecutionContext, spec *TaskSpec, _ *Admission) {
			exec.Source.CallID = "\n"
			spec.Source.CallID = "\n"
		}, want: ErrSourceInvalid},
		{name: "invalid source json", mutate: func(_ *createFixture, exec *ExecutionContext, spec *TaskSpec, _ *Admission) {
			exec.Source.Facts = []byte(`{"bad":`)
			spec.Source = exec.Source
		}, want: ErrSourceInvalid},
		{name: "invalid contract json", mutate: func(_ *createFixture, _ *ExecutionContext, spec *TaskSpec, _ *Admission) {
			spec.Contract = []byte(`{"bad":`)
		}, want: ErrInvalidTaskSpec},
		{name: "negative admission", mutate: func(_ *createFixture, _ *ExecutionContext, _ *TaskSpec, admission *Admission) {
			admission.MaxActivePerOwner = -1
		}, want: ErrInvalidTaskSpec},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			exec, spec, admission := fixture.exec, fixture.spec, Admission{}
			tt.mutate(&fixture, &exec, &spec, &admission)
			if _, err := fixture.svc.Create(context.Background(), exec, spec, admission); !errors.Is(err, tt.want) {
				t.Fatalf("Create() error = %v, want %v", err, tt.want)
			}
			if got := totalRows(t, fixture.store.db, "tasks"); got != 0 {
				t.Fatalf("tasks count = %d, want 0", got)
			}
			if got := totalRows(t, fixture.store.db, "task_wakeups"); got != 0 {
				t.Fatalf("wake count = %d, want 0", got)
			}
		})
	}
}

func TestCreateIdempotencyRejectsCorruptOrNoncanonicalStoredResponse(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateResult) []byte
	}{
		{name: "malformed json", mutate: func(*CreateResult) []byte { return []byte(`{"task":`) }},
		{name: "created false", mutate: func(result *CreateResult) []byte {
			result.Created = false
			return mustJSONBytes(t, *result)
		}},
		{name: "task id mismatch", mutate: func(result *CreateResult) []byte {
			result.Task.ID = "task-other"
			return mustJSONBytes(t, *result)
		}},
		{name: "created unix changed", mutate: func(result *CreateResult) []byte {
			result.Task.CreatedAtUnixMS++
			return mustJSONBytes(t, *result)
		}},
		{name: "invented progress", mutate: func(result *CreateResult) []byte {
			result.Task.Progress = []byte(`{"invented":true}`)
			return mustJSONBytes(t, *result)
		}},
		{name: "invented evidence", mutate: func(result *CreateResult) []byte {
			evidence := testEvidence()
			evidence.TaskID = result.Task.ID
			result.Task.Evidence = []Evidence{evidence}
			return mustJSONBytes(t, *result)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
			if err != nil {
				t.Fatal(err)
			}
			corrupt := created
			corruptJSON := tt.mutate(&corrupt)
			if _, err := fixture.store.db.Exec(`UPDATE tasks SET create_response_json = ?
				WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
				corruptJSON, fixture.exec.Owner.GameID, fixture.exec.Owner.WorldID, fixture.exec.Owner.EntityID, created.Task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("exact retry error = %v, want ErrInvalidTaskSpec", err)
			}
			assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
		})
	}

	t.Run("indexed equivalence mismatch", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.db.Exec(`UPDATE tasks SET equivalence_key = 'corrupt-index'`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("exact retry error = %v, want ErrInvalidTaskSpec", err)
		}
	})
}

func TestCreateIdempotencyRejectsRehashedNoncanonicalOrChangedResponse(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
	}{
		{name: "added whitespace", mutate: func(_ *testing.T, original []byte) []byte {
			return append(append([]byte{' ', '\n'}, original...), '\n')
		}},
		{name: "created timestamp changed", mutate: func(t *testing.T, original []byte) []byte {
			var result CreateResult
			if err := json.Unmarshal(original, &result); err != nil {
				t.Fatal(err)
			}
			result.Task.CreatedAtUnixMS++
			return mustJSONBytes(t, result)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
			if err != nil {
				t.Fatal(err)
			}
			var original []byte
			if err := fixture.store.db.QueryRow(`SELECT create_response_json FROM tasks
				WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
				fixture.exec.Owner.GameID, fixture.exec.Owner.WorldID, fixture.exec.Owner.EntityID, created.Task.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			changed := tt.mutate(t, original)
			changedHash := independentSHA256Hex(changed)
			if _, err := fixture.store.db.Exec(`UPDATE tasks SET create_response_json = ?, create_response_hash = ?
				WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
				changed, changedHash, fixture.exec.Owner.GameID, fixture.exec.Owner.WorldID, fixture.exec.Owner.EntityID, created.Task.ID); err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("exact retry error = %v, want ErrInvalidTaskSpec", err)
			}
			assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
		})
	}
}

func TestCreateCallerMutationCannotAlterDurableTaskOrExactResponse(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	originalExec := cloneExecutionContextForTest(t, fixture.exec)
	originalSpec, err := cloneTaskSpec(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	want := cloneCreateResultForTest(t, created)

	fixture.exec.Source.GameTime[0] = '['
	fixture.exec.Source.Facts[0] = '{'
	fixture.spec.Contract[0] = '['
	fixture.spec.Source.GameTime[0] = '['
	fixture.spec.Source.Facts[0] = '{'
	created.Task.Spec.Contract[0] = '['
	created.Task.Spec.Source.GameTime[0] = '['
	created.Task.Spec.Source.Facts[0] = '{'
	*created.Task.NextWakeAt = 999
	created.Task.Operations = append(created.Task.Operations, testOperation())

	stored, err := fixture.store.loadTask(context.Background(), originalExec.Owner, want.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, want.Task) {
		t.Fatalf("stored task after caller mutation = %+v, want %+v", stored, want.Task)
	}
	again, err := fixture.svc.Create(context.Background(), originalExec, originalSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("exact response after caller mutation = %+v, want %+v", again, want)
	}
}

func TestCreateFailureCancellationAndLimitsLeaveNoPartialRows(t *testing.T) {
	t.Run("injected wake failure", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		fixture.store.testAfterTaskInsert = func(context.Context) error { return errors.New("private injected failure") }
		if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrTaskConflict) {
			t.Fatalf("Create() error = %v, want ErrTaskConflict", err)
		}
		fixture.store.testAfterTaskInsert = nil
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 0, 0)
	})

	t.Run("context cancellation between rows", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		ctx, cancel := context.WithCancel(context.Background())
		fixture.store.testAfterTaskInsert = func(context.Context) error { cancel(); return nil }
		_, err := fixture.svc.Create(ctx, fixture.exec, fixture.spec, Admission{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Create() error = %v, want context.Canceled", err)
		}
		fixture.store.testAfterTaskInsert = nil
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 0, 0)
	})

	t.Run("pre-cancelled", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.svc.Create(ctx, fixture.exec, fixture.spec, Admission{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Create() error = %v, want context.Canceled", err)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 0, 0)
	})

	t.Run("oversized new task", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{MaxTaskBytes: 1})
		if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("Create() error = %v, want ErrInvalidTaskSpec", err)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 0, 0)
	})

	t.Run("world capacity preserves first", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{MaxTasksPerWorld: 1})
		first, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
		if err != nil {
			t.Fatal(err)
		}
		exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-b", "turn-b", "call-b")
		spec.EquivalenceKey = "equivalence-b"
		if _, err := fixture.svc.Create(context.Background(), exec, spec, Admission{}); !errors.Is(err, ErrTaskCapacityExceeded) {
			t.Fatalf("Create() error = %v, want ErrTaskCapacityExceeded", err)
		}
		stored, err := fixture.store.loadTask(context.Background(), fixture.exec.Owner, first.Task.ID)
		if err != nil || !reflect.DeepEqual(stored, first.Task) {
			t.Fatalf("first task after capacity failure = %+v, %v", stored, err)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	})
}

func TestCreateRequiresReadyWorldAndSafeService(t *testing.T) {
	ctx := context.Background()
	world, clock := testWorld(), Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	head := Head{Binding: Binding{World: world, RunID: "run-a", Generation: 1}, Clock: clock, Status: "ready"}
	exec, spec := createInputs(head, clock, testOwner(), "event", "turn", "call", "equivalence")

	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "missing.sqlite")})
	if _, err := NewService(store).Create(ctx, exec, spec, Admission{}); !errors.Is(err, ErrWorldNotReady) {
		t.Fatalf("missing-head Create() error = %v, want ErrWorldNotReady", err)
	}
	assertCreateRowCounts(t, store, exec.Owner, 0, 0)

	for _, tt := range []struct {
		name    string
		mutate  func(*worldHeadRow)
		wantErr error
	}{
		{name: "not ready", mutate: func(row *worldHeadRow) { row.Head.Status = "paused" }, wantErr: ErrWorldNotReady},
		{name: "save barrier", mutate: func(row *worldHeadRow) { row.SaveRequestID = "save-a"; row.BarrierStatus = "preparing" }, wantErr: ErrSaveInProgress},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			row, err := fixture.store.loadWorldHead(ctx, fixture.world)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&row)
			if err := fixture.store.putWorldHead(ctx, row); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.svc.Create(ctx, fixture.exec, fixture.spec, Admission{}); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, tt.wantErr)
			}
			assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 0, 0)
		})
	}

	var nilService *Service
	if _, err := nilService.Create(ctx, exec, spec, Admission{}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil Service.Create() error = %v, want ErrTaskConflict", err)
	}
	if _, err := NewService(nil).Create(ctx, exec, spec, Admission{}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil store Create() error = %v, want ErrTaskConflict", err)
	}
	if _, err := NewService(nil).ActivateWorld(ctx, world, "run-a", clock, CheckpointRef{Status: "absent", World: world}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil store ActivateWorld() error = %v, want ErrTaskConflict", err)
	}
	if _, err := NewService(store).Create(nil, exec, spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("nil context Create() error = %v, want ErrInvalidTaskSpec", err)
	}
}

func createInputs(head Head, clock Clock, owner session.AgentSessionKey, eventID, turnID, callID, equivalenceKey string) (ExecutionContext, TaskSpec) {
	source := SourceRef{
		Kind:     SourceKindInternal,
		EventID:  eventID,
		TurnID:   turnID,
		CallID:   callID,
		GameTime: []byte(`{"large_integer":9007199254740993,"decimal":1.2300}`),
		Facts:    []byte(`[{"kind":"clock","tick":100}]`),
	}
	exec := ExecutionContext{Owner: owner, Binding: head.Binding, Clock: clock, Source: source}
	spec := TaskSpec{
		Instruction:    "inspect later",
		ClockID:        clock.ID,
		WakeAt:         200,
		DeadlineAt:     300,
		ResultContract: ResultContractAuthoritativeEvidence,
		Contract:       []byte(`{"expected":"complete","amount":1.2500}`),
		EquivalenceKey: equivalenceKey,
		Source:         source,
	}
	return exec, spec
}

type createFixture struct {
	store *SQLiteStore
	svc   *Service
	world WorldKey
	clock Clock
	head  Head
	exec  ExecutionContext
	spec  TaskSpec
}

func newCreateFixture(t *testing.T, options StoreOptions) createFixture {
	t.Helper()
	if options.Path == "" {
		options.Path = filepath.Join(t.TempDir(), "tasks.sqlite")
	}
	store := openTaskTestStore(t, options)
	svc := deterministicTaskService(store)
	world := testWorld()
	clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	head, err := svc.ActivateWorld(context.Background(), world, "run-a", clock, CheckpointRef{Status: checkpointStatusAbsent, World: world})
	if err != nil {
		t.Fatalf("ActivateWorld() error = %v", err)
	}
	exec, spec := createInputs(head, clock, testOwner(), "event-a", "turn-a", "call-a", "equivalence-a")
	return createFixture{store: store, svc: svc, world: world, clock: clock, head: head, exec: exec, spec: spec}
}

func deterministicTaskService(store *SQLiteStore) *Service {
	svc := NewService(store)
	var counter atomic.Uint64
	svc.newID = func(prefix string) string {
		return fmt.Sprintf("%s_test_%d", prefix, counter.Add(1))
	}
	svc.nowUnixMS = func() int64 { return 1_700_000_000_123 }
	return svc
}

func withCreateCall(exec ExecutionContext, spec TaskSpec, eventID, turnID, callID string) (ExecutionContext, TaskSpec) {
	exec.Source.EventID = eventID
	exec.Source.TurnID = turnID
	exec.Source.CallID = callID
	spec.Source = exec.Source
	return exec, spec
}

func setTaskStateForCreateTest(t *testing.T, store *SQLiteStore, record Record, state State, revision uint64) {
	t.Helper()
	record.State = state
	record.Revision = revision
	if state == StateSucceeded || state == StateFailed || state == StateCancelled {
		record.NextWakeAt = nil
		record.NeedsReconcile = false
		record.PauseReason = ""
		record.NoProgressAttempts = 0
		record.ReconcileAttempts = 0
		reason := "cancelled"
		refs := []string{}
		source := record.Spec.Source
		if state == StateSucceeded || state == StateFailed {
			kind := EvidenceKindSatisfied
			if state == StateFailed {
				kind = EvidenceKindUnsatisfied
			}
			evidence := Evidence{
				FactID: "fact-terminal", TaskID: record.ID, Binding: testBinding(), StartRevision: 1,
				OccurredAt: record.CreatedAtGameTick, Kind: kind, Source: SourceRef{Kind: SourceKindEnvironment}, Applied: true,
			}
			record.Evidence = []Evidence{evidence}
			reason = kind
			refs = []string{evidence.FactID}
			source = evidence.Source
		}
		record.Result = &Result{
			ID: "result-terminal", TaskID: record.ID, Revision: revision, State: state, Reason: reason,
			OccurredAt: record.CreatedAtGameTick, EvidenceRefs: refs, Source: source,
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET state = ?, revision = ?, next_wake_at = ?, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		string(record.State), int64(record.Revision), nullableTick(record.NextWakeAt), data,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID); err != nil {
		t.Fatal(err)
	}
}

func assertCreateRowCounts(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, tasks, wakes int) {
	t.Helper()
	if got := scopedRowCount(t, store.db, "tasks", owner); got != tasks {
		t.Fatalf("tasks count = %d, want %d", got, tasks)
	}
	if got := scopedRowCount(t, store.db, "task_wakeups", owner); got != wakes {
		t.Fatalf("task_wakeups count = %d, want %d", got, wakes)
	}
}

func totalRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM " + table // table is always a test-owned literal.
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func loadOnlyWake(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey) (Wake, error) {
	t.Helper()
	var wakeID string
	if err := store.db.QueryRow(`SELECT wake_id FROM task_wakeups
		WHERE game_id = ? AND world_id = ? AND entity_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID).Scan(&wakeID); err != nil {
		return Wake{}, err
	}
	return store.loadWake(context.Background(), owner, wakeID)
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func independentSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneExecutionContextForTest(t *testing.T, value ExecutionContext) ExecutionContext {
	t.Helper()
	data := mustJSONBytes(t, value)
	var cloned ExecutionContext
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func cloneCreateResultForTest(t *testing.T, value CreateResult) CreateResult {
	t.Helper()
	data := mustJSONBytes(t, value)
	var cloned CreateResult
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func runConcurrentCreates(count int, call func(int) (CreateResult, error)) ([]CreateResult, []error) {
	results := make([]CreateResult, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = call(index)
		}(index)
	}
	wg.Wait()
	return results, errs
}
