package gateway

import (
	"context"
	"errors"
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
	err     error
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
		return e.commitTaskResult(cleanupCtx, head.Binding, current)
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
	return e.releaseTaskOperations(ctx, binding, record, "task_terminal")
}

func (e *streamEnvironment) releaseExecutionControl(ctx context.Context, exec task.ExecutionContext) error {
	head, _, ready := e.taskAuthority.Current()
	if !ready || head.Binding != exec.Binding {
		return nil
	}
	record, err := e.taskAuthority.registry.service.Read(ctx, exec.Owner, exec.TaskID)
	if err != nil {
		return err
	}
	if record.Result != nil {
		return e.commitTaskResult(ctx, head.Binding, record)
	}
	if record.State == task.StateWaiting {
		return nil
	}
	return e.releaseTaskOperations(ctx, head.Binding, record, "execution_ended")
}

var errTaskControlObsolete = errors.New("task control no longer required")

func (e *streamEnvironment) releaseTaskOperations(ctx context.Context, binding task.Binding, record task.Record, reason string) error {
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
		req := &protocol.TaskControlRequest{Scope: taskScopeToProtocol(binding), TaskId: record.ID, OperationId: op.ID, RequestId: newMessageID("task_release"), Reason: reason}
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
		controlCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
			message := &protocol.RuntimeMessage{MessageId: req.RequestId, Payload: &protocol.RuntimeMessage_TaskControl{TaskControl: req}}
			err := e.sendGuarded(controlCtx, message, func(send func() error) error {
				// Recheck eligibility at transport handoff, not only before waiting for sendSlot.
				_, epoch, _ := e.taskAuthority.Current()
				return e.taskAuthority.Guard(binding, epoch, func(task.Head) error {
					current, err := e.taskAuthority.registry.service.Read(controlCtx, record.Owner, record.ID)
					if err != nil {
						return err
					}
					if current.Result == nil && (reason == "task_terminal" || current.State == task.StateWaiting) {
						return errTaskControlObsolete
					}
					if current.Result != nil {
						req.Reason = "task_terminal"
					}
					for _, cleanup := range current.Cleanup {
						if cleanup.OperationID == op.ID {
							return errTaskControlObsolete
						}
					}
					for _, operation := range current.Operations {
						if operation.ID == op.ID && operation.Status == task.OperationStatusNotSent {
							return errTaskControlObsolete
						}
					}
					return send()
				})
			})
			if err == nil {
				select {
				case status = <-pending.result:
				case <-controlCtx.Done():
				case <-e.closed:
				}
			}
			e.pendingMu.Lock()
			delete(e.pendingControls, req.RequestId)
			if errors.Is(err, errTaskControlObsolete) {
				pending.err = errTaskControlObsolete
				delete(e.taskReleases, op.ID)
			}
			pending.status = status
			close(pending.done)
			e.pendingMu.Unlock()
		}
		cancel()
		if errors.Is(pending.err, errTaskControlObsolete) {
			continue
		}
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		// Disconnect fences admission before waiting lanes, and deactivates the
		// durable Head afterwards. The current lane can still record its release.
		err := e.taskAuthority.registry.service.RecordCleanup(persistCtx, binding, record.ID, task.Cleanup{OperationID: op.ID, Status: status, Reason: pending.request.Reason})
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
