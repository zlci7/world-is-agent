package gateway

import (
	"context"
	"errors"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/session"
)

// This error is returned only before the transport goroutine is launched.
// All failures after launch are conservatively treated as potentially sent.
type transportNotStartedError struct{ error }

func (e transportNotStartedError) Unwrap() error { return e.error }

func (e *streamEnvironment) recordUnsentTaskAction(ctx context.Context, req *protocol.ActionRequest, sendErr error) error {
	if req.GetTaskSource() == nil || e.taskAuthority == nil {
		return sendErr
	}
	source := req.TaskSource
	binding, err := taskBindingFromProtocol(source.Scope)
	if err != nil {
		return errors.Join(sendErr, err)
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	owner := session.AgentSessionKey{GameID: binding.World.GameID, WorldID: req.WorldId, EntityID: req.EntityId}
	err = e.taskAuthority.registry.service.MarkOperationNotSent(persistCtx, binding, owner, source.TaskId, source.OperationId, req.ActionId)
	return errors.Join(sendErr, err)
}
