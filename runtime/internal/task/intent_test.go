package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/session"
)

func TestApplyIntentWaitCommitsRevisionProgressAndReplacementWakeAtomically(t *testing.T) {
	fixture, created, originalWake := newIntentFixture(t, StoreOptions{})
	nextWake := int64(250)
	exec := intentExecution(fixture, created.Task, originalWake.ID, 1, "wait-a")

	got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{
		Kind:         "wait",
		NextWakeAt:   &nextWake,
		ProgressNote: "route selected",
	})
	if err != nil {
		t.Fatalf("ApplyIntent(wait) error = %v", err)
	}
	if got.ID != created.Task.ID || got.State != StateWaiting || got.Revision != 2 ||
		got.NextWakeAt == nil || *got.NextWakeAt != nextWake || got.Result != nil || got.NeedsReconcile || got.PauseReason != "" {
		t.Fatalf("wait result = %+v", got)
	}
	wantProgress := json.RawMessage(`{"author":"model","kind":"explanation","note":"route selected"}`)
	if !bytes.Equal(got.Progress, wantProgress) {
		t.Fatalf("Progress = %s, want %s", got.Progress, wantProgress)
	}
	if len(got.Evidence) != 0 {
		t.Fatalf("Evidence = %+v, want no model-authored evidence", got.Evidence)
	}

	wakes := loadTaskWakes(t, fixture.store, fixture.exec.Owner, created.Task.ID)
	if len(wakes) != 2 {
		t.Fatalf("wake count = %d, want 2: %+v", len(wakes), wakes)
	}
	byID := map[string]Wake{}
	for _, wake := range wakes {
		byID[wake.ID] = wake
	}
	consumed := byID[originalWake.ID]
	if consumed.Status != "consumed" || consumed.ExpectedRevision != 1 || consumed.DueTick != created.Task.Spec.WakeAt {
		t.Fatalf("original wake = %+v, want immutable consumed audit wake", consumed)
	}
	delete(byID, originalWake.ID)
	var replacement Wake
	for _, wake := range byID {
		replacement = wake
	}
	if replacement.ID == "" || replacement.ID == originalWake.ID || replacement.TaskID != created.Task.ID ||
		replacement.ExpectedRevision != 2 || replacement.DueTick != nextWake || replacement.Reason != "intent_wait" ||
		replacement.Status != "pending" || replacement.Generation != fixture.head.Binding.Generation ||
		replacement.Attempt != 0 || replacement.RetryAfterUnixMS != 0 || replacement.ClaimID != "" || replacement.ClaimedBy != "" {
		t.Fatalf("replacement wake = %+v", replacement)
	}

	stored, err := fixture.store.loadTask(context.Background(), fixture.exec.Owner, created.Task.ID)
	if err != nil || !reflect.DeepEqual(stored, got) {
		t.Fatalf("stored task = (%+v, %v), want %+v", stored, err, got)
	}
}

func TestApplyIntentWaitWithoutNotePreservesDurableFactsAndProgress(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	current := created.Task
	current.Progress = json.RawMessage(`{"author":"environment","status":"arrived"}`)
	current.Operations = []Operation{{
		ID: "operation-a", ActionID: "action-a", CommandFingerprint: "command-a",
		StartRevision: 1, Binding: fixture.head.Binding, Status: "registered",
		Receipt: json.RawMessage(`{"accepted":true}`),
	}}
	current.Evidence = []Evidence{{
		FactID: "fact-a", TaskID: current.ID, OperationID: "operation-a",
		Binding: fixture.head.Binding, StartRevision: 1, OccurredAt: 110,
		Kind: "progress", Details: json.RawMessage(`{"distance":3}`),
		Source:  SourceRef{Kind: SourceKindEnvironment, EventID: "fact-event", TurnID: "fact-turn", CallID: "fact-call"},
		Applied: true,
	}}
	current.Cleanup = []Cleanup{{OperationID: "operation-a", Status: CleanupStatusUnconfirmed, Reason: "lease"}}
	setRecordForIntentTest(t, fixture.store, current)

	nextWake := int64(260)
	got, err := fixture.svc.ApplyIntent(context.Background(), intentExecution(fixture, current, wake.ID, 1, "wait-preserve"), Intent{
		Kind: "wait", NextWakeAt: &nextWake,
	})
	if err != nil {
		t.Fatalf("ApplyIntent(wait) error = %v", err)
	}
	if !bytes.Equal(got.Progress, current.Progress) || !reflect.DeepEqual(got.Operations, current.Operations) ||
		!reflect.DeepEqual(got.Evidence, current.Evidence) || !reflect.DeepEqual(got.Cleanup, current.Cleanup) {
		t.Fatalf("wait did not preserve durable facts:\n got=%+v\nwant=%+v", got, current)
	}
}

