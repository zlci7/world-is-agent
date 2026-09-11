package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCheckpointRestoresPriorStateWithoutMutatingSnapshot(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	initial := createWakeTask(t, fixture, "checkpoint-initial", "actor", 200, 400)

	prepared, err := fixture.svc.PrepareCheckpoint(
		context.Background(), fixture.head.Binding, fixture.clock, "save-c1", nil,
	)
	if err != nil {
		t.Fatalf("PrepareCheckpoint error = %v", err)
	}
	if prepared.SaveRequestID != "save-c1" || prepared.Reference.Status != "confirmed" ||
		prepared.Reference.SchemaVersion != 1 || prepared.Reference.World != fixture.world ||
		prepared.Reference.ID == "" || len(prepared.Reference.Checksum) != 64 ||
		prepared.Head.Binding.Generation != fixture.head.Binding.Generation+1 {
		t.Fatalf("Prepared = %+v", prepared)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "save-c1", true); err != nil {
		t.Fatalf("FinishCheckpoint error = %v", err)
	}

	fixture.head = prepared.Head
	fixture.exec.Binding = prepared.Head.Binding
	fixture.exec.Clock = prepared.Head.Clock
	afterSave := createWakeTask(t, fixture, "checkpoint-after", "actor", 250, 400)
	snapshotBeforeRestore := checkpointBytesForTest(t, fixture.store, prepared.Reference.ID)

	restoredClock := Clock{ID: fixture.clock.ID, Tick: fixture.clock.Tick, Sequence: 1}
	restoredHead, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-b", restoredClock, prepared.Reference)
	if err != nil {
		t.Fatalf("ActivateWorld restore error = %v", err)
	}
	if restoredHead.Binding.RunID != "run-b" || restoredHead.Binding.Generation <= prepared.Head.Binding.Generation ||
		restoredHead.Clock != restoredClock || restoredHead.CheckpointID != prepared.Reference.ID {
		t.Fatalf("restored Head = %+v", restoredHead)
	}
	restored, err := fixture.svc.Read(context.Background(), initial.Task.Owner, initial.Task.ID)
	if err != nil || restored.ID != initial.Task.ID || restored.Revision != initial.Task.Revision ||
		restored.State != StateWaiting || restored.NextWakeAt == nil || *restored.NextWakeAt != 200 {
		t.Fatalf("restored initial Task = (%+v, %v)", restored, err)
	}
	if _, err := fixture.svc.Read(context.Background(), afterSave.Task.Owner, afterSave.Task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("post-save Task read error = %v, want task_not_found", err)
	}
	wakes := loadTaskWakes(t, fixture.store, restored.Owner, restored.ID)
	if len(wakes) != 1 || wakes[0].Status != wakeStatusPending || wakes[0].Generation != restoredHead.Binding.Generation {
		t.Fatalf("restored Wakes = %+v", wakes)
	}
	if snapshotAfterRestore := checkpointBytesForTest(t, fixture.store, prepared.Reference.ID); !bytes.Equal(snapshotAfterRestore, snapshotBeforeRestore) {
		t.Fatal("restore mutated immutable checkpoint bytes")
	}
}

func checkpointBytesForTest(t *testing.T, store *SQLiteStore, checkpointID string) []byte {
	t.Helper()
	var snapshot []byte
	if err := store.db.QueryRow(`SELECT snapshot_json FROM task_checkpoints WHERE checkpoint_id = ?`, checkpointID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), snapshot...)
}

func TestCheckpointPrepareIsExactRetryAndFencesPriorGeneration(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "checkpoint-retry", "actor", 200, 400)
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-retry", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("Create during barrier error = %v, want save_in_progress", err)
	}
	again, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-retry", []Evidence{})
	if err != nil || !reflect.DeepEqual(again, prepared) {
		t.Fatalf("exact PrepareCheckpoint retry = (%+v, %v), want %+v", again, err, prepared)
	}
	if got := totalRows(t, fixture.store.db, "task_checkpoints"); got != 1 {
		t.Fatalf("checkpoint count = %d, want 1", got)
	}

	different := Evidence{
		FactID: "fact-different", TaskID: created.Task.ID, Binding: fixture.head.Binding,
		StartRevision: created.Task.Revision, OccurredAt: fixture.clock.Tick, Kind: EvidenceKindProgress,
		Source: SourceRef{Kind: SourceKindEnvironment, EventID: "event-save", TurnID: "turn-save", CallID: "call-save"},
	}
	if _, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-retry", []Evidence{different}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different PrepareCheckpoint retry error = %v, want idempotency_conflict", err)
	}

	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "save-retry", false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "save-retry", true); err != nil {
		t.Fatalf("FinishCheckpoint retry error = %v", err)
	}
	again, err = fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-retry", nil)
	if err != nil || !reflect.DeepEqual(again, prepared) {
		t.Fatalf("post-finish PrepareCheckpoint retry = (%+v, %v), want %+v", again, err, prepared)
	}
	if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrGenerationStale) {
		t.Fatalf("old generation Create error = %v, want generation_stale", err)
	}
}

