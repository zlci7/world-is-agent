package gateway

import "gameagent/runtime/internal/task"

// ConsoleWorldSnapshot contains only defensive values, never execution handles.
type ConsoleWorldSnapshot struct {
	Head          task.Head
	Ready, Saving bool
	Names         map[string]string
}

func (s *Server) ConsoleWorld(world task.WorldKey) ConsoleWorldSnapshot {
	if s.worlds == nil {
		return ConsoleWorldSnapshot{}
	}
	slot := s.worlds.slot(world, false)
	if slot == nil {
		return ConsoleWorldSnapshot{}
	}
	if !slot.mu.TryLock() {
		return ConsoleWorldSnapshot{}
	}
	defer slot.mu.Unlock()
	result := ConsoleWorldSnapshot{Head: slot.head, Saving: slot.saving, Names: map[string]string{}}
	if slot.owner == nil {
		return result
	}
	select {
	case <-slot.owner.transport.closed:
		return result
	default:
	}
	select {
	case <-slot.owner.transport.stream.Context().Done():
		return result
	default:
	}
	result.Ready = slot.ready && !slot.saving && !slot.failed
	for id, entity := range slot.entities {
		result.Names[id] = entity.GetDisplayName()
	}
	return result
}
