package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
	"google.golang.org/protobuf/proto"
)

func (e *streamEnvironment) RegisterTaskAction(ctx context.Context, rc tool.RuntimeCallContext, req *protocol.ActionRequest) (err error) {
	defer func() {
		if err != nil {
			err = taskActionFailure{err}
		}
	}()
	if e.taskAuthority == nil || rc.ObservedTask == nil || req == nil || req.WorldId != rc.Execution.Owner.WorldID || req.EntityId != rc.Execution.Owner.EntityID || rc.Execution.Source.Kind != task.SourceKindTaskWake {
		return task.ErrSourceInvalid
	}
	return e.taskAuthority.Guard(rc.Execution.Binding, rc.AuthorityEpoch, func(head task.Head) error {
		exec := rc.Execution
		exec.Clock, exec.ExpectedRevision = head.Clock, rc.ObservedTask.Revision
		if rc.ObservedTask.ID != exec.TaskID || rc.ObservedTask.Owner != exec.Owner || rc.ObservedTask.State != task.StateRunning {
			return task.ErrTaskChanged
		}
		bytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
		if err != nil {
			return err
		}
		fingerprint := sha256.Sum256(bytes)
		op := task.Operation{ID: newMessageID("operation"), ActionID: req.ActionId, CommandFingerprint: hex.EncodeToString(fingerprint[:]), StartRevision: exec.ExpectedRevision, Binding: head.Binding, Status: task.OperationStatusRegistered}
		contract, err := taskProposalFromSpec(rc.ObservedTask.Spec)
		if err != nil {
			return err
		}
		op, err = e.taskAuthority.registry.service.RegisterOperation(ctx, exec, op)
		if err != nil {
			return err
		}
		req.TaskSource = &protocol.TaskActionSource{TaskId: exec.TaskID, WakeId: exec.WakeID, OperationId: op.ID, StartRevision: op.StartRevision, Scope: taskScopeToProtocol(op.Binding), TaskContract: contract}
		return nil
	})
}

// Evidence source identity follows the durable fact, so transport re-delivery
// through ActionResult, Event and Observation has a single canonical identity.
func (e *streamEnvironment) admitTaskEvidence(ctx context.Context, owner session.AgentSessionKey, values []*protocol.TaskEvidence, activeQuery bool, actionID string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if e.taskAuthority == nil {
		return nil, task.ErrWorldNotReady
	}
	a := e.taskAuthority
	head, epoch, ready := a.Current()
	if !ready {
		return nil, task.ErrWorldNotReady
	}
	ids := []string{}
	err := a.Guard(head.Binding, epoch, func(current task.Head) error {
		for _, value := range values {
			record, err := a.registry.service.Read(ctx, owner, value.GetTaskId())
			if err != nil {
				return err
			}
			evidence, err := taskEvidenceFromProtocol(value, task.SourceRef{Kind: task.SourceKindEnvironment, CallID: value.GetFactId()})
			if err != nil {
				return err
			}
			if evidence.OperationID == "" {
				return task.ErrEvidenceConflict
			}
			var operation *task.Operation
			for i := range record.Operations {
				if record.Operations[i].ID == evidence.OperationID {
					operation = &record.Operations[i]
					break
				}
			}
			if operation == nil || actionID != "" && operation.ActionID != actionID {
				return task.ErrEvidenceConflict
			}
			if evidence.Binding.World != current.Binding.World || evidence.Binding.RunID != current.Binding.RunID || evidence.Binding.Generation > current.Binding.Generation {
				return task.ErrGenerationStale
			}
			known := false
			for _, prior := range record.Evidence {
				if prior.FactID == evidence.FactID {
					known = true
					evidence.RevalidatedIn = prior.RevalidatedIn
				}
			}
			if !known && evidence.Binding != current.Binding {
				if !activeQuery || evidence.Binding.World != current.Binding.World || evidence.Binding.RunID != current.Binding.RunID || operation.Binding != evidence.Binding {
					return task.ErrGenerationStale
				}
				b := current.Binding
				evidence.RevalidatedIn = &b
			} else if !known && activeQuery && operation.Status == task.OperationStatusUncertain {
				b := current.Binding
				evidence.RevalidatedIn = &b
			}
			// The Service compares every field of a known fact. A current stream
			// can correlate a same-run duplicate without changing its original receipt.
			_, err = a.registry.service.AdmitEvidence(ctx, current.Binding, evidence)
			if err != nil {
				return err
			}
			found := false
			for _, id := range ids {
				found = found || id == record.ID
			}
			if !found {
				ids = append(ids, record.ID)
			}
		}
		return nil
	})
	return ids, err
}

