package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gameagent/console"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

func TestTaskConsoleReadsPersistedAppointments(t *testing.T) {
	f := newFixture(t, console.Assets())
	ctx := context.Background()
	store, err := task.OpenSQLiteStore(ctx, task.StoreOptions{Path: filepath.Join(f.root, agent.DefaultTaskDBPath)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := task.NewService(store)
	world := task.WorldKey{GameID: "stardew-valley", WorldID: "test-farm"}
	clock := task.Clock{ID: "generic.minutes.v1", Tick: 100, Sequence: 1}
	head, err := svc.ActivateWorld(ctx, world, "test-run", clock, task.CheckpointRef{Status: "absent", World: world})
	if err != nil {
		t.Fatal(err)
	}
	var first task.Record
	for i, instruction := range []string{"明天下午到湖边见面，抵达后再确认约定。", "在约定地点等待玩家，收到游戏反馈后记录结果。", "下次进入游戏后，继续确认之前的安排。", "取消这次会面，保留已记录的结果。"} {
		source := task.SourceRef{Kind: task.SourceKindInternal, EventID: fmt.Sprint("event-", i), TurnID: fmt.Sprint("turn-", i), CallID: fmt.Sprint("call-", i)}
		exec := task.ExecutionContext{Owner: session.AgentSessionKey{GameID: world.GameID, WorldID: world.WorldID, EntityID: fmt.Sprint("test-actor-", i+1)}, Binding: head.Binding, Clock: clock, Source: source}
		spec := task.TaskSpec{Instruction: instruction, ClockID: clock.ID, WakeAt: 200, DeadlineAt: 400, ResultContract: task.ResultContractAuthoritativeEvidence, EquivalenceKey: fmt.Sprint("test-", i), Source: source}
		created, err := svc.Create(ctx, exec, spec, task.Admission{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = created.Task
		}
		if i == 3 {
			exec.TaskID = created.Task.ID
			exec.ExpectedRevision = created.Task.Revision
			exec.Source.CallID = "cancel-call"
			if _, err = svc.ApplyIntent(ctx, exec, task.Intent{Kind: "cancel", Reason: "player_request"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	prepared, err := svc.PrepareCheckpoint(ctx, head.Binding, clock, "test-save", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.FinishCheckpoint(ctx, prepared.Head.Binding, "test-save", true); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.runtime.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.SelectGame(world.GameID); err != nil {
		t.Fatal(err)
	}
	cookie := f.session(t)
	q := url.Values{"instance_id": {f.runtime.Snapshot().InstanceID}, "game_id": {world.GameID}, "world_id": {world.WorldID}, "collection": {"active"}}
	response := f.do(t, http.MethodGet, "/api/tasks?"+q.Encode(), "", cookie)
	page := decodeBody[task.ConsolePage](t, response)
	if response.Code != 200 || len(page.Items) != 3 || page.Counts[task.StateCancelled] != 1 || page.World.Availability != "waiting_for_world" {
		t.Fatalf("persisted page: %d %+v", response.Code, page)
	}
	q.Del("collection")
	q.Set("entity_id", first.Owner.EntityID)
	q.Set("task_id", first.ID)
	response = f.do(t, http.MethodGet, "/api/tasks/detail?"+q.Encode(), "", cookie)
	detail := decodeBody[task.ConsolePage](t, response)
	if response.Code != 200 || len(detail.Items) != 1 || detail.Items[0].Facts == nil || detail.Items[0].Instruction != first.Spec.Instruction {
		t.Fatalf("persisted detail: %d %+v", response.Code, detail)
	}

	// Optional local browser QA uses this real HTTP server and the isolated test database.
	if output := os.Getenv("WIA_TASK_BROWSER_URL_FILE"); output != "" {
		go f.server.Serve()
		if err = os.WriteFile(output, []byte(f.server.BrowserURL()), 0600); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(20 * time.Minute); time.Now().Before(deadline); {
			if _, err = os.Stat(output + ".stop"); err == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
}
