package gateway

import (
	"testing"
	"time"

	"gameagent/runtime/internal/task"
)

func TestConsoleWorldDoesNotWaitForWorldMutation(t *testing.T) {
	registry := NewWorldRegistry(nil)
	world := task.WorldKey{GameID: "generic", WorldID: "world"}
	slot := registry.slot(world, true)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	server := &Server{worlds: registry}
	done := make(chan ConsoleWorldSnapshot, 1)
	go func() { done <- server.ConsoleWorld(world) }()
	select {
	case snapshot := <-done:
		if snapshot.Ready || snapshot.Saving || snapshot.Head.Binding.World != (task.WorldKey{}) {
			t.Fatalf("busy world must remain unconfirmed: %+v", snapshot)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("console waited for the execution lock")
	}
}

func TestConsoleWorldTracksBoundReadinessSavingAndDisconnect(t *testing.T) {
	server, _, _ := taskTestServer(t)
	stream, done := taskHandshake(t, server)
	request := worldRequest()
	request.Entities[0].DisplayName = "示例角色"
	if ready := bindTestWorld(t, stream, request); ready.Status != "ready" {
		t.Fatal(ready)
	}
	first := server.ConsoleWorld(checkpointTestWorld)
	if !first.Ready || first.Saving || first.Names["actor"] != "示例角色" {
		t.Fatalf("bound: %+v", first)
	}
	first.Names["actor"] = "caller mutation"
	if next := server.ConsoleWorld(checkpointTestWorld); next.Names["actor"] != "示例角色" {
		t.Fatal("snapshot exposes mutable names")
	}
	stream.incoming <- checkpointPrepareMessage(1, "console-save", 12, 2)
	prepared := recvCheckpointPrepared(t, stream)
	if prepared.Error != nil {
		t.Fatal(prepared.Error)
	}
	saving := server.ConsoleWorld(checkpointTestWorld)
	if saving.Ready || !saving.Saving || saving.Head.CheckpointID != prepared.Checkpoint.CheckpointId {
		t.Fatalf("saving: %+v", saving)
	}
	stream.shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not settle")
	}
	if disconnected := server.ConsoleWorld(checkpointTestWorld); disconnected.Ready {
		t.Fatalf("disconnected: %+v", disconnected)
	}
}