func TestCheckpointAdmitsFinalEvidenceBeforeSnapshot(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "checkpoint-evidence", "actor", 200, 400)
	evidence := Evidence{
		FactID: "fact-at-save", TaskID: created.Task.ID, Binding: fixture.head.Binding,
		StartRevision: created.Task.Revision, OccurredAt: fixture.clock.Tick, Kind: EvidenceKindProgress,
		Details: json.RawMessage(`{"amount":9007199254740993.00}`),
		Source: SourceRef{
			Kind: SourceKindEnvironment, EventID: "event-at-save", TurnID: "turn-at-save", CallID: "call-at-save",
			GameTime: json.RawMessage(`{"raw":9007199254740993.00}`),
		},
	}
	prepared, err := fixture.svc.PrepareCheckpoint(
		context.Background(), fixture.head.Binding, fixture.clock, "save-evidence", []Evidence{evidence},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(checkpointBytesForTest(t, fixture.store, prepared.Reference.ID), []byte("9007199254740993.00")) {
		t.Fatal("checkpoint changed opaque JSON numeric lexeme")
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "save-evidence", true); err != nil {
		t.Fatal(err)
	}
	restoredHead, err := fixture.svc.ActivateWorld(
		context.Background(), fixture.world, "run-evidence-restored", fixture.clock, prepared.Reference,
	)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Evidence) != 1 || restored.Evidence[0].FactID != evidence.FactID || restored.Evidence[0].Applied ||
		!bytes.Equal(restored.Evidence[0].Details, evidence.Details) ||
		!bytes.Equal(restored.Evidence[0].Source.GameTime, evidence.Source.GameTime) || !restored.NeedsReconcile {
		t.Fatalf("restored evidence Task = %+v", restored)
	}
	if restoredHead.Binding.Generation <= prepared.Head.Binding.Generation {
		t.Fatalf("restored generation = %d, prepared = %d", restoredHead.Binding.Generation, prepared.Head.Binding.Generation)
	}
}

func TestCheckpointConvertsRunningAttemptToDurableReconciliation(t *testing.T) {
	fixture, created, oldWake, exec, running, clock := runningWakeForRestart(t, 200)
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, clock, "save-running", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "save-running", true); err != nil {
		t.Fatal(err)
	}
	recovered, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != StateWaiting || !recovered.NeedsReconcile || recovered.Revision != running.Revision+1 ||
		recovered.NextWakeAt == nil || *recovered.NextWakeAt != clock.Tick {
		t.Fatalf("checkpoint-normalized Task = %+v", recovered)
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	if len(wakes) != 2 {
		t.Fatalf("wake count = %d, want consumed plus replacement", len(wakes))
	}
	var old, replacement Wake
	for _, wake := range wakes {
		if wake.ID == oldWake.ID {
			old = wake
		} else {
			replacement = wake
		}
	}
	if old.Status != wakeStatusConsumed || replacement.Status != wakeStatusPending ||
		replacement.Generation != prepared.Head.Binding.Generation || replacement.ExpectedRevision != recovered.Revision {
		t.Fatalf("checkpoint-normalized wakes = %+v", wakes)
	}
	if _, err := fixture.svc.FinishAttempt(context.Background(), exec, AttemptOutcome{Kind: AttemptOutcomeKindProgress}); !errors.Is(err, ErrGenerationStale) {
		t.Fatalf("old attempt callback error = %v, want generation_stale", err)
	}
}

