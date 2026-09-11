package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"gameagent/runtime/internal/session"
)

func TestOperationValidationRequiresKnownStatus(t *testing.T) {
	operation := testOperation()
	operation.Status = OperationStatusRegistered
	operation.Receipt = nil
	if err := operation.Validate(); err != nil {
		t.Fatalf("registered Operation.Validate() error = %v", err)
	}

	for _, status := range []string{"", " ", "completed", "stardew_arrived"} {
		candidate := operation
		candidate.Status = status
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("Operation.Validate() status %q error = %v, want ErrInvalidTaskSpec", status, err)
		}
	}
}

func TestRegisterOperationAppendsWithoutBusinessOrWakeMutationAndSurvivesReopen(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(fixture, created.Task, "basic")
	operation := registeredOperation(exec, "operation-basic")
	beforeCreate, beforeHistory := taskIdempotencyMetadata(t, fixture.store, created.Task.Owner, created.Task.ID)
	beforeWake := mustJSONBytes(t, loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID))

	got, err := fixture.svc.RegisterOperation(context.Background(), exec, operation)
	if err != nil || !reflect.DeepEqual(got, operation) {
		t.Fatalf("RegisterOperation() = (%+v, %v), want %+v", got, err, operation)
	}
	stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != created.Task.Revision || stored.State != created.Task.State ||
		!reflect.DeepEqual(stored.NextWakeAt, created.Task.NextWakeAt) || len(stored.Operations) != 1 ||
		!reflect.DeepEqual(stored.Operations[0], operation) || stored.NeedsReconcile != created.Task.NeedsReconcile ||
		!bytes.Equal(stored.Progress, created.Task.Progress) || !reflect.DeepEqual(stored.Evidence, created.Task.Evidence) ||
		!reflect.DeepEqual(stored.Result, created.Task.Result) {
		t.Fatalf("registered record changed business state: got=%+v before=%+v", stored, created.Task)
	}
	afterCreate, afterHistory := taskIdempotencyMetadata(t, fixture.store, created.Task.Owner, created.Task.ID)
	afterWake := mustJSONBytes(t, loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID))
	if !bytes.Equal(beforeCreate, afterCreate) || !bytes.Equal(beforeHistory, afterHistory) || !bytes.Equal(beforeWake, afterWake) {
		t.Fatal("RegisterOperation changed create/intent/wake metadata")
	}

	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTaskTestStore(t, StoreOptions{Path: path})
	reopenedTask, err := NewService(reopened).Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || len(reopenedTask.Operations) != 1 || !reflect.DeepEqual(reopenedTask.Operations[0], operation) {
		t.Fatalf("reopened operation = (%+v, %v)", reopenedTask.Operations, err)
	}
	_ = wake
}

func TestRegisterOperationExactRetryIsImmutableAcrossBusinessRevisionAndTerminalState(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(fixture, created.Task, "exact")
	operation := registeredOperation(exec, "operation-exact")
	want, err := fixture.svc.RegisterOperation(context.Background(), exec, operation)
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.State = StateCancelled
	current.Revision = 2
	current.NextWakeAt = nil
	current.Result = &Result{
		ID: "result-terminal", TaskID: current.ID, Revision: 2, State: StateCancelled,
		Reason: "stop", OccurredAt: fixture.clock.Tick, EvidenceRefs: []string{}, Source: exec.Source,
	}
	setRecordForIntentTest(t, fixture.store, current)

	again, err := fixture.svc.RegisterOperation(context.Background(), exec, operation)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("exact retry after terminal transition = (%+v, %v), want %+v", again, err, want)
	}
	changed := operation
	changed.ActionID = "other-action"
	if got, err := fixture.svc.RegisterOperation(context.Background(), exec, changed); !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Operation{}) {
		t.Fatalf("conflicting retry = (%+v, %v), want zero/ErrIdempotencyConflict", got, err)
	}
}

func TestRegisterOperationExactRetryStillRequiresTaskClockToMatchCurrentHead(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(fixture, created.Task, "clock-exact")
	operation := registeredOperation(exec, "operation-clock-exact")
	if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
		t.Fatal(err)
	}
	currentClock := Clock{ID: "replacement.clock.v1", Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, currentClock)
	exec.Clock = currentClock
	if got, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); !errors.Is(err, ErrClockMismatch) || !reflect.DeepEqual(got, Operation{}) {
		t.Fatalf("exact retry with task/head clock mismatch = (%+v, %v), want zero/ErrClockMismatch", got, err)
	}
}

