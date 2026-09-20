package gateway

import (
	"context"
	"sync"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
)

// StreamGroup owns handlers from admission through their final persistence.
// Returning the RPC on cancellation releases gRPC's blocked Recv/Send calls;
// Close then waits for their handler and its cleanup before releasing stores.
type StreamGroup struct {
	mu      sync.Mutex
	stopped bool
	active  map[*ownedStream]struct{}
	workers sync.WaitGroup
}

type ownedStream struct {
	protocol.GameAgentGateway_ConnectServer
	ctx    context.Context
	cancel context.CancelFunc
	sends  sync.WaitGroup
}

func (s *ownedStream) beginSend() func()        { s.sends.Add(1); return s.sends.Done }
func (s *ownedStream) Context() context.Context { return s.ctx }
func (s *ownedStream) Send(msg *protocol.RuntimeMessage) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.GameAgentGateway_ConnectServer.Send(msg)
}
func (s *ownedStream) Recv() (*protocol.AdapterMessage, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	msg, err := s.GameAgentGateway_ConnectServer.Recv()
	if canceled := s.ctx.Err(); canceled != nil {
		return nil, canceled
	}
	return msg, err
}
func (g *StreamGroup) Run(stream protocol.GameAgentGateway_ConnectServer, handler func(protocol.GameAgentGateway_ConnectServer) error) error {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return context.Canceled
	}
	ctx, cancel := context.WithCancel(stream.Context())
	retired := make(chan struct{})
	var retireOnce sync.Once
	owned := &ownedStream{GameAgentGateway_ConnectServer: stream, ctx: ctx, cancel: func() { cancel(); retireOnce.Do(func() { close(retired) }) }}
	if g.active == nil {
		g.active = make(map[*ownedStream]struct{})
	}
	g.active[owned] = struct{}{}
	g.workers.Add(1)
	g.mu.Unlock()
	defer cancel()
	result := make(chan error, 1)
	go func() {
		defer g.workers.Done()
		defer func() { g.mu.Lock(); delete(g.active, owned); g.mu.Unlock() }()
		err := handler(owned)
		result <- err
		owned.sends.Wait()
	}()
	select {
	case err := <-result:
		return err
	case <-retired:
		return ctx.Err()
	}
}
func (g *StreamGroup) Stop() {
	g.mu.Lock()
	g.stopped = true
	for stream := range g.active {
		stream.cancel()
	}
	g.mu.Unlock()
}
func (g *StreamGroup) Close() { g.Stop(); g.workers.Wait() }
