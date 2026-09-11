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
)

func TestReconcileTerminalEvidence(t *testing.T) {
	tests := []struct {
		kind      string
		wantState State
	}{
		{kind: EvidenceKindSatisfied, wantState: StateSucceeded},
		{kind: EvidenceKindUnsatisfied, wantState: StateFailed},
		{kind: EvidenceKindInterrupted, wantState: StateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			fixture, created, initialWake := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-terminal-"+tt.kind, tt.kind)
			evidence.OccurredAt = 95
			evidence.Details = json.RawMessage(`{"opaque":"met expired success waiting"}`)
			evidence.Source.GameTime = json.RawMessage(`{"outcome":"waiting"}`)
			evidence.Source.Facts = json.RawMessage(`[{"outcome":"success"}]`)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
			}

			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, created.Task.Revision))
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if got.Next != ReconcileNextSettled || got.Task.State != tt.wantState || got.Task.Revision != 2 ||
				got.Task.NextWakeAt != nil || got.Task.NeedsReconcile || got.Task.PauseReason != "" ||
				got.Task.NoProgressAttempts != 0 || got.Task.ReconcileAttempts != 0 || got.Task.Result == nil {
				t.Fatalf("Reconcile() = %+v", got)
			}
			wantResult := Result{
				ID: got.Task.Result.ID, TaskID: created.Task.ID, Revision: 2, State: tt.wantState,
				Reason: tt.kind, OccurredAt: evidence.OccurredAt,
				EvidenceRefs: []string{evidence.FactID}, Source: evidence.Source,
			}
			if !reflect.DeepEqual(*got.Task.Result, wantResult) {
				t.Fatalf("Result = %+v, want %+v", *got.Task.Result, wantResult)
			}
			if !got.Task.Evidence[0].Applied {
				t.Fatal("terminal Evidence remained unapplied")
			}
			storedWake, err := fixture.store.loadWake(context.Background(), created.Task.Owner, initialWake.ID)
			if err != nil || storedWake.Status != wakeStatusConsumed {
				t.Fatalf("wake after Reconcile = (%+v, %v)", storedWake, err)
			}

			again, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, created.Task.Revision))
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatalf("terminal retry = (%+v, %v), want immutable %+v", again, err, got)
			}
			got.Task.Progress = json.RawMessage(`{"caller":"mutated"}`)
			got.Task.Result.Source.Facts = json.RawMessage(`[{"caller":"mutated"}]`)
			stored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
			if err != nil || bytes.Equal(stored.Progress, got.Task.Progress) || bytes.Equal(stored.Result.Source.Facts, got.Task.Result.Source.Facts) {
				t.Fatalf("caller mutation aliased durable record: (%+v, %v)", stored, err)
			}
		})
	}
}

func TestReconcileSatisfiedWinsEarlierThanDeadlineEvidence(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	progress := taskEvidence(fixture.head.Binding, created.Task, "fact-progress", EvidenceKindProgress)
	progress.OccurredAt = 99
	progress.Details = json.RawMessage(`{"status":"expired","outcome":"failed"}`)
	satisfied := taskEvidence(fixture.head.Binding, created.Task, "fact-satisfied", EvidenceKindSatisfied)
	satisfied.OccurredAt = 90
	unsatisfied := taskEvidence(fixture.head.Binding, created.Task, "fact-deadline", EvidenceKindUnsatisfied)
	unsatisfied.OccurredAt = 100
	for _, evidence := range []Evidence{progress, unsatisfied, satisfied} {
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence(%s) = (%v, %v)", evidence.FactID, added, err)
		}
	}

	got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.State != StateSucceeded || got.Task.Result == nil || got.Task.Result.EvidenceRefs[0] != satisfied.FactID ||
		got.Task.Result.Reason != EvidenceKindSatisfied {
		t.Fatalf("terminal precedence = %+v", got)
	}
	if !got.Task.Evidence[0].Applied || !got.Task.Evidence[1].Applied || !got.Task.Evidence[2].Applied {
		t.Fatalf("not all Evidence applied: %+v", got.Task.Evidence)
	}
	if got.Task.Evidence[0].FactID != progress.FactID || got.Task.Evidence[1].FactID != unsatisfied.FactID || got.Task.Evidence[2].FactID != satisfied.FactID {
		t.Fatalf("durable Evidence order changed: %+v", got.Task.Evidence)
	}
}