func TestRegisterOperationSerializesIdenticalAndConflictingRaces(t *testing.T) {
	t.Run("identical", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "identical")
		operation := registeredOperation(exec, "operation-race")
		results, errs := runConcurrentOperations(2, func(index int) (Operation, error) {
			return fixture.svc.RegisterOperation(context.Background(), exec, operation)
		})
		for index := range errs {
			if errs[index] != nil || !reflect.DeepEqual(results[index], operation) {
				t.Fatalf("call %d = (%+v, %v)", index, results[index], errs[index])
			}
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || len(stored.Operations) != 1 {
			t.Fatalf("stored Operations = (%+v, %v)", stored.Operations, err)
		}
	})

	t.Run("conflicting", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "conflicting")
		left := registeredOperation(exec, "operation-conflict")
		right := left
		right.CommandFingerprint = "command-other"
		operations := []Operation{left, right}
		_, errs := runConcurrentOperations(2, func(index int) (Operation, error) {
			return fixture.svc.RegisterOperation(context.Background(), exec, operations[index])
		})
		successes, conflicts := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrIdempotencyConflict):
				conflicts++
			default:
				t.Fatalf("race error = %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("race outcomes successes=%d conflicts=%d", successes, conflicts)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || len(stored.Operations) != 1 || stored.Operations[0].ID != left.ID {
			t.Fatalf("stored Operations = (%+v, %v)", stored.Operations, err)
		}
	})
}

func TestRegisterOperationRejectsSameWorldCrossTaskIdentityButIsolatesOtherWorlds(t *testing.T) {
	fixture, first, _ := newIntentFixture(t, StoreOptions{})
	firstExec := operationExecution(fixture, first.Task, "first")
	operation := registeredOperation(firstExec, "operation-world-identity")
	if _, err := fixture.svc.RegisterOperation(context.Background(), firstExec, operation); err != nil {
		t.Fatal(err)
	}

	secondExec, secondSpec := withCreateCall(fixture.exec, fixture.spec, "op-e2", "op-t2", "op-c2")
	secondSpec.EquivalenceKey = "op-second"
	second, err := fixture.svc.Create(context.Background(), secondExec, secondSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	secondOperation := operation
	secondOperation.StartRevision = second.Task.Revision
	if got, err := fixture.svc.RegisterOperation(context.Background(), operationExecution(fixture, second.Task, "second"), secondOperation); !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Operation{}) {
		t.Fatalf("same-world cross-task operation = (%+v, %v), want zero/conflict", got, err)
	}

	otherWorld := WorldKey{GameID: fixture.world.GameID, WorldID: "world-other"}
	otherClock := fixture.clock
	otherHead, err := fixture.svc.ActivateWorld(context.Background(), otherWorld, "run-other", otherClock, CheckpointRef{Status: checkpointStatusAbsent, World: otherWorld})
	if err != nil {
		t.Fatal(err)
	}
	otherOwner := first.Task.Owner
	otherOwner.WorldID = otherWorld.WorldID
	otherExec, otherSpec := createInputs(otherHead, otherClock, otherOwner, "op-oe", "op-ot", "op-oc", "op-other")
	other, err := fixture.svc.Create(context.Background(), otherExec, otherSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	otherOpExec := ExecutionContext{Owner: otherOwner, Binding: otherHead.Binding, Clock: otherClock,
		Source: SourceRef{Kind: SourceKindInternal}, TaskID: other.Task.ID, ExpectedRevision: other.Task.Revision}
	otherOperation := registeredOperation(otherOpExec, operation.ID)
	if got, err := fixture.svc.RegisterOperation(context.Background(), otherOpExec, otherOperation); err != nil || got.ID != operation.ID {
		t.Fatalf("other-world operation = (%+v, %v)", got, err)
	}
}

func TestRegisterOperationRejectsInvalidAuthorityTaskAndShapeWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, createFixture, *Record, *ExecutionContext, *Operation)
		want   error
	}{
		{name: "blank task", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, _ *Operation) {
			exec.TaskID = " "
		}, want: ErrInvalidTaskSpec},
		{name: "zero revision", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, _ *Operation) {
			exec.ExpectedRevision = 0
		}, want: ErrInvalidTaskSpec},
		{name: "wrong owner", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, _ *Operation) {
			exec.Owner.EntityID = "actor-other"
		}, want: ErrTaskNotFound},
		{name: "stale revision", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, op *Operation) {
			exec.ExpectedRevision++
			op.StartRevision++
		}, want: ErrTaskChanged},
		{name: "revision overflow", mutate: func(t *testing.T, f createFixture, record *Record, exec *ExecutionContext, op *Operation) {
			record.Revision = uint64(math.MaxInt64)
			exec.ExpectedRevision = record.Revision
			op.StartRevision = record.Revision
			setRecordForIntentTest(t, f.store, *record)
		}, want: ErrInvalidTaskSpec},
		{name: "stale generation", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, op *Operation) {
			exec.Binding.Generation++
			op.Binding = exec.Binding
		}, want: ErrGenerationStale},
		{name: "clock mismatch", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, _ *Operation) {
			exec.Clock.Tick++
		}, want: ErrClockMismatch},
		{name: "clock id", mutate: func(_ *testing.T, _ createFixture, _ *Record, exec *ExecutionContext, _ *Operation) {
			exec.Clock.ID = "other.clock"
		}, want: ErrClockMismatch},
		{name: "unknown status", mutate: func(_ *testing.T, _ createFixture, _ *Record, _ *ExecutionContext, op *Operation) {
			op.Status = "finished"
		}, want: ErrInvalidTaskSpec},
		{name: "receipt", mutate: func(_ *testing.T, _ createFixture, _ *Record, _ *ExecutionContext, op *Operation) {
			op.Receipt = json.RawMessage(`{"ok":true}`)
		}, want: ErrInvalidTaskSpec},
		{name: "binding", mutate: func(_ *testing.T, _ createFixture, _ *Record, _ *ExecutionContext, op *Operation) {
			op.Binding.Generation++
		}, want: ErrGenerationStale},
		{name: "start revision", mutate: func(_ *testing.T, _ createFixture, _ *Record, _ *ExecutionContext, op *Operation) { op.StartRevision++ }, want: ErrTaskChanged},
		{name: "terminal", mutate: func(t *testing.T, f createFixture, record *Record, _ *ExecutionContext, _ *Operation) {
			record.State = StateCancelled
			record.Revision = 2
			record.NextWakeAt = nil
			record.Result = &Result{ID: "result-terminal", TaskID: record.ID, Revision: 2, State: StateCancelled, Reason: "stop", OccurredAt: f.clock.Tick, EvidenceRefs: []string{}, Source: f.exec.Source}
			setRecordForIntentTest(t, f.store, *record)
		}, want: ErrTaskTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			record := created.Task
			exec := operationExecution(fixture, record, "invalid")
			operation := registeredOperation(exec, "operation-invalid")
			tt.mutate(t, fixture, &record, &exec, &operation)
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			got, err := fixture.svc.RegisterOperation(context.Background(), exec, operation)
			if !errors.Is(err, tt.want) || !reflect.DeepEqual(got, Operation{}) {
				t.Fatalf("RegisterOperation() = (%+v, %v), want zero/%v", got, err, tt.want)
			}
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("failed registration changed durable rows")
			}
		})
	}
}