func TestCheckpointSameRunRestartUsesWorkingHead(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	createWakeTask(t, fixture, "checkpoint-before-restart", "actor", 200, 400)
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-working", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "save-working", true); err != nil {
		t.Fatal(err)
	}
	fixture.head = prepared.Head
	fixture.exec.Binding = prepared.Head.Binding
	postSave := createWakeTask(t, fixture, "checkpoint-working-head", "actor-b", 250, 400)

	restarted := deterministicRestartService(fixture.store)
	head, err := restarted.ActivateWorld(context.Background(), fixture.world, "run-a", fixture.clock, prepared.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if head != prepared.Head {
		t.Fatalf("same-run Head = %+v, want working %+v", head, prepared.Head)
	}
	if _, err := restarted.Read(context.Background(), postSave.Task.Owner, postSave.Task.ID); err != nil {
		t.Fatalf("same-run restart rewound post-save Task: %v", err)
	}
}

func TestWorldClockAndDeactivationLifecycle(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "world-lifecycle", "actor", 200, 400)
	advanced := Clock{ID: fixture.clock.ID, Tick: 150, Sequence: 2}
	head, err := fixture.svc.UpdateClock(context.Background(), fixture.head.Binding, advanced)
	if err != nil || head.Clock != advanced {
		t.Fatalf("UpdateClock = (%+v, %v)", head, err)
	}
	if again, err := fixture.svc.UpdateClock(context.Background(), fixture.head.Binding, advanced); err != nil || again != head {
		t.Fatalf("UpdateClock retry = (%+v, %v)", again, err)
	}
	if _, err := fixture.svc.UpdateClock(context.Background(), fixture.head.Binding, fixture.clock); !errors.Is(err, ErrClockRewound) {
		t.Fatalf("rewound UpdateClock error = %v", err)
	}
	if _, err := fixture.svc.UpdateClock(context.Background(), fixture.head.Binding, Clock{ID: advanced.ID, Tick: 151, Sequence: advanced.Sequence}); !errors.Is(err, ErrClockMismatch) {
		t.Fatalf("same-sequence changed UpdateClock error = %v", err)
	}

	taskBefore := rawTaskJSON(t, fixture.store, created.Task)
	wakeBefore := rawOnlyWakeJSON(t, fixture.store, created.Task)
	if err := fixture.svc.DeactivateWorld(context.Background(), fixture.head.Binding, "connection closed"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.DeactivateWorld(context.Background(), fixture.head.Binding, "connection closed"); err != nil {
		t.Fatalf("DeactivateWorld retry error = %v", err)
	}
	if !bytes.Equal(rawTaskJSON(t, fixture.store, created.Task), taskBefore) ||
		!bytes.Equal(rawOnlyWakeJSON(t, fixture.store, created.Task), wakeBefore) {
		t.Fatal("DeactivateWorld changed task working set")
	}
	fixture.exec.Clock = advanced
	if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrWorldNotReady) {
		t.Fatalf("Create after deactivation error = %v, want world_not_ready", err)
	}

	restarted := deterministicRestartService(fixture.store)
	reactivated, err := restarted.ActivateWorld(
		context.Background(), fixture.world, fixture.head.Binding.RunID, advanced,
		CheckpointRef{Status: checkpointStatusAbsent, World: fixture.world},
	)
	if err != nil {
		t.Fatalf("ActivateWorld after deactivation error = %v", err)
	}
	if reactivated.Status != worldHeadStatusReady || reactivated.Reason != "" ||
		reactivated.Binding.Generation != fixture.head.Binding.Generation+1 {
		t.Fatalf("reactivated Head = %+v", reactivated)
	}
	wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
	if len(wakes) != 1 || wakes[0].Status != wakeStatusPending || wakes[0].Generation != reactivated.Binding.Generation {
		t.Fatalf("reactivated wakes = %+v", wakes)
	}
	if _, err := fixture.svc.Create(context.Background(), fixture.exec, fixture.spec, Admission{}); !errors.Is(err, ErrGenerationStale) {
		t.Fatalf("pre-deactivation generation error = %v, want generation_stale", err)
	}
}

func TestCheckpointSameTickSnapshotsSelectExactWorkingSet(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	first := createWakeTask(t, fixture, "same-tick-first", "actor-a", 200, 400)
	c1, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-same-tick-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), c1.Head.Binding, c1.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}
	fixture.head = c1.Head
	fixture.exec.Binding = c1.Head.Binding
	second := createWakeTask(t, fixture, "same-tick-second", "actor-b", 250, 400)
	c2, err := fixture.svc.PrepareCheckpoint(context.Background(), c1.Head.Binding, fixture.clock, "save-same-tick-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), c2.Head.Binding, c2.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-c1", fixture.clock, c1.Reference); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.Read(context.Background(), first.Task.Owner, first.Task.ID); err != nil {
		t.Fatalf("C1 first Task: %v", err)
	}
	if _, err := fixture.svc.Read(context.Background(), second.Task.Owner, second.Task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("C1 second Task error = %v, want task_not_found", err)
	}

	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-c2", fixture.clock, c2.Reference); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.Read(context.Background(), first.Task.Owner, first.Task.ID); err != nil {
		t.Fatalf("C2 first Task: %v", err)
	}
	if _, err := fixture.svc.Read(context.Background(), second.Task.Owner, second.Task.ID); err != nil {
		t.Fatalf("C2 second Task: %v", err)
	}
}