func TestReconcileTerminalTieBreaksByKindThenFactID(t *testing.T) {
	t.Run("kind priority", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		facts := []Evidence{
			taskEvidence(fixture.head.Binding, created.Task, "fact-a-unsatisfied", EvidenceKindUnsatisfied),
			taskEvidence(fixture.head.Binding, created.Task, "fact-a-interrupted", EvidenceKindInterrupted),
			taskEvidence(fixture.head.Binding, created.Task, "fact-z-satisfied", EvidenceKindSatisfied),
		}
		for _, evidence := range facts {
			evidence.OccurredAt = 90
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence(%s) = (%v, %v)", evidence.FactID, added, err)
			}
		}
		got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		if err != nil || got.Task.Result == nil || got.Task.Result.EvidenceRefs[0] != "fact-z-satisfied" {
			t.Fatalf("kind tie result = (%+v, %v)", got, err)
		}
	})

	t.Run("fact id", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		for _, factID := range []string{"fact-z", "fact-a"} {
			evidence := taskEvidence(fixture.head.Binding, created.Task, factID, EvidenceKindSatisfied)
			evidence.OccurredAt = 90
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence(%s) = (%v, %v)", factID, added, err)
			}
		}
		got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		if err != nil || got.Task.Result == nil || got.Task.Result.EvidenceRefs[0] != "fact-a" {
			t.Fatalf("FactID tie result = (%+v, %v)", got, err)
		}
	})
}

func TestReconcileTerminalRetrySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	fixture, created, _ := newIntentFixture(t, StoreOptions{Path: path})
	evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-reopen", EvidenceKindSatisfied)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
	}
	want, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: 1})
	again, err := NewService(reopened).Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("reopened terminal retry = (%+v, %v), want %+v", again, err, want)
	}
}

func TestReconcileConcurrentAndFaultRollback(t *testing.T) {
	t.Run("concurrent terminal convergence", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-concurrent", EvidenceKindSatisfied)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		results := make([]ReconcileResult, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				results[index], errs[index] = fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil || results[i].Task.Result == nil || results[i].Next != ReconcileNextSettled {
				t.Fatalf("call %d = (%+v, %v)", i, results[i], err)
			}
		}
		if results[0].Task.Result.ID != results[1].Task.Result.ID || results[0].Task.Revision != 2 || results[1].Task.Revision != 2 {
			t.Fatalf("concurrent results differ: %+v / %+v", results[0], results[1])
		}
	})

	for _, stage := range []string{reconcileStageRecordUpdated, reconcileStageWakesConsumed} {
		t.Run("rollback/"+stage, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-fault-"+stage, EvidenceKindSatisfied)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
			}
			beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			fixture.store.testAfterReconcileStage = func(_ context.Context, got string) error {
				if got == stage {
					return errors.New("private reconcile fault")
				}
				return nil
			}
			if got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1)); err == nil || !reflect.DeepEqual(got, ReconcileResult{}) {
				t.Fatalf("faulted Reconcile() = (%+v, %v)", got, err)
			}
			fixture.store.testAfterReconcileStage = nil
			afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("faulted reconciliation changed durable bytes")
			}
		})
	}
}

