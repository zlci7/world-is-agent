package gateway

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
	"google.golang.org/protobuf/proto"
)

type worldTestStream struct {
	captureStream
	incoming     chan *protocol.AdapterMessage
	ctx          context.Context
	server       *Server
	readyGate    <-chan struct{}
	readySending chan struct{}
}

func (s *worldTestStream) Send(message *protocol.RuntimeMessage) error {
	if message.GetWorldBindingReady() != nil && s.readyGate != nil {
		close(s.readySending)
		select {
		case <-s.readyGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	return s.captureStream.Send(message)
}

func (s *worldTestStream) Context() context.Context { return s.ctx }
func (s *worldTestStream) Recv() (*protocol.AdapterMessage, error) {
	select {
	case m, ok := <-s.incoming:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
func newWorldTestStream(t *testing.T, server *Server, extensions []string) (*worldTestStream, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := &worldTestStream{captureStream: captureStream{sent: make(chan *protocol.RuntimeMessage, 32)}, incoming: make(chan *protocol.AdapterMessage, 32), ctx: ctx, server: server}
	done := make(chan error, 1)
	go func() { done <- server.Connect(stream) }()
	stream.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{GameId: "sim", SessionId: "connection", SupportedExtensions: extensions}}}
	return stream, done
}
func worldRequest() *protocol.WorldBinding {
	return &protocol.WorldBinding{Scope: &protocol.TaskScope{GameId: "sim", WorldId: "world", WorldRunId: "run"}, Clock: &protocol.WorldClock{ClockId: "game", NowTick: 10, Sequence: 1}, Entities: []*protocol.EntityRef{{EntityId: "actor", EntityType: "agent", DefinitionId: "generic"}, {EntityId: "other", EntityType: "agent", DefinitionId: "generic"}}}
}
func sendWorldCapabilities(s *worldTestStream) {
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{}}}
}

func TestWorldBindingUnnegotiatedRejected(t *testing.T) {
	s, _ := newWorldTestStream(t, NewServer(nil), []string{"gameagent.tasks.v1"})
	if m := s.recvSent(t); m.GetEnvironmentReady() == nil || len(m.GetEnvironmentReady().AcceptedExtensions) != 0 {
		t.Fatal(m)
	}
	if m := s.recvSent(t); m.GetCapabilityRequest() == nil {
		t.Fatal(m)
	}
	sendWorldCapabilities(s)
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldRequest()}}
	select {
	case m := <-s.sent:
		if m.GetError() == nil {
			t.Fatalf("expected task extension rejection, got %v", m)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("unnegotiated WorldBinding was silently ignored")
	}
}

