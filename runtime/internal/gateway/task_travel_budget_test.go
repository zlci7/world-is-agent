package gateway

import (
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/task"
)

// Adapter tests cover 158/180 seconds with its injected monotonic clock.
// This wire test uses short real deadlines to cover Runtime timeout propagation.
func TestTaskTravelBudget(t *testing.T) {
	f := newTaskWireFixtureWithRouteMode(t, false, protocol.ExecutionMode_EXECUTION_MODE_ASYNC, func(c *agent.Config) {
		c.ActionTimeout = 100 * time.Millisecond
		c.ActionStartTimeout = time.Second
		c.AsyncActionTimeout = 2 * time.Second
		c.TurnTimeout = 4 * time.Second
	})
	f.model.mode = "travel_budget"
	source := task.SourceRef{Kind: task.SourceKindInternal, EventID: "seed", TurnID: "seed", CallID: "seed"}
	exec := task.ExecutionContext{Owner: f.key, Binding: f.head.Binding, Clock: f.head.Clock, Source: source}
	created, err := f.service.Create(f.ctx, exec, task.TaskSpec{Instruction: "Travel then register a wait", ClockID: "game", WakeAt: 11, DeadlineAt: 100, ResultContract: task.ResultContractAuthoritativeEvidence, Source: source}, task.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 11, Sequence: 2}}}})
	f.observe(f.next())
	travel := f.next().GetAction()
	if travel.GetCapability() != "follow_route" || travel.GetTaskSource() == nil {
		t.Fatalf("expected registered async route: %v", travel)
	}
	f.send(actionStatusUpdateMessage(travel.ActionId, protocol.ActionStatus_ACTION_STATUS_ACCEPTED))
	f.send(actionStatusUpdateMessage(travel.ActionId, protocol.ActionStatus_ACTION_STATUS_RUNNING))
	select {
	case message := <-f.messages:
		t.Fatalf("async travel ended under the sync deadline: %v", message)
	case <-time.After(250 * time.Millisecond):
	}
	f.result(travel, nil, nil)
	next := f.next()
	if next.GetObserve() != nil {
		f.observe(next)
		next = f.next()
	}
	wait := next.GetAction()
	if wait.GetCapability() != "inspect_contract" || wait.GetTaskSource() == nil {
		t.Fatalf("expected registered sync wait: %v", next)
	}
	until := int64(20)
	waitSource := wait.TaskSource
	f.result(wait, []*protocol.TaskEvidence{{FactId: "wait-registered", TaskId: waitSource.TaskId, OperationId: waitSource.OperationId, Scope: waitSource.Scope, StartRevision: waitSource.StartRevision, OccurredAt: 11, Outcome: "progress", WaitUntil: &until}}, nil)
	if completion := f.next().GetTurnCompletion(); completion.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("wait registration kept Turn open: %v", completion)
	}
	record := f.awaitRecord(created.Task.ID, func(r task.Record) bool { return r.State == task.StateWaiting })
	if record.Result != nil || record.NextWakeAt == nil || *record.NextWakeAt != until || len(record.Operations) != 2 || f.model.calls.Load() != 2 {
		t.Fatalf("invalid async-to-wait handoff: %+v calls=%d", record, f.model.calls.Load())
	}
}