func TestReconcileProgressWaitUntil(t *testing.T) {
	tests := []struct {
		name     string
		wait     int64
		wantNext string
	}{
		{name: "future", wait: 150, wantNext: ReconcileNextSettled},
		{name: "exactly now", wait: 100, wantNext: ReconcileNextObserve},
		{name: "already past", wait: 95, wantNext: ReconcileNextObserve},
		{name: "deadline", wait: 300, wantNext: ReconcileNextSettled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, created, initialWake := newIntentFixture(t, StoreOptions{})
			evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-wait-"+tt.name, EvidenceKindProgress)
			evidence.OccurredAt = 90
			evidence.WaitUntil = &tt.wait
			evidence.Details = json.RawMessage(`{"integer":9007199254740993,"decimal":1.2300}`)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
			}
			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if got.Next != tt.wantNext || got.Task.State != StateWaiting || got.Task.Revision != 2 ||
				got.Task.NextWakeAt == nil || *got.Task.NextWakeAt != tt.wait || got.Task.Result != nil ||
				!bytes.Equal(got.Task.Progress, evidence.Details) || !got.Task.Evidence[0].Applied {
				t.Fatalf("wait reconciliation = %+v", got)
			}
			wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
			if len(wakes) != 2 {
				t.Fatalf("wake count = %d, want 2: %+v", len(wakes), wakes)
			}
			pending := 0
			for _, wake := range wakes {
				if wake.ID == initialWake.ID && wake.Status != wakeStatusConsumed {
					t.Fatalf("initial wake status = %q", wake.Status)
				}
				if wake.Status == wakeStatusPending {
					pending++
					if wake.ID == initialWake.ID || wake.Reason != wakeReasonEvidenceWait || wake.DueTick != tt.wait ||
						wake.ExpectedRevision != 2 || wake.Generation != fixture.head.Binding.Generation {
						t.Fatalf("replacement wake = %+v", wake)
					}
				}
			}
			if pending != 1 {
				t.Fatalf("pending wakes = %d, want 1", pending)
			}
			if tt.wantNext == ReconcileNextSettled {
				again, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
				if err != nil || !reflect.DeepEqual(again, got) {
					t.Fatalf("stale future-wait retry = (%+v, %v), want settled convergence %+v", again, err, got)
				}
			}
		})
	}

	t.Run("deterministic wait and independent raw progress selection", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		waitLater := int64(170)
		waitFirst := int64(150)
		facts := []Evidence{
			taskEvidence(fixture.head.Binding, created.Task, "fact-ordinary-newest", EvidenceKindProgress),
			taskEvidence(fixture.head.Binding, created.Task, "fact-wait-later", EvidenceKindProgress),
			taskEvidence(fixture.head.Binding, created.Task, "fact-wait-tie-late", EvidenceKindProgress),
			taskEvidence(fixture.head.Binding, created.Task, "fact-wait-tie-early", EvidenceKindProgress),
		}
		facts[0].OccurredAt = 100
		facts[0].Details = json.RawMessage(`{"raw":1.2300,"word":"expired"}`)
		facts[1].OccurredAt, facts[1].WaitUntil, facts[1].Details = 80, &waitLater, nil
		facts[2].OccurredAt, facts[2].WaitUntil, facts[2].Details = 95, &waitFirst, nil
		facts[3].OccurredAt, facts[3].WaitUntil, facts[3].Details = 90, &waitFirst, nil
		for _, evidence := range facts {
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence(%s) = (%v, %v)", evidence.FactID, added, err)
			}
		}
		got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
		if err != nil {
			t.Fatal(err)
		}
		if got.Task.NextWakeAt == nil || *got.Task.NextWakeAt != waitFirst || !bytes.Equal(got.Task.Progress, facts[0].Details) {
			t.Fatalf("selection result = %+v", got)
		}
		for index, evidence := range got.Task.Evidence {
			if !evidence.Applied || evidence.FactID != facts[index].FactID {
				t.Fatalf("Evidence order/application = %+v", got.Task.Evidence)
			}
		}
	})

	t.Run("wake insertion fault rolls back all bytes", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		wait := int64(150)
		evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-wait-fault", EvidenceKindProgress)
		evidence.OccurredAt, evidence.WaitUntil = 90, &wait
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		fixture.store.testAfterReconcileStage = func(_ context.Context, stage string) error {
			if stage == reconcileStageWakeInserted {
				return errors.New("private wake insertion fault")
			}
			return nil
		}
		if got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1)); err == nil || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("faulted Reconcile() = (%+v, %v)", got, err)
		}
		fixture.store.testAfterReconcileStage = nil
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("wake insertion fault changed durable bytes")
		}
	})
}