func TestApplyIntentCancelCreatesStableResultConsumesEveryExecutableWakeAndPreservesCleanup(t *testing.T) {
	fixture, created, initialWake := newIntentFixture(t, StoreOptions{})
	current := created.Task
	current.Progress = json.RawMessage(`{"author":"environment","status":"travelling"}`)
	current.Operations = []Operation{{
		ID: "operation-a", ActionID: "action-a", CommandFingerprint: "command-a",
		StartRevision: 1, Binding: fixture.head.Binding, Status: "uncertain",
	}}
	current.Evidence = []Evidence{{
		FactID: "fact-applied", TaskID: current.ID, OperationID: "operation-a",
		Binding: fixture.head.Binding, StartRevision: 1, OccurredAt: fixture.clock.Tick,
		Kind: "progress", Source: SourceRef{Kind: SourceKindEnvironment, EventID: "fact-e", TurnID: "fact-t", CallID: "fact-c"},
		Applied: true,
	}}
	current.Cleanup = []Cleanup{{OperationID: "operation-a", Status: CleanupStatusUnconfirmed, Reason: "release"}}
	setRecordForIntentTest(t, fixture.store, current)
	for index, status := range []string{"claimed", "enqueued", "running"} {
		insertIntentWake(t, fixture.store, current, Wake{
			ID: fmt.Sprintf("wake-extra-%d", index), TaskID: current.ID, Owner: current.Owner,
			ExpectedRevision: uint64(index + 2), DueTick: int64(220 + index), Reason: fmt.Sprintf("extra-%d", index),
			Status: status, ClaimID: fmt.Sprintf("claim-%d", index), ClaimedBy: "runtime-a",
			Generation: fixture.head.Binding.Generation, Attempt: index + 1,
		})
	}

	exec := intentExecution(fixture, current, initialWake.ID, 1, "cancel-a")
	got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "player withdrew"})
	if err != nil {
		t.Fatalf("ApplyIntent(cancel) error = %v", err)
	}
	if got.State != StateCancelled || got.Revision != 2 || got.NextWakeAt != nil || got.Result == nil {
		t.Fatalf("cancel result = %+v", got)
	}
	wantResult := Result{
		ID: got.Result.ID, TaskID: current.ID, Revision: 2, State: StateCancelled,
		Reason: "player withdrew", OccurredAt: fixture.clock.Tick,
		EvidenceRefs: []string{}, Source: exec.Source,
	}
	if !strings.HasPrefix(got.Result.ID, "result_") || !reflect.DeepEqual(*got.Result, wantResult) {
		t.Fatalf("Result = %+v, want %+v", got.Result, wantResult)
	}
	if !bytes.Equal(got.Progress, current.Progress) || !reflect.DeepEqual(got.Operations, current.Operations) ||
		!reflect.DeepEqual(got.Evidence, current.Evidence) || !reflect.DeepEqual(got.Cleanup, current.Cleanup) {
		t.Fatalf("cancel did not preserve progress/evidence/operations/cleanup: %+v", got)
	}
	for _, storedWake := range loadTaskWakes(t, fixture.store, current.Owner, current.ID) {
		if storedWake.Status != "consumed" {
			t.Fatalf("wake %+v remained executable", storedWake)
		}
	}

	again, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "player withdrew"})
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("exact cancel retry = (%+v, %v), want %+v", again, err, got)
	}
	if again.Result.ID != got.Result.ID || again.Revision != 2 {
		t.Fatalf("exact cancel retry changed identity: %+v", again)
	}

	newExec := intentExecution(fixture, got, "", 2, "cancel-new")
	if _, err := fixture.svc.ApplyIntent(context.Background(), newExec, Intent{Kind: "cancel", Reason: "again"}); !errors.Is(err, ErrTaskTerminal) {
		t.Fatalf("new terminal ApplyIntent() error = %v, want ErrTaskTerminal", err)
	}

	createAgain, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil || !reflect.DeepEqual(createAgain, created) {
		t.Fatalf("exact Create after cancel = (%+v, %v), want immutable %+v", createAgain, err, created)
	}
	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: path, MaxTaskBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	afterReopen, err := deterministicTaskService(reopened).ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "player withdrew"})
	if err != nil || !reflect.DeepEqual(afterReopen, got) {
		t.Fatalf("exact cancel retry after reopen = (%+v, %v), want %+v", afterReopen, err, got)
	}
}

func TestApplyIntentExactRetryPrecedesTaskRevisionTimeAndWriteLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	fixture, created, wake := newIntentFixture(t, StoreOptions{Path: path})
	nextWake := int64(220)
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "wait-exact")
	want, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "wait", NextWakeAt: &nextWake, ProgressNote: "moving"})
	if err != nil {
		t.Fatalf("ApplyIntent(wait) error = %v", err)
	}
	createAfterWait, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil || !reflect.DeepEqual(createAfterWait, created) {
		t.Fatalf("exact Create after wait = (%+v, %v), want immutable %+v", createAfterWait, err, created)
	}
	var replacementID string
	for _, candidate := range loadTaskWakes(t, fixture.store, fixture.exec.Owner, created.Task.ID) {
		if candidate.Status == wakeStatusPending {
			replacementID = candidate.ID
		}
	}
	if replacementID == "" {
		t.Fatal("wait did not create a pending replacement wake")
	}
	cancelExec := intentExecution(fixture, want, replacementID, want.Revision, "later-cancel")
	if _, err := fixture.svc.ApplyIntent(context.Background(), cancelExec, Intent{Kind: "cancel", Reason: "later decision"}); err != nil {
		t.Fatalf("later cancel error = %v", err)
	}

	advanced := Clock{ID: fixture.clock.ID, Tick: 350, Sequence: 2}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, advanced)
	exec.Clock = advanced
	exec.Binding = fixture.head.Binding
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: path, MaxTaskBytes: 1})
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	svc := deterministicTaskService(reopened)

	again, err := svc.ApplyIntent(context.Background(), exec, Intent{Kind: "wait", NextWakeAt: &nextWake, ProgressNote: "moving"})
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("exact retry after reopen/time/limit = (%+v, %v), want %+v", again, err, want)
	}
	if got := taskIntentHistoryCount(t, reopened.db, created.Task.Owner, created.Task.ID); got != 2 {
		t.Fatalf("intent history entries = %d, want 2", got)
	}
}

func TestApplyIntentExactRetryStillRequiresCurrentTaskClock(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	next := int64(240)
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "clock-authority")
	intent := Intent{Kind: "wait", NextWakeAt: &next}
	if _, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); err != nil {
		t.Fatal(err)
	}
	currentClock := Clock{ID: "replacement.clock.v1", Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, currentClock)
	exec.Clock = currentClock
	if got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); !errors.Is(err, ErrClockMismatch) || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("exact retry with task clock mismatch = (%+v, %v), want zero/ErrClockMismatch", got, err)
	}
}