func TestCheckpointPrepareFailuresRollbackWorkingSet(t *testing.T) {
	for _, stage := range []string{"working_set_fenced", "snapshot_inserted", "checkpoint_prepared"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			createWakeTask(t, fixture, "rollback-"+stage, "actor", 200, 400)
			before := checkpointWorldStateForTest(t, fixture.store, fixture.world)
			fixture.store.testAfterCheckpointStage = func(_ context.Context, got string) error {
				if got == stage {
					return errors.New("injected checkpoint failure")
				}
				return nil
			}
			_, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-rollback", nil)
			fixture.store.testAfterCheckpointStage = nil
			if err == nil {
				t.Fatal("PrepareCheckpoint fault returned nil")
			}
			after := checkpointWorldStateForTest(t, fixture.store, fixture.world)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("checkpoint failure changed working state\nbefore=%+v\nafter=%+v", before, after)
			}
			if got := totalRows(t, fixture.store.db, "task_checkpoints"); got != 0 {
				t.Fatalf("checkpoint count = %d, want 0", got)
			}
		})
	}

	t.Run("final evidence", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{})
		created := createWakeTask(t, fixture, "rollback-evidence", "actor", 200, 400)
		before := checkpointWorldStateForTest(t, fixture.store, fixture.world)
		fixture.store.testAfterNoRevisionStage = func(_ context.Context, stage string) error {
			if stage == noRevisionStageEvidenceMerged {
				return errors.New("injected evidence failure")
			}
			return nil
		}
		evidence := Evidence{
			FactID: "fact-rollback", TaskID: created.Task.ID, Binding: fixture.head.Binding,
			StartRevision: created.Task.Revision, OccurredAt: fixture.clock.Tick, Kind: EvidenceKindProgress,
			Source: SourceRef{Kind: SourceKindEnvironment},
		}
		_, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-evidence-rollback", []Evidence{evidence})
		fixture.store.testAfterNoRevisionStage = nil
		if err == nil {
			t.Fatal("PrepareCheckpoint evidence fault returned nil")
		}
		if after := checkpointWorldStateForTest(t, fixture.store, fixture.world); !reflect.DeepEqual(after, before) {
			t.Fatal("evidence failure changed working state")
		}
	})

	t.Run("snapshot capacity", func(t *testing.T) {
		fixture := newCreateFixture(t, StoreOptions{MaxSnapshotBytes: 1})
		createWakeTask(t, fixture, "snapshot-capacity", "actor", 200, 400)
		before := checkpointWorldStateForTest(t, fixture.store, fixture.world)
		if _, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-too-large", nil); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("oversized snapshot error = %v, want invalid_task_spec", err)
		}
		if after := checkpointWorldStateForTest(t, fixture.store, fixture.world); !reflect.DeepEqual(after, before) {
			t.Fatal("oversized snapshot changed working state")
		}
	})
}

func TestCheckpointRestoreFailureRollsBackAndPauses(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	createWakeTask(t, fixture, "restore-rollback-before", "actor-a", 200, 400)
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-restore-rollback", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, prepared.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}
	fixture.head = prepared.Head
	fixture.exec.Binding = prepared.Head.Binding
	postSave := createWakeTask(t, fixture, "restore-rollback-after", "actor-b", 250, 400)
	tasksBefore, wakesBefore := checkpointTaskWakeStateForTest(t, fixture.store, fixture.world)

	fixture.store.testAfterCheckpointStage = func(_ context.Context, stage string) error {
		if stage == "restore_task_inserted" {
			return errors.New("injected restore failure")
		}
		return nil
	}
	_, err = fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-restore-fault", fixture.clock, prepared.Reference)
	fixture.store.testAfterCheckpointStage = nil
	if err == nil {
		t.Fatal("ActivateWorld restore fault returned nil")
	}
	tasksAfter, wakesAfter := checkpointTaskWakeStateForTest(t, fixture.store, fixture.world)
	if !reflect.DeepEqual(tasksAfter, tasksBefore) || !reflect.DeepEqual(wakesAfter, wakesBefore) {
		t.Fatal("failed restore partially replaced working set")
	}
	if _, err := fixture.svc.Read(context.Background(), postSave.Task.Owner, postSave.Task.ID); err != nil {
		t.Fatalf("failed restore lost post-save Task: %v", err)
	}
	row, err := fixture.store.loadWorldHead(context.Background(), fixture.world)
	if err != nil || row.Head.Status != worldHeadStatusPaused {
		t.Fatalf("failed restore Head = (%+v, %v), want paused", row, err)
	}
}

