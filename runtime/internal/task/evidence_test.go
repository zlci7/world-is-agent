package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestEvidenceValidationUsesGenericKindsAndWaitShape(t *testing.T) {
	for _, kind := range []string{
		EvidenceKindProgress,
		EvidenceKindSatisfied,
		EvidenceKindUnsatisfied,
		EvidenceKindInterrupted,
	} {
		evidence := testEvidence()
		evidence.Kind = kind
		evidence.Applied = false
		evidence.RevalidatedIn = nil
		if kind != EvidenceKindProgress {
			evidence.WaitUntil = nil
		}
		if err := evidence.Validate(); err != nil {
			t.Fatalf("Evidence.Validate() kind %q error = %v", kind, err)
		}
	}

	for _, kind := range []string{"", " ", "met", "expired"} {
		evidence := testEvidence()
		evidence.Kind = kind
		evidence.WaitUntil = nil
		evidence.RevalidatedIn = nil
		if err := evidence.Validate(); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("Evidence.Validate() kind %q error = %v, want ErrInvalidTaskSpec", kind, err)
		}
	}

	for _, kind := range []string{EvidenceKindSatisfied, EvidenceKindUnsatisfied, EvidenceKindInterrupted} {
		evidence := testEvidence()
		evidence.Kind = kind
		evidence.RevalidatedIn = nil
		if err := evidence.Validate(); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("Evidence.Validate() %q with WaitUntil error = %v, want ErrInvalidTaskSpec", kind, err)
		}
	}
}

func TestAdmitEvidenceDeduplicatesFactAndSourceIdentityAndSurvivesAppliedTerminalState(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(fixture, created.Task, "evidence-idempotency")
	operation := registeredOperation(exec, "operation-evidence-idempotency")
	if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
		t.Fatal(err)
	}
	evidence := operationEvidence(operation, created.Task.ID, "fact-idempotent", EvidenceKindSatisfied)
	evidence.Details = json.RawMessage(`{"outcome":"met","amount":1.2300}`)
	evidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "source-event", TurnID: "source-turn", CallID: "source-call", Facts: json.RawMessage(`[{"opaque":"met"}]`)}

	added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
	if err != nil || !added {
		t.Fatalf("first AdmitEvidence() = (%v, %v), want (true, nil)", added, err)
	}
	added, err = fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
	if err != nil || added {
		t.Fatalf("duplicate AdmitEvidence() = (%v, %v), want (false, nil)", added, err)
	}

	changed := cloneEvidenceForTest(t, evidence)
	changed.Details = json.RawMessage(`{"outcome":"unsatisfied"}`)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, changed); !errors.Is(err, ErrEvidenceConflict) || added {
		t.Fatalf("changed FactID = (%v, %v), want false/ErrEvidenceConflict", added, err)
	}
	reusedSource := cloneEvidenceForTest(t, evidence)
	reusedSource.FactID = "fact-other"
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, reusedSource); !errors.Is(err, ErrEvidenceConflict) || added {
		t.Fatalf("reused source identity = (%v, %v), want false/ErrEvidenceConflict", added, err)
	}

	current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Evidence[0].Applied = true
	current.NeedsReconcile = false
	current.State = StateCancelled
	current.Revision = 2
	current.NextWakeAt = nil
	current.Result = &Result{ID: "result-after-evidence", TaskID: current.ID, Revision: 2, State: StateCancelled,
		Reason: "stopped", OccurredAt: fixture.clock.Tick, EvidenceRefs: []string{evidence.FactID}, Source: evidence.Source}
	setRecordForIntentTest(t, fixture.store, current)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || added {
		t.Fatalf("duplicate after Applied+terminal = (%v, %v), want false/nil", added, err)
	}
	stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || len(stored.Evidence) != 1 || !stored.Evidence[0].Applied || stored.State != StateCancelled {
		t.Fatalf("duplicate mutated terminal record: (%+v, %v)", stored, err)
	}
}

func TestAdmitEvidenceConcurrentIdenticalAndConflictingFactsAppendAtMostOnce(t *testing.T) {
	for _, variant := range []string{"identical", "conflicting"} {
		t.Run(variant, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-race", EvidenceKindProgress)
			evidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "race-event", TurnID: "race-turn", CallID: "race-call"}
			facts := []Evidence{evidence, cloneEvidenceForTest(t, evidence)}
			if variant == "conflicting" {
				facts[1].Details = json.RawMessage(`{"different":true}`)
			}
			added := make([]bool, 2)
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for index := range facts {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					added[index], errs[index] = fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, facts[index])
				}(index)
			}
			wg.Wait()
			trueCount, falseCount, conflictCount := 0, 0, 0
			for index, err := range errs {
				switch {
				case err == nil && added[index]:
					trueCount++
				case err == nil && !added[index]:
					falseCount++
				case errors.Is(err, ErrEvidenceConflict) && !added[index]:
					conflictCount++
				default:
					t.Fatalf("call %d = (%v, %v)", index, added[index], err)
				}
			}
			if trueCount != 1 || variant == "identical" && falseCount != 1 || variant == "conflicting" && conflictCount != 1 {
				t.Fatalf("outcomes true=%d false=%d conflict=%d", trueCount, falseCount, conflictCount)
			}
			stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil || len(stored.Evidence) != 1 {
				t.Fatalf("stored Evidence = (%+v, %v)", stored.Evidence, err)
			}
		})
	}
}