func TestApplyIntentExactRetryPrecedesNewUnappliedEvidenceAtSameRevision(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	next := int64(240)
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "evidence-exact")
	intent := Intent{Kind: "wait", NextWakeAt: &next}
	want, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.loadTask(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Evidence = append(current.Evidence, Evidence{
		FactID: "fact-after-response", TaskID: current.ID, Binding: fixture.head.Binding,
		StartRevision: current.Revision, OccurredAt: fixture.clock.Tick, Kind: "progress",
		Source: SourceRef{Kind: SourceKindEnvironment, EventID: "after-e", TurnID: "after-t", CallID: "after-c"},
	})
	current.NeedsReconcile = true
	setRecordForIntentTest(t, fixture.store, current)
	again, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("exact retry after same-revision Evidence = (%+v, %v), want %+v", again, err, want)
	}
}

func TestApplyIntentSameCallChangedDurableInputConflictsBeforeProspectiveChecks(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	nextWake := int64(240)
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "same-key")
	intent := Intent{Kind: "wait", NextWakeAt: &nextWake, ProgressNote: "moving"}
	want, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
	if err != nil {
		t.Fatalf("ApplyIntent() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ExecutionContext, *Intent)
	}{
		{name: "task", mutate: func(exec *ExecutionContext, _ *Intent) { exec.TaskID = "task-other" }},
		{name: "wake", mutate: func(exec *ExecutionContext, _ *Intent) { exec.WakeID = "wake-other" }},
		{name: "revision", mutate: func(exec *ExecutionContext, _ *Intent) { exec.ExpectedRevision = 2 }},
		{name: "source kind", mutate: func(exec *ExecutionContext, _ *Intent) { exec.Source.Kind = SourceKindInteraction }},
		{name: "source game time", mutate: func(exec *ExecutionContext, _ *Intent) { exec.Source.GameTime = json.RawMessage(`{"tick":101}`) }},
		{name: "source facts", mutate: func(exec *ExecutionContext, _ *Intent) { exec.Source.Facts = json.RawMessage(`[{"kind":"changed"}]`) }},
		{name: "next wake", mutate: func(_ *ExecutionContext, intent *Intent) { *intent.NextWakeAt = 250 }},
		{name: "note", mutate: func(_ *ExecutionContext, intent *Intent) { intent.ProgressNote = "changed" }},
		{name: "kind", mutate: func(_ *ExecutionContext, intent *Intent) {
			intent.Kind = "cancel"
			intent.NextWakeAt = nil
			intent.ProgressNote = ""
			intent.Reason = "changed"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changedExec := cloneExecutionContextForTest(t, exec)
			changedIntent := cloneIntentForTest(t, intent)
			tt.mutate(&changedExec, &changedIntent)
			if _, err := fixture.svc.ApplyIntent(context.Background(), changedExec, changedIntent); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed exact ApplyIntent() error = %v, want ErrIdempotencyConflict", err)
			}
		})
	}
	stored, err := fixture.store.loadTask(context.Background(), fixture.exec.Owner, created.Task.ID)
	if err != nil || !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored task = (%+v, %v), want %+v", stored, err, want)
	}
}

func TestApplyIntentExactIdentityUsesThreeDistinctOpaqueSourceFields(t *testing.T) {
	fixture, first, firstWake := newIntentFixture(t, StoreOptions{})
	firstExec := intentExecution(fixture, first.Task, firstWake.ID, 1, "opaque-first")
	firstExec.Source.EventID = "a\x00b"
	firstExec.Source.TurnID = "c"
	firstExec.Source.CallID = "d"
	if _, err := fixture.svc.ApplyIntent(context.Background(), firstExec, Intent{Kind: "wait", NextWakeAt: testInt64(240)}); err != nil {
		t.Fatalf("first ApplyIntent() error = %v", err)
	}

	secondExec, secondSpec := withCreateCall(fixture.exec, fixture.spec, "create-second-e", "create-second-t", "create-second-c")
	secondSpec.EquivalenceKey = "equivalence-second"
	second, err := fixture.svc.Create(context.Background(), secondExec, secondSpec, Admission{})
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	var secondWakeID string
	for _, candidate := range loadTaskWakes(t, fixture.store, second.Task.Owner, second.Task.ID) {
		if candidate.Status == wakeStatusPending {
			secondWakeID = candidate.ID
		}
	}
	applySecond := intentExecution(fixture, second.Task, secondWakeID, 1, "opaque-second")
	applySecond.Source.EventID = "a"
	applySecond.Source.TurnID = "b\x00c"
	applySecond.Source.CallID = "d"
	if _, err := fixture.svc.ApplyIntent(context.Background(), applySecond, Intent{Kind: "wait", NextWakeAt: testInt64(250)}); err != nil {
		t.Fatalf("distinct opaque identity ApplyIntent() error = %v", err)
	}
}

