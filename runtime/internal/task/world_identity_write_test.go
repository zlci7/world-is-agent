package task

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"gameagent/runtime/internal/session"
)

func TestRegisterOperationRejectsUnrelatedAmbiguousWorldIdentityGraph(t *testing.T) {
	for _, identity := range []string{"operation", "fact"} {
		for _, request := range []string{"new", "exact"} {
			t.Run(identity+"/"+request, func(t *testing.T) {
				fixture := newWorldIdentityWriteFixture(t)
				fixture.injectDuplicateIdentity(t, identity)

				operation := fixture.targetOperation
				if request == "new" {
					operation = registeredOperation(fixture.targetExec, "operation-new-with-"+identity+"-corruption")
				}
				beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, fixture.target.Task.Owner, fixture.target.Task.ID)
				got, err := fixture.svc.RegisterOperation(context.Background(), fixture.targetExec, operation)
				if !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Operation{}) {
					t.Fatalf("RegisterOperation(%s with duplicate %s ID) = (%+v, %v), want zero/ErrIdempotencyConflict", request, identity, got, err)
				}
				afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, fixture.target.Task.Owner, fixture.target.Task.ID)
				if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
					t.Fatal("rejected operation changed durable rows")
				}
			})
		}
	}
}

func TestAdmitEvidenceRejectsUnrelatedAmbiguousWorldIdentityGraph(t *testing.T) {
	for _, identity := range []string{"operation", "fact"} {
		for _, request := range []string{"task", "operation", "exact"} {
			t.Run(identity+"/"+request, func(t *testing.T) {
				fixture := newWorldIdentityWriteFixture(t)
				var evidence Evidence
				switch request {
				case "task":
					evidence = taskEvidence(fixture.head.Binding, fixture.target.Task, "fact-task-with-"+identity+"-corruption", EvidenceKindProgress)
				case "operation":
					evidence = operationEvidence(fixture.targetOperation, fixture.target.Task.ID, "fact-operation-with-"+identity+"-corruption", EvidenceKindProgress)
				case "exact":
					evidence = operationEvidence(fixture.targetOperation, fixture.target.Task.ID, "fact-exact-with-"+identity+"-corruption", EvidenceKindProgress)
					if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
						t.Fatalf("initial AdmitEvidence() = (%v, %v), want true/nil", added, err)
					}
				}
				fixture.injectDuplicateIdentity(t, identity)

				beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, fixture.store, fixture.target.Task.Owner, fixture.target.Task.ID)
				added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
				if !errors.Is(err, ErrEvidenceConflict) || added {
					t.Fatalf("AdmitEvidence(%s with duplicate %s ID) = (%v, %v), want false/ErrEvidenceConflict", request, identity, added, err)
				}
				afterTask, afterWake, afterHistory := snapshotIntentRows(t, fixture.store, fixture.target.Task.Owner, fixture.target.Task.ID)
				if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWake, afterWake) || !bytes.Equal(beforeHistory, afterHistory) {
					t.Fatal("rejected evidence changed durable rows")
				}
			})
		}
	}
}

