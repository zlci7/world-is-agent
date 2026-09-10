package task

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"gameagent/runtime/internal/session"
)

func TestAdmissionUnlimitedAllowsIndependentActiveTasks(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	first, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{})
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-b", "turn-b", "call-b")
	spec.EquivalenceKey = "equivalence-b"
	second, err := fixture.svc.Create(context.Background(), exec, spec, Admission{})
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if !first.Created || !second.Created || first.Task.ID == second.Task.ID {
		t.Fatalf("unlimited creates = (%+v, %+v), want two independent tasks", first, second)
	}
	assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 2, 2)
}

func TestAdmissionOneBlocksIndependentTaskButPreservesExactAndEquivalentReuse(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 1})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	exact, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 1})
	if err != nil || !reflect.DeepEqual(exact, created) {
		t.Fatalf("exact retry = (%+v, %v), want %+v", exact, err, created)
	}

	equivalentExec, equivalentSpec := withCreateCall(fixture.exec, fixture.spec, "event-equivalent", "turn-equivalent", "call-equivalent")
	equivalent, err := fixture.svc.Create(context.Background(), equivalentExec, equivalentSpec, Admission{MaxActivePerOwner: 1})
	if err != nil || equivalent.Created || equivalent.Task.ID != created.Task.ID {
		t.Fatalf("equivalent retry = (%+v, %v), want existing task", equivalent, err)
	}

	differentExec, differentSpec := withCreateCall(fixture.exec, fixture.spec, "event-b", "turn-b", "call-b")
	differentSpec.EquivalenceKey = "equivalence-b"
	if _, err := fixture.svc.Create(context.Background(), differentExec, differentSpec, Admission{MaxActivePerOwner: 1}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("independent Create() error = %v, want ErrTaskConflict", err)
	}
	assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
}

func TestAdmissionCountsOnlyActiveTasksForCompleteOwner(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 1})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	setTaskStateForCreateTest(t, fixture.store, created.Task, StateCancelled, 2)

	exec, spec := withCreateCall(fixture.exec, fixture.spec, "event-after", "turn-after", "call-after")
	spec.EquivalenceKey = "equivalence-after"
	if _, err := fixture.svc.Create(context.Background(), exec, spec, Admission{MaxActivePerOwner: 1}); err != nil {
		t.Fatalf("Create() after terminal error = %v", err)
	}

	otherOwner := session.AgentSessionKey{GameID: fixture.world.GameID, WorldID: fixture.world.WorldID, EntityID: "actor-b"}
	otherExec, otherSpec := createInputs(fixture.head, fixture.clock, otherOwner, "event-other", "turn-other", "call-other", "equivalence-other")
	if _, err := fixture.svc.Create(context.Background(), otherExec, otherSpec, Admission{MaxActivePerOwner: 1}); err != nil {
		t.Fatalf("other owner Create() error = %v", err)
	}

	otherWorld := WorldKey{GameID: fixture.world.GameID, WorldID: "world-b"}
	otherClock := Clock{ID: fixture.clock.ID, Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
	otherHead, err := fixture.svc.ActivateWorld(context.Background(), otherWorld, "run-b", otherClock, CheckpointRef{Status: checkpointStatusAbsent, World: otherWorld})
	if err != nil {
		t.Fatalf("ActivateWorld(other) error = %v", err)
	}
	sameEntityOtherWorld := session.AgentSessionKey{GameID: otherWorld.GameID, WorldID: otherWorld.WorldID, EntityID: fixture.exec.Owner.EntityID}
	worldExec, worldSpec := createInputs(otherHead, otherClock, sameEntityOtherWorld, "event-world", "turn-world", "call-world", "equivalence-world")
	if _, err := fixture.svc.Create(context.Background(), worldExec, worldSpec, Admission{MaxActivePerOwner: 1}); err != nil {
		t.Fatalf("other world Create() error = %v", err)
	}
}

func TestAdmissionUsesExactActiveAndTerminalStateSets(t *testing.T) {
	for _, state := range []State{StateWaiting, StateRunning, StatePaused} {
		t.Run("active/"+string(state), func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 1})
			if err != nil {
				t.Fatal(err)
			}
			setTaskStateForCreateTest(t, fixture.store, created.Task, state, 2)
			exec, spec := withCreateCall(fixture.exec, fixture.spec, "active-e", "active-t", "active-c")
			spec.EquivalenceKey = "active-other"
			if _, err := fixture.svc.Create(context.Background(), exec, spec, Admission{MaxActivePerOwner: 1}); !errors.Is(err, ErrTaskConflict) {
				t.Fatalf("Create() error = %v, want ErrTaskConflict", err)
			}
		})
	}
	for _, state := range []State{StateSucceeded, StateFailed, StateCancelled} {
		t.Run("terminal/"+string(state), func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 1})
			if err != nil {
				t.Fatal(err)
			}
			setTaskStateForCreateTest(t, fixture.store, created.Task, state, 2)
			exec, spec := withCreateCall(fixture.exec, fixture.spec, "terminal-e", "terminal-t", "terminal-c")
			spec.EquivalenceKey = "terminal-other"
			if _, err := fixture.svc.Create(context.Background(), exec, spec, Admission{MaxActivePerOwner: 1}); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
		})
	}
}

