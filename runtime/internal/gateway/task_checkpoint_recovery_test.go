package gateway

import (
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

func checkpointPrepareForTest(f *taskWireFixture, saveRequestID string, tick int64, sequence uint64) *protocol.AdapterMessage {
	return &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_CheckpointPrepare{CheckpointPrepare: &protocol.CheckpointPrepare{
		Scope:         taskScopeToProtocol(f.head.Binding),
		Clock:         &protocol.WorldClock{ClockId: "game", NowTick: tick, Sequence: sequence},
		SaveRequestId: saveRequestID,
	}}}
}

func checkpointFinishForTest(f *taskWireFixture, generation uint64, saveRequestID string, saved bool) *protocol.AdapterMessage {
	scope := *taskScopeToProtocol(f.head.Binding)
	scope.ExecutionGeneration = generation
	return &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_CheckpointFinish{CheckpointFinish: &protocol.CheckpointFinish{
		Scope:         &scope,
		SaveRequestId: saveRequestID,
		Saved:         saved,
	}}}
}

func worldBindingForTest(generation uint64, tick int64, sequence uint64) *protocol.WorldBinding {
	request := worldRequest()
	request.Scope = &protocol.TaskScope{GameId: "sim", WorldId: "world", WorldRunId: "run", ExecutionGeneration: generation}
	request.Clock = &protocol.WorldClock{ClockId: "game", NowTick: tick, Sequence: sequence}
	return request
}

// seedWaitingTask leaves a task waiting on a scheduled wake, which is the state a save interrupts.
func seedWaitingTask(t *testing.T, f *taskWireFixture) (task.Record, task.Operation) {
	t.Helper()
	r, op := seedWireOperation(t, f)
	waitUntil := int64(90)
	f.event("wait_registered", []*protocol.TaskEvidence{{
		FactId:        "wait_registered",
		TaskId:        r.ID,
		OperationId:   op.ID,
		Scope:         taskScopeToProtocol(f.head.Binding),
		StartRevision: op.StartRevision,
		OccurredAt:    10,
		Outcome:       "progress",
		WaitUntil:     &waitUntil,
	}}, nil)
	if ack := f.next().GetEventAck(); ack.GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("wait registration: %v", ack)
	}
	return f.awaitRecord(r.ID, func(record task.Record) bool {
		return record.State == task.StateWaiting && record.NextWakeAt != nil && *record.NextWakeAt == waitUntil
	}), op
}

// TestCheckpointBindingRecovery drops the Finish a save can lose and proves the world comes back on
// the working head with its waiting task intact, without a second action.
func TestCheckpointBindingRecovery(t *testing.T) {
	previous := taskCheckpointBarrierTimeout
	taskCheckpointBarrierTimeout = 200 * time.Millisecond
	defer func() { taskCheckpointBarrierTimeout = previous }()

	f := newTaskWireFixture(t, false)
	waiting, _ := seedWaitingTask(t, f)

	f.send(checkpointPrepareForTest(f, "save_lost", 50, 2))
	prepared := f.next().GetCheckpointPrepared()
	if prepared.GetError() != nil || prepared.GetCheckpoint().GetStatus() != "confirmed" || prepared.GetScope().GetExecutionGeneration() != 2 {
		t.Fatalf("prepare: %v", prepared)
	}
	if len(f.server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("admission stayed open during the barrier")
	}

	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldBindingForTest(2, 50, 2)}})
	if ready := f.next().GetWorldBindingReady(); ready.GetStatus() != "paused" || ready.GetError().GetCode() != "save_in_progress" {
		t.Fatalf("rebind before the bound expired: %v", ready)
	}

	time.Sleep(300 * time.Millisecond)
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldBindingForTest(2, 50, 2)}})
	ready := f.next().GetWorldBindingReady()
	if ready.GetStatus() != "ready" || ready.GetScope().GetExecutionGeneration() < 2 {
		t.Fatalf("rebind after the bound expired: %v", ready)
	}
	recovered, err := f.service.Read(f.ctx, f.key, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != task.StateWaiting || recovered.NextWakeAt == nil || *recovered.NextWakeAt != 90 || len(recovered.Evidence) != 1 {
		t.Fatalf("waiting task did not survive the lost save: %+v", recovered)
	}
	if len(f.server.WorldRegistry().ReadyWorlds()) != 1 {
		t.Fatal("admission did not reopen after recovery")
	}
}

// TestTaskWaitSurvivesSave runs the met fact of a pre-save operation through the observation reply
// the runtime asks for after a save, which is the only path that revalidates an older binding.
func TestTaskWaitSurvivesSave(t *testing.T) {
	f := newTaskWireFixture(t, false)
	waiting, op := seedWaitingTask(t, f)

	f.send(checkpointPrepareForTest(f, "save_completed", 50, 2))
	if prepared := f.next().GetCheckpointPrepared(); prepared.GetError() != nil {
		t.Fatalf("prepare: %v", prepared)
	}
	f.send(checkpointFinishForTest(f, 2, "save_completed", true))
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldBindingForTest(2, 50, 2)}})
	ready := f.next().GetWorldBindingReady()
	if ready.GetStatus() != "ready" {
		t.Fatalf("rebind after the save: %v", ready)
	}

	query := f.next()
	if query.GetObserve() == nil {
		t.Fatalf("no post-save observation: %v", query)
	}
	f.observe(query, &protocol.TaskEvidence{
		FactId:        "met",
		TaskId:        waiting.ID,
		OperationId:   op.ID,
		Scope:         taskScopeToProtocol(f.head.Binding),
		StartRevision: op.StartRevision,
		OccurredAt:    50,
		Outcome:       "satisfied",
	})
	settled := f.awaitRecord(waiting.ID, func(record task.Record) bool { return record.Result != nil })
	if len(settled.Evidence) != 2 {
		t.Fatalf("met fact did not close the task: %+v", settled)
	}
	applied := settled.Evidence[1]
	if applied.RevalidatedIn == nil || applied.RevalidatedIn.Generation <= applied.Binding.Generation {
		t.Fatalf("evidence was not revalidated into the current generation: %+v", applied)
	}
	if applied.Binding.Generation != 1 {
		t.Fatalf("original binding was rewritten: %+v", applied)
	}
	if f.model.calls.Load() != 0 {
		t.Fatal("deterministic evidence called the model")
	}
}