func (e *streamEnvironment) reconcileTaskIDs(ctx context.Context, owner session.AgentSessionKey, ids []string) error {
	for _, id := range ids {
		a := e.taskAuthority
		head, epoch, ready := a.Current()
		if !ready {
			return task.ErrWorldNotReady
		}
		var record task.Record
		err := a.Guard(head.Binding, epoch, func(current task.Head) error {
			r, err := a.registry.service.Read(ctx, owner, id)
			if err != nil {
				return err
			}
			exec := task.ExecutionContext{Owner: owner, Binding: current.Binding, Clock: current.Clock, TaskID: id, ExpectedRevision: r.Revision, Source: task.SourceRef{Kind: task.SourceKindEnvironment, EventID: id, TurnID: id, CallID: id}}
			result, err := a.registry.service.Reconcile(ctx, exec)
			record = result.Task
			return err
		})
		if err != nil {
			return err
		}
		if record.Result != nil {
			if err := e.releaseTaskControl(ctx, head.Binding, record); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *streamEnvironment) ReconcileTask(ctx context.Context, owner session.AgentSessionKey, id string) error {
	return e.reconcileTaskIDs(ctx, owner, []string{id})
}

func (e *streamEnvironment) coordinateTasks(ctx context.Context, owner session.AgentSessionKey, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = e.reconcileTaskIDs(ctx, owner, ids)
		if err == nil || task.BindingRecoveryError(err) {
			return err
		}
		if !task.TechnicalFailure(err) || attempt == 3 {
			break
		}
		if !task.WaitTechnicalRetry(ctx, e.taskAuthority.registry.dispatchConfig, attempt) {
			break
		}
	}
	if head, _, ready := e.taskAuthority.Current(); ready {
		r := e.taskAuthority.registry
		newTaskDispatch(r, r.service, r.dispatchConfig, nil, nil).PauseWorld(ctx, head.Binding, task.Wake{}, err)
	}
	return err
}

type taskObservationFailure struct{ error }

func (e taskObservationFailure) Unwrap() error { return e.error }

type taskActionFailure struct{ error }

func (e taskActionFailure) Unwrap() error { return e.error }

type taskReconcileQueryKey struct{}

func (s *Server) claimTaskInteraction(env *streamEnvironment, event *protocol.GameEvent) bool {
	source := event.GetInteractionSource()
	if s.agentLoop == nil || env.taskAuthority == nil || strings.TrimSpace(source.GetSourceId()) == "" || strings.TrimSpace(source.GetPlayerEntityId()) == "" || strings.TrimSpace(source.GetKind()) == "" {
		return false
	}
	head, epoch, ready := env.taskAuthority.Current()
	if !ready || !proto.Equal(source.Scope, taskScopeToProtocol(head.Binding)) {
		return false
	}
	correlated := source.TaskId == "" && source.OperationId == "" && source.Kind == "player"
	for _, fact := range event.TaskEvidence {
		correlated = correlated || source.TaskId == fact.TaskId && source.OperationId == fact.OperationId
	}
	if !correlated {
		return false
	}
	prefix := fmt.Sprintf("%q/%q/%q", head.Binding.World.GameID, head.Binding.World.WorldID, head.Binding.RunID)
	keys := []string{fmt.Sprintf("%s/source/%q", prefix, source.SourceId)}
	for _, fact := range event.TaskEvidence {
		keys = append(keys, fmt.Sprintf("%s/fact/%q", prefix, fact.FactId))
	}
	claimed := false
	_ = env.taskAuthority.Guard(head.Binding, epoch, func(task.Head) error {
		s.worlds.interactionMu.Lock()
		defer s.worlds.interactionMu.Unlock()
		for _, key := range keys {
			if _, ok := s.worlds.interactions[key]; ok {
				return nil
			}
		}
		for _, key := range keys {
			s.worlds.interactions[key] = struct{}{}
		}
		claimed = true
		return nil
	})
	return claimed
}

func (e *streamEnvironment) coordinateActionResult(ctx context.Context, req *protocol.ActionRequest, result actionResult) (*protocol.ActionResult, error) {
	if result.err != nil || e.taskAuthority == nil {
		return result.result, taskActionError(req, result.err)
	}
	owner := session.AgentSessionKey{GameID: e.taskAuthority.world.GameID, WorldID: req.WorldId, EntityID: req.EntityId}
	ids, err := e.admitTaskEvidence(ctx, owner, result.result.GetTaskEvidence(), false, req.ActionId)
	if err == nil {
		err = e.coordinateTasks(ctx, owner, ids)
	}
	return result.result, taskActionError(req, err)
}

func taskActionError(req *protocol.ActionRequest, err error) error {
	if err != nil && req.GetTaskSource() != nil {
		return taskActionFailure{err}
	}
	return err
}

func (e *streamEnvironment) guardActionSend(req *protocol.ActionRequest, send func() error) error {
	slot := e.taskAuthority.registry.slot(e.taskAuthority.world, false)
	if slot == nil {
		return task.ErrWorldNotReady
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if err := slot.check(e, slot.head.Binding); err != nil {
		return err
	}
	if req.TaskSource != nil && !proto.Equal(req.TaskSource.Scope, taskScopeToProtocol(slot.head.Binding)) {
		return task.ErrGenerationStale
	}
	return send()
}

func (e *streamEnvironment) queryTask(ctx context.Context, exec task.ExecutionContext) error {
	queryCtx, cancel := context.WithTimeout(context.WithValue(ctx, taskReconcileQueryKey{}, true), time.Second)
	defer cancel()
	_, err := e.Observe(queryCtx, exec.Owner.WorldID, exec.Owner.EntityID)
	if err == nil {
		err = errors.New("task evidence remains unconfirmed")
	}
	return taskObservationFailure{err}
}

// finishTaskExecution owns finite technical recovery after cognition has ended.
// Every retry reads current state before writing, including an ambiguous commit.
func (e *streamEnvironment) finishTaskExecution(ctx context.Context, exec task.ExecutionContext, turnErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	a := e.taskAuthority
	for failures := 0; failures < 3; {
		head, epoch, ready := a.Current()
		if !ready || head.Binding != exec.Binding {
			return nil
		}
		record, err := a.registry.service.Read(cleanupCtx, exec.Owner, exec.TaskID)
		if err != nil {
			failures++
			if failures == 3 || !task.TechnicalFailure(err) || !task.WaitTechnicalRetry(cleanupCtx, a.registry.dispatchConfig, failures) {
				return err
			}
			continue
		}
		if record.Result != nil {
			return e.releaseTaskControl(cleanupCtx, head.Binding, record)
		}
		if record.State != task.StateRunning {
			return nil
		}
		pending := false
		for _, evidence := range record.Evidence {
			pending = pending || !evidence.Applied
		}
		if pending {
			if err := e.coordinateTasks(cleanupCtx, exec.Owner, []string{record.ID}); err != nil {
				return err
			}
			continue
		}
		var actionErr taskActionFailure
		if errors.As(turnErr, &actionErr) {
			turnErr = e.queryTask(cleanupCtx, exec)
			continue
		}
		exec.Clock, exec.ExpectedRevision = head.Clock, record.Revision
		kind := task.AttemptOutcomeKindNoProgress
		var observeErr taskObservationFailure
		if record.NeedsReconcile {
			kind = task.AttemptOutcomeKindReconcileFailed
		} else if errors.As(turnErr, &observeErr) || head.Clock.Tick >= record.Spec.DeadlineAt {
			kind = task.AttemptOutcomeKindObservationFailed
		}
		err = a.Guard(head.Binding, epoch, func(task.Head) error {
			var err error
			record, err = a.registry.service.FinishAttempt(cleanupCtx, exec, task.AttemptOutcome{Kind: kind})
			return err
		})
		if err != nil {
			if task.BindingRecoveryError(err) {
				return nil
			}
			failures++
			if failures == 3 || !task.TechnicalFailure(err) {
				return err
			}
			if !task.WaitTechnicalRetry(cleanupCtx, a.registry.dispatchConfig, failures) {
				return err
			}
			continue
		}
		if record.State != task.StateRunning {
			return nil
		}
		turnErr = e.queryTask(cleanupCtx, exec)
	}
	return task.ErrTaskConflict
}
