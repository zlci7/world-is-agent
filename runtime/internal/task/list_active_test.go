package task

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gameagent/runtime/internal/session"
)

func TestListActive(t *testing.T) {
	f := newCreateFixture(t, StoreOptions{})
	ctx := context.Background()
	lister, ok := any(f.svc).(interface {
		ListActive(context.Context, session.AgentSessionKey, int) ([]Record, error)
	})
	if !ok {
		t.Fatal("Service must expose ListActive")
	}
	var records []Record
	for _, suffix := range []string{"a", "b", "c"} {
		exec, spec := withCreateCall(f.exec, f.spec, "event-"+suffix, "turn-"+suffix, "call-"+suffix)
		spec.EquivalenceKey = suffix
		got, err := f.svc.Create(ctx, exec, spec, Admission{})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, got.Task)
	}
	setTaskStateForCreateTest(t, f.store, records[0], StateCancelled, 2)
	setTaskStateForCreateTest(t, f.store, records[1], StatePaused, 2)
	for _, limit := range []int{-1, 0, 1, 2, 99} {
		got, err := lister.ListActive(ctx, f.exec.Owner, limit)
		if err != nil {
			t.Fatal(err)
		}
		want := max(0, min(limit, 2))
		if len(got) != want || got == nil {
			t.Fatalf("limit %d: %#v", limit, got)
		}
		if want > 0 && (got[0].ID != records[1].ID || got[0].State != StatePaused) {
			t.Fatalf("terminal hid paused task: %+v", got)
		}
		if want == 2 && got[1].ID != records[2].ID {
			t.Fatalf("unstable order: %+v", got)
		}
	}
	all, err := f.svc.List(ctx, f.exec.Owner, 1)
	if err != nil || len(all) != 1 || all[0].ID != records[0].ID {
		t.Fatalf("List changed: %+v %v", all, err)
	}
	got, _ := lister.ListActive(ctx, f.exec.Owner, 1)
	got[0].Spec.Contract[0] = '['
	*got[0].NextWakeAt = 1
	after, err := lister.ListActive(ctx, f.exec.Owner, 1)
	if err != nil || after[0].Spec.Contract[0] != '{' || *after[0].NextWakeAt != f.spec.WakeAt {
		t.Fatalf("caller mutation persisted: %+v %v", after, err)
	}
	for _, owner := range []session.AgentSessionKey{{GameID: f.exec.Owner.GameID, WorldID: f.exec.Owner.WorldID, EntityID: "other"}, {GameID: f.exec.Owner.GameID, WorldID: "other", EntityID: f.exec.Owner.EntityID}, {GameID: "other", WorldID: f.exec.Owner.WorldID, EntityID: f.exec.Owner.EntityID}} {
		got, err := lister.ListActive(ctx, owner, 10)
		if err != nil || !reflect.DeepEqual(got, []Record{}) {
			t.Fatalf("owner isolation: %+v %v", got, err)
		}
	}
	if _, err := lister.ListActive(ctx, session.AgentSessionKey{}, 0); !errors.Is(err, ErrInvalidTaskSpec) {
		t.Fatalf("invalid owner: %v", err)
	}
	setTaskStateForCreateTest(t, f.store, records[2], StateRunning, 2)
	running, err := lister.ListActive(ctx, f.exec.Owner, 99)
	if err != nil || len(running) != 2 || running[1].State != StateRunning {
		t.Fatalf("running task missing: %+v %v", running, err)
	}
	for _, record := range records[1:] {
		setTaskStateForCreateTest(t, f.store, record, StateCancelled, 2)
	}
	got, err = lister.ListActive(ctx, f.exec.Owner, 10)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("all terminal: %+v %v", got, err)
	}
}

func TestListActiveErrors(t *testing.T) {
	for _, scenario := range []string{"corrupt", "storage", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCreateFixture(t, StoreOptions{})
			ctx := context.Background()
			if _, err := f.svc.Create(ctx, f.exec, f.spec, Admission{}); err != nil {
				t.Fatal(err)
			}
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
			if _, err := f.svc.ListActive(ctx, f.exec.Owner, 1); !errors.Is(err, want) {
				t.Fatalf("error contract: %v want %v", err, want)
			}
		})
	}
}