func TestApplyIntentExactIdentityIsScopedToCompleteOwner(t *testing.T) {
	fixture, first, firstWake := newIntentFixture(t, StoreOptions{})
	firstExec := intentExecution(fixture, first.Task, firstWake.ID, 1, "same-owner-key")
	if _, err := fixture.svc.ApplyIntent(context.Background(), firstExec, Intent{Kind: "wait", NextWakeAt: testInt64(240)}); err != nil {
		t.Fatal(err)
	}

	otherOwner := session.AgentSessionKey{GameID: fixture.world.GameID, WorldID: fixture.world.WorldID, EntityID: "actor-b"}
	createExec, createSpec := createInputs(fixture.head, fixture.clock, otherOwner, "create-b-e", "create-b-t", "create-b-c", "equivalence-b")
	second, err := fixture.svc.Create(context.Background(), createExec, createSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	var otherWakeID string
	for _, candidate := range loadTaskWakes(t, fixture.store, otherOwner, second.Task.ID) {
		if candidate.Status == wakeStatusPending {
			otherWakeID = candidate.ID
		}
	}
	secondExec := firstExec
	secondExec.Owner = otherOwner
	secondExec.TaskID = second.Task.ID
	secondExec.WakeID = otherWakeID
	if got, err := fixture.svc.ApplyIntent(context.Background(), secondExec, Intent{Kind: "wait", NextWakeAt: testInt64(250)}); err != nil || got.ID != second.Task.ID {
		t.Fatalf("second owner ApplyIntent() = (%+v, %v)", got, err)
	}

	wrongOwner := secondExec
	wrongOwner.TaskID = first.Task.ID
	wrongOwner.Source.EventID = "wrong-owner-e"
	wrongOwner.Source.TurnID = "wrong-owner-t"
	wrongOwner.Source.CallID = "wrong-owner-c"
	if _, err := fixture.svc.ApplyIntent(context.Background(), wrongOwner, Intent{Kind: "cancel", Reason: "stop"}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("wrong owner ApplyIntent() error = %v, want ErrTaskNotFound", err)
	}
}

func TestApplyIntentRejectsReuseOfCreateCallIdentity(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "unused")
	exec.Source = fixture.spec.Source
	if got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "wait", NextWakeAt: testInt64(250)}); !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("ApplyIntent with Create call identity = (%+v, %v), want zero/ErrIdempotencyConflict", got, err)
	}
}

func TestApplyIntentRejectsRevisionEvidenceAndAuthorityFailuresWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *createFixture, *Record, *ExecutionContext, *Intent)
		want   error
	}{
		{name: "missing task", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) { exec.TaskID = "" }, want: ErrInvalidTaskSpec},
		{name: "zero revision", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.ExpectedRevision = 0
		}, want: ErrInvalidTaskSpec},
		{name: "missing event", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.Source.EventID = ""
		}, want: ErrSourceInvalid},
		{name: "missing turn", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.Source.TurnID = ""
		}, want: ErrSourceInvalid},
		{name: "missing call", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.Source.CallID = ""
		}, want: ErrSourceInvalid},
		{name: "wrong owner", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.Owner.EntityID = "actor-other"
		}, want: ErrTaskNotFound},
		{name: "stale revision", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.ExpectedRevision = 2
		}, want: ErrTaskChanged},
		{name: "stale generation", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			exec.Binding.Generation++
		}, want: ErrGenerationStale},
		{name: "clock mismatch", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) { exec.Clock.Tick++ }, want: ErrClockMismatch},
		{name: "clock rewind", mutate: func(_ *testing.T, fixture *createFixture, _ *Record, exec *ExecutionContext, _ *Intent) {
			advanced := Clock{ID: fixture.clock.ID, Tick: fixture.clock.Tick + 1, Sequence: fixture.clock.Sequence + 1}
			setWorldClockForIntentTest(t, fixture.store, fixture.head, advanced)
		}, want: ErrClockRewound},
		{name: "needs reconcile wait", mutate: func(t *testing.T, fixture *createFixture, record *Record, _ *ExecutionContext, _ *Intent) {
			record.NeedsReconcile = true
			setRecordForIntentTest(t, fixture.store, *record)
		}, want: ErrTaskChanged},
		{name: "unapplied evidence", mutate: func(t *testing.T, fixture *createFixture, record *Record, _ *ExecutionContext, _ *Intent) {
			record.Evidence = []Evidence{{FactID: "fact-new", TaskID: record.ID, Binding: fixture.head.Binding, StartRevision: 1, OccurredAt: 100, Kind: "progress", Source: SourceRef{Kind: SourceKindEnvironment, EventID: "e", TurnID: "t", CallID: "c"}}}
			record.NeedsReconcile = true
			setRecordForIntentTest(t, fixture.store, *record)
		}, want: ErrTaskChanged},
		{name: "revision overflow", mutate: func(t *testing.T, fixture *createFixture, record *Record, exec *ExecutionContext, _ *Intent) {
			record.Revision = uint64(math.MaxInt64)
			exec.ExpectedRevision = record.Revision
			setRecordForIntentTest(t, fixture.store, *record)
		}, want: ErrInvalidTaskSpec},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			record := created.Task
			exec := intentExecution(fixture, record, wake.ID, 1, "failure")
			next := int64(250)
			intent := Intent{Kind: "wait", NextWakeAt: &next}
			tt.mutate(t, &fixture, &record, &exec, &intent)
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, fixture.exec.Owner, created.Task.ID)
			got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ApplyIntent() error = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("ApplyIntent() record = %+v, want zero", got)
			}
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, fixture.exec.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("failed ApplyIntent changed durable rows")
			}
		})
	}
}

func TestApplyIntentRejectsInvalidShapesAndWaitWindowWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		intent func() Intent
		valid  bool
	}{
		{name: "at now", intent: func() Intent { return Intent{Kind: "wait", NextWakeAt: testInt64(100)} }},
		{name: "before now", intent: func() Intent { return Intent{Kind: "wait", NextWakeAt: testInt64(99)} }},
		{name: "after deadline", intent: func() Intent { return Intent{Kind: "wait", NextWakeAt: testInt64(301)} }},
		{name: "at deadline", intent: func() Intent { return Intent{Kind: "wait", NextWakeAt: testInt64(300)} }, valid: true},
		{name: "wait missing wake", intent: func() Intent { return Intent{Kind: "wait"} }},
		{name: "wait reason", intent: func() Intent { return Intent{Kind: "wait", NextWakeAt: testInt64(250), Reason: "extra"} }},
		{name: "cancel missing reason", intent: func() Intent { return Intent{Kind: "cancel"} }},
		{name: "cancel with wake", intent: func() Intent { return Intent{Kind: "cancel", NextWakeAt: testInt64(250), Reason: "stop"} }},
		{name: "succeeded", intent: func() Intent { return Intent{Kind: "succeeded"} }},
		{name: "failed", intent: func() Intent { return Intent{Kind: "failed"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			exec := intentExecution(fixture, created.Task, wake.ID, 1, "shape")
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, fixture.exec.Owner, created.Task.ID)
			got, err := fixture.svc.ApplyIntent(context.Background(), exec, tt.intent())
			if tt.valid {
				if err != nil || got.NextWakeAt == nil || *got.NextWakeAt != 300 {
					t.Fatalf("ApplyIntent() = (%+v, %v), want deadline wait", got, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("ApplyIntent() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
			}
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, fixture.exec.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("invalid intent changed durable rows")
			}
		})
	}
}

func TestApplyIntentNilServiceStoreAndContextFailSafely(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "nil")
	intent := Intent{Kind: "cancel", Reason: "stop"}
	var nilService *Service
	for name, call := range map[string]func() (Record, error){
		"nil service": func() (Record, error) { return nilService.ApplyIntent(context.Background(), exec, intent) },
		"nil store":   func() (Record, error) { return NewService(nil).ApplyIntent(context.Background(), exec, intent) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			if !errors.Is(err, ErrTaskConflict) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("ApplyIntent() = (%+v, %v), want zero/ErrTaskConflict", got, err)
			}
		})
	}
	if got, err := fixture.svc.ApplyIntent(nil, exec, intent); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("nil-context ApplyIntent() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
	}
}

