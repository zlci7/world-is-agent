package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConsoleWorldDistinguishesOfflineRecoveryAndExpiredSaveBarrier(t *testing.T) {
	row := worldHeadRow{Head: Head{Status: worldHeadStatusPaused, Reason: "connection_closed"}}
	if got := consoleWorld(row, time.Second); got.Availability != "waiting_for_world" || got.ReasonCode != "" {
		t.Fatalf("ordinary disconnect: %+v", got)
	}
	row.Head.Reason = string(CodeClockRewound)
	if got := consoleWorld(row, time.Second); got.Availability != "recovery_blocked" || got.ReasonCode != string(CodeClockRewound) {
		t.Fatalf("clock rewind: %+v", got)
	}
	row.Head.Reason = ""
	row.BarrierStatus = checkpointBarrierPrepared
	row.BarrierPreparedAtUnixMS = time.Now().Add(-2 * time.Second).UnixMilli()
	if got := consoleWorld(row, time.Second); got.SaveBarrierActive || got.Availability != "waiting_for_world" {
		t.Fatalf("expired save: %+v", got)
	}
	row.BarrierPreparedAtUnixMS = time.Now().UnixMilli()
	if got := consoleWorld(row, time.Second); !got.SaveBarrierActive || got.Availability != "waiting_for_world" {
		t.Fatalf("save requires live corroboration: %+v", got)
	}
}

func TestConsoleProjectsProtoJSONGameTimeWithoutPrecisionLoss(t *testing.T) {
	for _, raw := range []string{`{"tick":"9007199254740993","day":2}`, `{"tick":9007199254740993,"day":2}`} {
		record := Record{Result: &Result{Source: SourceRef{GameTime: json.RawMessage(raw)}}}
		data, err := json.Marshal(projectConsoleTask(record, true))
		if err != nil || !strings.Contains(string(data), `"tick":"9007199254740993"`) || !strings.Contains(string(data), `"day":2`) {
			t.Fatalf("game time %s: %s %v", raw, data, err)
		}
	}
}

func consoleTestQuery(f createFixture) ConsoleQuery {
	return ConsoleQuery{InstanceID: "instance-test", GameID: f.world.GameID, WorldID: f.world.WorldID, Collection: "active", Limit: 1}
}

func TestConsoleSnapshotIsReadOnlyBoundedAndScoped(t *testing.T) {
	f := newCreateFixture(t, StoreOptions{})
	f.spec.Contract = json.RawMessage(`{"private":"DO_NOT_EXPOSE_CONTRACT"}`)
	a := createWakeTask(t, f, "console-a", "actor-a", 200, 400)
	createWakeTask(t, f, "console-b", "actor-b", 200, 400)
	c := createWakeTask(t, f, "console-c", "actor-c", 200, 400)
	setTaskStateForCreateTest(t, f.store, c.Task, StateCancelled, 2)
	beforeTask, beforeWake, beforeHistory := snapshotIntentRows(t, f.store, a.Task.Owner, a.Task.ID)
	q := consoleTestQuery(f)
	first, err := f.store.ConsoleTasks(context.Background(), q, false)
	if err != nil || len(first.Items) != 1 || first.Items[0].EntityID != "actor-a" || first.NextCursor == "" || first.Counts[StateWaiting] != 2 || first.Counts[StateCancelled] != 1 {
		t.Fatalf("page=%+v err=%v", first, err)
	}
	q.Cursor = first.NextCursor
	second, err := f.store.ConsoleTasks(context.Background(), q, false)
	if err != nil || len(second.Items) != 1 || second.Items[0].EntityID != "actor-b" || second.NextCursor != "" {
		t.Fatalf("next=%+v err=%v", second, err)
	}
	q.State = "paused"
	if _, err := f.store.ConsoleTasks(context.Background(), q, false); !errors.Is(err, ErrConsoleQuery) {
		t.Fatalf("cursor accepted different filter: %v", err)
	}
	q = ConsoleQuery{InstanceID: q.InstanceID, GameID: q.GameID, WorldID: q.WorldID, EntityID: a.Task.Owner.EntityID, TaskID: a.Task.ID}
	detail, err := f.store.ConsoleTasks(context.Background(), q, true)
	if err != nil || len(detail.Items) != 1 || detail.Items[0].Facts == nil || detail.Items[0].Revision != a.Task.Revision {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	data, _ := json.Marshal(detail)
	if strings.Contains(string(data), "DO_NOT_EXPOSE") || strings.Contains(string(data), "record_json") || strings.Contains(string(data), "\"contract\"") {
		t.Fatalf("unsafe DTO: %s", data)
	}
	q.EntityID = "different-owner"
	if _, err := f.store.ConsoleTasks(context.Background(), q, true); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("wrong owner: %v", err)
	}
	assertTaskSnapshotEqual(t, f.store, a.Task, beforeTask, beforeWake, beforeHistory, "console reads")
	worlds, err := f.store.ConsoleWorlds(context.Background(), ConsoleQuery{InstanceID: "instance-test", GameID: f.world.GameID})
	if err != nil || len(worlds.Worlds) != 1 || worlds.Worlds[0].Availability != "waiting_for_world" {
		t.Fatalf("worlds=%+v err=%v", worlds, err)
	}
}

