package task

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gameagent/runtime/internal/session"
)

// completeTask commits one authoritative result through the real create, evidence
// and reconcile path, so the query reads exactly what the runtime writes.
func completeTask(t *testing.T, f createFixture, suffix string, occurredAt int64) Record {
	t.Helper()
	ctx := context.Background()
	exec, spec := withCreateCall(f.exec, f.spec, "event-"+suffix, "turn-"+suffix, "call-"+suffix)
	spec.EquivalenceKey = suffix
	created, err := f.svc.Create(ctx, exec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	evidence := taskEvidence(f.head.Binding, created.Task, "fact-"+suffix, EvidenceKindSatisfied)
	evidence.OccurredAt = occurredAt
	if _, err := f.svc.AdmitEvidence(ctx, f.head.Binding, evidence); err != nil {
		t.Fatal(err)
	}
	reconciled, err := f.svc.Reconcile(ctx, reconcileExecution(f, created.Task, created.Task.Revision))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Task.Result == nil {
		t.Fatalf("task %s committed without a result", suffix)
	}
	return reconciled.Task
}

func TestListRecentResults(t *testing.T) {
	ctx := context.Background()
	f := newCreateFixture(t, StoreOptions{})
	// The first task keeps the newest occurrence, so ordering cannot follow task ids.
	first := completeTask(t, f, "a", 99)
	second := completeTask(t, f, "b", 95)
	third := completeTask(t, f, "c", 90)
	waitingExec, waitingSpec := withCreateCall(f.exec, f.spec, "event-w", "turn-w", "call-w")
	waitingSpec.EquivalenceKey = "w"
	if _, err := f.svc.Create(ctx, waitingExec, waitingSpec, Admission{}); err != nil {
		t.Fatal(err)
	}

	wantOrder := []Result{*first.Result, *second.Result, *third.Result}
	for _, limit := range []int{-1, 0} {
		got, err := f.svc.ListRecentResults(ctx, f.exec.Owner, limit)
		if err != nil || len(got) != 0 || got == nil {
			t.Fatalf("limit %d = %#v (%v), want an empty list", limit, got, err)
		}
	}
	for _, tt := range []struct {
		limit int
		want  []Result
	}{{1, wantOrder[:1]}, {2, wantOrder[:2]}, {3, wantOrder}, {99, wantOrder}} {
		got, err := f.svc.ListRecentResults(ctx, f.exec.Owner, tt.limit)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("limit %d order = %+v, want %+v", tt.limit, got, tt.want)
		}
		if got[0].ID != wantOrder[0].ID {
			t.Fatalf("limit %d ignored the newest result: %+v", tt.limit, got)
		}
	}

	all, err := f.svc.List(ctx, f.exec.Owner, 3)
	if err != nil || len(all) != 3 || all[0].ID != first.ID {
		t.Fatalf("List changed: %+v %v", all, err)
	}
	active, err := f.svc.ListActive(ctx, f.exec.Owner, 3)
	if err != nil || len(active) != 1 || active[0].State != StateWaiting {
		t.Fatalf("ListActive changed: %+v %v", active, err)
	}
}

func TestListRecentResultsOrdersTiesAndStaysBounded(t *testing.T) {
	ctx := context.Background()
	f := newCreateFixture(t, StoreOptions{})
	first := completeTask(t, f, "a", 90)
	second := completeTask(t, f, "b", 90)
	dropped := completeTask(t, f, "c", 80)
	newest := completeTask(t, f, "d", 95)
	if !(first.Result.ID < second.Result.ID) {
		t.Fatalf("fixture order = %s %s", first.Result.ID, second.Result.ID)
	}
	got, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{*newest.Result, *first.Result, *second.Result}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tie order = %+v, want newest first then equal occurrences by result id", got)
	}
	bounded, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 99)
	if err != nil || len(bounded) != 3 || !reflect.DeepEqual(bounded, want) {
		t.Fatalf("bounded read = %+v (%v)", bounded, err)
	}
	for _, result := range bounded {
		if result.ID == dropped.Result.ID {
			t.Fatalf("oldest result survived the fixed bound: %+v", bounded)
		}
	}
}

func TestListRecentResultsIsolatesOwnersAndCopiesResults(t *testing.T) {
	ctx := context.Background()
	f := newCreateFixture(t, StoreOptions{})
	record := completeTask(t, f, "a", 90)
	got, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3)
	if err != nil || len(got) != 1 {
		t.Fatalf("owner read = %+v (%v)", got, err)
	}
	got[0].EvidenceRefs[0] = "mutated"
	got[0].Source.EventID = "mutated"
	after, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3)
	if err != nil || after[0].EvidenceRefs[0] != record.Result.EvidenceRefs[0] || !reflect.DeepEqual(after[0].Source, record.Result.Source) {
		t.Fatalf("caller mutation persisted: %+v %v", after, err)
	}
	for _, owner := range []session.AgentSessionKey{
		{GameID: f.exec.Owner.GameID, WorldID: f.exec.Owner.WorldID, EntityID: "other"},
		{GameID: f.exec.Owner.GameID, WorldID: "other", EntityID: f.exec.Owner.EntityID},
		{GameID: "other", WorldID: f.exec.Owner.WorldID, EntityID: f.exec.Owner.EntityID},
	} {
		other, err := f.svc.ListRecentResults(ctx, owner, 3)
		if err != nil || !reflect.DeepEqual(other, []Result{}) {
			t.Fatalf("owner isolation = %+v (%v)", other, err)
		}
	}
	if _, err := f.svc.ListRecentResults(ctx, session.AgentSessionKey{}, 3); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("invalid owner: %v", err)
	}
}