func TestApplyIntentRequiresCurrentReadyUnbarrieredWorldAndNonterminalTask(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *createFixture, *Record, *ExecutionContext)
		want   error
	}{
		{name: "missing head", mutate: func(t *testing.T, fixture *createFixture, _ *Record, _ *ExecutionContext) {
			if _, err := fixture.store.db.Exec(`DELETE FROM task_world_heads WHERE game_id = ? AND world_id = ?`, fixture.world.GameID, fixture.world.WorldID); err != nil {
				t.Fatal(err)
			}
		}, want: ErrWorldNotReady},
		{name: "not ready", mutate: func(t *testing.T, fixture *createFixture, _ *Record, _ *ExecutionContext) {
			setWorldHeadStateForIntentTest(t, fixture.store, fixture.head, "paused", "restore", "", "")
		}, want: ErrWorldNotReady},
		{name: "save barrier", mutate: func(t *testing.T, fixture *createFixture, _ *Record, _ *ExecutionContext) {
			setWorldHeadStateForIntentTest(t, fixture.store, fixture.head, worldHeadStatusReady, "", "save-a", "preparing")
		}, want: ErrSaveInProgress},
		{name: "stale run", mutate: func(_ *testing.T, _ *createFixture, _ *Record, exec *ExecutionContext) {
			exec.Binding.RunID = "run-stale"
		}, want: ErrGenerationStale},
		{name: "task clock differs", mutate: func(t *testing.T, fixture *createFixture, _ *Record, exec *ExecutionContext) {
			clock := Clock{ID: "new.clock.v1", Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
			setWorldClockForIntentTest(t, fixture.store, fixture.head, clock)
			exec.Clock = clock
		}, want: ErrClockMismatch},
		{name: "terminal", mutate: func(t *testing.T, fixture *createFixture, record *Record, exec *ExecutionContext) {
			record.State = StateCancelled
			record.Revision = 2
			record.NextWakeAt = nil
			record.Result = &Result{ID: "result-terminal", TaskID: record.ID, Revision: 2, State: StateCancelled, Reason: "done", OccurredAt: 100, EvidenceRefs: []string{}, Source: exec.Source}
			exec.ExpectedRevision = 2
			setRecordForIntentTest(t, fixture.store, *record)
		}, want: ErrTaskTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			record := created.Task
			exec := intentExecution(fixture, record, wake.ID, 1, "authority")
			tt.mutate(t, &fixture, &record, &exec)
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, fixture.exec.Owner, created.Task.ID)
			got, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "cancel", Reason: "stop"})
			if !errors.Is(err, tt.want) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("ApplyIntent() = (%+v, %v), want zero/%v", got, err, tt.want)
			}
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, fixture.exec.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("authority failure changed durable rows")
			}
		})
	}
}

func TestApplyIntentBlocksWaitAndCancelWhenFactsNeedReconcile(t *testing.T) {
	for _, kind := range []string{"wait", "cancel"} {
		for _, state := range []string{"needs_reconcile", "unapplied_evidence"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				fixture, created, wake := newIntentFixture(t, StoreOptions{})
				record := created.Task
				if state == "needs_reconcile" {
					record.NeedsReconcile = true
				} else {
					record.Evidence = []Evidence{{
						FactID: "fact-new", TaskID: record.ID, Binding: fixture.head.Binding,
						StartRevision: record.Revision, OccurredAt: fixture.clock.Tick, Kind: "progress",
						Source: SourceRef{Kind: SourceKindEnvironment, EventID: "fact-e", TurnID: "fact-t", CallID: "fact-c"},
					}}
					record.NeedsReconcile = true
				}
				setRecordForIntentTest(t, fixture.store, record)
				exec := intentExecution(fixture, record, wake.ID, record.Revision, "evidence")
				intent := Intent{Kind: kind, Reason: "stop"}
				if kind == "wait" {
					intent.Reason = ""
					intent.NextWakeAt = testInt64(250)
				}
				if _, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); !errors.Is(err, ErrTaskChanged) {
					t.Fatalf("ApplyIntent() error = %v, want ErrTaskChanged", err)
				}
			})
		}
	}
}

