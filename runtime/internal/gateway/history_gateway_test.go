package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/llm/fake"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func requestToolResultCode(messages []model.Message, code string) bool {
	for _, message := range messages {
		for _, result := range message.ToolResults {
			if result.Code == code {
				return true
			}
		}
	}
	return false
}

type shutdownHistoryStore struct {
	memory.HistoryStore
	started chan struct{}
	release chan struct{}
}

type blockedHistoryStream struct {
	protocol.GameAgentGateway_ConnectServer
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (s *blockedHistoryStream) Send(*protocol.RuntimeMessage) error {
	close(s.started)
	<-s.release
	close(s.finished)
	return nil
}

func TestStreamEnvironmentCloseUnblocksActiveAndQueuedSends(t *testing.T) {
	stream := &blockedHistoryStream{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	defer func() { close(stream.release); <-stream.finished }()
	env := newStreamEnvironment(stream)
	results := make(chan error, 2)
	go func() { results <- env.send(&protocol.RuntimeMessage{}) }()
	<-stream.started
	go func() { results <- env.send(&protocol.RuntimeMessage{}) }()
	env.close()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, io.EOF) {
				t.Fatalf("closed send error = %v, want EOF", err)
			}
		case <-time.After(time.Second):
			t.Fatal("closed stream kept a sender blocked")
		}
	}
	if err := env.send(&protocol.RuntimeMessage{}); !errors.Is(err, io.EOF) {
		t.Fatalf("send after close error = %v", err)
	}
	env.close()
}

func (s *shutdownHistoryStore) AppendHistory(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
	close(s.started)
	<-s.release
	return s.HistoryStore.AppendHistory(ctx, batch)
}

func TestConnectShutdownWaitsForTerminalHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := &shutdownHistoryStore{HistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{}), started: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(store.release)
		}
	}()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t), agent.WithHistoryStore(store))
	protocol.RegisterGameAgentGatewayServer(server, NewServer(loop))
	startGatewayServer(t, server, listener)
	conn := dialGateway(t, ctx, listener)
	defer conn.Close()
	client := protocol.NewGameAgentGatewayClient(conn)
	stream := connectReadyStreamWithCapabilities(t, ctx, client, "session:shutdown", capabilityListMessage)
	if request := sendAcceptedNPCEvent(t, stream, 1, "npc:Linus", "Linus").GetObserve(); request == nil {
		t.Fatal("turn did not begin")
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.started:
	case <-ctx.Done():
		t.Fatal("terminal write did not start")
	}
	stopped := make(chan struct{})
	go func() { server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("server returned before terminal persistence finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.release)
	released = true
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("server did not finish after terminal persistence")
	}
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world:test", EntityID: "npc:Linus"}
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 1, Bytes: 1 << 20})
	if err != nil || len(page.Sources) != 1 {
		t.Fatalf("terminal write not visible at shutdown: %+v %v", page, err)
	}
}

func assertGatewayHistory(t *testing.T, req model.Request, eventID string) {
	t.Helper()
	for _, message := range req.Messages {
		const marker = "[History]\n"
		index := strings.Index(message.Content, marker)
		if index < 0 {
			continue
		}
		var history struct {
			Sources []struct {
				SourceID string              `json:"source_id"`
				Content  memory.HistoryBatch `json:"content"`
			} `json:"sources"`
		}
		if err := json.NewDecoder(strings.NewReader(message.Content[index+len(marker):])).Decode(&history); err != nil {
			t.Fatal(err)
		}
		if len(history.Sources) != 1 || history.Sources[0].SourceID == "" {
			t.Fatalf("invalid history sources: %+v", history)
		}
		batch := history.Sources[0].Content
		if batch.Event.ID != eventID || batch.Terminal.Status != "completed" || len(batch.Steps) != 1 || len(batch.Steps[0].Executions) != 1 {
			t.Fatalf("terminal source missing: %+v", batch)
		}
		execution := batch.Steps[0].Executions[0]
		if execution.Call.Arguments["text"] != "gateway memory line" || execution.ActionID == "" || execution.ActionResult.GetActionId() != execution.ActionID || execution.ActionResult.GetStatus() != protocol.ActionStatus_ACTION_STATUS_SUCCEEDED {
			t.Fatalf("actual receipt missing: %+v", execution)
		}
		return
	}
	t.Fatal("final model request omitted committed History")
}
