package gateway

import (
	"google.golang.org/grpc"
	"time"
)

// ShutdownGRPC gives active RPCs at most five seconds to leave gracefully.
func ShutdownGRPC(server *grpc.Server) {
	done := make(chan struct{})
	go func() { server.GracefulStop(); close(done) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		server.Stop()
		<-done
	}
}
