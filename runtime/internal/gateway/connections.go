package gateway

import "sort"

type ConnectionInfo struct {
	ConnectionID   string `json:"connection_id"`
	GameID         string `json:"game_id"`
	AdapterID      string `json:"adapter_id"`
	AdapterVersion string `json:"adapter_version"`
	GameVersion    string `json:"game_version"`
	SessionID      string `json:"session_id"`
}

func WithConnectionReady(fn func()) ServerOption {
	return func(s *Server) { s.connectionReady = fn }
}

// Connections reports completed capability bootstraps, not world readiness.
func (s *Server) Connections() []ConnectionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ConnectionInfo, 0, len(s.connections))
	for connection := range s.connections {
		select {
		case <-connection.transport.closed:
			continue
		default:
		}
		select {
		case <-connection.transport.stream.Context().Done():
			continue
		default:
		}
		hello := connection.hello
		result = append(result, ConnectionInfo{ConnectionID: connection.id, GameID: hello.GameId, AdapterID: hello.AdapterId, AdapterVersion: hello.AdapterVersion, GameVersion: hello.GameVersion, SessionID: hello.SessionId})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ConnectionID < result[j].ConnectionID })
	return result
}
