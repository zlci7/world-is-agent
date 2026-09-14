package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

type taskToolWorld struct {
	head  task.Head
	epoch uint64
	ready bool
}

func (w *taskToolWorld) Current() (task.Head, uint64, bool) { return w.head, w.epoch, w.ready }
func (w *taskToolWorld) GuardOwner(binding task.Binding, epoch uint64, entityID string, fn func(task.Head) error) error {
	if !w.ready {
		return task.ErrWorldNotReady
	}
	if w.head.Binding != binding || epoch != w.epoch {
		return task.ErrGenerationStale
	}
	return fn(w.head)
}
func taskToolFixture(t *testing.T) (*task.Service, *taskToolWorld, RuntimeCallContext) {
	t.Helper()
	store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := task.NewService(store)
	world := task.WorldKey{GameID: "simulation", WorldID: "world"}
	head, err := svc.ActivateWorld(context.Background(), world, "run", task.Clock{ID: "clock", Tick: 100, Sequence: 1}, task.CheckpointRef{Status: "absent", World: world})
	if err != nil {
		t.Fatal(err)
	}
	rc := RuntimeCallContext{Execution: task.ExecutionContext{Owner: session.AgentSessionKey{GameID: "simulation", WorldID: "world", EntityID: "actor"}, Binding: head.Binding, Clock: head.Clock, Source: task.SourceRef{Kind: task.SourceKindInteraction, EventID: "event", TurnID: "turn"}}, InteractionSourceID: "player-source", AuthorityEpoch: 1}
	return svc, &taskToolWorld{head: head, epoch: 1, ready: true}, rc
}
func taskProposalResult() *protocol.ActionResult {
	return &protocol.ActionResult{Status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, TaskProposal: &protocol.TaskProposal{Clock: &protocol.WorldClock{ClockId: "clock", NowTick: 100, Sequence: 1}, WakeAt: 120, DeadlineAt: 200, ParticipantEntityIds: []string{"actor"}, EquivalenceKey: "appointment"}}
}
func taskCreateCall(ref string) model.ToolCall {
	return model.ToolCall{ID: "create", Name: "create_task", Arguments: map[string]any{"proposal_ref": ref, "instruction": "Inspect the agreed location"}}
}
func captureTaskProposal(t *testing.T, tt *TaskTools, rc RuntimeCallContext) string {
	t.Helper()
	ref, err := tt.CaptureProposal(context.Background(), rc, taskProposalResult())
	if err != nil || ref == "" {
		t.Fatalf("capture: %q %v", ref, err)
	}
	return ref
}
func TestTaskToolsCurrentClockCreateAndIdempotency(t *testing.T) {
	svc, world, rc := taskToolFixture(t)
	tt := NewTaskTools(svc, world, rc)
	ref := captureTaskProposal(t, tt, rc)
	if len(tt.Entries(rc)) != 1 {
		t.Fatal("proposal must admit create_task")
	}
	world.head, _ = svc.UpdateClock(context.Background(), rc.Execution.Binding, task.Clock{ID: "clock", Tick: 110, Sequence: 2})
	call := taskCreateCall(ref)
	got, err := tt.Execute(context.Background(), rc, call)
	if err != nil || got.Status != "succeeded" || got.Output["created"] != true {
		t.Fatalf("create: %+v %v", got, err)
	}
	record, err := svc.Read(context.Background(), rc.Execution.Owner, got.Output["task_id"].(string))
	if err != nil || record.CreatedAtGameTick != 110 || record.Spec.WakeAt != 120 || record.Spec.DeadlineAt != 200 || record.Spec.Instruction != "Inspect the agreed location" || record.Spec.Source.CallID != "create" {
		t.Fatalf("persisted: %+v %v", record, err)
	}
	again, err := tt.Execute(context.Background(), rc, call)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("exact retry: %+v %v", again, err)
	}
	world.head, _ = svc.UpdateClock(context.Background(), rc.Execution.Binding, task.Clock{ID: "clock", Tick: 121, Sequence: 3})
	again, err = tt.Execute(context.Background(), rc, call)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("exact retry lost original response after clock advance: %+v %v", again, err)
	}
	call.Arguments["instruction"] = "changed"
	got, err = tt.Execute(context.Background(), rc, call)
	if err != nil || got.Code != "idempotency_conflict" {
		t.Fatalf("changed retry: %+v %v", got, err)
	}
}
func TestTaskToolsProposalAuthorityAndLifecycle(t *testing.T) {
	for _, scenario := range []string{"forged", "owner", "world", "run", "generation", "event", "turn", "source", "task source", "clock", "expired", "barrier", "disconnect", "turn end", "binding after capture"} {
		t.Run(scenario, func(t *testing.T) {
			svc, world, rc := taskToolFixture(t)
			tt := NewTaskTools(svc, world, rc)
			ref := captureTaskProposal(t, tt, rc)
			switch scenario {
			case "forged":
				ref = "made-up"
			case "owner":
				rc.Execution.Owner.EntityID = "other"
			case "world":
				rc.Execution.Owner.WorldID = "other"
			case "run":
				rc.Execution.Binding.RunID = "other"
			case "generation":
				rc.Execution.Binding.Generation++
			case "event":
				rc.Execution.Source.EventID = "other"
			case "turn":
				rc.Execution.Source.TurnID = "other"
			case "source":
				rc.InteractionSourceID = "other"
			case "task source":
				rc.Execution.Source.Kind = task.SourceKindTaskWake
			case "clock":
				world.head.Clock.ID = "other"
			case "expired":
				world.head, _ = svc.UpdateClock(context.Background(), rc.Execution.Binding, task.Clock{ID: "clock", Tick: 120, Sequence: 2})
			case "barrier":
				world.epoch++
			case "disconnect":
				world.ready = false
			case "turn end":
				tt.Close()
			case "binding after capture":
				world.head.Binding.Generation++
			}
			got, err := tt.Execute(context.Background(), rc, taskCreateCall(ref))
			if err == nil && got.Status == "succeeded" {
				t.Fatalf("authority accepted: %+v", got)
			}
			records, readErr := svc.List(context.Background(), session.AgentSessionKey{GameID: "simulation", WorldID: "world", EntityID: "actor"}, 10)
			if readErr != nil || len(records) != 0 {
				t.Fatalf("unauthorized write: %+v %v", records, readErr)
			}
		})
	}
}
func TestTaskToolsSchemaConditionalValidation(t *testing.T) {
	svc, world, rc := taskToolFixture(t)
	tt := NewTaskTools(svc, world, rc)
	ref := captureTaskProposal(t, tt, rc)
	for _, entry := range tt.Entries(rc) {
		if entry.Kind != KindRuntime || entry.Execution != ExecutionSync || entry.Concurrency != ConcurrencySequential || !entry.Policy.ExclusivePerStep || entry.Policy.SettleAfterSuccess {
			t.Fatalf("policy: %+v", entry)
		}
	}
	for _, args := range []map[string]any{
		{"task_id": "task", "intent": "wait"}, {"task_id": "task", "intent": "wait", "next_wakeup_at": 130, "reason": ""},
		{"task_id": "task", "intent": "cancel", "reason": " "}, {"task_id": "task", "intent": "cancel", "reason": "cancel", "progress_note": ""},
		{"task_id": "task", "intent": "cancel", "reason": "cancel", "next_wakeup_at": 130}, {"task_id": "task", "intent": "cancel", "reason": "cancel", "expected_revision": 5},
		{"task_id": "task", "intent": "cancel", "reason": "cancel", "state": "running"}, {"task_id": "task", "intent": "cancel", "reason": "cancel", "generation": 1},
		{"task_id": "task", "intent": "cancel", "reason": strings.Repeat("x", 513)}, {"task_id": "task", "intent": "wait", "next_wakeup_at": 130, "progress_note": strings.Repeat("x", 2049)},
	} {
		got, err := tt.Execute(context.Background(), rc, model.ToolCall{ID: "update", Name: "update_task", Arguments: args})
		if err != nil || got.Status != "invalid" || got.Message != "tool_arguments_invalid" {
			t.Fatalf("args accepted: %+v %v", got, err)
		}
	}
	for _, instruction := range []string{"", " ", strings.Repeat("x", 2049)} {
		call := taskCreateCall(ref)
		call.Arguments["instruction"] = instruction
		got, err := tt.Execute(context.Background(), rc, call)
		if err != nil || got.Status != "invalid" {
			t.Fatalf("instruction accepted: %+v %v", got, err)
		}
	}
}
func TestTaskToolsIntentObservedRevisionAndSources(t *testing.T) {
	svc, world, rc := taskToolFixture(t)
	tt := NewTaskTools(svc, world, rc)
	ref := captureTaskProposal(t, tt, rc)
	created, err := tt.Execute(context.Background(), rc, taskCreateCall(ref))
	if err != nil {
		t.Fatal(err)
	}
	record, _ := svc.Read(context.Background(), rc.Execution.Owner, created.Output["task_id"].(string))
	rc.ObservedTask = &record
	for _, mutate := range []func(*RuntimeCallContext, map[string]any){
		func(_ *RuntimeCallContext, args map[string]any) { args["task_id"] = "other" },
		func(rc *RuntimeCallContext, _ map[string]any) {
			r := *rc.ObservedTask
			r.Owner.EntityID = "other"
			rc.ObservedTask = &r
		},
		func(_ *RuntimeCallContext, args map[string]any) {
			args["intent"] = "wait"
			delete(args, "reason")
			args["next_wakeup_at"] = 130
		},
	} {
		candidate := rc
		args := map[string]any{"task_id": record.ID, "intent": "cancel", "reason": "player request"}
		mutate(&candidate, args)
		got, err := tt.Execute(context.Background(), candidate, model.ToolCall{ID: "bad", Name: "update_task", Arguments: args})
		if err == nil && got.Status == "succeeded" {
			t.Fatalf("unauthorized update: %+v", got)
		}
	}
	exec := rc.Execution
	exec.TaskID = record.ID
	exec.ExpectedRevision = record.Revision
	exec.Source.CallID = "advance"
	tick := int64(150)
	_, err = svc.ApplyIntent(context.Background(), exec, task.Intent{Kind: "wait", NextWakeAt: &tick})
	if err != nil {
		t.Fatal(err)
	}
	got, err := tt.Execute(context.Background(), rc, model.ToolCall{ID: "cancel", Name: "update_task", Arguments: map[string]any{"task_id": record.ID, "intent": "cancel", "reason": "player request"}})
	if err != nil || got.Code != "task_changed" {
		t.Fatalf("observed revision was replaced: %+v %v", got, err)
	}
}
func TestTaskToolsWaitUsesCurrentClock(t *testing.T) {
	for _, now := range []int64{110, 130} {
		t.Run(fmt.Sprint(now), func(t *testing.T) {
			svc, world, rc := taskToolFixture(t)
			tt := NewTaskTools(svc, world, rc)
			created, err := tt.Execute(context.Background(), rc, taskCreateCall(captureTaskProposal(t, tt, rc)))
			if err != nil {
				t.Fatal(err)
			}
			record, _ := svc.Read(context.Background(), rc.Execution.Owner, created.Output["task_id"].(string))
			rc.Execution.Source.Kind = task.SourceKindTaskWake
			rc.Execution.TaskID = record.ID
			rc.Execution.WakeID = "wake"
			rc.InteractionSourceID = ""
			rc.ObservedTask = &record
			tt = NewTaskTools(svc, world, rc)
			world.head, _ = svc.UpdateClock(context.Background(), rc.Execution.Binding, task.Clock{ID: "clock", Tick: now, Sequence: 2})
			got, err := tt.Execute(context.Background(), rc, model.ToolCall{ID: "wait", Name: "update_task", Arguments: map[string]any{"task_id": record.ID, "intent": "wait", "next_wakeup_at": 130}})
			if now == 110 && (err != nil || got.Status != "succeeded") {
				t.Fatalf("valid wait: %+v %v", got, err)
			}
			if now == 130 && (err != nil || got.Status != "invalid") {
				t.Fatalf("expired wait: %+v %v", got, err)
			}
		})
	}
}
func TestTaskToolErrorClassificationRedaction(t *testing.T) {
	business := []error{task.ErrTaskNotFound, task.ErrTaskConflict, task.ErrTaskChanged, task.ErrIdempotencyConflict, task.ErrTaskTerminal, task.ErrTaskCapacityExceeded}
	technical := []error{task.ErrInvalidTaskSpec, task.ErrSourceInvalid, task.ErrWorldMismatch, task.ErrGenerationStale, task.ErrClockMismatch, task.ErrClockRewound, task.ErrWorldNotReady, task.ErrSaveInProgress, task.ErrCheckpointInvalid, task.ErrStoreInUse, context.Canceled, context.DeadlineExceeded, errors.New("SECRET DB PROMPT"), task.WrapError(task.CodeTaskConflict, context.Canceled), task.WrapError(task.CodeTaskConflict, errors.New("SECRET DB ERROR"))}
	for _, group := range []struct {
		errs     []error
		rejected bool
	}{{business, true}, {technical, false}} {
		for _, cause := range group.errs {
			call := model.ToolCall{ID: "id", Name: "create_task", Arguments: map[string]any{"instruction": "SECRET DB PROMPT"}}
			got, err := taskToolError(call, fmt.Errorf("SECRET DB PROMPT: %w", cause))
			if group.rejected && (err != nil || got.Status != "rejected") || !group.rejected && err == nil {
				t.Fatalf("classification %v: %+v %v", cause, got, err)
			}
			data, _ := json.Marshal(got)
			if strings.Contains(string(data), "SECRET") || err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("leaked: %s %v", data, err)
			}
		}
	}
}