func TestRegisterOperationRollbackContextAndCallerMutationKeepOriginalRows(t *testing.T) {
	for _, stage := range []string{noRevisionStageOperationMerged} {
		t.Run(stage, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			exec := operationExecution(fixture, created.Task, "rollback")
			operation := registeredOperation(exec, "operation-rollback")
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			fixture.store.testAfterNoRevisionStage = func(_ context.Context, got string) error {
				if got == stage {
					return errors.New("sensitive-operation-payload")
				}
				return nil
			}
			if got, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err == nil || !reflect.DeepEqual(got, Operation{}) {
				t.Fatalf("faulted RegisterOperation = (%+v, %v)", got, err)
			}
			assertTaskErrorSanitized(t, errForOperation(fixture.svc, exec, operation), "sensitive-operation-payload", fixture.store.path)
			fixture.store.testAfterNoRevisionStage = nil
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("faulted registration changed rows")
			}
		})
	}

	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(fixture, created.Task, "clone")
	operation := registeredOperation(exec, "operation-clone")
	operation.Receipt = json.RawMessage{}
	stored, err := fixture.svc.RegisterOperation(context.Background(), exec, operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.CommandFingerprint = "caller-mutated"
	operation.Binding.RunID = "caller-run"
	stored.ActionID = "returned-mutated"
	again, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || len(again.Operations) != 1 || again.Operations[0].CommandFingerprint != "command-operation-clone" || again.Operations[0].ActionID != "action-operation-clone" {
		t.Fatalf("caller mutation reached durable operation: (%+v, %v)", again.Operations, err)
	}
}

func operationExecution(fixture createFixture, record Record, suffix string) ExecutionContext {
	return ExecutionContext{
		Owner: record.Owner, Binding: fixture.head.Binding, Clock: fixture.clock,
		Source: SourceRef{Kind: SourceKindInternal, EventID: "operation-e-" + suffix, TurnID: "operation-t-" + suffix, CallID: "operation-c-" + suffix},
		TaskID: record.ID, ExpectedRevision: record.Revision,
	}
}