func TestApplyIntentObservesEvidenceCommittedWhileWaitingForWriter(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	external, err := sql.Open("sqlite", taskSQLiteDSN(fixture.store.path, 2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	external.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = external.Close() })
	tx, err := external.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	record := created.Task
	record.Evidence = []Evidence{{
		FactID: "fact-racing", TaskID: record.ID, Binding: fixture.head.Binding,
		StartRevision: record.Revision, OccurredAt: fixture.clock.Tick, Kind: "progress",
		Source: SourceRef{Kind: SourceKindEnvironment, EventID: "race-e", TurnID: "race-t", CallID: "race-c"},
	}}
	record.NeedsReconcile = true
	if _, err := tx.Exec(`UPDATE tasks SET record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		mustJSONBytes(t, record), record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}

	type outcome struct {
		record Record
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		next := int64(250)
		got, err := fixture.svc.ApplyIntent(context.Background(), intentExecution(fixture, created.Task, wake.ID, 1, "race"), Intent{Kind: "wait", NextWakeAt: &next})
		done <- outcome{record: got, err: err}
	}()
	select {
	case got := <-done:
		_ = tx.Rollback()
		t.Fatalf("ApplyIntent returned before writer committed: %+v", got)
	case <-time.After(75 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if !errors.Is(got.err, ErrTaskChanged) || !reflect.DeepEqual(got.record, Record{}) {
			t.Fatalf("ApplyIntent after evidence commit = (%+v, %v), want zero/ErrTaskChanged", got.record, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ApplyIntent did not resume after evidence writer committed")
	}
	stored, err := fixture.store.loadTask(context.Background(), record.Owner, record.ID)
	if err != nil || len(stored.Evidence) != 1 || stored.Evidence[0].FactID != "fact-racing" || stored.Evidence[0].Applied {
		t.Fatalf("stored evidence = (%+v, %v)", stored.Evidence, err)
	}
}

func TestApplyIntentInjectedFailuresAndCancellationRollBackEveryDurableRow(t *testing.T) {
	for _, kind := range []string{"wait", "cancel"} {
		stages := []string{intentStageTaskUpdated, intentStageWakesConsumed, intentStageHistoryStored}
		if kind == "wait" {
			stages = append(stages, intentStageWakeInserted)
		}
		for _, stage := range stages {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				fixture, created, wake := newIntentFixture(t, StoreOptions{})
				beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
				fixture.store.testAfterIntentStage = func(_ context.Context, got string) error {
					if got == stage {
						return errors.New("private fault payload")
					}
					return nil
				}
				exec := intentExecution(fixture, created.Task, wake.ID, 1, "fault")
				intent := Intent{Kind: kind, Reason: "private cancellation reason"}
				if kind == "wait" {
					intent.Reason = ""
					intent.NextWakeAt = testInt64(250)
					intent.ProgressNote = "private progress note"
				}
				got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
				if !errors.Is(err, ErrTaskConflict) || !reflect.DeepEqual(got, Record{}) {
					t.Fatalf("ApplyIntent() = (%+v, %v), want zero/ErrTaskConflict", got, err)
				}
				assertTaskErrorSanitized(t, err, "private fault payload", "private cancellation reason", "private progress note", fixture.store.path, "UPDATE tasks")
				afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
				if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
					t.Fatal("faulted intent left a partial durable mutation")
				}
			})
		}
	}

	t.Run("context cancelled after task update", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		ctx, cancel := context.WithCancel(context.Background())
		fixture.store.testAfterIntentStage = func(ctx context.Context, stage string) error {
			if stage == intentStageTaskUpdated {
				cancel()
			}
			return ctx.Err()
		}
		_, err := fixture.svc.ApplyIntent(ctx, intentExecution(fixture, created.Task, wake.ID, 1, "cancel-context"), Intent{Kind: "cancel", Reason: "stop"})
		if !errors.Is(err, ErrTaskConflict) || !errors.Is(err, context.Canceled) {
			t.Fatalf("ApplyIntent() error = %v, want task conflict wrapping context cancellation", err)
		}
		afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("cancelled context left a partial durable mutation")
		}
	})
}

func TestApplyIntentRejectsCorruptOrNoncanonicalHistoryAndIndexedTaskState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, session.AgentSessionKey, string)
	}{
		{name: "outer hash", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			updateIntentHistoryRaw(t, store, owner, taskID, nil, "bad-hash")
		}},
		{name: "outer whitespace rehashed", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			raw := readIntentHistoryRaw(t, store, owner, taskID)
			raw = append([]byte(" \n"), raw...)
			updateIntentHistoryRaw(t, store, owner, taskID, raw, independentSHA256Hex(raw))
		}},
		{name: "request whitespace rehashed", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, store, owner, taskID))
			history[0].RequestJSON = append([]byte(" \n"), history[0].RequestJSON...)
			history[0].RequestHash = independentSHA256Hex(history[0].RequestJSON)
			writeIntentCallsForTest(t, store, owner, taskID, history)
		}},
		{name: "response whitespace rehashed", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, store, owner, taskID))
			history[0].ResponseJSON = append([]byte(" \n"), history[0].ResponseJSON...)
			history[0].ResponseHash = independentSHA256Hex(history[0].ResponseJSON)
			writeIntentCallsForTest(t, store, owner, taskID, history)
		}},
		{name: "response immutable owner rehashed", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, store, owner, taskID))
			var response Record
			if err := json.Unmarshal(history[0].ResponseJSON, &response); err != nil {
				t.Fatal(err)
			}
			response.Owner.EntityID = "other-actor"
			history[0].ResponseJSON = mustJSONBytes(t, response)
			history[0].ResponseHash = independentSHA256Hex(history[0].ResponseJSON)
			writeIntentCallsForTest(t, store, owner, taskID, history)
		}},
		{name: "response revision rehashed", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, store, owner, taskID))
			var response Record
			if err := json.Unmarshal(history[0].ResponseJSON, &response); err != nil {
				t.Fatal(err)
			}
			response.Revision++
			history[0].ResponseJSON = mustJSONBytes(t, response)
			history[0].ResponseHash = independentSHA256Hex(history[0].ResponseJSON)
			writeIntentCallsForTest(t, store, owner, taskID, history)
		}},
		{name: "response reconcile flag rehashed", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, store, owner, taskID))
			var response Record
			if err := json.Unmarshal(history[0].ResponseJSON, &response); err != nil {
				t.Fatal(err)
			}
			response.NeedsReconcile = true
			history[0].ResponseJSON = mustJSONBytes(t, response)
			history[0].ResponseHash = independentSHA256Hex(history[0].ResponseJSON)
			writeIntentCallsForTest(t, store, owner, taskID, history)
		}},
		{name: "latest response progress diverges from current row", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, store, owner, taskID))
			var response Record
			if err := json.Unmarshal(history[0].ResponseJSON, &response); err != nil {
				t.Fatal(err)
			}
			response.Progress = json.RawMessage(`{"author":"environment","status":"invented"}`)
			history[0].ResponseJSON = mustJSONBytes(t, response)
			history[0].ResponseHash = independentSHA256Hex(history[0].ResponseJSON)
			writeIntentCallsForTest(t, store, owner, taskID, history)
		}},
		{name: "indexed state mismatch", mutate: func(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) {
			if _, err := store.db.Exec(`UPDATE tasks SET state = ? WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
				string(StatePaused), owner.GameID, owner.WorldID, owner.EntityID, taskID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			next := int64(250)
			exec := intentExecution(fixture, created.Task, wake.ID, 1, "corrupt")
			intent := Intent{Kind: "wait", NextWakeAt: &next}
			if _, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, fixture.store, created.Task.Owner, created.Task.ID)
			got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
			if !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("corrupt exact retry = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
			}
		})
	}
}

