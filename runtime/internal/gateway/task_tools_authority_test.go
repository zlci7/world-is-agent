package gateway

import (
	"context"
	"errors"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

func TestTaskToolsRejectUnregisteredOwnerBeforePersistence(t *testing.T) {
	for _, phase := range []string{"proposal", "create"} {
		t.Run(phase, func(t *testing.T) {
			server, _, _ := taskTestServer(t)
			stream, _ := taskHandshake(t, server)
			bindTestWorld(t, stream, worldRequest())
			entry, _ := server.WorldRegistry().Current(task.WorldKey{GameID: "sim", WorldID: "world"})
			svc, world := entry.Environment.(*streamEnvironment).TaskRuntime()
			head, epoch, _ := world.Current()
			rc := tool.RuntimeCallContext{Execution: task.ExecutionContext{Owner: session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}, Binding: head.Binding, Clock: head.Clock, Source: task.SourceRef{Kind: task.SourceKindInteraction, EventID: "event", TurnID: "turn"}}, InteractionSourceID: "player", AuthorityEpoch: epoch}
			removeOwner := func() {
				slot := server.worlds.slot(head.Binding.World, false)
				slot.mu.Lock()
				delete(slot.entities, "actor")
				slot.mu.Unlock()
			}
			if phase == "proposal" {
				removeOwner()
			}
			tt := tool.NewTaskTools(svc, world, rc)
			defer tt.Close()
			ref, err := tt.CaptureProposal(context.Background(), rc, &protocol.ActionResult{Status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, TaskProposal: &protocol.TaskProposal{Clock: worldRequest().Clock, WakeAt: 30, DeadlineAt: 50}})
			if phase == "proposal" {
				if !errors.Is(err, task.ErrSourceInvalid) || ref != "" {
					t.Fatalf("unregistered owner received proposal authority: ref=%q err=%v", ref, err)
				}
			} else {
				if err != nil || ref == "" {
					t.Fatalf("capture: %q %v", ref, err)
				}
				removeOwner()
				got, err := tt.Execute(context.Background(), rc, model.ToolCall{ID: "create", Name: "create_task", Arguments: map[string]any{"proposal_ref": ref, "instruction": "Inspect later"}})
				if err == nil || err.Error() != "source_invalid" || got.Status == "succeeded" {
					t.Fatalf("unregistered owner created task: %+v %v", got, err)
				}
			}
			records, err := svc.ListActive(context.Background(), rc.Execution.Owner, 10)
			if err != nil || len(records) != 0 {
				t.Fatalf("unexpected durable task: %+v %v", records, err)
			}
			if _, ready := server.worlds.Current(head.Binding.World); !ready {
				t.Fatal("invalid owner paused the world")
			}
		})
	}
}

func TestWorldTaskToolsAuthorityLifecycle(t *testing.T) {
	for _, scenario := range []string{"clock advance", "save barrier", "disconnect", "rebind"} {
		t.Run(scenario, func(t *testing.T) {
			server, _, _ := taskTestServer(t)
			stream, done := taskHandshake(t, server)
			ready := bindTestWorld(t, stream, worldRequest())
			entry, _ := server.WorldRegistry().Current(task.WorldKey{GameID: "sim", WorldID: "world"})
			wrapped := &historyMaintenanceEnvironment{Environment: entry.Environment}
			provider, ok := any(wrapped).(interface {
				TaskRuntime() (*task.Service, tool.TaskWorld)
			})
			if !ok {
				t.Fatal("bound environment must expose guarded task authority")
			}
			svc, world := provider.TaskRuntime()
			head, epoch, ok := world.Current()
			if !ok {
				t.Fatal("bound authority unavailable")
			}
			rc := tool.RuntimeCallContext{Execution: task.ExecutionContext{Owner: session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}, Binding: head.Binding, Clock: head.Clock, Source: task.SourceRef{Kind: task.SourceKindInteraction, EventID: "event", TurnID: "turn"}}, InteractionSourceID: "player", AuthorityEpoch: epoch}
			tt := tool.NewTaskTools(svc, world, rc)
			defer tt.Close()
			ref, err := tt.CaptureProposal(context.Background(), rc, &protocol.ActionResult{Status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, TaskProposal: &protocol.TaskProposal{Clock: &protocol.WorldClock{ClockId: "game", NowTick: 10, Sequence: 1}, WakeAt: 30, DeadlineAt: 50}})
			if err != nil || ref == "" {
				t.Fatalf("capture: %s %v", ref, err)
			}
			switch scenario {
			case "clock advance":
				if err := server.WorldRegistry().UpdateClock(context.Background(), entry.Environment.(*streamEnvironment), &protocol.WorldClockUpdate{Scope: ready.Scope, Clock: &protocol.WorldClock{ClockId: "game", NowTick: 20, Sequence: 2}}); err != nil {
					t.Fatal(err)
				}
			case "save barrier":
				for _, saving := range []bool{true, false} {
					if err := server.WorldRegistry().SetSaveBarrier(entry.Environment.(*streamEnvironment), head.Binding, saving); err != nil {
						t.Fatal(err)
					}
				}
			case "disconnect":
				close(stream.incoming)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			case "rebind":
				request := worldRequest()
				request.Scope = ready.Scope
				bindTestWorld(t, stream, request)
			}
			got, err := tt.Execute(context.Background(), rc, model.ToolCall{ID: "create", Name: "create_task", Arguments: map[string]any{"proposal_ref": ref, "instruction": "Inspect later"}})
			if scenario == "clock advance" {
				if err != nil || got.Status != "succeeded" {
					t.Fatalf("normal advance invalidated ref: %+v %v", got, err)
				}
			} else if err == nil && got.Status == "succeeded" {
				t.Fatalf("expired authority accepted: %+v", got)
			}
		})
	}
}
