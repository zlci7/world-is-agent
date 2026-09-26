package storyapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gameagent/runtime/internal/model"
)

type scriptedGenerator struct {
	mu       sync.Mutex
	requests []string
	fail     bool
	delay    time.Duration
}

func (g *scriptedGenerator) GenerateText(ctx context.Context, req model.TextRequest) (model.TextResponse, error) {
	if g.delay > 0 {
		timer := time.NewTimer(g.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return model.TextResponse{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	g.mu.Lock()
	g.requests = append(g.requests, req.Input)
	fail := g.fail
	g.mu.Unlock()
	if fail {
		return model.TextResponse{Text: `{"unknown":true}`}, nil
	}
	if strings.Contains(req.System, "重要 NPC") {
		if strings.Contains(req.Input, "你的身份：沈岚") {
			return model.TextResponse{Text: `{"speech":"沈岚压低声音说：先别惊动客人。","action_intent":"保护柜台下的东西","silent":false,"memory":"玩家主动向我提供了消息。"}`}, nil
		}
		if strings.Contains(req.Input, "新刺激") {
			return model.TextResponse{Text: `{"speech":"铁杉抬眼看向柜台，手按住了刀柄。","action_intent":"观察异常","silent":false,"memory":"沈岚的公开回应让我提高警惕。"}`}, nil
		}
		return model.TextResponse{Text: `{"speech":"","action_intent":"保持观察","silent":true,"memory":"我看见有人在客栈里行动。"}`}, nil
	}
	return model.TextResponse{Text: `{"narrative":"雨声敲打着屋檐。沈岚的回答让柜台边的空气紧了一瞬，铁杉也把视线从河面收了回来。","time_minutes":0,"scene":"旧渡口客栈"}`}, nil
}

func newTestApp(t *testing.T, generator model.TextGenerator) *App {
	t.Helper()
	app, err := Open(context.Background(), Options{DataRoot: t.TempDir(), UserID: LocalUserID, Generator: generator})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func waitRun(t *testing.T, app *App, worldID, runID string) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := app.Run(context.Background(), worldID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "accepted" && run.Status != "running" {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return Run{}
}

func TestCoreTurnKeepsPrivatePerceptionAndCommitsAtomically(t *testing.T) {
	generator := &scriptedGenerator{}
	app := newTestApp(t, generator)
	world, err := app.CreateWorld(context.Background(), "调查 A", "guided", "旅人", "寻找失踪信使", true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "req-1", Input: "我私下对老板说：今晚有人会来搜查。"})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, app, world.WorldID, run.RunID)
	if finished.Status != "completed" {
		t.Fatalf("status = %s, error=%s", finished.Status, finished.Error)
	}
	snapshot, err := app.ReadWorld(context.Background(), world.WorldID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.TurnSeq != 1 || len(snapshot.Messages) != 3 {
		t.Fatalf("summary/messages = %+v/%d", snapshot.Summary, len(snapshot.Messages))
	}
	if len(snapshot.Bystanders) != 10 {
		t.Fatalf("bystanders = %v", snapshot.Bystanders)
	}
	mercenary := snapshot.Perceptions["npc:mercenary"]
	if len(mercenary) == 0 {
		t.Fatal("mercenary has no perception")
	}
	for _, perception := range mercenary {
		if strings.Contains(perception.Content, "今晚有人会来搜查") {
			t.Fatalf("private text leaked to mercenary: %+v", perception)
		}
	}
	for _, memory := range snapshot.Memories["npc:mercenary"] {
		if strings.Contains(memory.Content, "今晚有人会来搜查") {
			t.Fatalf("private text leaked to mercenary memory: %+v", memory)
		}
	}
	innkeeper := snapshot.Memories["npc:innkeeper"]
	if len(innkeeper) == 0 || !strings.Contains(innkeeper[len(innkeeper)-1].Content, "今晚有人会来搜查") {
		t.Fatalf("innkeeper memory missing private message: %+v", innkeeper)
	}
	if len(snapshot.Events) < 3 {
		t.Fatalf("events = %+v", snapshot.Events)
	}
	var followUp bool
	generator.mu.Lock()
	requests := append([]string(nil), generator.requests...)
	generator.mu.Unlock()
	for _, request := range requests {
		if strings.Contains(request, "你的身份：铁杉") && strings.Contains(request, "今晚有人会来搜查") {
			t.Fatalf("private text leaked to mercenary prompt: %s", request)
		}
		if strings.Contains(request, "剧本：") && strings.Contains(request, "今晚有人会来搜查") {
			t.Fatalf("private text leaked to host prompt: %s", request)
		}
	}
	for _, event := range snapshot.Events {
		if event.Stage == 2 && event.ActorID == "npc:mercenary" && event.EventType == "npc_dialogue" {
			followUp = true
		}
	}
	if !followUp {
		t.Fatalf("stage-two follow-up missing: %+v", snapshot.Events)
	}
}

func TestWaitAdvancesWorldClockOnlyAfterCommit(t *testing.T) {
	app := newTestApp(t, &scriptedGenerator{})
	world, err := app.CreateWorld(context.Background(), "等待测试", "guided", "旅人", "", true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "wait-1", Input: "我等待一会儿。"})
	if err != nil {
		t.Fatal(err)
	}
	if finished := waitRun(t, app, world.WorldID, run.RunID); finished.Status != "completed" {
		t.Fatalf("run = %+v", finished)
	}
	snapshot, err := app.ReadWorld(context.Background(), world.WorldID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Clock != "第 1 日 19:30" {
		t.Fatalf("clock = %q", snapshot.Summary.Clock)
	}
}

func TestActivationRequestIsIdempotentAcrossVersionChanges(t *testing.T) {
	app := newTestApp(t, &scriptedGenerator{})
	first, err := app.CreateWorld(context.Background(), "存档一", "guided", "旅人", "", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.CreateWorld(context.Background(), "存档二", "open", "旅人", "", false)
	if err != nil {
		t.Fatal(err)
	}
	status, err := app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	activated, err := app.ActivateWorld(context.Background(), second.WorldID, status.ActiveRevision, "activate-once")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ActivateWorld(context.Background(), first.WorldID, activated.ActiveRevision, "switch-other"); err != nil {
		t.Fatal(err)
	}
	replayed, err := app.ActivateWorld(context.Background(), second.WorldID, status.ActiveRevision, "activate-once")
	if err != nil {
		t.Fatal(err)
	}
	if activated.ActiveWorld == nil || replayed.ActiveWorld == nil || replayed.ActiveWorld.WorldID != first.WorldID || replayed.ActiveRevision != activated.ActiveRevision+1 {
		t.Fatalf("activation replay = %+v / %+v", activated, replayed)
	}
	if _, err := app.ActivateWorld(context.Background(), first.WorldID, replayed.ActiveRevision, "activate-once"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different activation payload error = %v", err)
	}
}

func TestCancellationDoesNotCommitACompletedTurn(t *testing.T) {
	app := newTestApp(t, &scriptedGenerator{delay: 100 * time.Millisecond})
	world, err := app.CreateWorld(context.Background(), "取消测试", "guided", "旅人", "", true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "cancel-1", Input: "我先观察雨势。"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.CancelRun(context.Background(), world.WorldID, run.RunID); err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, app, world.WorldID, run.RunID)
	if finished.Status != "cancelled" {
		t.Fatalf("run = %+v", finished)
	}
	snapshot, err := app.ReadWorld(context.Background(), world.WorldID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.TurnSeq != 0 || len(snapshot.Messages) != 1 {
		t.Fatalf("cancelled turn entered history: %+v", snapshot)
	}
}

func TestIdempotencyAndFailedInputAreNotHistory(t *testing.T) {
	generator := &scriptedGenerator{}
	app := newTestApp(t, generator)
	world, err := app.CreateWorld(context.Background(), "调查 B", "open", "旅人", "", true)
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "same", Input: "我看看柜台。"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "same", Input: "我看看柜台。"})
	if err != nil || duplicate.RunID != first.RunID {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	if _, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "same", Input: "另一句话。"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	waitRun(t, app, world.WorldID, first.RunID)
	generator.mu.Lock()
	generator.fail = true
	generator.mu.Unlock()
	failed, err := app.SubmitRun(context.Background(), world.WorldID, RunRequest{RequestKey: "bad", Input: "告诉佣兵秘密。"})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitRun(t, app, world.WorldID, failed.RunID); result.Status != "failed" {
		t.Fatalf("failed run status = %+v", result)
	}
	snapshot, err := app.ReadWorld(context.Background(), world.WorldID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range snapshot.Messages {
		if strings.Contains(message.Content, "告诉佣兵秘密") {
			t.Fatalf("failed input entered message history: %+v", message)
		}
	}
	for _, items := range snapshot.Memories {
		for _, memory := range items {
			if strings.Contains(memory.Content, "告诉佣兵秘密") {
				t.Fatalf("failed input entered memory: %+v", memory)
			}
		}
	}
}

func TestSaveAsAndReadContinueIsolated(t *testing.T) {
	app := newTestApp(t, &scriptedGenerator{})
	original, err := app.CreateWorld(context.Background(), "调查 C", "guided", "旅人", "", true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := app.SubmitRun(context.Background(), original.WorldID, RunRequest{RequestKey: "save-input", Input: "我观察窗边。"})
	if err != nil {
		t.Fatal(err)
	}
	if waitRun(t, app, original.WorldID, run.RunID).Status != "completed" {
		t.Fatal("initial run failed")
	}
	status, err := app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := app.SaveAs(context.Background(), original.WorldID, "调查 C 的分支", "copy-1", status.ActiveRevision)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operation, err = app.CopyOperation(context.Background(), operation.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Status == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if operation.Status != "ready" {
		t.Fatalf("copy status = %+v", operation)
	}
	if _, err := app.ActivateWorld(context.Background(), operation.TargetWorldID, status.ActiveRevision); err != nil {
		t.Fatal(err)
	}
	branch, err := app.CurrentWorld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if branch.WorldID != operation.TargetWorldID {
		t.Fatalf("active world = %+v", branch)
	}
	run2, err := app.SubmitRun(context.Background(), branch.WorldID, RunRequest{RequestKey: "branch-input", Input: "我继续询问老板。"})
	if err != nil {
		t.Fatal(err)
	}
	if waitRun(t, app, branch.WorldID, run2.RunID).Status != "completed" {
		t.Fatal("branch run failed")
	}
	originalSnapshot, err := app.ReadWorld(context.Background(), original.WorldID, 100)
	if err != nil {
		t.Fatal(err)
	}
	branchSnapshot, err := app.ReadWorld(context.Background(), branch.WorldID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if originalSnapshot.Summary.TurnSeq != 1 || branchSnapshot.Summary.TurnSeq != 2 {
		t.Fatalf("turns = %d/%d", originalSnapshot.Summary.TurnSeq, branchSnapshot.Summary.TurnSeq)
	}
}

func TestSaveAsWaitsForTheCurrentRunBoundary(t *testing.T) {
	app := newTestApp(t, &scriptedGenerator{delay: 80 * time.Millisecond})
	original, err := app.CreateWorld(context.Background(), "等待边界", "guided", "旅人", "", true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := app.SubmitRun(context.Background(), original.WorldID, RunRequest{RequestKey: "save-while-running", Input: "我靠近柜台。"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := app.SaveAs(context.Background(), original.WorldID, "运行完成后的分支", "copy-while-running", status.ActiveRevision)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		operation, err = app.CopyOperation(context.Background(), operation.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Status != "copying" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if operation.Status != "ready" {
		t.Fatalf("copy operation = %+v", operation)
	}
	if finished := waitRun(t, app, original.WorldID, run.RunID); finished.Status != "completed" {
		t.Fatalf("source run = %+v", finished)
	}
	branch, err := app.ReadWorld(context.Background(), operation.TargetWorldID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if branch.Summary.TurnSeq != 1 {
		t.Fatalf("branch summary = %+v", branch.Summary)
	}
}

func TestStrictJSONRejectsDuplicateKeys(t *testing.T) {
	if err := validateStrictJSON([]byte(`{"speech":"x","speech":"y"}`)); err == nil {
		t.Fatal("duplicate key accepted")
	}
	var value npcDecision
	if err := generateJSON(context.Background(), &scriptedGenerator{fail: true}, "", "", &value, 100); err == nil {
		t.Fatal("unknown field accepted")
	}
	_ = json.Valid
}