func TestApplyIntentClonesCallerInputAndReturnedExactRecords(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	next := int64(250)
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "alias")
	originalExec := cloneExecutionContextForTest(t, exec)
	intent := Intent{Kind: "wait", NextWakeAt: &next, ProgressNote: "moving"}
	originalIntent := cloneIntentForTest(t, intent)
	got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
	if err != nil {
		t.Fatal(err)
	}
	*intent.NextWakeAt = 299
	exec.Source.GameTime[0] = '['
	*got.NextWakeAt = 298
	got.Progress[0] = '['
	got.Spec.Contract[0] = '['

	again, err := fixture.svc.ApplyIntent(context.Background(), originalExec, originalIntent)
	if err != nil {
		t.Fatal(err)
	}
	if again.NextWakeAt == nil || *again.NextWakeAt != 250 || !bytes.Equal(again.Progress, json.RawMessage(`{"author":"model","kind":"explanation","note":"moving"}`)) || !json.Valid(again.Spec.Contract) {
		t.Fatalf("caller mutation altered exact response: %+v", again)
	}
	stored, err := fixture.store.loadTask(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || !reflect.DeepEqual(stored, again) {
		t.Fatalf("stored record = (%+v, %v), want %+v", stored, err, again)
	}
}

func TestApplyIntentWaitCapacityPreservesTerminalHeadroomForShortCancel(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{MaxTaskBytes: 20 << 10})
	oversized := strings.Repeat("n", 4<<10)
	next := int64(250)
	exec := intentExecution(fixture, created.Task, wake.ID, 1, "oversized")
	intent := Intent{Kind: "wait", NextWakeAt: &next, ProgressNote: oversized}
	request, err := prepareIntentRequest(exec, intent)
	if err != nil {
		t.Fatal(err)
	}
	updated := created.Task
	updated.Revision = 2
	updated.NextWakeAt = &next
	updated.Progress = json.RawMessage(fmt.Sprintf(`{"author":"model","kind":"explanation","note":%q}`, oversized))
	responseJSON := mustJSONBytes(t, updated)
	createJSON := mustJSONBytes(t, initialCreateResult(created.Task))
	historyJSON := mustJSONBytes(t, []intentCall{{
		RequestJSON: request.requestJSON, RequestHash: request.fingerprint,
		ResponseJSON: responseJSON, ResponseHash: sha256Hex(responseJSON),
	}})
	if !taskBytesFit(20<<10, len(responseJSON), len(createJSON), len(historyJSON), len(responseJSON)) {
		t.Fatal("candidate wait without terminal reserve exceeds fixture capacity")
	}
	beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	if _, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("oversized wait error = %v, want ErrInvalidTaskSpec", err)
	}
	afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
	if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
		t.Fatal("oversized wait changed durable rows")
	}
	cancelExec := intentExecution(fixture, created.Task, wake.ID, 1, "short-cancel")
	if got, err := fixture.svc.ApplyIntent(context.Background(), cancelExec, Intent{Kind: "cancel", Reason: "stop"}); err != nil || got.State != StateCancelled {
		t.Fatalf("short cancel after rejected wait = (%+v, %v)", got, err)
	}
}

func newIntentFixture(t *testing.T, options StoreOptions) (createFixture, CreateResult, Wake) {
	t.Helper()
	fixture := newCreateFixture(t, options)
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wake, err := loadOnlyWake(t, fixture.store, fixture.exec.Owner)
	if err != nil {
		t.Fatalf("loadOnlyWake() error = %v", err)
	}
	return fixture, created, wake
}

func intentExecution(fixture createFixture, record Record, wakeID string, revision uint64, suffix string) ExecutionContext {
	return ExecutionContext{
		Owner: fixture.exec.Owner, Binding: fixture.head.Binding, Clock: fixture.clock,
		Source: SourceRef{
			Kind: SourceKindInternal, EventID: "intent-event-" + suffix,
			TurnID: "intent-turn-" + suffix, CallID: "intent-call-" + suffix,
			GameTime: json.RawMessage(`{"tick":100}`), Facts: json.RawMessage(`[{"kind":"task_context"}]`),
		},
		TaskID: record.ID, WakeID: wakeID, ExpectedRevision: revision,
	}
}

func setRecordForIntentTest(t *testing.T, store *SQLiteStore, record Record) {
	t.Helper()
	data := mustJSONBytes(t, record)
	if _, err := store.db.Exec(`UPDATE tasks SET state = ?, revision = ?, next_wake_at = ?, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		string(record.State), int64(record.Revision), nullableTick(record.NextWakeAt), data,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID); err != nil {
		t.Fatal(err)
	}
}

func insertIntentWake(t *testing.T, store *SQLiteStore, record Record, wake Wake) {
	t.Helper()
	wakeJSON := mustJSONBytes(t, wake)
	if _, err := store.db.Exec(`INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		record.Spec.ClockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
		wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS, wakeJSON); err != nil {
		t.Fatal(err)
	}
}

