package bootstrap_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"path/filepath"
)

func TestConnectRejectsBeforeReadyWithoutStartingCapabilityDiscovery(t *testing.T) {
	runtime, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()

	previousID := ""
	for attempt := 0; attempt < 2; attempt++ {
		stream := openRuntimeStream(t, client, "rimworld", "early")
		message := recvRuntime(t, stream)
		if got := message.GetError(); got == nil || got.Code != "runtime_not_ready" {
			t.Fatalf("first response = %+v, want runtime_not_ready", message.Payload)
		}
		if message.MessageId == "" || message.MessageId == previousID || message.MessageId == "hello-early" || message.CorrelationId != "hello-early" {
			t.Fatalf("rejection must have its own unique ID and correlate to Hello: %+v", message)
		}
		previousID = message.MessageId
		if _, err := stream.Recv(); err != io.EOF {
			t.Fatalf("second Recv error = %v, want EOF", err)
		}
	}
	if got := runtime.Snapshot(); got.ConnectionCount != 0 || got.LastConnectionError == nil || got.LastConnectionError.Code != "runtime_not_ready" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestConnectRejectsWrongGameBeforeCapabilityRequestAndPreservesValidConnection(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()
	valid, validCancel := connectRuntimeReady(t, client, "rimworld", "valid")
	defer validCancel()
	waitConnectionCount(t, runtime, 1)

	rejected := openRuntimeStream(t, client, "stardew-valley", "wrong")
	message := recvRuntime(t, rejected)
	if got := message.GetError(); got == nil || got.Code != "game_mismatch" {
		t.Fatalf("first response = %+v, want game_mismatch", message.Payload)
	}
	if message.MessageId == "" || message.MessageId == "hello-wrong" || message.CorrelationId != "hello-wrong" {
		t.Fatalf("rejection must have its own ID and correlate to Hello: %+v", message)
	}
	if _, err := rejected.Recv(); err != io.EOF {
		t.Fatalf("second Recv error = %v, want EOF", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.ConnectionCount != 1 || snapshot.Adapters[0].SessionID != "valid" {
		t.Fatalf("rejection changed valid connection: %+v", snapshot)
	}
	if snapshot.LastConnectionError == nil || snapshot.LastConnectionError.ExpectedGameID != "rimworld" || snapshot.LastConnectionError.ReceivedGameID != "stardew-valley" {
		t.Fatalf("connection error = %+v", snapshot.LastConnectionError)
	}

	_ = valid
}

func TestConnectCountsOnlyCompletedLiveHandshakesAndClearsLastErrorOnSuccess(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()

	rejected := openRuntimeStream(t, client, "stardew-valley", "wrong")
	_ = recvRuntime(t, rejected)
	_, _ = rejected.Recv()

	failedContext, failedCancel := context.WithCancel(context.Background())
	failed, err := client.Connect(failedContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Send(runtimeHello("rimworld", "failed")); err != nil {
		t.Fatal(err)
	}
	if recvRuntime(t, failed).GetEnvironmentReady() == nil || recvRuntime(t, failed).GetCapabilityRequest() == nil {
		t.Fatal("accepted Hello did not reach capability discovery")
	}
	failedCancel()
	waitConnectionCount(t, runtime, 0)

	first, cancelFirst := connectRuntimeReady(t, client, "rimworld", "one")
	defer cancelFirst()
	second, cancelSecond := connectRuntimeReady(t, client, "rimworld", "two")
	defer cancelSecond()
	waitConnectionCount(t, runtime, 2)
	snapshot := runtime.Snapshot()
	if snapshot.LastConnectionError != nil {
		t.Fatalf("successful handshake retained error: %+v", snapshot.LastConnectionError)
	}

	cancelFirst()
	waitConnectionCount(t, runtime, 1)
	if got := runtime.Snapshot().Adapters; len(got) != 1 || got[0].SessionID != "two" {
		t.Fatalf("after first cancel adapters = %+v", got)
	}
	cancelSecond()
	waitConnectionCount(t, runtime, 0)
	_, _ = first.Recv()
	_, _ = second.Recv()
}

func TestConnectAdmissionUsesAppliedGame(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	if err := runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot(); got.LoadedGame.ID != "stardew-valley" || got.ConfiguredGame.ID != "stardew-valley" || got.RestartRequired {
		t.Fatalf("snapshot after selection = %+v", got)
	}
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()

	stream, cancel := connectRuntimeReady(t, client, "stardew-valley", "loaded-game")
	defer cancel()
	waitConnectionCount(t, runtime, 1)
	_ = stream
}

func readyRuntime(t *testing.T, gameID string) *bootstrap.Runtime {
	t.Helper()
	root := t.TempDir()
	prepareSelected(t, root, gameID)
	writeConfig(t, root, "model.json", validModelConfig)
	runtime, err := bootstrap.Open(root, stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.Ready() {
		runtime.Close()
		t.Fatalf("Runtime is not ready: %+v", runtime.Snapshot())
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func runtimeGatewayClient(t *testing.T, runtime *bootstrap.Runtime) (protocol.GameAgentGatewayClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	protocol.RegisterGameAgentGatewayServer(server, runtime)
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	connection, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		server.Stop()
		t.Fatal(err)
	}
	return protocol.NewGameAgentGatewayClient(connection), func() {
		_ = connection.Close()
		server.Stop()
		<-serveDone
		_ = listener.Close()
	}
}

func openRuntimeStream(t *testing.T, client protocol.GameAgentGatewayClient, gameID, sessionID string) protocol.GameAgentGateway_ConnectClient {
	t.Helper()
	stream, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(runtimeHello(gameID, sessionID)); err != nil {
		t.Fatal(err)
	}
	return stream
}

func connectRuntimeReady(t *testing.T, client protocol.GameAgentGatewayClient, gameID, sessionID string) (protocol.GameAgentGateway_ConnectClient, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Connect(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := stream.Send(runtimeHello(gameID, sessionID)); err != nil {
		cancel()
		t.Fatal(err)
	}
	if message := recvRuntime(t, stream); message.GetEnvironmentReady() == nil {
		cancel()
		t.Fatalf("first response = %+v, want EnvironmentReady", message.Payload)
	}
	request := recvRuntime(t, stream)
	if request.GetCapabilityRequest() == nil {
		cancel()
		t.Fatalf("second response = %+v, want CapabilityRequest", request.Payload)
	}
	if err := stream.Send(&protocol.AdapterMessage{MessageId: "caps-" + sessionID, CorrelationId: request.MessageId, Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{Revision: 1}}}); err != nil {
		cancel()
		t.Fatal(err)
	}
	return stream, cancel
}

func runtimeHello(gameID, sessionID string) *protocol.AdapterMessage {
	return &protocol.AdapterMessage{MessageId: "hello-" + sessionID, Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{
		GameId: gameID, AdapterId: "test-adapter", AdapterVersion: "1.0.0", GameVersion: "1.0.0", ProtocolVersion: "v1alpha2", SessionId: sessionID,
	}}}
}

func recvRuntime(t *testing.T, stream protocol.GameAgentGateway_ConnectClient) *protocol.RuntimeMessage {
	t.Helper()
	message, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func waitConnectionCount(t *testing.T, runtime *bootstrap.Runtime, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.Snapshot().ConnectionCount == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("connection count = %d, want %d; snapshot = %+v", runtime.Snapshot().ConnectionCount, want, runtime.Snapshot())
}

func TestLiveReplacementRetiresIdleAndPartialHandshakeStreams(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()
	idle, cancel := connectRuntimeReady(t, client, "rimworld", "idle")
	defer cancel()
	waitConnectionCount(t, runtime, 1)
	partial := openRuntimeStream(t, client, "rimworld", "partial")
	recvRuntime(t, partial)
	recvRuntime(t, partial)
	if err := runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []protocol.GameAgentGateway_ConnectClient{idle, partial} {
		done := make(chan error, 1)
		go func() { _, err := stream.Recv(); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("retired stream accepted messages")
			}
		case <-time.After(time.Second):
			t.Fatal("retired stream remained open")
		}
	}
	replacement, stop := connectRuntimeReady(t, client, "stardew-valley", "replacement")
	defer stop()
	_ = replacement
	waitConnectionCount(t, runtime, 1)
}

func TestReadyModelReplacementRequiresNewCredentialAndPreservesReadyOnValidationFailure(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	before := runtime.HistoryStore()
	if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "fake", Model: "second", APIKey: "test-new-key"}); err != nil {
		t.Fatal(err)
	}
	if !runtime.Ready() || runtime.Snapshot().Model.Model != "second" || runtime.HistoryStore() == before {
		t.Fatal("model replacement not published")
	}
	for _, setup := range []bootstrap.ModelSetup{{Provider: "fake", Model: "third"}, {Provider: "unsupported", APIKey: "test-bad-key"}} {
		if err := runtime.ApplyModelConfiguration(setup); err == nil {
			t.Fatal("invalid candidate accepted")
		}
		if !runtime.Ready() || runtime.Snapshot().Model.Model != "second" {
			t.Fatal("invalid candidate disrupted runtime")
		}
	}
}

func TestLiveReplacementCancelsActiveTurnAndPreservesTerminalHistory(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()
	stream, cancel := connectRuntimeReady(t, client, "rimworld", "active")
	defer cancel()
	event := &protocol.GameEvent{EventId: "live-turn", EventType: "interaction", WorldId: "test-world", TargetEntityId: "colonist", Entities: []*protocol.EntityRef{{EntityId: "colonist", EntityType: "colonist", DefinitionId: "archetype:colonist"}}}
	if err := stream.Send(&protocol.AdapterMessage{MessageId: "event", Payload: &protocol.AdapterMessage_Event{Event: event}}); err != nil {
		t.Fatal(err)
	}
	if recvRuntime(t, stream).GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("event not accepted")
	}
	if recvRuntime(t, stream).GetObserve() == nil {
		t.Fatal("turn did not start")
	}
	started := time.Now()
	if err := runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("switch waited for observe timeout")
	}
	if err := runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	store := runtime.HistoryStore()
	snapshot, err := store.BeginHistorySnapshot(context.Background(), session.AgentSessionKey{GameID: "rimworld", WorldID: "test-world", EntityID: "colonist"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 10, Bytes: 1 << 20})
	if err != nil || len(page.Sources) != 1 {
		t.Fatalf("terminal history missing: %+v %v", page, err)
	}
	if page.Sources[0].Batch.Event.ID != "live-turn" || page.Sources[0].Batch.Terminal.Status == "" {
		t.Fatal("terminal history not retained")
	}
}

func TestLiveModelReplacementUsesNewProviderForNewStream(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()
	type request struct {
		Model string `json:"model"`
		Key   string
	}
	requests := make(chan request, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		_ = json.NewDecoder(r.Body).Decode(&body)
		body.Key = r.Header.Get("Authorization")
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer endpoint.Close()
	var previous protocol.GameAgentGateway_ConnectClient
	for _, name := range []string{"first", "second"} {
		if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "deepseek", Model: name, APIKey: "test-" + name, BaseURL: endpoint.URL}); err != nil {
			t.Fatal(err)
		}
		if previous != nil {
			for {
				msg, err := previous.Recv()
				if err != nil {
					break
				}
				if msg.GetObserve() != nil {
					t.Fatal("retired generation requested an observation")
				}
			}
		}
		stream, cancel := connectRuntimeReady(t, client, "rimworld", name)
		defer cancel()
		previous = stream
		event := &protocol.GameEvent{EventId: name, EventType: "interaction", WorldId: "model-world", TargetEntityId: "colonist", Entities: []*protocol.EntityRef{{EntityId: "colonist", EntityType: "colonist", DefinitionId: "archetype:colonist"}}}
		if err := stream.Send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Event{Event: event}}); err != nil {
			t.Fatal(err)
		}
		recvRuntime(t, stream)
		observe := recvRuntime(t, stream)
		if observe.GetObserve() == nil {
			t.Fatal("turn did not observe")
		}
		if err := stream.Send(&protocol.AdapterMessage{CorrelationId: observe.MessageId, Payload: &protocol.AdapterMessage_Observation{Observation: &protocol.Observation{WorldId: "model-world", EntityId: "colonist", Revision: 1}}}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-requests:
			if got.Model != name || got.Key != "Bearer test-"+name {
				t.Fatalf("provider generation mismatch: %s", got.Model)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("active model was not called")
		}
	}
}