func TestAdmissionOneSerializesConcurrentDistinctAndEquivalentCreates(t *testing.T) {
	t.Run("distinct", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		const count = 8
		results, errs := runConcurrentCreates(count, func(index int) (CreateResult, error) {
			exec, spec := withCreateCall(fixture.exec, fixture.spec,
				fmt.Sprintf("event-%d", index), fmt.Sprintf("turn-%d", index), fmt.Sprintf("call-%d", index))
			spec.EquivalenceKey = fmt.Sprintf("equivalence-%d", index)
			return fixture.svc.Create(context.Background(), exec, spec, Admission{MaxActivePerOwner: 1})
		})
		created, conflicts := 0, 0
		for index, err := range errs {
			switch {
			case err == nil && results[index].Created:
				created++
			case errors.Is(err, ErrTaskConflict):
				conflicts++
			default:
				t.Fatalf("result[%d] = (%+v, %v)", index, results[index], err)
			}
		}
		if created != 1 || conflicts != count-1 {
			t.Fatalf("created/conflicts = %d/%d, want 1/%d", created, conflicts, count-1)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	})

	t.Run("equivalent", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		const count = 8
		results, errs := runConcurrentCreates(count, func(index int) (CreateResult, error) {
			exec, spec := withCreateCall(fixture.exec, fixture.spec,
				fmt.Sprintf("event-%d", index), fmt.Sprintf("turn-%d", index), fmt.Sprintf("call-%d", index))
			return fixture.svc.Create(context.Background(), exec, spec, Admission{MaxActivePerOwner: 1})
		})
		var taskID string
		created := 0
		for index, err := range errs {
			if err != nil {
				t.Fatalf("result[%d] error = %v", index, err)
			}
			if results[index].Created {
				created++
			}
			if taskID == "" {
				taskID = results[index].Task.ID
			} else if results[index].Task.ID != taskID {
				t.Fatalf("result[%d] task = %q, want %q", index, results[index].Task.ID, taskID)
			}
		}
		if created != 1 {
			t.Fatalf("created responses = %d, want 1", created)
		}
		assertCreateRowCounts(t, fixture.store, fixture.exec.Owner, 1, 1)
	})
}

func TestAdmissionPositiveLimitIsGenericAndProspectivePolicyDoesNotChangeRetries(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	first, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 2})
	if err != nil {
		t.Fatal(err)
	}
	secondExec, secondSpec := withCreateCall(fixture.exec, fixture.spec, "event-second", "turn-second", "call-second")
	secondSpec.EquivalenceKey = "equivalence-second"
	if _, err := fixture.svc.Create(context.Background(), secondExec, secondSpec, Admission{MaxActivePerOwner: 2}); err != nil {
		t.Fatal(err)
	}
	thirdExec, thirdSpec := withCreateCall(fixture.exec, fixture.spec, "event-third", "turn-third", "call-third")
	thirdSpec.EquivalenceKey = "equivalence-third"
	if _, err := fixture.svc.Create(context.Background(), thirdExec, thirdSpec, Admission{MaxActivePerOwner: 2}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("third Create() error = %v, want ErrTaskConflict", err)
	}

	exact, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{MaxActivePerOwner: 1})
	if err != nil || !reflect.DeepEqual(exact, first) {
		t.Fatalf("exact retry under smaller prospective policy = (%+v, %v), want %+v", exact, err, first)
	}
	equivalentExec, equivalentSpec := withCreateCall(fixture.exec, fixture.spec, "event-equivalent-lower", "turn-equivalent-lower", "call-equivalent-lower")
	equivalent, err := fixture.svc.Create(context.Background(), equivalentExec, equivalentSpec, Admission{MaxActivePerOwner: 1})
	if err != nil || equivalent.Created || equivalent.Task.ID != first.Task.ID {
		t.Fatalf("equivalent retry under smaller prospective policy = (%+v, %v)", equivalent, err)
	}
}