func loadTaskWakes(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) []Wake {
	t.Helper()
	rows, err := store.db.Query(`SELECT wake_id FROM task_wakeups
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ? ORDER BY wake_id`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var wakes []Wake
	for _, id := range ids {
		wake, err := store.loadWake(context.Background(), owner, id)
		if err != nil {
			t.Fatalf("loadWake(%q) error = %v", id, err)
		}
		wakes = append(wakes, wake)
	}
	return wakes
}

func setWorldClockForIntentTest(t *testing.T, store *SQLiteStore, head Head, clock Clock) {
	t.Helper()
	row := worldHeadRow{Head: head}
	row.Head.Clock = clock
	data := mustJSONBytes(t, row)
	if _, err := store.db.Exec(`UPDATE task_world_heads SET clock_id = ?, clock_tick = ?, clock_sequence = ?, head_json = ?
		WHERE game_id = ? AND world_id = ?`, clock.ID, clock.Tick, int64(clock.Sequence), data,
		head.Binding.World.GameID, head.Binding.World.WorldID); err != nil {
		t.Fatal(err)
	}
}

func setWorldHeadStateForIntentTest(t *testing.T, store *SQLiteStore, head Head, status, reason, saveRequestID, barrierStatus string) {
	t.Helper()
	row := worldHeadRow{Head: head, SaveRequestID: saveRequestID, BarrierStatus: barrierStatus}
	row.Head.Status = status
	row.Head.Reason = reason
	data := mustJSONBytes(t, row)
	if _, err := store.db.Exec(`UPDATE task_world_heads SET status = ?, reason = ?, save_request_id = ?, barrier_status = ?, head_json = ?
		WHERE game_id = ? AND world_id = ?`, status, reason, saveRequestID, barrierStatus, data,
		head.Binding.World.GameID, head.Binding.World.WorldID); err != nil {
		t.Fatal(err)
	}
}

func cloneIntentForTest(t *testing.T, intent Intent) Intent {
	t.Helper()
	data := mustJSONBytes(t, intent)
	var cloned Intent
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func snapshotIntentRows(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) ([]byte, []byte, []byte) {
	t.Helper()
	type taskSnapshot struct {
		State, ClockID, CreateResponseHash, HistoryHash string
		Revision                                        int64
		NextWakeAt                                      sql.NullInt64
		RecordJSON, CreateResponseJSON, HistoryJSON     []byte
	}
	var taskRow taskSnapshot
	if err := store.db.QueryRow(`SELECT state, revision, clock_id, next_wake_at, record_json,
		create_response_json, create_response_hash, intent_history_json, intent_history_hash FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID).Scan(
		&taskRow.State, &taskRow.Revision, &taskRow.ClockID, &taskRow.NextWakeAt, &taskRow.RecordJSON,
		&taskRow.CreateResponseJSON, &taskRow.CreateResponseHash, &taskRow.HistoryJSON, &taskRow.HistoryHash,
	); err != nil {
		t.Fatal(err)
	}
	taskJSON := mustJSONBytes(t, taskRow)
	type wakeSnapshot struct {
		ID, GameID, WorldID, EntityID, TaskID, ClockID, Reason, Status, ClaimID, ClaimedBy string
		ExpectedRevision, DueTick, Generation, Attempt, RetryAfterUnixMS                   int64
		WakeJSON                                                                           []byte
	}
	rows, err := store.db.Query(`SELECT wake_id, game_id, world_id, entity_id, task_id, clock_id,
		expected_revision, due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json FROM task_wakeups
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ? ORDER BY wake_id`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var wakes []wakeSnapshot
	for rows.Next() {
		var wake wakeSnapshot
		if err := rows.Scan(&wake.ID, &wake.GameID, &wake.WorldID, &wake.EntityID, &wake.TaskID, &wake.ClockID,
			&wake.ExpectedRevision, &wake.DueTick, &wake.Reason, &wake.Status, &wake.ClaimID, &wake.ClaimedBy,
			&wake.Generation, &wake.Attempt, &wake.RetryAfterUnixMS, &wake.WakeJSON); err != nil {
			t.Fatal(err)
		}
		wake.WakeJSON = append([]byte(nil), wake.WakeJSON...)
		wakes = append(wakes, wake)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wakeJSON, err := json.Marshal(wakes)
	if err != nil {
		t.Fatal(err)
	}
	return taskJSON, wakeJSON, append([]byte(nil), taskRow.HistoryJSON...)
}

func taskIntentHistoryCount(t *testing.T, db *sql.DB, owner session.AgentSessionKey, taskID string) int {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(`SELECT intent_history_json FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func readIntentHistoryRaw(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) []byte {
	t.Helper()
	var raw []byte
	if err := store.db.QueryRow(`SELECT intent_history_json FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), raw...)
}

func updateIntentHistoryRaw(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string, raw []byte, hash string) {
	t.Helper()
	if raw == nil {
		raw = readIntentHistoryRaw(t, store, owner, taskID)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET intent_history_json = ?, intent_history_hash = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		raw, hash, owner.GameID, owner.WorldID, owner.EntityID, taskID); err != nil {
		t.Fatal(err)
	}
}

func decodeIntentCallsForTest(t *testing.T, raw []byte) []intentCall {
	t.Helper()
	var history []intentCall
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatal(err)
	}
	return history
}

func writeIntentCallsForTest(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string, history []intentCall) {
	t.Helper()
	raw := mustJSONBytes(t, history)
	updateIntentHistoryRaw(t, store, owner, taskID, raw, independentSHA256Hex(raw))
}
