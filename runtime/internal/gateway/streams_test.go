package gateway

import (
	"context"
	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"testing"
	"time"
)

type retiringSendStream struct {
	protocol.GameAgentGateway_ConnectServer
	ctx                        context.Context
	started, release, finished chan struct{}
}

func (s *retiringSendStream) Context() context.Context { return s.ctx }
func (s *retiringSendStream) Send(*protocol.RuntimeMessage) error {
	close(s.started)
	<-s.release
	close(s.finished)
	return nil
}
func TestStreamRetirementWaitsForTransportSend(t *testing.T) {
	group := &StreamGroup{}
	raw := &retiringSendStream{ctx: context.Background(), started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	rpc := make(chan error, 1)
	go func() {
		rpc <- group.Run(raw, func(s protocol.GameAgentGateway_ConnectServer) error {
			env := newStreamEnvironment(s)
			send := make(chan error, 1)
			go func() { send <- env.send(&protocol.RuntimeMessage{}) }()
			<-raw.started
			<-s.Context().Done()
			env.close()
			<-send
			return nil
		})
	}()
	<-raw.started
	closed := make(chan struct{})
	go func() { group.Close(); close(closed) }()
	<-rpc
	select {
	case <-closed:
		t.Error("retirement returned while transport send remained active")
	case <-time.After(20 * time.Millisecond):
	}
	close(raw.release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("retirement did not drain send")
	}
	<-raw.finished
}
