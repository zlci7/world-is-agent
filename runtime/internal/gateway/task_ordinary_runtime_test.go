package gateway

import (
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

func TestTaskPausedWorldPreservesOrdinaryConversationAndAction(t *testing.T) {
	for _, reason := range []string{"checkpoint", "task_fault", "clock_rewound", "saving"} {
		t.Run(reason, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			f.server.dispatcher.Stop()
			entry, _ := f.server.worlds.Current(f.head.Binding.World)
			env := entry.Environment.(*streamEnvironment)
			switch reason {
			case "checkpoint":
				request := worldRequest()
				request.Checkpoint = &protocol.TaskCheckpointRef{Status: "unconfirmed", GameId: "sim", WorldId: "world", SchemaVersion: 1, Reason: "save_failed"}
				f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: request}})
				if ready := f.next().GetWorldBindingReady(); ready.GetStatus() != "paused" {
					t.Fatalf("checkpoint did not pause: %v", ready)
				}
			case "task_fault":
				newTaskDispatch(f.server.worlds, f.service, f.server.worlds.dispatchConfig, nil, nil).PauseWorld(f.ctx, f.head.Binding, task.Wake{}, task.ErrTaskConflict)
			case "clock_rewound":
				if err := f.server.worlds.UpdateClock(f.ctx, env, &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 9, Sequence: 2}}); err == nil {
					t.Fatal("rewind accepted")
				}
			case "saving":
				if err := f.server.worlds.SetSaveBarrier(env, f.head.Binding, true); err != nil {
					t.Fatal(err)
				}
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_ActionResult{ActionResult: &protocol.ActionResult{TaskProposal: &protocol.TaskProposal{}}}})
			if message := f.next(); message.GetError().GetCode() != "world_not_ready" {
				t.Fatalf("Task payload admitted: %v", message)
			}
			f.model.mode = "ordinary_after_pause"
			f.event("ordinary-player", nil, nil)
			if ack := f.next(); ack.GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatalf("ordinary player rejected while Task paused: %v", ack)
			}
			f.observe(f.next())
			action := f.next().GetAction()
			if action == nil || action.TaskSource != nil {
				t.Fatalf("ordinary action unavailable: %v", action)
			}
			f.result(action, nil, nil)
			if completion := f.next().GetTurnCompletion(); completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
				t.Fatalf("ordinary turn failed: %v", completion)
			}
			if f.model.calls.Load() != 2 {
				t.Fatal("ordinary conversation did not complete")
			}
		})
	}
}

func TestTaskPauseDuringOrdinaryActionPreservesTurn(t *testing.T) {
	f := newTaskWireFixture(t, false)
	f.server.dispatcher.Stop()
	seedWireOperation(t, f)
	f.model.mode = "ordinary_pause_during_action"
	f.event("ordinary-player", nil, &protocol.InteractionSource{SourceId: "player-input", Kind: "player", PlayerEntityId: "player", Scope: taskScopeToProtocol(f.head.Binding)})
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("no ordinary ack")
	}
	f.observe(f.next())
	action := f.next().GetAction()
	if action == nil || action.TaskSource != nil {
		t.Fatalf("ordinary action: %v", action)
	}
	newTaskDispatch(f.server.worlds, f.service, f.server.worlds.dispatchConfig, nil, nil).PauseWorld(f.ctx, f.head.Binding, task.Wake{}, task.ErrTaskConflict)
	f.result(action, nil, nil)
	if completion := f.next().GetTurnCompletion(); completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("Task pause interrupted an ordinary action turn: %v", completion)
	}
	if f.model.calls.Load() != 2 {
		t.Fatal("ordinary acknowledgment was interrupted")
	}
}