func TestAdmitEvidenceRejectsCompleteSourceIdentityReusedAcrossSameWorldTasks(t *testing.T) {
	fixture, first, _ := newIntentFixture(t, StoreOptions{})
	firstEvidence := taskEvidence(fixture.head.Binding, first.Task, "fact-source-world-first", EvidenceKindProgress)
	firstEvidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "shared-event", TurnID: "shared-turn", CallID: "shared-call"}
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, firstEvidence); err != nil || !added {
		t.Fatalf("first AdmitEvidence() = (%v, %v), want true/nil", added, err)
	}

	secondExec, secondSpec := withCreateCall(fixture.exec, fixture.spec, "source-task-e2", "source-task-t2", "source-task-c2")
	secondSpec.EquivalenceKey = "source-task-second"
	second, err := fixture.svc.Create(context.Background(), secondExec, secondSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	secondEvidence := taskEvidence(fixture.head.Binding, second.Task, "fact-source-world-second", EvidenceKindProgress)
	secondEvidence.Source = firstEvidence.Source
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, secondEvidence); !errors.Is(err, ErrEvidenceConflict) || added {
		t.Fatalf("same-world source reuse = (%v, %v), want false/ErrEvidenceConflict", added, err)
	}
	stored, err := fixture.svc.Read(context.Background(), second.Task.Owner, second.Task.ID)
	if err != nil || len(stored.Evidence) != 0 || stored.NeedsReconcile {
		t.Fatalf("second Task changed = (%+v, %v)", stored, err)
	}
}

func TestAdmitEvidenceKeepsCompleteSourceIdentityIsolatedAcrossWorlds(t *testing.T) {
	store := openTaskTestStore(t, StoreOptions{Path: t.TempDir() + "/tasks.sqlite"})
	svc := deterministicTaskService(store)
	clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
	worlds := []WorldKey{{GameID: "fake-game", WorldID: "source-world-a"}, {GameID: "fake-game", WorldID: "source-world-b"}}
	for index, world := range worlds {
		head, err := svc.ActivateWorld(context.Background(), world, "source-run", clock, CheckpointRef{Status: checkpointStatusAbsent, World: world})
		if err != nil {
			t.Fatal(err)
		}
		owner := testOwner()
		owner.WorldID = world.WorldID
		exec, spec := createInputs(head, clock, owner, "create-event-"+world.WorldID, "create-turn-"+world.WorldID, "create-call-"+world.WorldID, "source-"+world.WorldID)
		created, err := svc.Create(context.Background(), exec, spec, Admission{})
		if err != nil {
			t.Fatal(err)
		}
		evidence := taskEvidence(head.Binding, created.Task, "fact-source-world-"+string(rune('a'+index)), EvidenceKindProgress)
		evidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "shared-event", TurnID: "shared-turn", CallID: "shared-call"}
		if added, err := svc.AdmitEvidence(context.Background(), head.Binding, evidence); err != nil || !added {
			t.Fatalf("world %s AdmitEvidence() = (%v, %v), want true/nil", world.WorldID, added, err)
		}
	}
}

func TestAdmitEvidenceFactIdentityConflictsOnEveryImmutableFieldAcrossTasks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, createFixture, CreateResult, Operation, *Evidence)
	}{
		{name: "task", mutate: func(t *testing.T, fixture createFixture, _ CreateResult, _ Operation, value *Evidence) {
			exec, spec := withCreateCall(fixture.exec, fixture.spec, "fact-task-e", "fact-task-t", "fact-task-c")
			spec.EquivalenceKey = "fact-task-other"
			other, err := fixture.svc.Create(context.Background(), exec, spec, Admission{})
			if err != nil {
				t.Fatal(err)
			}
			value.TaskID = other.Task.ID
			value.OperationID = ""
			value.StartRevision = other.Task.Revision
		}},
		{name: "operation", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.OperationID = "operation-other"
		}},
		{name: "binding", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.Binding.Generation++
		}},
		{name: "start revision", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.StartRevision++
		}},
		{name: "occurred time", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) { value.OccurredAt-- }},
		{name: "kind", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.Kind = EvidenceKindSatisfied
		}},
		{name: "wait", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.WaitUntil = testInt64(200)
		}},
		{name: "details", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.Details = json.RawMessage(`{"changed":true}`)
		}},
		{name: "source", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, _ Operation, value *Evidence) {
			value.Source.CallID = "changed-call"
		}},
		{name: "revalidation", mutate: func(_ *testing.T, _ createFixture, _ CreateResult, operation Operation, value *Evidence) {
			current := operation.Binding
			current.Generation++
			value.RevalidatedIn = &current
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			exec := operationExecution(fixture, created.Task, "fact-fields")
			operation := registeredOperation(exec, "operation-fact-fields")
			if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
				t.Fatal(err)
			}
			original := operationEvidence(operation, created.Task.ID, "fact-immutable", EvidenceKindProgress)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, original); err != nil || !added {
				t.Fatalf("initial evidence = (%v, %v)", added, err)
			}
			changed := cloneEvidenceForTest(t, original)
			tt.mutate(t, fixture, created, operation, &changed)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, changed); !errors.Is(err, ErrEvidenceConflict) || added {
				t.Fatalf("changed %s = (%v, %v), want false/ErrEvidenceConflict", tt.name, added, err)
			}
			stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil || len(stored.Evidence) != 1 || !reflect.DeepEqual(stored.Evidence[0], original) {
				t.Fatalf("original evidence changed: (%+v, %v)", stored.Evidence, err)
			}
		})
	}
}

func TestAdmitEvidenceUsesOperationStartRevisionAfterTaskBusinessRevisionAdvances(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	opExec := operationExecution(fixture, created.Task, "old-revision")
	operation := registeredOperation(opExec, "operation-old-revision")
	if _, err := fixture.svc.RegisterOperation(context.Background(), opExec, operation); err != nil {
		t.Fatal(err)
	}
	next := int64(250)
	intentExec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "advance-before-evidence")
	advanced, err := fixture.svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: &next})
	if err != nil || advanced.Revision != 2 {
		t.Fatalf("ApplyIntent() = (%+v, %v)", advanced, err)
	}

	evidence := operationEvidence(operation, created.Task.ID, "fact-old-revision", EvidenceKindProgress)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("old operation revision Evidence = (%v, %v), want true/nil", added, err)
	}
	wrong := operationEvidence(operation, created.Task.ID, "fact-current-revision", EvidenceKindProgress)
	wrong.StartRevision = advanced.Revision
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, wrong); !errors.Is(err, ErrTaskChanged) || added {
		t.Fatalf("current task revision substituted for operation = (%v, %v), want false/ErrTaskChanged", added, err)
	}
}