func registeredOperation(exec ExecutionContext, id string) Operation {
	return Operation{
		ID: id, ActionID: "action-" + id, CommandFingerprint: "command-" + id,
		StartRevision: exec.ExpectedRevision, Binding: exec.Binding, Status: OperationStatusRegistered,
	}
}

func runConcurrentOperations(count int, call func(int) (Operation, error)) ([]Operation, []error) {
	results := make([]Operation, count)
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

func taskIdempotencyMetadata(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) ([]byte, []byte) {
	t.Helper()
	var createResponse, intentHistory []byte
	if err := store.db.QueryRow(`SELECT create_response_json, intent_history_json FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID).Scan(&createResponse, &intentHistory); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), createResponse...), append([]byte(nil), intentHistory...)
}

func errForOperation(svc *Service, exec ExecutionContext, operation Operation) error {
	_, err := svc.RegisterOperation(context.Background(), exec, operation)
	return err
}

func TestReadUsesCompleteOwnerAndReturnsDetachedRecordsWithoutWrites(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	svc := NewService(store)
	ownerA := testOwner()
	ownerB := ownerA
	ownerB.EntityID = "actor-b"
	worldB := ownerA
	worldB.WorldID = "world-b"

	for index, owner := range []session.AgentSessionKey{ownerA, ownerB, worldB} {
		record, wake := taskStoreFixture(owner, "task-collision", "wake-read-"+string(rune('a'+index)))
		record.Progress = json.RawMessage(`{"owner":"` + owner.EntityID + `","nested":[1,2]}`)
		if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
			t.Fatalf("insert owner %+v: %v", owner, err)
		}
	}
	beforeTask, beforeWake := readTaskAndWakeBytes(t, store, ownerA, "task-collision")

	got, err := svc.Read(context.Background(), ownerA, "task-collision")
	if err != nil || got.Owner != ownerA || got.ID != "task-collision" {
		t.Fatalf("Read(ownerA) = (%+v, %v)", got, err)
	}
	gotB, err := svc.Read(context.Background(), ownerB, "task-collision")
	if err != nil || gotB.Owner != ownerB {
		t.Fatalf("Read(ownerB) = (%+v, %v)", gotB, err)
	}
	gotWorldB, err := svc.Read(context.Background(), worldB, "task-collision")
	if err != nil || gotWorldB.Owner != worldB {
		t.Fatalf("Read(worldB) = (%+v, %v)", gotWorldB, err)
	}
	missing := ownerA
	missing.EntityID = "actor-missing"
	if hidden, err := svc.Read(context.Background(), missing, "task-collision"); !errors.Is(err, ErrTaskNotFound) || !reflect.DeepEqual(hidden, Record{}) {
		t.Fatalf("Read(wrong owner) = (%+v, %v), want zero/ErrTaskNotFound", hidden, err)
	}

	got.Progress[0] = '['
	got.Spec.Contract[0] = '['
	got.Operations = append(got.Operations, Operation{ID: "caller-only"})
	again, err := svc.Read(context.Background(), ownerA, "task-collision")
	if err != nil || bytes.Equal(again.Progress, got.Progress) || bytes.Equal(again.Spec.Contract, got.Spec.Contract) || len(again.Operations) != 0 {
		t.Fatalf("Read() aliased caller mutation: (%+v, %v)", again, err)
	}
	afterTask, afterWake := readTaskAndWakeBytes(t, store, ownerA, "task-collision")
	if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) {
		t.Fatal("Read() modified durable rows")
	}
}

func TestListUsesSQLLimitDeterministicOrderIsolationAndDetachedSlices(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	svc := NewService(store)
	owner := testOwner()
	other := owner
	other.EntityID = "actor-other"
	for _, id := range []string{"task-c", "task-a", "task-b"} {
		record, wake := taskStoreFixture(owner, id, "wake-"+id)
		if err := store.insertTaskAndWake(context.Background(), record, wake); err != nil {
			t.Fatal(err)
		}
	}
	otherRecord, otherWake := taskStoreFixture(other, "task-a", "wake-other")
	if err := store.insertTaskAndWake(context.Background(), otherRecord, otherWake); err != nil {
		t.Fatal(err)
	}

	got, err := svc.List(context.Background(), owner, 2)
	if err != nil || len(got) != 2 || got[0].ID != "task-a" || got[1].ID != "task-b" {
		t.Fatalf("List(limit=2) = (%+v, %v)", got, err)
	}
	got[0].Spec.Contract[0] = '['
	got[0].ID = "caller-mutated"
	again, err := svc.List(context.Background(), owner, 3)
	if err != nil || len(again) != 3 || again[0].ID != "task-a" || bytes.Equal(again[0].Spec.Contract, got[0].Spec.Contract) {
		t.Fatalf("List() reused returned storage: (%+v, %v)", again, err)
	}
	otherOnly, err := svc.List(context.Background(), other, 10)
	if err != nil || len(otherOnly) != 1 || otherOnly[0].Owner != other {
		t.Fatalf("List(other) = (%+v, %v)", otherOnly, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1} {
		empty, err := svc.List(context.Background(), owner, limit)
		if err != nil || empty == nil || len(empty) != 0 {
			t.Fatalf("List(limit=%d) after close = (%+v, %v), want nonnil empty", limit, empty, err)
		}
	}
}

func TestReadAndListFailClosedOnCorruptMetadataWithoutPartialResults(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	firstExec, firstSpec := withCreateCall(fixture.exec, fixture.spec, "read-e1", "read-t1", "read-c1")
	firstSpec.EquivalenceKey = "read-first"
	first, err := fixture.svc.Create(context.Background(), firstExec, firstSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	secondExec, secondSpec := withCreateCall(fixture.exec, fixture.spec, "read-e2", "read-t2", "read-c2")
	secondSpec.EquivalenceKey = "read-second"
	second, err := fixture.svc.Create(context.Background(), secondExec, secondSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.ID > second.Task.ID {
		first, second = second, first
	}
	if _, err := fixture.store.db.Exec(`UPDATE tasks SET create_response_hash = 'corrupt'
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		second.Task.Owner.GameID, second.Task.Owner.WorldID, second.Task.Owner.EntityID, second.Task.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := fixture.svc.Read(context.Background(), second.Task.Owner, second.Task.ID); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("Read(corrupt) = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
	}
	if got, err := fixture.svc.List(context.Background(), second.Task.Owner, 10); !errors.Is(err, ErrInvalidTaskSpec) || got != nil {
		t.Fatalf("List(corrupt) = (%+v, %v), want nil/ErrInvalidTaskSpec", got, err)
	}
}

func TestReadAndListRejectUnsafeServiceOwnerTaskAndContext(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	invalidOwner := created.Task.Owner
	invalidOwner.EntityID = " "
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	readCases := []struct {
		name string
		svc  *Service
		ctx  context.Context
		own  session.AgentSessionKey
		id   string
		want error
	}{
		{name: "nil service", svc: nil, ctx: context.Background(), own: created.Task.Owner, id: created.Task.ID, want: ErrTaskConflict},
		{name: "nil store", svc: NewService(nil), ctx: context.Background(), own: created.Task.Owner, id: created.Task.ID, want: ErrTaskConflict},
		{name: "nil context", svc: fixture.svc, ctx: nil, own: created.Task.Owner, id: created.Task.ID, want: ErrInvalidTaskSpec},
		{name: "cancelled context", svc: fixture.svc, ctx: cancelled, own: created.Task.Owner, id: created.Task.ID, want: ErrTaskConflict},
		{name: "invalid owner", svc: fixture.svc, ctx: context.Background(), own: invalidOwner, id: created.Task.ID, want: ErrInvalidTaskSpec},
		{name: "blank task", svc: fixture.svc, ctx: context.Background(), own: created.Task.Owner, id: " ", want: ErrInvalidTaskSpec},
	}
	for _, tt := range readCases {
		t.Run("Read/"+tt.name, func(t *testing.T) {
			if got, err := tt.svc.Read(tt.ctx, tt.own, tt.id); !errors.Is(err, tt.want) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("Read() = (%+v, %v), want zero/%v", got, err, tt.want)
			}
		})
	}
	for _, tt := range readCases[:5] {
		t.Run("List/"+tt.name, func(t *testing.T) {
			if got, err := tt.svc.List(tt.ctx, tt.own, 1); !errors.Is(err, tt.want) || got != nil {
				t.Fatalf("List() = (%+v, %v), want nil/%v", got, err, tt.want)
			}
		})
	}
}

func readTaskAndWakeBytes(t *testing.T, store *SQLiteStore, owner session.AgentSessionKey, taskID string) ([]byte, []byte) {
	t.Helper()
	var taskJSON []byte
	if err := store.db.QueryRow(`SELECT record_json FROM tasks
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID).Scan(&taskJSON); err != nil {
		t.Fatal(err)
	}
	var wakeJSON []byte
	if err := store.db.QueryRow(`SELECT wake_json FROM task_wakeups
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID).Scan(&wakeJSON); err != nil {
		t.Fatal(err)
	}
	return taskJSON, wakeJSON
}
