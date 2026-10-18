package gateway

import (
	"context"
	"errors"
	"log"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
)

func WithExperienceHistory(store memory.HistoryStore) ServerOption {
	return func(s *Server) { s.experienceHistory = store }
}

// An experience acknowledgment confirms durable storage, independently of turn admission.
func (s *Server) recordExperience(env *streamEnvironment, conn agent.ConnectionContext, messageID string, event *protocol.GameEvent, negotiated bool) error {
	reject := func(code, message string) error {
		return env.send(eventAckMessage(messageID, event.GetEventId(), protocol.EventAckStatus_EVENT_ACK_STATUS_REJECTED, &protocol.Error{Code: code, Message: message}))
	}
	if !negotiated || s.experienceHistory == nil {
		return reject("experience_not_negotiated", "Confirmed experiences were not negotiated")
	}
	resolved, ackErr := resolveAgentTarget(conn, event)
	if ackErr != nil {
		return reject(ackErr.Code, ackErr.Message)
	}
	batch, err := memory.ExperienceHistoryBatch(resolved.Key, event)
	if err != nil {
		return reject("experience_invalid", err.Error())
	}
	if env.taskAuthority != nil {
		if ackErr := env.taskAuthority.RegisterEventTarget(event, resolved.Target); ackErr != nil {
			return reject(ackErr.Code, ackErr.Message)
		}
	}
	ctx, cancel := context.WithTimeout(env.stream.Context(), 2*time.Second)
	defer cancel()
	_, err = s.experienceHistory.AppendHistory(ctx, batch)
	if errors.Is(err, memory.ErrHistoryConflict) {
		return reject("experience_conflict", "The experience id already identifies different content")
	}
	if err != nil {
		log.Printf("experience persistence owner=%s error=%s", resolved.Key.DiagnosticID(), logSafeError(err))
		return reject("experience_write_failed", "The experience was not committed; retry the same receipt")
	}
	return env.send(eventAckMessage(messageID, event.EventId, protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED, nil))
}