func TestCheckpointInvalidNewRunPreservesTasksAndPauses(t *testing.T) {
	tests := []struct {
		name    string
		makeRef func(Prepared) CheckpointRef
		want    error
	}{
		{name: "bad checksum", makeRef: func(prepared Prepared) CheckpointRef {
			ref := prepared.Reference
			ref.Checksum = "f" + ref.Checksum[1:]
			if ref.Checksum == prepared.Reference.Checksum {
				ref.Checksum = "e" + ref.Checksum[1:]
			}
			return ref
		}, want: ErrCheckpointInvalid},
		{name: "missing", makeRef: func(prepared Prepared) CheckpointRef {
			ref := prepared.Reference
			ref.ID = "checkpoint-missing"
			return ref
		}, want: ErrCheckpointMissing},
		{name: "wrong world", makeRef: func(prepared Prepared) CheckpointRef {
			ref := prepared.Reference
			ref.World.WorldID = "other-world"
			return ref
		}, want: ErrCheckpointInvalid},
		{name: "unconfirmed", makeRef: func(prepared Prepared) CheckpointRef {
			return CheckpointRef{
				Status: checkpointStatusUnconfirmed, SchemaVersion: checkpointSchemaVersion,
				World: prepared.Reference.World, Reason: "runtime unavailable",
			}
		}, want: ErrCheckpointUnconfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created := createWakeTask(t, fixture, "invalid-ref-"+tt.name, "actor", 200, 400)
			prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-invalid-ref", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, prepared.SaveRequestID, true); err != nil {
				t.Fatal(err)
			}
			beforeTasks, beforeWakes := checkpointTaskWakeStateForTest(t, fixture.store, fixture.world)
			if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-invalid-ref", fixture.clock, tt.makeRef(prepared)); !errors.Is(err, tt.want) {
				t.Fatalf("ActivateWorld error = %v, want %v", err, tt.want)
			}
			afterTasks, afterWakes := checkpointTaskWakeStateForTest(t, fixture.store, fixture.world)
			if !reflect.DeepEqual(afterTasks, beforeTasks) || !reflect.DeepEqual(afterWakes, beforeWakes) {
				t.Fatal("invalid reference changed Task or wake rows")
			}
			row, err := fixture.store.loadWorldHead(context.Background(), fixture.world)
			if err != nil || row.Head.Status != worldHeadStatusPaused || row.Head.Reason != tt.want.Error() {
				t.Fatalf("paused Head = (%+v, %v)", row, err)
			}
			if _, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID); err != nil {
				t.Fatalf("invalid reference lost Task: %v", err)
			}
		})
	}
}

func TestCheckpointRebindsClaimedAndEnqueuedWakes(t *testing.T) {
	for _, status := range []string{wakeStatusClaimed, wakeStatusEnqueued} {
		t.Run(status, func(t *testing.T) {
			fixture := newCreateFixture(t, StoreOptions{})
			created := createWakeTask(t, fixture, "checkpoint-"+status, "actor", 200, 400)
			clock := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
			if _, err := fixture.svc.UpdateClock(context.Background(), fixture.head.Binding, clock); err != nil {
				t.Fatal(err)
			}
			claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
			if err != nil || len(claimed) != 1 {
				t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
			}
			if status == wakeStatusEnqueued {
				if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, clock, "save-"+status, nil)
			if err != nil {
				t.Fatal(err)
			}
			wakes := loadTaskWakes(t, fixture.store, created.Task.Owner, created.Task.ID)
			if len(wakes) != 1 || wakes[0].Status != wakeStatusPending || wakes[0].ClaimID != "" ||
				wakes[0].ClaimedBy != "" || wakes[0].Attempt != 1 || wakes[0].Generation != prepared.Head.Binding.Generation {
				t.Fatalf("checkpoint-rebound Wake = %+v", wakes)
			}
		})
	}
}

