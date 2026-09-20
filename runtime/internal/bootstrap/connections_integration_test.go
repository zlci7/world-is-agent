package bootstrap_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/bootstrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestConnectRejectsBeforeReadyWithoutStartingCapabilityDiscovery(t *testing.T) {
	runtime, err := bootstrap.Open(t.TempDir(), stubEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()

	stream := openRuntimeStream(t, client, "rimworld", "early")
	message := recvRuntime(t, stream)
	if got := message.GetError(); got == nil || got.Code != "runtime_not_ready" {
		t.Fatalf("first response = %+v, want runtime_not_ready", message.Payload)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("second Recv error = %v, want EOF", err)
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

func TestConnectAdmissionUsesLoadedGameAfterNextGameIsConfigured(t *testing.T) {
	runtime := readyRuntime(t, "rimworld")
	if err := runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot(); got.LoadedGame.ID != "rimworld" || got.ConfiguredGame.ID != "stardew-valley" || !got.RestartRequired {
		t.Fatalf("snapshot after selection = %+v", got)
	}
	client, cleanup := runtimeGatewayClient(t, runtime)
	defer cleanup()

	stream, cancel := connectRuntimeReady(t, client, "rimworld", "loaded-game")
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
