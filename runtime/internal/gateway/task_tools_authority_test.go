package gateway

import (
	"context"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

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