func TestAmbiguousWorldIdentityGraphDoesNotBlockOtherWorldWrites(t *testing.T) {
	for _, identity := range []string{"operation", "fact"} {
		t.Run(identity, func(t *testing.T) {
			fixture := newWorldIdentityWriteFixture(t)
			fixture.injectDuplicateIdentity(t, identity)

			otherWorld := WorldKey{GameID: fixture.world.GameID, WorldID: "world-clean-control-" + identity}
			otherHead, err := fixture.svc.ActivateWorld(context.Background(), otherWorld, "run-clean-control", fixture.clock,
				CheckpointRef{Status: checkpointStatusAbsent, World: otherWorld})
			if err != nil {
				t.Fatal(err)
			}
			otherOwner := fixture.target.Task.Owner
			otherOwner.WorldID = otherWorld.WorldID
			createExec, createSpec := createInputs(otherHead, fixture.clock, otherOwner,
				"clean-create-event-"+identity, "clean-create-turn-"+identity, "clean-create-call-"+identity, "clean-equivalence-"+identity)
			created, err := fixture.svc.Create(context.Background(), createExec, createSpec, Admission{})
			if err != nil {
				t.Fatal(err)
			}
			exec := ExecutionContext{
				Owner: otherOwner, Binding: otherHead.Binding, Clock: fixture.clock,
				Source: SourceRef{Kind: SourceKindInternal, EventID: "clean-operation-event-" + identity,
					TurnID: "clean-operation-turn-" + identity, CallID: "clean-operation-call-" + identity},
				TaskID: created.Task.ID, ExpectedRevision: created.Task.Revision,
			}
			operationID := "operation-clean-control-" + identity
			if identity == "operation" {
				operationID = "operation-world-duplicate"
			}
			operation := registeredOperation(exec, operationID)
			if got, err := fixture.svc.RegisterOperation(context.Background(), exec, operation); err != nil || !reflect.DeepEqual(got, operation) {
				t.Fatalf("other-world RegisterOperation() = (%+v, %v), want %+v", got, err, operation)
			}
			factID := "fact-clean-control-" + identity
			if identity == "fact" {
				factID = "fact-world-duplicate"
			}
			evidence := operationEvidence(operation, created.Task.ID, factID, EvidenceKindProgress)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), otherHead.Binding, evidence); err != nil || !added {
				t.Fatalf("other-world AdmitEvidence() = (%v, %v), want true/nil", added, err)
			}
		})
	}
}

type worldIdentityWriteFixture struct {
	createFixture
	first           CreateResult
	second          CreateResult
	target          CreateResult
	targetExec      ExecutionContext
	targetOperation Operation
}

func newWorldIdentityWriteFixture(t *testing.T) worldIdentityWriteFixture {
	t.Helper()
	fixture, first, _ := newIntentFixture(t, StoreOptions{})
	second := createWorldIdentityTask(t, fixture, first.Task.Owner, "identity-second")
	target := createWorldIdentityTask(t, fixture, first.Task.Owner, "identity-target")
	targetExec := operationExecution(fixture, target.Task, "identity-target-existing")
	targetOperation := registeredOperation(targetExec, "operation-identity-target-existing")
	if _, err := fixture.svc.RegisterOperation(context.Background(), targetExec, targetOperation); err != nil {
		t.Fatal(err)
	}
	return worldIdentityWriteFixture{
		createFixture:   fixture,
		first:           first,
		second:          second,
		target:          target,
		targetExec:      targetExec,
		targetOperation: targetOperation,
	}
}

func createWorldIdentityTask(t *testing.T, fixture createFixture, baseOwner session.AgentSessionKey, suffix string) CreateResult {
	t.Helper()
	owner := baseOwner
	owner.EntityID = "actor-" + suffix
	exec, spec := createInputs(fixture.head, fixture.clock, owner,
		"create-event-"+suffix, "create-turn-"+suffix, "create-call-"+suffix, "equivalence-"+suffix)
	created, err := fixture.svc.Create(context.Background(), exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func (fixture worldIdentityWriteFixture) injectDuplicateIdentity(t *testing.T, identity string) {
	t.Helper()
	first := fixture.first.Task
	second := fixture.second.Task
	switch identity {
	case "operation":
		exec := operationExecution(fixture.createFixture, first, "identity-corrupt-existing")
		duplicate := registeredOperation(exec, "operation-world-duplicate")
		first.Operations = []Operation{duplicate}
		second.Operations = []Operation{duplicate}
	case "fact":
		firstEvidence := taskEvidence(fixture.head.Binding, first, "fact-world-duplicate", EvidenceKindProgress)
		firstEvidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "duplicate-fact-event-first", TurnID: "duplicate-fact-turn-first", CallID: "duplicate-fact-call-first"}
		secondEvidence := taskEvidence(fixture.head.Binding, second, firstEvidence.FactID, EvidenceKindProgress)
		secondEvidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "duplicate-fact-event-second", TurnID: "duplicate-fact-turn-second", CallID: "duplicate-fact-call-second"}
		first.Evidence = []Evidence{firstEvidence}
		first.NeedsReconcile = true
		second.Evidence = []Evidence{secondEvidence}
		second.NeedsReconcile = true
	default:
		t.Fatalf("unknown identity kind %q", identity)
	}
	setRecordForIntentTest(t, fixture.store, first)
	setRecordForIntentTest(t, fixture.store, second)
}
