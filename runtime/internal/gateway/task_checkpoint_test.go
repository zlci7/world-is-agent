package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

var checkpointTestWorld = task.WorldKey{GameID: "sim", WorldID: "world"}

func checkpointTestScope(generation uint64) *protocol.TaskScope {
	return &protocol.TaskScope{GameId: "sim", WorldId: "world", WorldRunId: "run", ExecutionGeneration: generation}
}

func checkpointTestClock(tick int64, sequence uint64) *protocol.WorldClock {
	return &protocol.WorldClock{ClockId: "game", NowTick: tick, Sequence: sequence}
}

func boundWorldRequest(generation uint64, tick int64, sequence uint64) *protocol.WorldBinding {
	request := worldRequest()
	request.Scope = checkpointTestScope(generation)
	request.Clock = checkpointTestClock(tick, sequence)
	return request
}

func checkpointPrepareMessage(generation uint64, saveRequestID string, tick int64, sequence uint64, evidence ...*protocol.TaskEvidence) *protocol.AdapterMessage {
	return &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_CheckpointPrepare{CheckpointPrepare: &protocol.CheckpointPrepare{
		Scope:         checkpointTestScope(generation),
		Clock:         checkpointTestClock(tick, sequence),
		SaveRequestId: saveRequestID,
		FinalEvidence: evidence,
	}}}
}

func checkpointFinishMessage(generation uint64, saveRequestID string, saved bool) *protocol.AdapterMessage {
	return &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_CheckpointFinish{CheckpointFinish: &protocol.CheckpointFinish{
		Scope:         checkpointTestScope(generation),
		SaveRequestId: saveRequestID,
		Saved:         saved,
	}}}
}

func recvCheckpointPrepared(t *testing.T, s *worldTestStream) *protocol.CheckpointPrepared {
	t.Helper()
	prepared := s.recvSent(t).GetCheckpointPrepared()
	if prepared == nil {
		t.Fatal("missing CheckpointPrepared reply")
	}
	return prepared
}

func TestCheckpointPrepareHostsTheSaveBarrier(t *testing.T) {
	server, service, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	if ready := bindTestWorld(t, s, worldRequest()); ready.Status != "ready" || ready.Scope.ExecutionGeneration != 1 {
		t.Fatal(ready)
	}

	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2)
	prepared := recvCheckpointPrepared(t, s)
	if prepared.Error != nil || prepared.SaveRequestId != "save_1" {
		t.Fatalf("prepare reply: %v", prepared)
	}
	if prepared.Scope.GetExecutionGeneration() != 2 || prepared.Scope.GetWorldRunId() != "run" {
		t.Fatalf("prepared scope must carry the fenced generation: %v", prepared.Scope)
	}
	reference := prepared.Checkpoint
	if reference == nil || reference.Status != "confirmed" || reference.SchemaVersion != 1 || !strings.HasPrefix(reference.CheckpointId, "checkpoint_") || reference.Checksum == "" {
		t.Fatalf("prepared reference: %v", reference)
	}
	if reference.GameId != "sim" || reference.WorldId != "world" {
		t.Fatalf("prepared reference identity: %v", reference)
	}

	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("admission stayed open while the save barrier held")
	}
	head, err := service.ReadHead(context.Background(), checkpointTestWorld)
	if err != nil {
		t.Fatal(err)
	}
	if head.Binding.Generation != 2 || head.CheckpointID != reference.CheckpointId {
		t.Fatalf("persisted head: %+v", head)
	}

	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: checkpointTestScope(2), Clock: checkpointTestClock(12, 2)}}}
	if code := s.recvSent(t).GetError().GetCode(); code != "save_in_progress" {
		t.Fatalf("clock update during the save barrier: %q", code)
	}

	s.incoming <- checkpointFinishMessage(2, "save_1", true)
	// The same-run rejoin publishes its own generation, so the Adapter applies whatever the
	// runtime reports instead of assuming the prepared one.
	ready := bindTestWorld(t, s, boundWorldRequest(2, 12, 2))
	if ready.Status != "ready" || ready.Error != nil || ready.Scope.ExecutionGeneration < 2 {
		t.Fatalf("rebind after finish: %v", ready)
	}
	published := server.WorldRegistry().ReadyWorlds()
	if len(published) != 1 || published[0].Binding.Generation != ready.Scope.ExecutionGeneration || published[0].Clock.Tick != 12 {
		t.Fatal("admission did not reopen after the save barrier was released")
	}
}

