package gateway

import (
	"context"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
)

func TestConnectionsOmitsClosedAndCanceledTransports(t *testing.T) {
	server := NewServer(nil)
	activeContext := context.Background()
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()

	active := snapshotConnection("active", "active-session", activeContext)
	closed := snapshotConnection("closed", "closed-session", context.Background())
	closed.transport.close()
	canceled := snapshotConnection("canceled", "canceled-session", canceledContext)
	server.connections[active] = struct{}{}
	server.connections[closed] = struct{}{}
	server.connections[canceled] = struct{}{}

	connections := server.Connections()
	if len(connections) != 1 || connections[0].ConnectionID != active.id {
		t.Fatalf("Connections() = %+v, want only active connection", connections)
	}
}

func TestConnectionsKeepsReplacementWhenOldConnectionCleansUp(t *testing.T) {
	server := NewServer(nil)
	oldConnection := snapshotConnection("old", "same-session", context.Background())
	replacement := snapshotConnection("replacement", "same-session", context.Background())
	server.connections[oldConnection] = struct{}{}
	server.connections[replacement] = struct{}{}

	oldConnection.transport.close()
	delete(server.connections, oldConnection)

	connections := server.Connections()
	if len(connections) != 1 || connections[0].ConnectionID != replacement.id {
		t.Fatalf("Connections() = %+v, want replacement connection", connections)
	}
}

func snapshotConnection(id, sessionID string, ctx context.Context) *worldConnection {
	stream := &worldTestStream{ctx: ctx}
	return &worldConnection{
		id: id,
		hello: &protocol.AdapterHello{
			GameId:         "fake-game",
			AdapterId:      "fake-adapter",
			AdapterVersion: "1.0.0",
			GameVersion:    "2.0.0",
			SessionId:      sessionID,
		},
		transport: newStreamEnvironment(stream),
	}
}
