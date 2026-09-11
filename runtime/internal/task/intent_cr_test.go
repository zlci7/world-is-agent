package task

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCreateAndApplyIntentShareOwnerCallIdentity(t *testing.T) {
	t.Run("intent then create", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		nextWake := int64(240)
		intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "shared-after-intent")
		intent := Intent{Kind: "wait", NextWakeAt: &nextWake}
		want, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
		if err != nil {
			t.Fatal(err)
		}

		createExec, createSpec := fixture.exec, fixture.spec
		createExec.Source = intentExec.Source
		createSpec.Source = intentExec.Source
		createSpec.EquivalenceKey = "identity-after-intent"
		if got, err := fixture.svc.Create(context.Background(), createExec, createSpec, Admission{}); !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, CreateResult{}) {
			t.Fatalf("Create after same-owner intent identity = (%+v, %v), want zero/ErrIdempotencyConflict", got, err)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 2)

		again, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
		if err != nil || !reflect.DeepEqual(again, want) {
			t.Fatalf("exact ApplyIntent retry after rejected Create = (%+v, %v), want %+v", again, err, want)
		}
	})

	t.Run("create then intent", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "unused")
		intentExec.Source = fixture.exec.Source
		if got, err := fixture.svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: testInt64(240)}); !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Record{}) {
			t.Fatalf("ApplyIntent after same-owner Create identity = (%+v, %v), want zero/ErrIdempotencyConflict", got, err)
		}
		again, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
		if err != nil || !reflect.DeepEqual(again, created) {
			t.Fatalf("exact Create retry after rejected ApplyIntent = (%+v, %v), want %+v", again, err, created)
		}
	})

	t.Run("complete owner isolation", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		nextWake := int64(240)
		intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "shared-across-owners")
		if _, err := fixture.svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: &nextWake}); err != nil {
			t.Fatal(err)
		}

		otherOwner := fixture.exec.Owner
		otherOwner.EntityID = "actor-other"
		createExec, createSpec := createInputs(fixture.head, fixture.clock, otherOwner,
			intentExec.Source.EventID, intentExec.Source.TurnID, intentExec.Source.CallID, "other-owner-equivalence")
		createExec.Source = intentExec.Source
		createSpec.Source = intentExec.Source
		got, err := fixture.svc.Create(context.Background(), createExec, createSpec, Admission{})
		if err != nil || !got.Created || got.Task.Owner != otherOwner {
			t.Fatalf("other-owner Create with reused opaque identity = (%+v, %v)", got, err)
		}
		assertCreateRowCounts(t, fixture.store, otherOwner, 1, 1)
	})
}

func TestCreateAndApplyIntentConcurrentIdentityRaceAllowsOneCommit(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	nextWake := int64(240)
	intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "concurrent-shared")
	intent := Intent{Kind: "wait", NextWakeAt: &nextWake}
	createExec, createSpec := fixture.exec, fixture.spec
	createExec.Source = intentExec.Source
	createSpec.Source = intentExec.Source
	createSpec.EquivalenceKey = "concurrent-shared-equivalence"

	intentHasWriter := make(chan struct{})
	releaseIntent := make(chan struct{})
	var blockOnce sync.Once
	fixture.store.testAfterIntentStage = func(ctx context.Context, stage string) error {
		if stage == intentStageTaskUpdated {
			blockOnce.Do(func() {
				close(intentHasWriter)
				select {
				case <-releaseIntent:
				case <-ctx.Done():
				}
			})
		}
		return ctx.Err()
	}
	t.Cleanup(func() { fixture.store.testAfterIntentStage = nil })

	type applyOutcome struct {
		record Record
		err    error
	}
	type createOutcome struct {
		result CreateResult
		err    error
	}
	applyDone := make(chan applyOutcome, 1)
	createStarted := make(chan struct{})
	createDone := make(chan createOutcome, 1)
	go func() {
		record, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
		applyDone <- applyOutcome{record: record, err: err}
	}()
	select {
	case <-intentHasWriter:
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyIntent did not reach the held immediate transaction")
	}
	go func() {
		close(createStarted)
		result, err := fixture.svc.Create(context.Background(), createExec, createSpec, Admission{})
		createDone <- createOutcome{result: result, err: err}
	}()
	<-createStarted
	close(releaseIntent)

	apply := <-applyDone
	createdRace := <-createDone
	if apply.err != nil || apply.record.Revision != 2 {
		t.Fatalf("concurrent ApplyIntent = (%+v, %v), want committed revision 2", apply.record, apply.err)
	}
	if !errors.Is(createdRace.err, ErrIdempotencyConflict) || !reflect.DeepEqual(createdRace.result, CreateResult{}) {
		t.Fatalf("concurrent Create = (%+v, %v), want zero/ErrIdempotencyConflict", createdRace.result, createdRace.err)
	}
	assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 2)

	again, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
	if err != nil || !reflect.DeepEqual(again, apply.record) {
		t.Fatalf("exact ApplyIntent retry after race = (%+v, %v), want %+v", again, err, apply.record)
	}
}