func TestConsoleCursorExpiresAfterCheckpointAndRejectsInvalidInput(t *testing.T) {
	f := newCreateFixture(t, StoreOptions{})
	createWakeTask(t, f, "cursor-a", "a", 200, 400)
	createWakeTask(t, f, "cursor-b", "b", 200, 400)
	q := consoleTestQuery(f)
	page, err := f.store.ConsoleTasks(context.Background(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.svc.PrepareCheckpoint(context.Background(), f.head.Binding, f.clock, "console-save", nil)
	if err != nil {
		t.Fatal(err)
	}
	q.Cursor = page.NextCursor
	if _, err := f.store.ConsoleTasks(context.Background(), q, false); !errors.Is(err, ErrConsoleSnapshotChanged) {
		t.Fatalf("old working set cursor: %v", err)
	}
	worlds, err := f.store.ConsoleWorlds(context.Background(), ConsoleQuery{InstanceID: "instance-test", GameID: f.world.GameID})
	if err != nil || worlds.Worlds[0].Availability != "waiting_for_world" {
		t.Fatalf("prepared=%+v worlds=%+v err=%v", prepared, worlds, err)
	}
	for _, mutate := range []func(*ConsoleQuery){func(q *ConsoleQuery) { q.Limit = 51 }, func(q *ConsoleQuery) { q.Collection = "all" }, func(q *ConsoleQuery) { q.State = "failed" }, func(q *ConsoleQuery) { q.Cursor = "not-json" }, func(q *ConsoleQuery) { q.WorldID = "" }} {
		q := consoleTestQuery(f)
		mutate(&q)
		if _, err := f.store.ConsoleTasks(context.Background(), q, false); !errors.Is(err, ErrConsoleQuery) {
			t.Fatalf("invalid %+v: %v", q, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.store.ConsoleTasks(ctx, consoleTestQuery(f), false); err == nil {
		t.Fatal("cancelled query succeeded")
	}
}

func TestConsoleReadBudgetRejectsOversizedRecordBeforeDecode(t *testing.T) {
	f := newCreateFixture(t, StoreOptions{})
	createWakeTask(t, f, "budget", "a", 200, 400)
	if _, err := f.store.db.Exec(`UPDATE tasks SET record_json=?`, strings.Repeat("x", consoleReadBytes+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ConsoleTasks(context.Background(), consoleTestQuery(f), false); !errors.Is(err, ErrConsoleBudget) {
		t.Fatalf("budget: %v", err)
	}
}

func TestConsoleAndTaskResultsUseRestoredWorkingSet(t *testing.T) {
	f := newCreateFixture(t, StoreOptions{})
	ctx := context.Background()
	created := createWakeTask(t, f, "rollback", "original", 200, 400)
	prepared, err := f.svc.PrepareCheckpoint(ctx, f.head.Binding, f.clock, "before-result", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.FinishCheckpoint(ctx, prepared.Head.Binding, "before-result", true); err != nil {
		t.Fatal(err)
	}
	setTaskStateForCreateTest(t, f.store, created.Task, StateSucceeded, 2)
	q := consoleTestQuery(f)
	q.Collection = "terminal"
	page, err := f.store.ConsoleTasks(ctx, q, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].Result == nil {
		t.Fatalf("before rollback: %+v %v", page, err)
	}
	if _, err = f.svc.ActivateWorld(ctx, f.world, "restored-before-result", f.clock, prepared.Reference); err != nil {
		t.Fatal(err)
	}
	page, err = f.store.ConsoleTasks(ctx, q, false)
	if err != nil || len(page.Items) != 0 || page.Counts[StateSucceeded] != 0 {
		t.Fatalf("future result remained visible: %+v %v", page, err)
	}
	q.Collection = "active"
	page, err = f.store.ConsoleTasks(ctx, q, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != StateWaiting || page.Items[0].Result != nil {
		t.Fatalf("restored task: %+v %v", page, err)
	}
	results, err := f.svc.ListRecentResults(ctx, created.Task.Owner, 3)
	if err != nil || len(results) != 0 {
		t.Fatalf("task context still sees future result: %+v %v", results, err)
	}
}

func TestConsoleRecoversCommittedWorkingSetAcrossProcessExitAndExactRollback(t *testing.T) {
	const envName = "WIA_TASK_CONSOLE_PROCESS"
	if input := os.Getenv(envName); input != "" {
		var args struct {
			Path  string
			World WorldKey
			Clock Clock
			Run   string
		}
		data, err := os.ReadFile(input)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		store, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: args.Path})
		if err != nil {
			t.Fatal(err)
		}
		svc := NewService(store)
		head, err := svc.ActivateWorld(context.Background(), args.World, args.Run, args.Clock, CheckpointRef{Status: "absent", World: args.World})
		if err != nil {
			t.Fatal(err)
		}
		owner := testOwner()
		owner.GameID, owner.WorldID = args.World.GameID, args.World.WorldID
		execution, spec := createInputs(head, args.Clock, owner, "child-event", "child-turn", "child-call", "child-equivalence")
		if _, err := svc.Create(context.Background(), execution, spec, Admission{}); err != nil {
			t.Fatal(err)
		}
		// Exiting without Close models loss of the Runtime process after a durable commit.
		os.Exit(0)
	}
	f := newCreateFixture(t, StoreOptions{})
	initial := createWakeTask(t, f, "before-save", "original", 200, 400)
	prepared, err := f.svc.PrepareCheckpoint(context.Background(), f.head.Binding, f.clock, "process-save", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.FinishCheckpoint(context.Background(), prepared.Head.Binding, "process-save", true); err != nil {
		t.Fatal(err)
	}
	path := f.store.path
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "process.json")
	data, _ := json.Marshal(struct {
		Path  string
		World WorldKey
		Clock Clock
		Run   string
	}{path, f.world, f.clock, f.head.Binding.RunID})
	if err = os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConsoleRecoversCommittedWorkingSetAcrossProcessExitAndExactRollback$")
	cmd.Env = append(os.Environ(), envName+"="+input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	store := openTaskTestStore(t, StoreOptions{Path: path})
	svc := NewService(store)
	head, err := svc.ActivateWorld(context.Background(), f.world, f.head.Binding.RunID, f.clock, CheckpointRef{Status: "absent", World: f.world})
	if err != nil {
		t.Fatal(err)
	}
	q := consoleTestQuery(f)
	q.Limit = 50
	page, err := store.ConsoleTasks(context.Background(), q, false)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("working head: %+v %v", page, err)
	}
	if head.Binding.Generation <= prepared.Head.Binding.Generation {
		t.Fatal("restart did not fence old generation")
	}
	if _, err = svc.ActivateWorld(context.Background(), f.world, "reloaded-run", f.clock, prepared.Reference); err != nil {
		t.Fatal(err)
	}
	page, err = store.ConsoleTasks(context.Background(), q, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].TaskID != initial.Task.ID {
		t.Fatalf("exact rollback: %+v %v", page, err)
	}
	got, err := svc.Read(context.Background(), initial.Task.Owner, initial.Task.ID)
	if err != nil || !reflect.DeepEqual(got.Spec, initial.Task.Spec) {
		t.Fatalf("saved task changed: %+v %v", got, err)
	}
}