func TestAdmitEvidenceResolvesTaskOnlyInsideCurrentWorldAndRejectsAmbiguity(t *testing.T) {
	t.Run("ambiguous owner rows", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		otherOwner := created.Task.Owner
		otherOwner.EntityID = "actor-other"
		other, wake := taskStoreFixture(otherOwner, created.Task.ID, "wake-ambiguous-owner")
		other.Spec.Source.EventID = "ambiguous-e"
		other.Spec.Source.TurnID = "ambiguous-t"
		other.Spec.Source.CallID = "ambiguous-c"
		other.Spec.EquivalenceKey = "ambiguous-other"
		if err := fixture.store.insertTaskAndWake(context.Background(), other, wake); err != nil {
			t.Fatal(err)
		}
		evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-ambiguous", EvidenceKindProgress)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, ErrEvidenceConflict) || added {
			t.Fatalf("ambiguous task = (%v, %v), want false/ErrEvidenceConflict", added, err)
		}
	})

	t.Run("other world is invisible", func(t *testing.T) {
		store := openTaskTestStore(t, StoreOptions{Path: t.TempDir() + "/tasks.sqlite"})
		svc := deterministicTaskService(store)
		currentWorld := testWorld()
		clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
		currentHead, err := svc.ActivateWorld(context.Background(), currentWorld, "run-current", clock, CheckpointRef{Status: checkpointStatusAbsent, World: currentWorld})
		if err != nil {
			t.Fatal(err)
		}
		otherOwner := testOwner()
		otherOwner.WorldID = "world-other"
		other, wake := taskStoreFixture(otherOwner, "task-other-world-only", "wake-other-world-only")
		if err := store.insertTaskAndWake(context.Background(), other, wake); err != nil {
			t.Fatal(err)
		}
		evidence := taskEvidence(currentHead.Binding, Record{ID: other.ID, Revision: 1}, "fact-hidden", EvidenceKindProgress)
		if added, err := svc.AdmitEvidence(context.Background(), currentHead.Binding, evidence); !errors.Is(err, ErrTaskNotFound) || added {
			t.Fatalf("other-world task resolution = (%v, %v), want false/ErrTaskNotFound", added, err)
		}
	})
}

func TestAdmitEvidenceRequiresRegisteredOperationAssociation(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	opExec := operationExecution(fixture, created.Task, "association")
	operation := registeredOperation(opExec, "operation-associated")
	if _, err := fixture.svc.RegisterOperation(context.Background(), opExec, operation); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Evidence)
		want   error
	}{
		{name: "missing operation", mutate: func(value *Evidence) { value.OperationID = "operation-missing" }, want: ErrEvidenceConflict},
		{name: "wrong start revision", mutate: func(value *Evidence) { value.StartRevision++ }, want: ErrTaskChanged},
		{name: "wrong task", mutate: func(value *Evidence) { value.TaskID = "task-missing" }, want: ErrTaskNotFound},
		{name: "wrong world", mutate: func(value *Evidence) { value.Binding.World.WorldID = "world-other" }, want: ErrWorldMismatch},
		{name: "wrong run", mutate: func(value *Evidence) { value.Binding.RunID = "run-other" }, want: ErrGenerationStale},
		{name: "wrong generation", mutate: func(value *Evidence) { value.Binding.Generation++ }, want: ErrGenerationStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := operationEvidence(operation, created.Task.ID, "fact-"+tt.name, EvidenceKindProgress)
			tt.mutate(&evidence)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, tt.want) || added {
				t.Fatalf("AdmitEvidence() = (%v, %v), want false/%v", added, err, tt.want)
			}
		})
	}
}

func TestAdmitEvidenceRequiresTaskClockToMatchCurrentHead(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	currentClock := Clock{ID: "replacement.clock.v1", Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, currentClock)
	evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-clock-mismatch", EvidenceKindProgress)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, ErrClockMismatch) || added {
		t.Fatalf("AdmitEvidence() = (%v, %v), want false/ErrClockMismatch", added, err)
	}
}

func TestAdmitEvidenceExactDuplicateStillRequiresTaskClockToMatchCurrentHead(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-clock-exact", EvidenceKindProgress)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("initial AdmitEvidence() = (%v, %v)", added, err)
	}
	currentClock := Clock{ID: "replacement.clock.v1", Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, currentClock)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, ErrClockMismatch) || added {
		t.Fatalf("exact duplicate with task/head clock mismatch = (%v, %v), want false/ErrClockMismatch", added, err)
	}
}

