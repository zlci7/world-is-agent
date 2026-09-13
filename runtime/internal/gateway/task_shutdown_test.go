package gateway

import (
	"context"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestTaskShutdownForcesLongConnectedStream(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	defer listener.Close()
	transport := grpc.NewServer()
	defer transport.Stop()
	server := NewServer(nil)
	protocol.RegisterGameAgentGatewayServer(transport, server)
	go transport.Serve(listener)
	connection := dialGateway(t, context.Background(), listener)
	defer connection.Close()
	stream, err := protocol.NewGameAgentGatewayClient(connection).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{GameId: "sim", SessionId: "long_connection"}}}); err != nil {
		t.Fatal(err)
	}
	if ready := recvRuntimeMessage(t, stream).GetEnvironmentReady(); ready == nil {
		t.Fatal("missing EnvironmentReady")
	}
	if request := recvRuntimeMessage(t, stream).GetCapabilityRequest(); request == nil {
		t.Fatal("missing CapabilityRequest")
	}
	if err := stream.Send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{}}}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	done := make(chan struct{})
	go func() { ShutdownGRPC(transport); close(done) }()
	select {
	case <-done:
	case <-time.After(5500 * time.Millisecond):
		t.Fatal("long stream prevented bounded shutdown")
	}
	if elapsed := time.Since(started); elapsed > 5500*time.Millisecond {
		t.Fatalf("shutdown=%s", elapsed)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("connected stream survived Stop")
	}
}
