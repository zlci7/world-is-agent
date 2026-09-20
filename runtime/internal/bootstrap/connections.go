package bootstrap

import (
	"fmt"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
)

type ConnectionError struct {
	Code           string `json:"code"`
	ExpectedGameID string `json:"expected_game_id"`
	ReceivedGameID string `json:"received_game_id"`
	Message        string `json:"message"`
}

// Connect chooses one complete runtime bundle after Hello. The bundle remains
// fixed for this stream, including when the next launch's game is changed.
func (r *Runtime) Connect(stream protocol.GameAgentGateway_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return fmt.Errorf("expected adapter hello as first message")
	}
	r.mu.Lock()
	bundle := r.bundle
	expected, code, message := "", "", ""
	if r.snapshot.LoadedGame != nil {
		expected = r.snapshot.LoadedGame.ID
	}
	if !r.snapshot.Ready || bundle == nil {
		code, message = "runtime_not_ready", "finish Runtime setup before connecting the game"
	} else if hello.GameId != expected {
		code, message = "game_mismatch", fmt.Sprintf("Runtime loaded game %q; adapter reported %q", expected, hello.GameId)
	}
	if code != "" {
		r.lastConnectionError = &ConnectionError{Code: code, ExpectedGameID: expected, ReceivedGameID: hello.GameId, Message: message}
	}
	r.mu.Unlock()
	if code != "" {
		return stream.Send(&protocol.RuntimeMessage{CorrelationId: first.MessageId, Payload: &protocol.RuntimeMessage_Error{Error: &protocol.Error{Code: code, Message: message}}})
	}
	return bundle.gateway.Connect(&helloStream{GameAgentGateway_ConnectServer: stream, first: first})
}

type helloStream struct {
	protocol.GameAgentGateway_ConnectServer
	first *protocol.AdapterMessage
}

func (s *helloStream) Recv() (*protocol.AdapterMessage, error) {
	if s.first != nil {
		first := s.first
		s.first = nil
		return first, nil
	}
	return s.GameAgentGateway_ConnectServer.Recv()
}