func TestDuplicateOwnerCallIdentityAcrossTasksFailsClosed(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	first, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	secondExec, secondSpec := withCreateCall(fixture.exec, fixture.spec, "second-event", "second-turn", "second-call")
	secondSpec.EquivalenceKey = "second-equivalence"
	second, err := fixture.svc.Create(context.Background(), secondExec, secondSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	secondWake := onlyPendingWakeForTask(t, fixture.store, second.Task)

	nextWake := int64(250)
	duplicateExec := intentExecution(fixture, second.Task, secondWake.ID, 1, "unused")
	duplicateExec.Source = fixture.exec.Source
	duplicateIntent := Intent{Kind: "wait", NextWakeAt: &nextWake}
	request, err := prepareIntentRequest(duplicateExec, duplicateIntent)
	if err != nil {
		t.Fatal(err)
	}
	duplicateCurrent := second.Task
	duplicateCurrent.Revision = 2
	duplicateCurrent.NextWakeAt = &nextWake
	responseJSON := mustJSONBytes(t, duplicateCurrent)
	call := intentCall{
		RequestJSON:  append(json.RawMessage(nil), request.requestJSON...),
		RequestHash:  independentSHA256Hex(request.requestJSON),
		ResponseJSON: responseJSON,
		ResponseHash: independentSHA256Hex(responseJSON),
	}
	setRecordForIntentTest(t, fixture.store, duplicateCurrent)
	writeIntentCallsForTest(t, fixture.store, duplicateCurrent.Owner, duplicateCurrent.ID, []intentCall{call})

	if got, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, CreateResult{}) {
		t.Fatalf("exact Create with cross-task duplicate identity = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
	}
	if got, err := fixture.svc.ApplyIntent(context.Background(), duplicateExec, duplicateIntent); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("exact ApplyIntent with cross-task duplicate identity = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
	}
	assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 2, 2)
	if first.Task.ID == second.Task.ID {
		t.Fatal("fixture did not create two distinct tasks")
	}
}

func TestCreateReservesCapacityForShortestCancelBeforeWriting(t *testing.T) {
	const reviewerLimit = 20480
	largeInstruction := strings.Repeat("x", 7250)

	t.Run("reviewer threshold rejects without partial rows", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{MaxTaskBytes: reviewerLimit})
		fixture.spec.Instruction = largeInstruction
		nextWake := fixture.spec.WakeAt
		candidate := Record{
			ID: "task_test_1", Owner: fixture.exec.Owner, Spec: fixture.spec,
			State: StateWaiting, Revision: 1, CreatedAtGameTick: fixture.clock.Tick,
			CreatedAtUnixMS: 1_700_000_000_123, NextWakeAt: &nextWake,
			Operations: []Operation{}, Evidence: []Evidence{}, Cleanup: []Cleanup{},
		}
		candidateJSON := mustJSONBytes(t, candidate)
		createResponseJSON := mustJSONBytes(t, CreateResult{Task: candidate, Created: true})
		legacyStoredBytes := len(candidateJSON) + len(createResponseJSON) + len([]byte("[]"))
		if legacyStoredBytes < 15500 || legacyStoredBytes > 16100 || legacyStoredBytes >= reviewerLimit {
			t.Fatalf("reviewer fixture stored bytes = %d, want about 15814 and below %d", legacyStoredBytes, reviewerLimit)
		}
		cancelSource := SourceRef{Kind: SourceKindInternal, EventID: "e", TurnID: "t", CallID: "c"}
		cancelRequest := intentRequest{
			TaskID: candidate.ID, ExpectedRevision: candidate.Revision, Source: cancelSource,
			Intent: Intent{Kind: "cancel", Reason: "x"},
		}
		cancelledCandidate := candidate
		cancelledCandidate.State = StateCancelled
		cancelledCandidate.Revision = 2
		cancelledCandidate.NextWakeAt = nil
		cancelledCandidate.Result = &Result{
			ID: "result_x", TaskID: candidate.ID, Revision: 2, State: StateCancelled,
			Reason: "x", OccurredAt: fixture.clock.Tick, EvidenceRefs: []string{}, Source: cancelSource,
		}
		cancelRequestJSON := mustJSONBytes(t, cancelRequest)
		cancelResponseJSON := mustJSONBytes(t, cancelledCandidate)
		shortestCancelHistoryJSON := mustJSONBytes(t, []intentCall{{
			RequestJSON: cancelRequestJSON, RequestHash: independentSHA256Hex(cancelRequestJSON),
			ResponseJSON: cancelResponseJSON, ResponseHash: independentSHA256Hex(cancelResponseJSON),
		}})
		shortestCancelBytes := len(cancelResponseJSON) + len(createResponseJSON) + len(shortestCancelHistoryJSON)
		if shortestCancelBytes <= reviewerLimit {
			t.Fatalf("shortest cancel bytes = %d, want above reviewer limit %d", shortestCancelBytes, reviewerLimit)
		}
		t.Logf("initial durable bytes=%d, shortest cancel durable bytes=%d, limit=%d", legacyStoredBytes, shortestCancelBytes, reviewerLimit)

		if got, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, CreateResult{}) {
			t.Fatalf("Create without terminal capacity = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 0, 0)
	})

	t.Run("safe threshold permits create and shortest cancel", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{MaxTaskBytes: 40 << 10})
		fixture.spec.Instruction = largeInstruction
		created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
		if err != nil {
			t.Fatalf("Create with terminal capacity error = %v", err)
		}
		wake := onlyPendingWakeForTask(t, fixture.store, created.Task)
		cancelExec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "shortest-cancel")
		cancelled, err := fixture.svc.ApplyIntent(context.Background(), cancelExec, Intent{Kind: "cancel", Reason: "x"})
		if err != nil || cancelled.State != StateCancelled || cancelled.Result == nil {
			t.Fatalf("shortest cancel after admitted Create = (%+v, %v)", cancelled, err)
		}
	})
}