func TestCheckpointRestoresCreateAndIntentIdempotency(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	createExec, spec := fixture.exec, fixture.spec
	createExec.Source.EventID, createExec.Source.TurnID, createExec.Source.CallID = "event-idempotent", "turn-idempotent", "call-idempotent"
	spec.Source = createExec.Source
	spec.EquivalenceKey = "eq-idempotent"
	created, err := fixture.svc.Create(context.Background(), createExec, spec, Admission{})
	if err != nil {
		t.Fatal(err)
	}
	clock := Clock{ID: fixture.clock.ID, Tick: spec.WakeAt, Sequence: 2}
	if _, err := fixture.svc.UpdateClock(context.Background(), fixture.head.Binding, clock); err != nil {
		t.Fatal(err)
	}
	claimed, err := fixture.svc.ClaimDue(context.Background(), fixture.head.Binding, clock, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
	}
	if err := fixture.svc.MarkEnqueued(context.Background(), fixture.head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
		t.Fatal(err)
	}
	intentExec, _, err := fixture.svc.BeginWake(context.Background(), fixture.head.Binding, claimed[0].ID, claimed[0].ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	intentExec.Source.EventID, intentExec.Source.TurnID, intentExec.Source.CallID = "event-wait", "turn-wait", "call-wait"
	next := int64(250)
	intent := Intent{Kind: "wait", NextWakeAt: &next, ProgressNote: "still working"}
	waiting, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, clock, "save-idempotency", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, prepared.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}
	restoredHead, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-idempotency", clock, prepared.Reference)
	if err != nil {
		t.Fatal(err)
	}

	createExec.Binding, createExec.Clock = restoredHead.Binding, clock
	createAgain, err := fixture.svc.Create(context.Background(), createExec, spec, Admission{})
	if err != nil || !reflect.DeepEqual(createAgain, created) {
		t.Fatalf("restored Create retry = (%+v, %v), want %+v", createAgain, err, created)
	}
	intentExec.Binding, intentExec.Clock = restoredHead.Binding, clock
	waitingAgain, err := fixture.svc.ApplyIntent(context.Background(), intentExec, intent)
	if err != nil || !reflect.DeepEqual(waitingAgain, waiting) {
		t.Fatalf("restored ApplyIntent retry = (%+v, %v), want %+v", waitingAgain, err, waiting)
	}
}

func TestCheckpointRestoresImmutableTerminalResult(t *testing.T) {
	fixture, created, _ := newIntentFixture(t, StoreOptions{})
	evidence := taskEvidence(fixture.head.Binding, created.Task, "fact-checkpoint-terminal", EvidenceKindSatisfied)
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("AdmitEvidence = (%v, %v)", added, err)
	}
	terminal, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, created.Task.Revision))
	if err != nil || terminal.Task.Result == nil {
		t.Fatalf("Reconcile = (%+v, %v)", terminal, err)
	}
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, prepared.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-terminal", fixture.clock, prepared.Reference); err != nil {
		t.Fatal(err)
	}
	restored, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || !reflect.DeepEqual(restored, terminal.Task) {
		t.Fatalf("restored terminal Task = (%+v, %v), want %+v", restored, err, terminal.Task)
	}
}

func TestCheckpointRestoreSurvivesStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	fixture := newCreateFixture(t, StoreOptions{Path: path})
	created := createWakeTask(t, fixture, "checkpoint-reopen", "actor", 200, 400)
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-reopen", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, prepared.SaveRequestID, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTaskTestStore(t, StoreOptions{Path: path})
	svc := deterministicTaskService(reopened)
	if _, err := svc.ActivateWorld(context.Background(), fixture.world, "run-reopened", fixture.clock, prepared.Reference); err != nil {
		t.Fatal(err)
	}
	if restored, err := svc.Read(context.Background(), created.Task.Owner, created.Task.ID); err != nil || restored.ID != created.Task.ID {
		t.Fatalf("reopened restored Task = (%+v, %v)", restored, err)
	}
}