func TestReconcileOrdinaryProgressAndNoFactRouting(t *testing.T) {
	for _, afterDeadline := range []bool{false, true} {
		name := "before deadline"
		wantNext := ReconcileNextDecide
		if afterDeadline {
			name = "at deadline"
			wantNext = ReconcileNextObserve
		}
		t.Run("ordinary progress/"+name, func(t *testing.T) {
			fixture, created, initialWake := newIntentFixture(t, StoreOptions{})
			older := taskEvidence(fixture.head.Binding, created.Task, "fact-ordinary-z", EvidenceKindProgress)
			older.OccurredAt = 90
			older.Details = json.RawMessage(`{"progress":"older"}`)
			newer := taskEvidence(fixture.head.Binding, created.Task, "fact-ordinary-a", EvidenceKindProgress)
			newer.OccurredAt = 95
			newer.Details = json.RawMessage(`{"integer":9007199254740993,"decimal":1.2300}`)
			for _, evidence := range []Evidence{older, newer} {
				if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
					t.Fatalf("AdmitEvidence(%s) = (%v, %v)", evidence.FactID, added, err)
				}
			}
			if afterDeadline {
				fixture.clock = Clock{ID: fixture.clock.ID, Tick: fixture.spec.DeadlineAt, Sequence: fixture.clock.Sequence + 1}
				setWorldClockForIntentTest(t, fixture.store, fixture.head, fixture.clock)
				fixture.head.Clock = fixture.clock
			}
			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if err != nil {
				t.Fatal(err)
			}
			if got.Next != wantNext || got.Task.State != StateRunning || got.Task.Revision != 2 ||
				got.Task.NextWakeAt != nil || got.Task.NeedsReconcile || got.Task.Result != nil ||
				!bytes.Equal(got.Task.Progress, newer.Details) {
				t.Fatalf("ordinary reconciliation = %+v", got)
			}
			for _, evidence := range got.Task.Evidence {
				if !evidence.Applied {
					t.Fatalf("unapplied evidence: %+v", got.Task.Evidence)
				}
			}
			wake, err := fixture.store.loadWake(context.Background(), created.Task.Owner, initialWake.ID)
			if err != nil || wake.Status != wakeStatusConsumed {
				t.Fatalf("wake = (%+v, %v)", wake, err)
			}
			again, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if !errors.Is(err, ErrTaskChanged) || !reflect.DeepEqual(again, ReconcileResult{}) {
				t.Fatalf("stale ordinary retry = (%+v, %v), want zero/ErrTaskChanged", again, err)
			}
		})
	}

	tests := []struct {
		name      string
		state     State
		deadline  bool
		marker    bool
		wantNext  string
		wantError error
	}{
		{name: "running decides", state: StateRunning, wantNext: ReconcileNextDecide},
		{name: "waiting settles", state: StateWaiting, wantNext: ReconcileNextSettled},
		{name: "paused settles", state: StatePaused, wantNext: ReconcileNextSettled},
		{name: "marker observes", state: StateRunning, marker: true, wantNext: ReconcileNextObserve},
		{name: "deadline observes", state: StateRunning, deadline: true, wantNext: ReconcileNextObserve},
	}
	for _, tt := range tests {
		t.Run("no facts/"+tt.name, func(t *testing.T) {
			fixture, created, _ := newIntentFixture(t, StoreOptions{})
			current := created.Task
			current.State = tt.state
			current.NeedsReconcile = tt.marker
			if tt.state == StateRunning {
				current.NextWakeAt = nil
			}
			if tt.state == StatePaused {
				current.PauseReason = "operator"
			}
			setRecordForIntentTest(t, fixture.store, current)
			if tt.deadline {
				fixture.clock = Clock{ID: fixture.clock.ID, Tick: fixture.spec.DeadlineAt, Sequence: fixture.clock.Sequence + 1}
				setWorldClockForIntentTest(t, fixture.store, fixture.head, fixture.clock)
				fixture.head.Clock = fixture.clock
			}
			beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if err != nil || got.Next != tt.wantNext || !reflect.DeepEqual(got.Task, current) {
				t.Fatalf("Reconcile() = (%+v, %v), want next=%s unchanged", got, err, tt.wantNext)
			}
			afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
				t.Fatal("no-fact routing mutated durable bytes")
			}
		})
	}

	t.Run("stale nonterminal", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		if got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 2)); !errors.Is(err, ErrTaskChanged) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("stale Reconcile() = (%+v, %v)", got, err)
		}
	})

	t.Run("revision overflow", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		current := created.Task
		current.Revision = uint64(math.MaxInt64)
		current.Evidence = []Evidence{{
			FactID: "fact-overflow", TaskID: current.ID, Binding: fixture.head.Binding,
			StartRevision: current.Revision, OccurredAt: 90, Kind: EvidenceKindProgress,
			Source: SourceRef{Kind: SourceKindEnvironment},
		}}
		current.NeedsReconcile = true
		setRecordForIntentTest(t, fixture.store, current)
		beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, current, current.Revision)); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("overflow Reconcile() = (%+v, %v)", got, err)
		}
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("overflow changed durable bytes")
		}
	})
}