func TestCancelExactHistoryAnchorsTerminalResultAndShape(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "result id", mutate: func(response *Record) { response.Result.ID = "result_forged" }},
		{name: "result occurred at", mutate: func(response *Record) { response.Result.OccurredAt++ }},
	} {
		t.Run("history "+tt.name, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			exec := intentExecution(fixture, created.Task, wake.ID, 1, "cancel-anchor")
			intent := Intent{Kind: "cancel", Reason: "stop"}
			if _, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); err != nil {
				t.Fatal(err)
			}
			history := decodeIntentCallsForTest(t, readIntentHistoryRaw(t, fixture.store, created.Task.Owner, created.Task.ID))
			var response Record
			if err := json.Unmarshal(history[0].ResponseJSON, &response); err != nil {
				t.Fatal(err)
			}
			tt.mutate(&response)
			history[0].ResponseJSON = mustJSONBytes(t, response)
			history[0].ResponseHash = independentSHA256Hex(history[0].ResponseJSON)
			writeIntentCallsForTest(t, fixture.store, created.Task.Owner, created.Task.ID, history)

			if got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("exact cancel retry with forged %s = (%+v, %v), want zero/ErrInvalidTaskSpec", tt.name, got, err)
			}
		})
	}

	for _, tt := range []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "state", mutate: func(current *Record) { current.State = StateFailed }},
		{name: "revision", mutate: func(current *Record) { current.Revision++ }},
		{name: "next wake", mutate: func(current *Record) { current.NextWakeAt = testInt64(299) }},
	} {
		t.Run("current "+tt.name, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			exec := intentExecution(fixture, created.Task, wake.ID, 1, "cancel-current-anchor")
			intent := Intent{Kind: "cancel", Reason: "stop"}
			current, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&current)
			setRecordForIntentTest(t, fixture.store, current)

			if got, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("exact cancel retry with divergent current %s = (%+v, %v), want zero/ErrInvalidTaskSpec", tt.name, got, err)
			}
		})
	}

	t.Run("same-revision cleanup and evidence may advance", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		exec := intentExecution(fixture, created.Task, wake.ID, 1, "cancel-later-facts")
		intent := Intent{Kind: "cancel", Reason: "stop"}
		want, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
		if err != nil {
			t.Fatal(err)
		}
		current := want
		current.Operations = append(current.Operations, Operation{
			ID: "operation-later", ActionID: "action-later", CommandFingerprint: "command-later",
			StartRevision: current.Revision, Binding: fixture.head.Binding, Status: OperationStatusRegistered,
		})
		current.Cleanup = append(current.Cleanup, Cleanup{OperationID: "operation-later", Status: CleanupStatusReleased})
		current.Evidence = append(current.Evidence, Evidence{
			FactID: "fact-later", TaskID: current.ID, Binding: fixture.head.Binding,
			StartRevision: current.Revision, OccurredAt: fixture.clock.Tick, Kind: EvidenceKindProgress,
			Source: SourceRef{Kind: SourceKindEnvironment, EventID: "later-e", TurnID: "later-t", CallID: "later-c"}, Applied: true,
		})
		setRecordForIntentTest(t, fixture.store, current)

		again, err := fixture.svc.ApplyIntent(context.Background(), exec, intent)
		if err != nil || !reflect.DeepEqual(again, want) {
			t.Fatalf("exact cancel retry after allowed same-revision changes = (%+v, %v), want %+v", again, err, want)
		}
	})
}