func TestCheckpointC1AndC2RestoreExactTaskVersions(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "checkpoint-versions", "actor", 200, 400)
	c1, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-version-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), c1.Head.Binding, c1.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}

	clock200 := Clock{ID: fixture.clock.ID, Tick: 200, Sequence: 2}
	if _, err := fixture.svc.UpdateClock(context.Background(), c1.Head.Binding, clock200); err != nil {
		t.Fatal(err)
	}
	claimed, err := fixture.svc.ClaimDue(context.Background(), c1.Head.Binding, clock200, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%+v, %v)", claimed, err)
	}
	if err := fixture.svc.MarkEnqueued(context.Background(), c1.Head.Binding, claimed[0].ID, claimed[0].ClaimID); err != nil {
		t.Fatal(err)
	}
	exec, _, err := fixture.svc.BeginWake(context.Background(), c1.Head.Binding, claimed[0].ID, claimed[0].ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	exec.Source.EventID, exec.Source.TurnID, exec.Source.CallID = "event-version-wait", "turn-version-wait", "call-version-wait"
	next := int64(300)
	waiting, err := fixture.svc.ApplyIntent(context.Background(), exec, Intent{Kind: "wait", NextWakeAt: &next, ProgressNote: "arrived"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := fixture.svc.PrepareCheckpoint(context.Background(), c1.Head.Binding, clock200, "save-version-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), c2.Head.Binding, c2.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}

	clock300 := Clock{ID: fixture.clock.ID, Tick: 300, Sequence: 3}
	if _, err := fixture.svc.UpdateClock(context.Background(), c2.Head.Binding, clock300); err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{
		FactID: "fact-after-c2", TaskID: created.Task.ID, Binding: c2.Head.Binding,
		StartRevision: waiting.Revision, OccurredAt: clock300.Tick, Kind: EvidenceKindSatisfied,
		Source: SourceRef{Kind: SourceKindEnvironment, EventID: "event-after-c2", TurnID: "turn-after-c2", CallID: "call-after-c2"},
	}
	if added, err := fixture.svc.AdmitEvidence(context.Background(), c2.Head.Binding, evidence); err != nil || !added {
		t.Fatalf("AdmitEvidence = (%v, %v)", added, err)
	}
	reconcileExec := ExecutionContext{
		Owner: created.Task.Owner, Binding: c2.Head.Binding, Clock: clock300,
		Source: SourceRef{Kind: SourceKindInternal, EventID: "event-reconcile-after-c2", TurnID: "turn-reconcile-after-c2", CallID: "call-reconcile-after-c2"},
		TaskID: created.Task.ID, ExpectedRevision: waiting.Revision,
	}
	terminal, err := fixture.svc.Reconcile(context.Background(), reconcileExec)
	if err != nil || terminal.Task.State != StateSucceeded {
		t.Fatalf("post-C2 Reconcile = (%+v, %v)", terminal, err)
	}

	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-version-c1", fixture.clock, c1.Reference); err != nil {
		t.Fatal(err)
	}
	fromC1, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || fromC1.Revision != created.Task.Revision || fromC1.State != StateWaiting ||
		fromC1.NextWakeAt == nil || *fromC1.NextWakeAt != 200 || len(fromC1.Evidence) != 0 {
		t.Fatalf("C1 Task = (%+v, %v)", fromC1, err)
	}

	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-version-c2", clock200, c2.Reference); err != nil {
		t.Fatal(err)
	}
	fromC2, err := fixture.svc.Read(context.Background(), created.Task.Owner, created.Task.ID)
	if err != nil || fromC2.Revision != waiting.Revision || fromC2.State != StateWaiting ||
		fromC2.NextWakeAt == nil || *fromC2.NextWakeAt != next || !bytes.Equal(fromC2.Progress, waiting.Progress) ||
		len(fromC2.Evidence) != 0 || fromC2.Result != nil {
		t.Fatalf("C2 Task = (%+v, %v)", fromC2, err)
	}
}

func TestCheckpointFinalEvidenceBatchIsAtomic(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	created := createWakeTask(t, fixture, "evidence-batch", "actor", 200, 400)
	before := checkpointWorldStateForTest(t, fixture.store, fixture.world)
	sharedSource := SourceRef{Kind: SourceKindEnvironment, EventID: "event-shared", TurnID: "turn-shared", CallID: "call-shared"}
	first := Evidence{
		FactID: "fact-a", TaskID: created.Task.ID, Binding: fixture.head.Binding,
		StartRevision: created.Task.Revision, OccurredAt: fixture.clock.Tick, Kind: EvidenceKindProgress, Source: sharedSource,
	}
	second := first
	second.FactID = "fact-b"
	if _, err := fixture.svc.PrepareCheckpoint(
		context.Background(), fixture.head.Binding, fixture.clock, "save-evidence-batch", []Evidence{first, second},
	); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf("PrepareCheckpoint evidence conflict = %v, want evidence_conflict", err)
	}
	if after := checkpointWorldStateForTest(t, fixture.store, fixture.world); !reflect.DeepEqual(after, before) {
		t.Fatal("conflicting final Evidence left a partial merge")
	}
	if got := totalRows(t, fixture.store.db, "task_checkpoints"); got != 0 {
		t.Fatalf("checkpoint count = %d, want 0", got)
	}
}