func TestReconcileDeadlineNeverInventsFailure(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	fixture.clock = Clock{ID: fixture.clock.ID, Tick: fixture.spec.DeadlineAt, Sequence: fixture.clock.Sequence + 1}
	setWorldClockForIntentTest(t, fixture.store, fixture.head, fixture.clock)
	fixture.head.Clock = fixture.clock

	got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
	if err != nil || got.Next != ReconcileNextObserve || got.Task.State != StateWaiting || got.Task.Result != nil || got.Task.Revision != 1 {
		t.Fatalf("deadline Reconcile() = (%+v, %v)", got, err)
	}

	satisfied := taskEvidence(fixture.head.Binding, created.Task, "fact-deadline-race-satisfied", EvidenceKindSatisfied)
	satisfied.OccurredAt = fixture.clock.Tick
	var admitted bool
	var admitErr error
	var raced ReconcileResult
	var reconcileErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		admitted, admitErr = fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, satisfied)
	}()
	go func() {
		defer wg.Done()
		raced, reconcileErr = fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
	}()
	wg.Wait()
	if admitErr != nil || !admitted || reconcileErr != nil {
		t.Fatalf("race outcomes admitted=(%v,%v) reconcile=(%+v,%v)", admitted, admitErr, raced, reconcileErr)
	}
	if raced.Task.State != StateSucceeded {
		raced, reconcileErr = fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
	}
	if reconcileErr != nil || raced.Task.State != StateSucceeded || raced.Task.Result == nil ||
		raced.Task.Result.EvidenceRefs[0] != satisfied.FactID {
		t.Fatalf("deadline race final = (%+v, %v)", raced, reconcileErr)
	}
}

func TestReconcileAuthorityAndWorldIdentityFailClosed(t *testing.T) {
	t.Run("safe nil entry points", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := reconcileExecution(fixture, created.Task, 1)
		if got, err := (*Service)(nil).Reconcile(context.Background(), exec); !errors.Is(err, ErrTaskConflict) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("nil service = (%+v, %v)", got, err)
		}
		if got, err := NewService(nil).Reconcile(context.Background(), exec); !errors.Is(err, ErrTaskConflict) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("nil store = (%+v, %v)", got, err)
		}
		if got, err := fixture.svc.Reconcile(nil, exec); !errors.Is(err, ErrInvalidTaskSpec) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("nil context = (%+v, %v)", got, err)
		}
	})

	t.Run("authority before terminal disclosure", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-authority-terminal", EvidenceKindSatisfied)
		if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
			t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
		}
		if _, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1)); err != nil {
			t.Fatal(err)
		}
		current := fixture.head.Binding
		current.Generation++
		setWorldBindingForEvidenceTest(t, fixture.store, fixture.head, current)
		if got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1)); !errors.Is(err, ErrGenerationStale) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("stale terminal retry = (%+v, %v)", got, err)
		}
	})

	t.Run("save barrier and clock mismatch", func(t *testing.T) {
		fixture, created, _ := newIntentFixture(t, StoreOptions{})
		exec := reconcileExecution(fixture, created.Task, 1)
		setWorldHeadStateForIntentTest(t, fixture.store, fixture.head, worldHeadStatusReady, "saving", "save-a", "preparing")
		if got, err := fixture.svc.Reconcile(context.Background(), exec); !errors.Is(err, ErrSaveInProgress) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("save barrier = (%+v, %v)", got, err)
		}
		setWorldHeadStateForIntentTest(t, fixture.store, fixture.head, worldHeadStatusReady, "", "", "")
		currentClock := Clock{ID: "replacement.clock.v1", Tick: fixture.clock.Tick, Sequence: fixture.clock.Sequence}
		setWorldClockForIntentTest(t, fixture.store, fixture.head, currentClock)
		exec.Clock = currentClock
		if got, err := fixture.svc.Reconcile(context.Background(), exec); !errors.Is(err, ErrClockMismatch) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("task clock mismatch = (%+v, %v)", got, err)
		}
	})

	t.Run("world identity ambiguity", func(t *testing.T) {
		fixture := newWorldIdentityWriteFixture(t)
		fixture.injectDuplicateIdentity(t, "fact")
		exec := fixture.targetExec
		exec.Source = SourceRef{Kind: SourceKindInternal, EventID: "reconcile-corrupt-e", TurnID: "reconcile-corrupt-t", CallID: "reconcile-corrupt-c"}
		if got, err := fixture.svc.Reconcile(context.Background(), exec); !errors.Is(err, ErrTaskConflict) || !reflect.DeepEqual(got, ReconcileResult{}) {
			t.Fatalf("ambiguous identity = (%+v, %v)", got, err)
		}
	})
}

func reconcileExecution(fixture createFixture, record Record, expectedRevision uint64) ExecutionContext {
	return ExecutionContext{
		Owner: fixture.exec.Owner, Binding: fixture.head.Binding, Clock: fixture.clock,
		Source: SourceRef{Kind: SourceKindInternal, EventID: "reconcile-event", TurnID: "reconcile-turn", CallID: "reconcile-call"},
		TaskID: record.ID, ExpectedRevision: expectedRevision,
	}
}