func TestInitialReplacementFailureRecoversStreamAdmission(t *testing.T) {
	for _, recovery := range []string{"configure", "select-game"} {
		t.Run(recovery, func(t *testing.T) {
			root := t.TempDir()
			prepareSelected(t, root, "stardew-valley")
			writeConfig(t, root, "model.json", validModelConfig)
			cfg, err := agent.LoadConfigFile(filepath.Join(root, "config", "games", "stardew-valley", "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			options := cfg.Task.StoreOptions
			options.Path = dataroot.ResolvePath(root, cfg.Task.DBPath)
			conflict, err := task.OpenSQLiteStore(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer conflict.Close()
			runtime, err := bootstrap.Open(root, stubEnv(nil))
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			if got := runtime.Snapshot(); got.Ready || got.ReasonCode != "initialization_failed" {
				t.Fatalf("startup did not expose task lock conflict: %+v", got)
			}
			if err := runtime.ApplyModelConfiguration(bootstrap.ModelSetup{Provider: "fake", Model: "candidate", APIKey: "test-candidate-key"}); err == nil {
				t.Fatal("replacement ignored task lock conflict")
			}
			if got := runtime.Snapshot(); got.State != bootstrap.StateNeedsConfiguration || got.Ready {
				t.Fatalf("failure is not recoverable: %+v", got)
			}
			if err := conflict.Close(); err != nil {
				t.Fatal(err)
			}
			if recovery == "configure" {
				err = runtime.Configure()
			} else {
				err = runtime.SelectGame("stardew-valley")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !runtime.Ready() {
				t.Fatal(runtime.Reason())
			}
			client, cleanup := runtimeGatewayClient(t, runtime)
			defer cleanup()
			stream, cancel := connectRuntimeReady(t, client, "stardew-valley", "recovered")
			defer cancel()
			_ = stream
			waitConnectionCount(t, runtime, 1)
		})
	}
}