func TestCheckpointPrepareRejectsSecondSaveWhileBarrierHolds(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())

	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2)
	if prepared := recvCheckpointPrepared(t, s); prepared.Error != nil {
		t.Fatal(prepared.Error)
	}
	s.incoming <- checkpointPrepareMessage(1, "save_2", 13, 3)
	if code := recvCheckpointPrepared(t, s).GetError().GetCode(); code != "save_in_progress" {
		t.Fatalf("second save during the barrier: %q", code)
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("second save reopened admission")
	}
}

func TestCheckpointPrepareRetryReturnsTheOriginalSnapshot(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())

	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2)
	first := recvCheckpointPrepared(t, s)
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2)
	second := recvCheckpointPrepared(t, s)
	if second.Error != nil {
		t.Fatal(second.Error)
	}
	if second.Checkpoint.GetCheckpointId() != first.Checkpoint.GetCheckpointId() || second.Scope.GetExecutionGeneration() != first.Scope.GetExecutionGeneration() {
		t.Fatalf("retry created a second snapshot: %v vs %v", second, first)
	}
}

func TestCheckpointFinishRequiresTheMatchingRequest(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())

	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2)
	if prepared := recvCheckpointPrepared(t, s); prepared.Error != nil {
		t.Fatal(prepared.Error)
	}
	s.incoming <- checkpointFinishMessage(2, "save_other", true)
	if s.recvSent(t).GetError() == nil {
		t.Fatal("finish for a foreign save request was accepted")
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("foreign finish reopened admission")
	}

	s.incoming <- checkpointFinishMessage(2, "save_1", true)
	if ready := bindTestWorld(t, s, boundWorldRequest(2, 12, 2)); ready.Status != "ready" {
		t.Fatalf("rebind after the matching finish: %v", ready)
	}
}

func TestCheckpointPrepareFailureKeepsTheWorldRecoverable(t *testing.T) {
	server, service, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())

	unknown := &protocol.TaskEvidence{
		FactId: "fact_unknown", TaskId: "task_missing", OperationId: "operation_missing", StartRevision: 1,
		Scope: checkpointTestScope(1), OccurredAt: 12, Outcome: "succeeded",
	}
	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2, unknown)
	if prepared := recvCheckpointPrepared(t, s); prepared.Error == nil {
		t.Fatal("prepare admitted evidence for a task that does not exist")
	}

	head, err := service.ReadHead(context.Background(), checkpointTestWorld)
	if err != nil {
		t.Fatal(err)
	}
	if head.Binding.Generation != 1 || head.CheckpointID != "" {
		t.Fatalf("failed prepare fenced the working set: %+v", head)
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("failed prepare reopened admission before a rebind")
	}
	ready := bindTestWorld(t, s, boundWorldRequest(1, 12, 2))
	if ready.Status != "ready" || ready.Error != nil {
		t.Fatalf("rebind after a failed prepare: %v", ready)
	}
}

func TestCheckpointBarrierSurvivesDisconnectWithoutLatchingFailure(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, done := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())

	s.incoming <- checkpointPrepareMessage(1, "save_1", 12, 2)
	if prepared := recvCheckpointPrepared(t, s); prepared.Error != nil {
		t.Fatal(prepared.Error)
	}
	s.shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected stream did not finish")
	}

	reconnect, _ := taskHandshake(t, server)
	ready := bindTestWorld(t, reconnect, boundWorldRequest(2, 12, 2))
	if ready.Status != "paused" || ready.GetError().GetCode() != "save_in_progress" {
		t.Fatalf("rebind while the save barrier held: %v", ready)
	}
	select {
	case <-reconnect.sent:
		t.Fatal("a rebind during the save barrier published an unexpected message")
	case <-time.After(20 * time.Millisecond):
	}
}