func TestReadRejectsCorruptOperationEvidenceGraph(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "duplicate operation id", mutate: func(record *Record) { record.Operations = append(record.Operations, record.Operations[0]) }},
		{name: "duplicate fact id", mutate: func(record *Record) {
			duplicate := record.Evidence[0]
			duplicate.Source.CallID = "other-call"
			record.Evidence = append(record.Evidence, duplicate)
		}},
		{name: "evidence task mismatch", mutate: func(record *Record) { record.Evidence[0].TaskID = "task-other" }},
		{name: "evidence operation missing", mutate: func(record *Record) { record.Evidence[0].OperationID = "operation-other" }},
		{name: "evidence start revision mismatch", mutate: func(record *Record) { record.Evidence[0].StartRevision++ }},
		{name: "evidence operation binding mismatch", mutate: func(record *Record) { record.Evidence[0].Binding.Generation++ }},
		{name: "fake current generation revalidation", mutate: func(record *Record) {
			current := record.Evidence[0].Binding
			record.Evidence[0].RevalidatedIn = &current
		}},
		{name: "progress wait equals occurrence", mutate: func(record *Record) { record.Evidence[0].WaitUntil = testInt64(record.Evidence[0].OccurredAt) }},
		{name: "progress wait beyond deadline", mutate: func(record *Record) { record.Evidence[0].WaitUntil = testInt64(record.Spec.DeadlineAt + 1) }},
		{name: "duplicate source identity", mutate: func(record *Record) {
			duplicate := record.Evidence[0]
			duplicate.FactID = "fact-other"
			record.Evidence = append(record.Evidence, duplicate)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			exec := operationExecution(fixture, created.Task, "corrupt")
			operation := registeredOperation(exec, "operation-corrupt")
			if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
				t.Fatal(err)
			}
			evidence := operationEvidence(operation, created.Task.ID, "fact-corrupt", EvidenceKindProgress)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
			}
			record, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&record)
			if _, err := fixture.store.db.Exec(`UPDATE tasks SET record_json = ?
				WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
				mustJSONBytes(t, record), record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID); err != nil {
				t.Fatal(err)
			}
			if got, err := fixture.svc.Read(context.Background(), record.Owner, record.ID); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("Read(corrupt graph) = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
			}
		})
	}
}

func TestReadAndListFailClosedOnCrossTaskSourceIdentityCorruption(t *testing.T) {
	t.Run("Read", func(t *testing.T) {
		fixture, first := crossTaskSourceCorruptionFixture(t)
		got, err := fixture.svc.Read(context.Background(), first.Owner, first.ID)
		if !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
			t.Fatalf("Read() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
		}
	})

	t.Run("List", func(t *testing.T) {
		fixture, first := crossTaskSourceCorruptionFixture(t)
		got, err := fixture.svc.List(context.Background(), first.Owner, 10)
		if !errors.Is(err, ErrInvalidTaskSpec) || len(got) != 0 {
			t.Fatalf("List() = (%+v, %v), want empty/ErrInvalidTaskSpec", got, err)
		}
	})
}

func TestReadWrongOwnerOrTaskRemainsNotFoundWhenWorldIdentityGraphIsCorrupt(t *testing.T) {
	for _, variant := range []string{"owner", "task"} {
		t.Run(variant, func(t *testing.T) {
			fixture, first := crossTaskSourceCorruptionFixture(t)
			owner, taskID := first.Owner, first.ID
			if variant == "owner" {
				owner.EntityID = "actor-not-present"
			} else {
				taskID = "task-not-present"
			}
			got, err := fixture.svc.Read(context.Background(), owner, taskID)
			if !errors.Is(err, ErrTaskNotFound) || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("Read(wrong %s) = (%+v, %v), want zero/ErrTaskNotFound", variant, got, err)
			}
		})
	}
}

func TestReadAndListFailClosedOnCrossTaskOperationAndFactIdentityCorruption(t *testing.T) {
	for _, identity := range []string{"operation", "fact"} {
		for _, api := range []string{"Read", "List"} {
			t.Run(identity+"/"+api, func(t *testing.T) {
				fixture, first, _ := newIntentFixture(t, StoreOptions{})
				otherOwner := first.Task.Owner
				otherOwner.EntityID = "actor-identity-corrupt"
				otherExec, otherSpec := createInputs(fixture.head, fixture.clock, otherOwner,
					"identity-create-event", "identity-create-turn", "identity-create-call", "identity-other")
				other, err := fixture.svc.Create(context.Background(), otherExec, otherSpec, Admission{})
				if err != nil {
					t.Fatal(err)
				}

				otherRecord := other.Task
				switch identity {
				case "operation":
					firstExec := operationExecution(fixture, first.Task, "identity-corrupt")
					operation := registeredOperation(firstExec, "operation-cross-task-corrupt")
					if _, err := fixture.svc.RegisterOperation(context.Background(), firstExec, operation); err != nil {
						t.Fatal(err)
					}
					otherRecord.Operations = []Operation{operation}
				case "fact":
					firstEvidence := taskEvidence(fixture.head.Binding, first.Task, "fact-cross-task-corrupt", EvidenceKindProgress)
					if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, firstEvidence); err != nil || !added {
						t.Fatalf("first AdmitEvidence() = (%v, %v)", added, err)
					}
					otherEvidence := taskEvidence(fixture.head.Binding, other.Task, firstEvidence.FactID, EvidenceKindProgress)
					otherEvidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "other-fact-event", TurnID: "other-fact-turn", CallID: "other-fact-call"}
					otherRecord.Evidence = []Evidence{otherEvidence}
					otherRecord.NeedsReconcile = true
				}
				setRecordForIntentTest(t, fixture.store, otherRecord)

				switch api {
				case "Read":
					got, err := fixture.svc.Read(context.Background(), first.Task.Owner, first.Task.ID)
					if !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
						t.Fatalf("Read() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
					}
				case "List":
					got, err := fixture.svc.List(context.Background(), first.Task.Owner, 10)
					if !errors.Is(err, ErrInvalidTaskSpec) || len(got) != 0 {
						t.Fatalf("List() = (%+v, %v), want empty/ErrInvalidTaskSpec", got, err)
					}
				}
			})
		}
	}
}

func TestReadAndListFailClosedWhenUnappliedEvidenceLacksReconcileMarker(t *testing.T) {
	setup := func(t *testing.T) (createFixture, Record) {
		t.Helper()
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-missing-reconcile", EvidenceKindProgress)
		corrupt := created.Task
		corrupt.Evidence = []Evidence{evidence}
		corrupt.NeedsReconcile = false
		setRecordForIntentTest(t, fixture.store, corrupt)
		return fixture, corrupt
	}

	t.Run("Read", func(t *testing.T) {
		fixture, corrupt := setup(t)
		got, err := fixture.svc.Read(context.Background(), corrupt.Owner, corrupt.ID)
		if !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Record{}) {
			t.Fatalf("Read() = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
		}
	})

	t.Run("List", func(t *testing.T) {
		fixture, corrupt := setup(t)
		got, err := fixture.svc.List(context.Background(), corrupt.Owner, 10)
		if !errors.Is(err, ErrInvalidTaskSpec) || len(got) != 0 {
			t.Fatalf("List() = (%+v, %v), want empty/ErrInvalidTaskSpec", got, err)
		}
	})
}

func TestAdmitEvidenceAllowsOnlyExplicitSameRunOperationRevalidation(t *testing.T) {
	newFixture := func(t *testing.T) (createFixture, CreateResult, Operation, Binding) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "revalidate")
		operation := registeredOperation(exec, "operation-revalidate")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		current := fixture.head.Binding
		current.Generation++
		setWorldBindingForEvidenceTest(t, fixture.store, fixture.head, current)
		return fixture, created, operation, current
	}

	t.Run("valid", func(t *testing.T) {
		fixture, created, operation, current := newFixture(t)
		evidence := operationEvidence(operation, created.Task.ID, "fact-revalidated", EvidenceKindSatisfied)
		evidence.RevalidatedIn = &current
		if added, err := fixture.svc.AdmitEvidence(context.Background(), current, evidence); err != nil || !added {
			t.Fatalf("valid revalidation = (%v, %v), want true/nil", added, err)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || stored.Evidence[0].Binding != operation.Binding || stored.Evidence[0].RevalidatedIn == nil || *stored.Evidence[0].RevalidatedIn != current {
			t.Fatalf("stored revalidated evidence = (%+v, %v)", stored.Evidence, err)
		}
	})

	tests := []struct {
		name   string
		mutate func(*Evidence, Binding)
		want   error
	}{
		{name: "ordinary old callback", mutate: func(value *Evidence, _ Binding) {}, want: ErrGenerationStale},
		{name: "different run", mutate: func(value *Evidence, current Binding) {
			value.Binding.RunID = "old-run"
			value.RevalidatedIn = &current
		}, want: ErrGenerationStale},
		{name: "wrong revalidation", mutate: func(value *Evidence, current Binding) { current.Generation++; value.RevalidatedIn = &current }, want: ErrGenerationStale},
		{name: "current generation claimed old", mutate: func(value *Evidence, current Binding) { value.Binding = current; value.RevalidatedIn = &current }, want: ErrEvidenceConflict},
		{name: "future generation", mutate: func(value *Evidence, current Binding) {
			value.Binding.Generation = current.Generation + 1
			value.RevalidatedIn = &current
		}, want: ErrEvidenceConflict},
		{name: "without operation", mutate: func(value *Evidence, current Binding) {
			value.OperationID = ""
			value.Binding = current
			value.StartRevision = 1
			value.RevalidatedIn = &current
		}, want: ErrEvidenceConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, operation, current := newFixture(t)
			evidence := operationEvidence(operation, created.Task.ID, "fact-invalid-revalidation", EvidenceKindProgress)
			tt.mutate(&evidence, current)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), current, evidence); !errors.Is(err, tt.want) || added {
				t.Fatalf("AdmitEvidence() = (%v, %v), want false/%v", added, err, tt.want)
			}
		})
	}
}

func TestAdmitEvidenceRevalidatesUncertainOperationOnlyByActiveQuery(t *testing.T) {
	newFixture := func(t *testing.T) (createFixture, CreateResult, Operation) {
		t.Helper()
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "uncertain-active-query")
		operation := registeredOperation(exec, "operation-uncertain-active-query")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil {
			t.Fatal(err)
		}
		current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.Operations[0].Status = OperationStatusUncertain
		setRecordForIntentTest(t, fixture.store, current)
		return fixture, created, operation
	}

	t.Run("same binding active query", func(t *testing.T) {
		fixture, created, operation := newFixture(t)
		evidence := operationEvidence(operation, created.Task.ID, "fact-uncertain-active-query", EvidenceKindProgress)
		evidence.RevalidatedIn = &fixture.head.Binding
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v), want true/nil", added, err)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil || len(stored.Evidence) != 1 || stored.Evidence[0].RevalidatedIn == nil || *stored.Evidence[0].RevalidatedIn != fixture.head.Binding {
			t.Fatalf("stored Evidence = (%+v, %v)", stored.Evidence, err)
		}
	})

	t.Run("late message without active query", func(t *testing.T) {
		fixture, created, operation := newFixture(t)
		evidence := operationEvidence(operation, created.Task.ID, "fact-uncertain-late", EvidenceKindProgress)
		evidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "fresh-event", TurnID: "fresh-turn", CallID: "fresh-call"}
		before := snapshotD2Rows(t, fixture.store, created.Task)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, ErrEvidenceConflict) || added {
			t.Fatalf("AdmitEvidence() = (%v, %v), want false/evidence_conflict", added, err)
		}
		assertD2Rows(t, fixture.store, created.Task, before)
	})
}

func TestAdmitEvidenceEnforcesTimeWaitAndInputApplicationBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(createFixture, Record, *Evidence)
		want   error
		ok     bool
	}{
		{name: "progress without wait", mutate: func(_ createFixture, _ Record, _ *Evidence) {}, ok: true},
		{name: "wait at deadline", mutate: func(_ createFixture, record Record, value *Evidence) {
			value.WaitUntil = testInt64(record.Spec.DeadlineAt)
		}, ok: true},
		{name: "late valid wait", mutate: func(_ createFixture, _ Record, value *Evidence) {
			value.OccurredAt = 80
			value.WaitUntil = testInt64(90)
		}, ok: true},
		{name: "wait equals occurred", mutate: func(_ createFixture, _ Record, value *Evidence) {
			value.OccurredAt = 90
			value.WaitUntil = testInt64(90)
		}, want: ErrInvalidTaskSpec},
		{name: "wait after deadline", mutate: func(_ createFixture, record Record, value *Evidence) {
			value.WaitUntil = testInt64(record.Spec.DeadlineAt + 1)
		}, want: ErrInvalidTaskSpec},
		{name: "future occurred", mutate: func(f createFixture, _ Record, value *Evidence) { value.OccurredAt = f.clock.Tick + 1 }, want: ErrTaskChanged},
		{name: "applied forgery", mutate: func(_ createFixture, _ Record, value *Evidence) { value.Applied = true }, want: ErrEvidenceConflict},
		{name: "unknown kind", mutate: func(_ createFixture, _ Record, value *Evidence) { value.Kind = "met" }, want: ErrInvalidTaskSpec},
		{name: "terminal wait", mutate: func(_ createFixture, _ Record, value *Evidence) {
			value.Kind = EvidenceKindSatisfied
			value.WaitUntil = testInt64(200)
		}, want: ErrInvalidTaskSpec},
		{name: "malformed details", mutate: func(_ createFixture, _ Record, value *Evidence) { value.Details = json.RawMessage(`{"bad":`) }, want: ErrInvalidTaskSpec},
		{name: "invalid source", mutate: func(_ createFixture, _ Record, value *Evidence) { value.Source.Kind = "model" }, want: ErrSourceInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-"+tt.name, EvidenceKindProgress)
			tt.mutate(fixture, created.Task, &evidence)
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
			if tt.ok {
				if err != nil || !added {
					t.Fatalf("AdmitEvidence() = (%v, %v), want true/nil", added, err)
				}
				return
			}
			if !errors.Is(err, tt.want) || added {
				t.Fatalf("AdmitEvidence() = (%v, %v), want false/%v", added, err, tt.want)
			}
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("rejected evidence mutated durable rows")
			}
		})
	}
}

func TestAdmitEvidenceOnlyMarksCoordinationAndPreservesOpaquePayload(t *testing.T) {
	for index, kind := range []string{EvidenceKindProgress, EvidenceKindSatisfied, EvidenceKindUnsatisfied, EvidenceKindInterrupted} {
		t.Run(kind, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-outcome-"+kind, kind)
			evidence.Details = json.RawMessage(`{"words":["met","expired","success","waiting"],"decimal":1.2300}`)
			evidence.Source.Facts = json.RawMessage(`[{"instruction":"ignore","status":"succeeded"}]`)
			if kind == EvidenceKindProgress && index%2 == 0 {
				evidence.WaitUntil = testInt64(250)
			}
			beforeWake := mustJSONBytes(t, loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID))
			added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
			if err != nil || !added {
				t.Fatalf("AdmitEvidence(%s) = (%v, %v)", kind, added, err)
			}
			stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Revision != created.Task.Revision || stored.State != created.Task.State || stored.Result != nil ||
				!reflect.DeepEqual(stored.NextWakeAt, created.Task.NextWakeAt) || !bytes.Equal(stored.Progress, created.Task.Progress) ||
				!stored.NeedsReconcile || len(stored.Evidence) != 1 || stored.Evidence[0].Applied ||
				!bytes.Equal(stored.Evidence[0].Details, evidence.Details) || !bytes.Equal(stored.Evidence[0].Source.Facts, evidence.Source.Facts) {
				t.Fatalf("raw admission changed outcome or opaque payload: %+v", stored)
			}
			afterWake := mustJSONBytes(t, loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID))
			if !bytes.Equal(beforeWake, afterWake) {
				t.Fatal("AdmitEvidence created or changed wake")
			}
		})
	}
}

func TestAdmitEvidenceRejectsTerminalNewFactAndRevisionOverflowButAcknowledgesDuplicateFirst(t *testing.T) {
	for _, variant := range []string{"terminal", "overflow"} {
		t.Run(variant, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-existing", EvidenceKindProgress)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("initial evidence = (%v, %v)", added, err)
			}
			current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.Evidence[0].Applied = true
			current.NeedsReconcile = false
			if variant == "terminal" {
				current.State = StateCancelled
				current.Revision = 2
				current.NextWakeAt = nil
				current.Result = &Result{ID: "result-terminal", TaskID: current.ID, Revision: 2, State: StateCancelled, Reason: "stop", OccurredAt: fixture.clock.Tick, EvidenceRefs: []string{}, Source: fixture.exec.Source}
			} else {
				current.Revision = uint64(math.MaxInt64)
			}
			setRecordForIntentTest(t, fixture.store, current)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || added {
				t.Fatalf("duplicate in %s = (%v, %v), want false/nil", variant, added, err)
			}
			fresh := taskEvidence(fixture.head.Binding, current, "fact-new", EvidenceKindProgress)
			want := ErrInvalidTaskSpec
			if variant == "terminal" {
				want = ErrTaskTerminal
			}
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, fresh); !errors.Is(err, want) || added {
				t.Fatalf("new evidence in %s = (%v, %v), want false/%v", variant, added, err, want)
			}
		})
	}
}

func TestNoRevisionExactRetriesPreserveCreateAndIntentResponsesAcrossSmallerReopen(t *testing.T) {
	fixture, created, wake := newIntentFixture(t, StoreOptions{})
	next := int64(250)
	intentExec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "metadata")
	intentResult, err := fixture.svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: &next, ProgressNote: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	opExec := operationExecution(fixture, intentResult, "metadata")
	operation := registeredOperation(opExec, "operation-metadata")
	if _, err := fixture.svc.RegisterOperation(context.Background(), opExec, operation); err != nil {
		t.Fatal(err)
	}
	evidence := operationEvidence(operation, created.Task.ID, "fact-metadata", EvidenceKindProgress)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
	}

	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: 1})
	svc := deterministicTaskService(reopened)
	if got, err := svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("Create exact retry after reopen = (%+v, %v), want %+v", got, err, created)
	}
	if got, err := svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: &next, ProgressNote: "continue"}); err != nil || !reflect.DeepEqual(got, intentResult) {
		t.Fatalf("ApplyIntent exact retry after reopen = (%+v, %v), want %+v", got, err, intentResult)
	}
	if got, err := svc.RegisterOperation(context.Background(), opExec, operation); err != nil || !reflect.DeepEqual(got, operation) {
		t.Fatalf("RegisterOperation exact retry after reopen = (%+v, %v), want %+v", got, err, operation)
	}
	if added, err := svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || added {
		t.Fatalf("Evidence duplicate after reopen = (%v, %v), want false/nil", added, err)
	}
}

func TestNoRevisionPayloadCapacityRejectsBeforeMutationAndKeepsShortCancelHeadroom(t *testing.T) {
	t.Run("operation", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		exec := operationExecution(fixture, created.Task, "capacity-small")
		small := registeredOperation(exec, "operation-small")
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, small); err != nil {
			t.Fatal(err)
		}
		beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		oversized := registeredOperation(exec, "operation-oversized")
		oversized.CommandFingerprint = strings.Repeat("x", 200<<10)
		if got, err := fixture.svc.RegisterOperation(context.Background(), exec, oversized); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, Operation{}) {
			t.Fatalf("oversized operation = (%+v, %v), want zero/ErrInvalidTaskSpec", got, err)
		}
		afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("oversized operation changed durable rows")
		}
		cancelExec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "capacity-operation-cancel")
		if got, err := fixture.svc.ApplyIntent(context.Background(), cancelExec, Intent{Kind: "cancel", Reason: "x"}); err != nil || got.State != StateCancelled {
			t.Fatalf("short cancel after accepted operation = (%+v, %v)", got, err)
		}
	})

	t.Run("evidence", func(t *testing.T) {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		oversized := taskEvidence(fixture.head.Binding, created.Task, "fact-oversized", EvidenceKindProgress)
		oversized.Details = json.RawMessage(`"` + strings.Repeat("x", 200<<10) + `"`)
		beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, oversized); !errors.Is(err, ErrInvalidTaskSpec) || added {
			t.Fatalf("oversized evidence = (%v, %v), want false/ErrInvalidTaskSpec", added, err)
		}
		afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("oversized evidence changed durable rows")
		}

		small := taskEvidence(fixture.head.Binding, created.Task, "fact-small", EvidenceKindProgress)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, small); err != nil || !added {
			t.Fatalf("small evidence = (%v, %v)", added, err)
		}
		current, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.Evidence[0].Applied = true
		current.NeedsReconcile = false
		setRecordForIntentTest(t, fixture.store, current)
		cancelExec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "capacity-evidence-cancel")
		if got, err := fixture.svc.ApplyIntent(context.Background(), cancelExec, Intent{Kind: "cancel", Reason: "x"}); err != nil || got.State != StateCancelled {
			t.Fatalf("short cancel after accepted/applied evidence = (%+v, %v)", got, err)
		}
	})
}

func TestNoRevisionMergeFaultAndContextCancellationRollBackAllMetadata(t *testing.T) {
	for _, variant := range []string{"fault", "context"} {
		t.Run(variant, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-rollback", EvidenceKindProgress)
			beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			ctx := context.Background()
			var cancel context.CancelFunc
			if variant == "context" {
				ctx, cancel = context.WithCancel(ctx)
			}
			fixture.store.testAfterNoRevisionStage = func(_ context.Context, stage string) error {
				if stage != noRevisionStageEvidenceMerged {
					t.Fatalf("stage = %q", stage)
				}
				if cancel != nil {
					cancel()
					return nil
				}
				return errors.New("secret-evidence-details")
			}
			added, err := fixture.svc.AdmitEvidence(ctx, fixture.head.Binding, evidence)
			if !errors.Is(err, ErrTaskConflict) || added {
				t.Fatalf("faulted AdmitEvidence = (%v, %v), want false/ErrTaskConflict", added, err)
			}
			assertTaskErrorSanitized(t, err, "secret-evidence-details", fixture.store.path, string(evidence.Details))
			afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("faulted evidence merge changed durable rows")
			}
		})
	}
}

func TestOperationEvidenceIntentRaceNeverLosesACommittedWriter(t *testing.T) {
	for iteration := 0; iteration < 4; iteration++ {
		fixture, created, wake := newIntentFixture(t, StoreOptions{})
		baseExec := operationExecution(fixture, created.Task, "race-base")
		baseOperation := registeredOperation(baseExec, "operation-race-base")
		if _, err := fixture.svc.RegisterOperation(context.Background(), baseExec, baseOperation); err != nil {
			t.Fatal(err)
		}
		secondExec := operationExecution(fixture, created.Task, "race-second")
		secondOperation := registeredOperation(secondExec, "operation-race-second")
		evidence := operationEvidence(baseOperation, created.Task.ID, "fact-race-three-way", EvidenceKindProgress)
		next := int64(250)
		intentExec := intentExecution(fixture, created.Task, wake.ID, created.Task.Revision, "three-way")

		var (
			registered  Operation
			registerErr error
			added       bool
			evidenceErr error
			intentGot   Record
			intentErr   error
			wg          sync.WaitGroup
		)
		wg.Add(3)
		go func() {
			defer wg.Done()
			registered, registerErr = fixture.svc.RegisterOperation(context.Background(), secondExec, secondOperation)
		}()
		go func() {
			defer wg.Done()
			added, evidenceErr = fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
		}()
		go func() {
			defer wg.Done()
			intentGot, intentErr = fixture.svc.ApplyIntent(context.Background(), intentExec, Intent{Kind: "wait", NextWakeAt: &next})
		}()
		wg.Wait()

		if evidenceErr != nil || !added {
			t.Fatalf("iteration %d evidence = (%v, %v)", iteration, added, evidenceErr)
		}
		if registerErr != nil && !errors.Is(registerErr, ErrTaskChanged) {
			t.Fatalf("iteration %d register error = %v", iteration, registerErr)
		}
		if intentErr != nil && !errors.Is(intentErr, ErrTaskChanged) {
			t.Fatalf("iteration %d intent error = %v", iteration, intentErr)
		}
		stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(stored.Evidence) != 1 || stored.Evidence[0].FactID != evidence.FactID || len(stored.Operations) < 1 {
			t.Fatalf("iteration %d lost base operation/evidence: %+v", iteration, stored)
		}
		if registerErr == nil {
			if registered.ID != secondOperation.ID || !recordHasOperation(stored, secondOperation.ID) {
				t.Fatalf("iteration %d lost committed second operation: %+v", iteration, stored.Operations)
			}
		}
		if intentErr == nil {
			if intentGot.Revision != 2 || stored.Revision != 2 || stored.NextWakeAt == nil || *stored.NextWakeAt != next {
				t.Fatalf("iteration %d lost committed intent: got=%+v stored=%+v", iteration, intentGot, stored)
			}
		} else if stored.Revision != 1 {
			t.Fatalf("iteration %d rejected intent but revision=%d", iteration, stored.Revision)
		}
	}
}

func TestRegisterOperationAndAdmitEvidenceRejectUnsafeServiceContextAndWorldState(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	exec := operationExecution(fixture, created.Task, "unsafe")
	operation := registeredOperation(exec, "operation-unsafe")
	evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-unsafe", EvidenceKindProgress)

	if _, err := (*Service)(nil).RegisterOperation(context.Background(), exec, operation); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil Service RegisterOperation error = %v", err)
	}
	if _, err := NewService(nil).RegisterOperation(context.Background(), exec, operation); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil store RegisterOperation error = %v", err)
	}
	if _, err := fixture.svc.RegisterOperation(nil, exec, operation); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("nil context RegisterOperation error = %v", err)
	}
	if _, err := (*Service)(nil).AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil Service AdmitEvidence error = %v", err)
	}
	if _, err := NewService(nil).AdmitEvidence(context.Background(), fixture.head.Binding, evidence); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("nil store AdmitEvidence error = %v", err)
	}
	if _, err := fixture.svc.AdmitEvidence(nil, fixture.head.Binding, evidence); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("nil context AdmitEvidence error = %v", err)
	}

	for _, state := range []string{"paused", "barrier"} {
		t.Run(state, func(t *testing.T) {
			local, record, _ := newIntentFixture(t, StoreOptions{})
			if state == "paused" {
				setWorldHeadStateForIntentTest(t, local.store, local.head, "paused", "restore", "", "")
			} else {
				setWorldHeadStateForIntentTest(t, local.store, local.head, worldHeadStatusReady, "", "save-a", "preparing")
			}
			want := ErrWorldNotReady
			if state == "barrier" {
				want = ErrSaveInProgress
			}
			opExec := operationExecution(local, record.Task, state)
			if _, err := local.svc.RegisterOperation(context.Background(), opExec, registeredOperation(opExec, "operation-"+state)); !errors.Is(err, want) {
				t.Fatalf("RegisterOperation(%s) error = %v, want %v", state, err, want)
			}
			if _, err := local.svc.AdmitEvidence(context.Background(), local.head.Binding, taskEvidence(local.head.Binding, record.Task, "fact-"+state, EvidenceKindProgress)); !errors.Is(err, want) {
				t.Fatalf("AdmitEvidence(%s) error = %v, want %v", state, err, want)
			}
		})
	}
}

func recordHasOperation(record Record, operationID string) bool {
	for _, operation := range record.Operations {
		if operation.ID == operationID {
			return true
		}
	}
	return false
}

func taskEvidence(binding Binding, record Record, factID, kind string) Evidence {
	return Evidence{
		FactID: factID, TaskID: record.ID, Binding: binding, StartRevision: record.Revision,
		OccurredAt: 90, Kind: kind, Details: json.RawMessage(`{"step":"observed"}`),
		Source: SourceRef{Kind: SourceKindEnvironment, EventID: "event-" + factID, TurnID: "turn-" + factID, CallID: "call-" + factID},
	}
}

func operationEvidence(operation Operation, taskID, factID, kind string) Evidence {
	return Evidence{
		FactID: factID, TaskID: taskID, OperationID: operation.ID, Binding: operation.Binding,
		StartRevision: operation.StartRevision, OccurredAt: 90, Kind: kind,
		Details: json.RawMessage(`{"step":"observed"}`),
		Source:  SourceRef{Kind: SourceKindEnvironment, EventID: "event-" + factID, TurnID: "turn-" + factID, CallID: "call-" + factID},
	}
}

func cloneEvidenceForTest(t *testing.T, evidence Evidence) Evidence {
	t.Helper()
	data := mustJSONBytes(t, evidence)
	var cloned Evidence
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func crossTaskSourceCorruptionFixture(t *testing.T) (createFixture, Record) {
	t.Helper()
	fixture, first, _ := newIntentFixture(t, StoreOptions{})
	firstEvidence := taskEvidence(fixture.head.Binding, first.Task, "fact-corrupt-source-first", EvidenceKindProgress)
	firstEvidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "corrupt-event", TurnID: "corrupt-turn", CallID: "corrupt-call"}
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, firstEvidence); err != nil || !added {
		t.Fatalf("first AdmitEvidence() = (%v, %v)", added, err)
	}

	otherOwner := first.Task.Owner
	otherOwner.EntityID = "actor-source-corrupt"
	otherExec, otherSpec := createInputs(fixture.head, fixture.clock, otherOwner,
		"corrupt-create-event", "corrupt-create-turn", "corrupt-create-call", "corrupt-source-other")
	other, err := fixture.svc.Create(context.Background(), otherExec, otherSpec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	otherEvidence := taskEvidence(fixture.head.Binding, other.Task, "fact-corrupt-source-other", EvidenceKindProgress)
	otherEvidence.Source = firstEvidence.Source
	otherRecord := other.Task
	otherRecord.Evidence = []Evidence{otherEvidence}
	otherRecord.NeedsReconcile = true
	setRecordForIntentTest(t, fixture.store, otherRecord)
	return fixture, first.Task
}

func setWorldBindingForEvidenceTest(t *testing.T, store *SQLiteStore, head Head, binding Binding) {
	t.Helper()
	row, err := store.loadWorldHead(context.Background(), head.Binding.World)
	if err != nil {
		t.Fatal(err)
	}
	row.Head.Binding = binding
	data := mustJSONBytes(t, row)
	if _, err := store.db.Exec(`UPDATE task_world_heads SET run_id = ?, generation = ?, head_json = ?
		WHERE game_id = ? AND world_id = ?`, binding.RunID, int64(binding.Generation), data,
		binding.World.GameID, binding.World.WorldID); err != nil {
		t.Fatal(err)
	}
}
