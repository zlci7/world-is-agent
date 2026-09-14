package gateway

import (
	"context"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"google.golang.org/protobuf/proto"
)

type pendingTaskControl struct {
	request *protocol.TaskControlRequest
	result  chan string
	done    chan struct{}
	status  string
}

func (e *streamEnvironment) ReleaseTask(ctx context.Context, record task.Record) error {
	if e.taskAuthority == nil {
		return task.ErrWorldNotReady
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return e.retryTaskWork(cleanupCtx, func() error {
		head, _, ready := e.taskAuthority.Current()
		if !ready {
			return task.ErrWorldNotReady
		}
		current, err := e.taskAuthority.registry.service.Read(cleanupCtx, record.Owner, record.ID)
		if err != nil {
			return err
		}
		return e.releaseTaskControl(cleanupCtx, head.Binding, current)
	})
}

func (e *historyMaintenanceEnvironment) ReleaseTask(ctx context.Context, record task.Record) error {
	if releaser, ok := e.Environment.(interface {
		ReleaseTask(context.Context, task.Record) error
	}); ok {
		return releaser.ReleaseTask(ctx, record)
	}
	return nil
}

func (e *streamEnvironment) releaseTaskControl(ctx context.Context, binding task.Binding, record task.Record) error {
	if record.Result == nil {
		return nil
	}
	for _, op := range record.Operations {
		if op.Status == task.OperationStatusNotSent {
			continue
		}
		if op.Binding.World != binding.World || op.Binding.RunID != binding.RunID {
			continue
		}
		done := false
		for _, c := range record.Cleanup {
			done = done || c.OperationID == op.ID
		}
		if done {
			continue
		}
		req := &protocol.TaskControlRequest{Scope: taskScopeToProtocol(binding), TaskId: record.ID, OperationId: op.ID, RequestId: newMessageID("task_release"), Reason: "task_terminal"}
		pending := &pendingTaskControl{request: req, result: make(chan string, 1), done: make(chan struct{})}
		e.pendingMu.Lock()
		prior, exists := e.taskReleases[op.ID]
		if exists {
			pending = prior
		} else {
			e.taskReleases[op.ID] = pending
			e.pendingControls[req.RequestId] = pending
		}
		e.pendingMu.Unlock()
		controlCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		status := task.CleanupStatusUnconfirmed
		if exists {
			select {
			case <-pending.done:
				status = pending.status
			case <-controlCtx.Done():
				cancel()
				return controlCtx.Err()
			}
		} else {
			err := e.sendContext(controlCtx, &protocol.RuntimeMessage{MessageId: req.RequestId, Payload: &protocol.RuntimeMessage_TaskControl{TaskControl: req}})
			if err == nil {
				select {
				case status = <-pending.result:
				case <-controlCtx.Done():
				case <-e.closed:
				}
			}
			e.pendingMu.Lock()
			delete(e.pendingControls, req.RequestId)
			pending.status = status
			close(pending.done)
			e.pendingMu.Unlock()
		}
		cancel()
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		// Disconnect fences admission before waiting lanes, and deactivates the
		// durable Head afterwards. The current lane can still record its release.
		err := e.taskAuthority.registry.service.RecordCleanup(persistCtx, binding, record.ID, task.Cleanup{OperationID: op.ID, Status: status, Reason: "task_terminal"})
		persistCancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *streamEnvironment) resolveTaskControl(result *protocol.TaskControlResult) bool {
	if e.taskAuthority == nil {
		return false
	}
	head, _, ready := e.taskAuthority.Current()
	if !ready || !proto.Equal(result.GetScope(), taskScopeToProtocol(head.Binding)) {
		return false
	}
	if result.Status != task.CleanupStatusReleased && result.Status != task.CleanupStatusHandedOff && result.Status != task.CleanupStatusUnconfirmed {
		return false
	}
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	p, ok := e.pendingControls[result.RequestId]
	if !ok || result.TaskId != p.request.TaskId || result.OperationId != p.request.OperationId || !proto.Equal(result.Scope, p.request.Scope) {
		return false
	}
	delete(e.pendingControls, result.RequestId)
	p.result <- result.Status
	return true
}