func taskTestServer(t *testing.T) (*Server, *task.Service, *task.SQLiteStore) {
	t.Helper()
	store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	service := task.NewService(store)
	server := NewServer(nil, WithTaskService(service))
	t.Cleanup(func() { server.Close(context.Background()) })
	return server, service, store
}
func taskHandshake(t *testing.T, server *Server) (*worldTestStream, <-chan error) {
	t.Helper()
	s, done := newWorldTestStream(t, server, []string{"unknown", "gameagent.tasks.v1", "gameagent.tasks.v1", "unknown"})
	ready := s.recvSent(t).GetEnvironmentReady()
	if ready == nil || !reflect.DeepEqual(ready.AcceptedExtensions, []string{"gameagent.tasks.v1"}) {
		t.Fatalf("negotiation: %v", ready)
	}
	if m := s.recvSent(t); m.GetCapabilityRequest() == nil {
		t.Fatal(m)
	}
	sendWorldCapabilities(s)
	return s, done
}
func bindTestWorld(t *testing.T, s *worldTestStream, request *protocol.WorldBinding) *protocol.WorldBindingReady {
	t.Helper()
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: request}}
	ready := s.recvSent(t).GetWorldBindingReady()
	if ready == nil {
		t.Fatal("missing WorldBindingReady")
	}
	if ready.Status == "ready" {
		deadline := time.Now().Add(time.Second)
		for {
			entry, ok := s.server.WorldRegistry().Current(task.WorldKey{GameID: ready.Scope.GameId, WorldID: ready.Scope.WorldId})
			if ok && entry.Head.Binding.Generation == ready.Scope.ExecutionGeneration {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("WorldBindingReady admission was not published")
			}
			time.Sleep(time.Millisecond)
		}
	}
	return ready
}
func TestWorldBindingHandshakeAndGating(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, _ := taskHandshake(t, server)
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: worldRequest().Scope, Clock: worldRequest().Clock}}}
	if e := s.recvSent(t).GetError(); e.GetCode() != "world_not_ready" {
		t.Fatal(e)
	}
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{}}}
	if e := s.recvSent(t).GetError(); e.GetCode() != "world_not_ready" {
		t.Fatal(e)
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("pre-ready admission")
	}
	ready := bindTestWorld(t, s, worldRequest())
	if ready.Status != "ready" || ready.Error != nil || ready.Scope.ExecutionGeneration != 1 {
		t.Fatal(ready)
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 1 {
		t.Fatal("missing ready world")
	}
}
func TestWorldBindingBeforeCapabilities(t *testing.T) {
	server, _, _ := taskTestServer(t)
	s, done := newWorldTestStream(t, server, []string{"gameagent.tasks.v1"})
	s.recvSent(t)
	s.recvSent(t)
	s.incoming <- &protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldRequest()}}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("out-of-order binding accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("bootstrap waited for buffering")
	}
	if len(server.WorldRegistry().ReadyWorlds()) != 0 {
		t.Fatal("out-of-order world ready")
	}
}
func TestWorldBindingValidationAndPausedActivation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*protocol.WorldBinding)
		code   string
	}{
		{"game", func(b *protocol.WorldBinding) { b.Scope.GameId = "else" }, "world_mismatch"},
		{"world", func(b *protocol.WorldBinding) { b.Scope.WorldId = "" }, "invalid_task_spec"},
		{"run", func(b *protocol.WorldBinding) { b.Scope.WorldRunId = "" }, "invalid_task_spec"},
		{"generation", func(b *protocol.WorldBinding) { b.Scope.ExecutionGeneration = 20 }, "generation_stale"},
		{"no entities", func(b *protocol.WorldBinding) { b.Entities = nil }, "invalid_task_spec"},
		{"duplicate entities", func(b *protocol.WorldBinding) { b.Entities = append(b.Entities, b.Entities[0]) }, "invalid_task_spec"},
		{"noncanonical", func(b *protocol.WorldBinding) { b.Entities[0].EntityId = " actor " }, "invalid_task_spec"},
		{"empty definition", func(b *protocol.WorldBinding) { b.Entities[0].DefinitionId = "" }, "invalid_task_spec"},
		{"checkpoint world", func(b *protocol.WorldBinding) {
			b.Checkpoint = &protocol.TaskCheckpointRef{Status: "absent", GameId: "sim", WorldId: "other"}
		}, "checkpoint_invalid"},
		{"unconfirmed", func(b *protocol.WorldBinding) {
			b.Checkpoint = &protocol.TaskCheckpointRef{Status: "unconfirmed", GameId: "sim", WorldId: "world", SchemaVersion: 1, Reason: "save_failed"}
		}, "checkpoint_unconfirmed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := taskTestServer(t)
			s, _ := taskHandshake(t, server)
			b := worldRequest()
			test.mutate(b)
			ready := bindTestWorld(t, s, b)
			if ready.Status != "paused" || ready.Error.GetCode() != test.code {
				t.Fatalf("ready=%v", ready)
			}
			if len(server.WorldRegistry().ReadyWorlds()) != 0 {
				t.Fatal("invalid binding ready")
			}
		})
	}
}
func TestWorldBindingOwnershipRebindDisconnectAndSnapshots(t *testing.T) {
	server, service, _ := taskTestServer(t)
	s, done := taskHandshake(t, server)
	ready := bindTestWorld(t, s, worldRequest())
	registry := server.WorldRegistry()
	world := task.WorldKey{GameID: "sim", WorldID: "world"}
	entry, ok := registry.Current(world)
	if !ok {
		t.Fatal("missing entry")
	}
	oldEnv, oldLanes := entry.Environment, entry.Lanes
	entry.Entities["actor"].DefinitionId = "mutated"
	delete(entry.Entities, "other")
	entry.Head.Clock.Tick = 900
	heads := registry.ReadyWorlds()
	heads[0].Clock.Tick = 999
	current, _ := registry.Current(world)
	if current.Entities["actor"].DefinitionId != "generic" || len(current.Entities) != 2 || current.Head.Clock.Tick != 10 {
		t.Fatal("snapshot aliases registry")
	}
	competitor, _ := taskHandshake(t, server)
	if conflict := bindTestWorld(t, competitor, worldRequest()); conflict.Status != "paused" || conflict.Error.GetCode() != "world_not_ready" {
		t.Fatal(conflict)
	}
	request := worldRequest()
	request.Scope.ExecutionGeneration = ready.Scope.ExecutionGeneration
	rebound := bindTestWorld(t, s, request)
	if rebound.Status != "ready" || rebound.Scope.ExecutionGeneration != 2 {
		t.Fatalf("rebind=%v", rebound)
	}
	select {
	case <-oldEnv.(*streamEnvironment).closed:
	default:
		t.Fatal("old action admission open")
	}
	if _, err := oldLanes.GetOrCreate(session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}); !errors.Is(err, session.ErrLaneClosed) {
		t.Fatal(err)
	}
	if err := registry.Guard(oldEnv.(*streamEnvironment), current.Head.Binding, func(WorldEntry) error { return nil }); !errors.Is(err, task.ErrGenerationStale) {
		t.Fatalf("stale stream: %v", err)
	}
	isolated := worldRequest()
	isolated.Scope.WorldId = "second"
	if other := bindTestWorld(t, competitor, isolated); other.Status != "ready" {
		t.Fatal(other)
	}
	close(s.incoming)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Current(world); ok {
		t.Fatal("disconnected world ready")
	}
	if len(registry.ReadyWorlds()) != 1 {
		t.Fatal("disconnect affected second world")
	}
	binding, _ := taskBindingFromProtocol(rebound.Scope)
	if _, err := service.UpdateClock(context.Background(), binding, task.Clock{ID: "game", Tick: 11, Sequence: 2}); !errors.Is(err, task.ErrWorldNotReady) {
		t.Fatalf("persistent world stayed ready: %v", err)
	}
}

func TestWorldClockAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*protocol.WorldClockUpdate)
		code   string
		paused bool
	}{
		{"success", func(u *protocol.WorldClockUpdate) { u.Clock.NowTick = 12; u.Clock.Sequence = 2 }, "", false},
		{"duplicate", func(u *protocol.WorldClockUpdate) {}, "", false},
		{"conflicting duplicate", func(u *protocol.WorldClockUpdate) { u.Clock.NowTick = 11 }, "clock_mismatch", true},
		{"conflicting rewind duplicate", func(u *protocol.WorldClockUpdate) { u.Clock.NowTick = 9 }, "clock_mismatch", true},
		{"sequence regression", func(u *protocol.WorldClockUpdate) { u.Clock.Sequence = 0 }, "clock_rewound", false},
		{"tick rewind", func(u *protocol.WorldClockUpdate) { u.Clock.NowTick = 9; u.Clock.Sequence = 2 }, "clock_rewound", false},
		{"clock ID", func(u *protocol.WorldClockUpdate) { u.Clock.ClockId = "wrong" }, "clock_mismatch", false},
		{"game", func(u *protocol.WorldClockUpdate) { u.Scope.GameId = "wrong" }, "world_mismatch", false},
		{"world", func(u *protocol.WorldClockUpdate) { u.Scope.WorldId = "wrong" }, "world_mismatch", false},
		{"run", func(u *protocol.WorldClockUpdate) { u.Scope.WorldRunId = "wrong" }, "generation_stale", false},
		{"generation", func(u *protocol.WorldClockUpdate) { u.Scope.ExecutionGeneration = 8 }, "generation_stale", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service, _ := taskTestServer(t)
			s, _ := taskHandshake(t, server)
			ready := bindTestWorld(t, s, worldRequest())
			u := &protocol.WorldClockUpdate{Scope: proto.Clone(ready.Scope).(*protocol.TaskScope), Clock: worldRequest().Clock}
			test.mutate(u)
			entry, _ := server.WorldRegistry().Current(task.WorldKey{GameID: "sim", WorldID: "world"})
			err := server.WorldRegistry().UpdateClock(context.Background(), entry.Environment.(*streamEnvironment), u)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if taskErrorToProtocol(err).GetCode() != test.code {
				t.Fatalf("error=%v want=%s", err, test.code)
			}
			current, ok := server.WorldRegistry().Current(entry.Head.Binding.World)
			if test.paused {
				if ok {
					t.Fatal("conflicting duplicate admission open")
				}
				return
			}
			if !ok {
				t.Fatal("valid binding lost")
			}
			want := int64(10)
			if test.name == "success" {
				want = 12
			}
			if current.Head.Clock.Tick != want {
				t.Fatal(current.Head.Clock)
			}
			// Exact persisted clock must match the snapshot; a stale snapshot fails Service authority.
			if _, err := service.UpdateClock(context.Background(), current.Head.Binding, current.Head.Clock); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestWorldBindingSaveBarrierAndDeactivationFailure(t *testing.T) {
	server, service, store := taskTestServer(t)
	s, done := taskHandshake(t, server)
	bindTestWorld(t, s, worldRequest())
	registry := server.WorldRegistry()
	world := task.WorldKey{GameID: "sim", WorldID: "world"}
	entry, _ := registry.Current(world)
	env := entry.Environment.(*streamEnvironment)
	if err := registry.SetSaveBarrier(env, entry.Head.Binding, true); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := registry.Guard(env, entry.Head.Binding, func(WorldEntry) error { called = true; return nil }); !errors.Is(err, task.ErrSaveInProgress) || called {
		t.Fatal(err)
	}
	if len(registry.ReadyWorlds()) != 0 {
		t.Fatal("save barrier dispatch admitted")
	}
	if err := registry.SetSaveBarrier(env, entry.Head.Binding, false); err != nil {
		t.Fatal(err)
	}
	if err := registry.Guard(env, entry.Head.Binding, func(snapshot WorldEntry) error {
		if snapshot.Head.Clock.Tick != 10 {
			t.Fatal(snapshot.Head)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A real closed database forces persistence failure after in-memory fencing.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	close(s.incoming)
	<-done
	if _, ok := registry.Current(world); ok {
		t.Fatal("failed deactivation left admission open")
	}
	if _, err := entry.Lanes.GetOrCreate(session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}); !errors.Is(err, session.ErrLaneClosed) {
		t.Fatal(err)
	}
	_ = service
}

func TestWorldBindingCatalogSnapshot(t *testing.T) {
	// A catalog entry snapshot retains its independent tool set and schema.
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{{Name: "inspect", InputSchemaJson: `{"type":"object"}`}}})
	if err != nil {
		t.Fatal(err)
	}
	view := catalog.Snapshot()
	available := view.Available()
	available[0].Name = "mutated"
	if view.Available()[0].Name != "inspect" {
		t.Fatal("catalog snapshot aliases returned tools")
	}
}