func TestListRecentResultsErrors(t *testing.T) {
	for _, scenario := range []string{"corrupt", "storage", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := newCreateFixture(t, StoreOptions{})
			completeTask(t, f, "a", 90)
			want := error(ErrInvalidTaskSpec)
			switch scenario {
			case "corrupt":
				if _, err := f.store.db.Exec(`UPDATE tasks SET record_json = '{}'`); err != nil {
					t.Fatal(err)
				}
			case "storage":
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				want = ErrTaskConflict
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			if _, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3); !errors.Is(err, want) {
				t.Fatalf("error contract: %v want %v", err, want)
			}
		})
	}
}

func TestListRecentResultsScanBoundIsTheWorldCapacity(t *testing.T) {
	ctx := context.Background()
	f := newCreateFixture(t, StoreOptions{MaxTasksPerWorld: 3})
	oldest := completeTask(t, f, "a", 60)
	_ = completeTask(t, f, "b", 80)
	_ = completeTask(t, f, "c", 99)
	overflowExec, overflowSpec := withCreateCall(f.exec, f.spec, "event-d", "turn-d", "call-d")
	overflowSpec.EquivalenceKey = "d"
	if _, err := f.svc.Create(ctx, overflowExec, overflowSpec, Admission{}); !errors.Is(err, ErrTaskCapacityExceeded) {
		t.Fatalf("capacity guard: %v", err)
	}
	got, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("capacity bound = %+v (%v)", got, err)
	}
	if got[2].ID != oldest.Result.ID {
		t.Fatalf("oldest of the bounded set = %+v", got)
	}
}

// completeTaskOnBinding commits one task against an explicitly given world head, so a
// test can act on the head a save or a load published.
func completeTaskOnBinding(t *testing.T, f createFixture, head Head, suffix string, occurredAt int64) Record {
	t.Helper()
	ctx := context.Background()
	source := SourceRef{Kind: SourceKindInternal, EventID: "event-" + suffix, TurnID: "turn-" + suffix, CallID: "call-" + suffix}
	spec := f.spec
	spec.Source, spec.EquivalenceKey = source, suffix
	created, err := f.svc.Create(ctx, ExecutionContext{Owner: f.exec.Owner, Binding: head.Binding, Clock: head.Clock, Source: source}, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	evidence := taskEvidence(head.Binding, created.Task, "fact-"+suffix, EvidenceKindSatisfied)
	evidence.OccurredAt = occurredAt
	if _, err := f.svc.AdmitEvidence(ctx, head.Binding, evidence); err != nil {
		t.Fatal(err)
	}
	reconciled, err := f.svc.Reconcile(ctx, ExecutionContext{
		Owner: f.exec.Owner, Binding: head.Binding, Clock: head.Clock, TaskID: created.Task.ID, ExpectedRevision: created.Task.Revision,
		Source: SourceRef{Kind: SourceKindInternal, EventID: "reconcile", TurnID: "reconcile", CallID: "reconcile-" + suffix},
	})
	if err != nil || reconciled.Task.Result == nil {
		t.Fatalf("task %s committed no result: %+v (%v)", suffix, reconciled.Task, err)
	}
	return reconciled.Task
}

func TestListRecentResultsAfterRestore(t *testing.T) {
	ctx := context.Background()
	f := newCreateFixture(t, StoreOptions{})
	before := completeTask(t, f, "a", 90)
	prepared, err := f.svc.PrepareCheckpoint(ctx, f.head.Binding, f.clock, "save-results", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.FinishCheckpoint(ctx, prepared.Head.Binding, prepared.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}
	head, err := f.svc.ActivateWorld(ctx, f.world, "run-after-save", f.clock, prepared.Reference)
	if err != nil {
		t.Fatal(err)
	}
	after := completeTaskOnBinding(t, f, head, "b", 95)
	if got, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3); err != nil || !reflect.DeepEqual(got, []Result{*after.Result, *before.Result}) {
		t.Fatalf("results after the save = %+v (%v)", got, err)
	}

	// Loading the save replaces the working set, so a result that only existed after
	// the save is no longer a fact the runtime may act on.
	if _, err := f.svc.ActivateWorld(ctx, f.world, "run-restored", f.clock, prepared.Reference); err != nil {
		t.Fatal(err)
	}
	restored, err := f.svc.ListRecentResults(ctx, f.exec.Owner, 3)
	if err != nil || !reflect.DeepEqual(restored, []Result{*before.Result}) {
		t.Fatalf("results after the load = %+v (%v), want only the saved timeline", restored, err)
	}
	if _, err := f.svc.Read(ctx, f.exec.Owner, after.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("post-save task survived the load: %v", err)
	}
}