func TestCheckpointCorruptionDoesNotFallBack(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	createWakeTask(t, fixture, "corrupt-before", "actor-a", 200, 400)
	prepared, err := fixture.svc.PrepareCheckpoint(context.Background(), fixture.head.Binding, fixture.clock, "save-corrupt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, prepared.SaveRequestID, true); err != nil {
		t.Fatal(err)
	}
	fixture.head = prepared.Head
	fixture.exec.Binding = prepared.Head.Binding
	postSave := createWakeTask(t, fixture, "corrupt-after", "actor-b", 250, 400)
	beforeTasks, beforeWakes := checkpointTaskWakeStateForTest(t, fixture.store, fixture.world)
	corrupt := append(checkpointBytesForTest(t, fixture.store, prepared.Reference.ID), ' ')
	if _, err := fixture.store.db.Exec(`UPDATE task_checkpoints SET snapshot_json = ? WHERE checkpoint_id = ?`, corrupt, prepared.Reference.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.ActivateWorld(context.Background(), fixture.world, "run-corrupt", fixture.clock, prepared.Reference); !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("corrupt restore error = %v, want checkpoint_invalid", err)
	}
	afterTasks, afterWakes := checkpointTaskWakeStateForTest(t, fixture.store, fixture.world)
	if !reflect.DeepEqual(afterTasks, beforeTasks) || !reflect.DeepEqual(afterWakes, beforeWakes) {
		t.Fatal("corrupt checkpoint changed working set or fell back")
	}
	if _, err := fixture.svc.Read(context.Background(), postSave.Task.Owner, postSave.Task.ID); err != nil {
		t.Fatalf("corrupt checkpoint lost post-save Task: %v", err)
	}
}

func TestCheckpointConcurrentExactPrepareConverges(t *testing.T) {
	fixture := newCreateFixture(t, StoreOptions{})
	createWakeTask(t, fixture, "concurrent-prepare", "actor", 200, 400)
	results := make([]Prepared, 2)
	errs := make([]error, 2)
	done := make(chan int, 2)
	for index := range results {
		go func(index int) {
			results[index], errs[index] = fixture.svc.PrepareCheckpoint(
				context.Background(), fixture.head.Binding, fixture.clock, "save-concurrent", nil,
			)
			done <- index
		}(index)
	}
	<-done
	<-done
	for index, err := range errs {
		if err != nil {
			t.Fatalf("PrepareCheckpoint(%d) error = %v", index, err)
		}
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("concurrent Prepared values differ: %+v / %+v", results[0], results[1])
	}
	if got := totalRows(t, fixture.store.db, "task_checkpoints"); got != 1 {
		t.Fatalf("checkpoint count = %d, want 1", got)
	}
}

type checkpointWorldState struct {
	Head  []byte
	Tasks [][]byte
	Wakes [][]byte
}

func checkpointWorldStateForTest(t *testing.T, store *SQLiteStore, world WorldKey) checkpointWorldState {
	t.Helper()
	var head []byte
	if err := store.db.QueryRow(`SELECT head_json FROM task_world_heads WHERE game_id = ? AND world_id = ?`, world.GameID, world.WorldID).Scan(&head); err != nil {
		t.Fatal(err)
	}
	tasks, wakes := checkpointTaskWakeStateForTest(t, store, world)
	return checkpointWorldState{Head: append([]byte(nil), head...), Tasks: tasks, Wakes: wakes}
}

func checkpointTaskWakeStateForTest(t *testing.T, store *SQLiteStore, world WorldKey) ([][]byte, [][]byte) {
	t.Helper()
	return checkpointRawRowsForTest(t, store, `SELECT record_json FROM tasks WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id`, world),
		checkpointRawRowsForTest(t, store, `SELECT wake_json FROM task_wakeups WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id, wake_id`, world)
}

func checkpointRawRowsForTest(t *testing.T, store *SQLiteStore, query string, world WorldKey) [][]byte {
	t.Helper()
	rows, err := store.db.Query(query, world.GameID, world.WorldID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make([][]byte, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		result = append(result, append([]byte(nil), raw...))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