func TestApplyIntentGeneratesCandidateIDsBeforeImmediateTransaction(t *testing.T) {
	for _, kind := range []string{"wait", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			fixture, created, wake := newIntentFixture(t, StoreOptions{})
			var calls []string
			fixture.svc.newID = func(prefix string) string {
				calls = append(calls, prefix)
				if inUse := fixture.store.db.Stats().InUse; inUse != 0 {
					calls = append(calls, "inside-transaction")
				}
				return prefix + "_candidate"
			}
			exec := intentExecution(fixture, created.Task, wake.ID, 1, "candidate-ids-"+kind)
			intent := Intent{Kind: "cancel", Reason: "stop"}
			if kind == "wait" {
				intent = Intent{Kind: "wait", NextWakeAt: testInt64(240)}
			}
			if _, err := fixture.svc.ApplyIntent(context.Background(), exec, intent); err != nil {
				t.Fatal(err)
			}
			if want := []string{"result", "wake"}; !reflect.DeepEqual(calls, want) {
				t.Fatalf("newID calls = %v, want %v entirely before transaction", calls, want)
			}
		})
	}
}

func TestApplyIntentUsesClonedIntentAfterRequestPreparation(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	originalWakeAt := int64(240)
	intentExec := intentExecution(fixture, created.Task, wake.ID, 1, "cloned-request")
	intent := Intent{Kind: "wait", NextWakeAt: &originalWakeAt, ProgressNote: "original note"}
	originalExec := cloneExecutionContextForTest(t, intentExec)
	originalIntent := cloneIntentForTest(t, intent)

	mutatedWakeAt := int64(260)
	fixture.svc.newID = func(prefix string) string {
		if prefix == "wake" {
			*intent.NextWakeAt = mutatedWakeAt
			intent.ProgressNote = "mutated note"
			intentExec.Source.GameTime[0] = '['
		}
		return prefix + "_candidate"
	}

	got, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
	if err != nil {
		t.Fatalf("ApplyIntent() error = %v", err)
	}
	if *intent.NextWakeAt != mutatedWakeAt {
		t.Fatal("controlled callback did not mutate the caller-owned intent")
	}
	if got.NextWakeAt == nil || *got.NextWakeAt != *originalIntent.NextWakeAt ||
		!reflect.DeepEqual(got.Progress, json.RawMessage(`{"author":"model","kind":"explanation","note":"original note"}`)) {
		t.Fatalf("ApplyIntent() used caller mutation after request preparation: %+v", got)
	}
	pending := onlyPendingWakeForTask(t, fixture.store, got)
	if pending.DueTick != *originalIntent.NextWakeAt || pending.ExpectedRevision != got.Revision {
		t.Fatalf("replacement Wake = %+v, want original cloned wake %d/revision %d", pending, *originalIntent.NextWakeAt, got.Revision)
	}

	again, err := fixture.svc.ApplyIntent(context.Background(), originalExec, originalIntent)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("exact retry after caller mutation = (%+v, %v), want %+v", again, err, got)
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
	reopenedResult, err := deterministicTaskService(reopened).ApplyIntent(context.Background(), originalExec, originalIntent)
	if err != nil || !reflect.DeepEqual(reopenedResult, got) {
		t.Fatalf("exact retry after reopen = (%+v, %v), want %+v", reopenedResult, err, got)
	}
}

func onlyPendingWakeForTask(t *testing.T, store *SQLiteStore, task Record) Wake {
	t.Helper()
	var found *Wake
	for _, wake := range loadTaskWakes(t, store, task.Owner, task.ID) {
		if wake.Status != wakeStatusPending {
			continue
		}
		if found != nil {
			t.Fatalf("task %q has multiple pending wakes", task.ID)
		}
		candidate := wake
		found = &candidate
	}
	if found == nil {
		t.Fatalf("task %q has no pending wake", task.ID)
	}
	return *found
}
