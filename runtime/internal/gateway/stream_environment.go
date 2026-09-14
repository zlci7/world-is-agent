package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

type streamEnvironment struct {
	taskAuthority *worldTaskAuthority
	stream        protocolv1alpha2.GameAgentGateway_ConnectServer

	sendSlot  chan struct{}
	closed    chan struct{}
	closeOnce sync.Once

	pendingMu           sync.Mutex
	pendingObservations map[string]pendingObservation
	pendingActions      map[string]*pendingAction
	pendingControls     map[string]*pendingTaskControl
	taskReleases        map[string]*pendingTaskControl
}

type pendingObservation struct {
	worldID  string
	entityID string
	ch       chan observeResult
}

type observeResult struct {
	observation *protocolv1alpha2.Observation
	err         error
}

type pendingAction struct {
	request *protocolv1alpha2.ActionRequest
	updates chan actionStatusResult
	results chan actionResult
}

type actionStatusResult struct {
	update *protocolv1alpha2.ActionStatusUpdate
	err    error
}

type actionResult struct {
	result *protocolv1alpha2.ActionResult
	err    error
}

func newStreamEnvironment(stream protocolv1alpha2.GameAgentGateway_ConnectServer) *streamEnvironment {
	return &streamEnvironment{
		stream:              stream,
		sendSlot:            make(chan struct{}, 1),
		closed:              make(chan struct{}),
		pendingObservations: make(map[string]pendingObservation),
		pendingActions:      make(map[string]*pendingAction),
		pendingControls:     make(map[string]*pendingTaskControl),
		taskReleases:        make(map[string]*pendingTaskControl),
	}
}

func (e *streamEnvironment) Observe(ctx context.Context, worldID string, entityID string) (*protocolv1alpha2.Observation, error) {
	if worldID == "" {
		return nil, fmt.Errorf("world id is empty")
	}
	if entityID == "" {
		return nil, fmt.Errorf("entity id is empty")
	}

	messageID := newMessageID("observe")
	ch := make(chan observeResult, 1)

	e.pendingMu.Lock()
	e.pendingObservations[messageID] = pendingObservation{
		worldID:  worldID,
		entityID: entityID,
		ch:       ch,
	}
	e.pendingMu.Unlock()

	defer func() {
		e.pendingMu.Lock()
		delete(e.pendingObservations, messageID)
		e.pendingMu.Unlock()
	}()

	msg := &protocolv1alpha2.RuntimeMessage{
		MessageId: messageID,
		Payload: &protocolv1alpha2.RuntimeMessage_Observe{
			Observe: &protocolv1alpha2.ObserveRequest{
				EntityId: entityID,
				WorldId:  worldID,
			},
		},
	}

	if err := e.sendContext(ctx, msg); err != nil {
		return nil, taskObservationFailure{err}
	}

	select {
	case result := <-ch:
		if result.err != nil {
			return nil, taskObservationFailure{result.err}
		}
		if e.taskAuthority != nil {
			owner := session.AgentSessionKey{GameID: e.taskAuthority.world.GameID, WorldID: worldID, EntityID: entityID}
			active, _ := ctx.Value(taskReconcileQueryKey{}).(bool)
			ids, err := e.admitTaskEvidence(ctx, owner, result.observation.GetTaskEvidence(), active, "")
			if err == nil {
				err = e.coordinateTasks(ctx, owner, ids)
			}
			if err != nil {
				return nil, taskObservationFailure{err}
			}
		}
		return result.observation, result.err
	case <-ctx.Done():
		return nil, taskObservationFailure{ctx.Err()}
	}
}

func (e *streamEnvironment) SubmitAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (*protocolv1alpha2.ActionResult, error) {
	if req == nil {
		return nil, fmt.Errorf("action request is nil")
	}
	if req.ActionId == "" {
		return nil, fmt.Errorf("action id is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, taskActionError(req, e.recordUnsentTaskAction(ctx, req, err))
	}

	pending := newPendingAction()
	pending.request = req
	e.setPendingAction(req.ActionId, pending)
	defer e.deletePendingAction(req.ActionId)

	if err := e.sendActionRequest(ctx, req); err != nil {
		return nil, taskActionError(req, err)
	}

	select {
	case result := <-pending.results:
		return e.coordinateActionResult(ctx, req, result)
	case <-ctx.Done():
		e.CancelAction(req.ActionId, "action_timeout")
		return nil, taskActionError(req, ctx.Err())
	}
}

func (e *streamEnvironment) StartAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (agent.ActionStart, error) {
	if req == nil {
		return agent.ActionStart{}, fmt.Errorf("action request is nil")
	}
	if req.ActionId == "" {
		return agent.ActionStart{}, fmt.Errorf("action id is empty")
	}
	if err := ctx.Err(); err != nil {
		return agent.ActionStart{}, taskActionError(req, e.recordUnsentTaskAction(ctx, req, err))
	}

	pending := newPendingAction()
	pending.request = req
	e.setPendingAction(req.ActionId, pending)

	if err := e.sendActionRequest(ctx, req); err != nil {
		e.deletePendingAction(req.ActionId)
		return agent.ActionStart{}, taskActionError(req, err)
	}

	for {
		select {
		case update := <-pending.updates:
			if update.err != nil {
				e.deletePendingAction(req.ActionId)
				return agent.ActionStart{}, update.err
			}
			if update.update == nil || !isActionStartStatus(update.update.GetStatus()) {
				continue
			}
			return agent.ActionStart{Update: update.update}, nil
		case result := <-pending.results:
			e.deletePendingAction(req.ActionId)
			r, err := e.coordinateActionResult(ctx, req, result)
			return agent.ActionStart{Result: r}, err
		case <-ctx.Done():
			e.deletePendingAction(req.ActionId)
			e.CancelAction(req.ActionId, "action_start_timeout")
			return agent.ActionStart{}, taskActionError(req, ctx.Err())
		}
	}
}

func (e *streamEnvironment) WaitActionResult(ctx context.Context, actionID string) (*protocolv1alpha2.ActionResult, error) {
	if actionID == "" {
		return nil, fmt.Errorf("action id is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pending := e.getPendingAction(actionID)
	if pending == nil {
		return nil, fmt.Errorf("pending action %q not found", actionID)
	}
	defer e.deletePendingAction(actionID)

	select {
	case result := <-pending.results:
		if pending.request != nil {
			return e.coordinateActionResult(ctx, pending.request, result)
		}
		return result.result, result.err
	case <-ctx.Done():
		e.CancelAction(actionID, "async_action_timeout")
		return nil, taskActionError(pending.request, ctx.Err())
	}
}

func (e *streamEnvironment) CancelAction(actionID string, reason string) {
	if actionID == "" {
		return
	}
	msg := &protocolv1alpha2.RuntimeMessage{
		MessageId: newMessageID("cancel_action"),
		Payload: &protocolv1alpha2.RuntimeMessage_CancelAction{
			CancelAction: &protocolv1alpha2.CancelActionRequest{
				ActionId: actionID,
				Reason:   reason,
			},
		},
	}
	_ = e.send(msg)
}

func (e *streamEnvironment) SendTurnCompletion(ctx context.Context, completion *protocolv1alpha2.TurnCompletion) error {
	if completion == nil {
		return fmt.Errorf("turn completion is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	msg := &protocolv1alpha2.RuntimeMessage{
		MessageId: newMessageID("turn_completion"),
		Payload: &protocolv1alpha2.RuntimeMessage_TurnCompletion{
			TurnCompletion: completion,
		},
	}
	return e.send(msg)
}

func (e *streamEnvironment) resolveObservation(correlationID string, observation *protocolv1alpha2.Observation) {
	e.pendingMu.Lock()
	pending, ok := e.pendingObservations[correlationID]
	delete(e.pendingObservations, correlationID)
	e.pendingMu.Unlock()

	if !ok {
		return
	}

	if observation.WorldId != pending.worldID || observation.EntityId != pending.entityID {
		pending.ch <- observeResult{err: observationScopeMismatchError{
			requestedWorldID:  pending.worldID,
			requestedEntityID: pending.entityID,
			actualWorldID:     observation.WorldId,
			actualEntityID:    observation.EntityId,
		}}
		return
	}

	pending.ch <- observeResult{observation: observation}
}

func (e *streamEnvironment) failObservation(correlationID string, err error) {
	e.pendingMu.Lock()
	pending, ok := e.pendingObservations[correlationID]
	e.pendingMu.Unlock()

	if !ok {
		return
	}

	pending.ch <- observeResult{err: err}
}

func (e *streamEnvironment) resolveActionStatusUpdate(actionID string, update *protocolv1alpha2.ActionStatusUpdate) {
	pending := e.getPendingAction(actionID)
	if pending == nil {
		return
	}
	select {
	case pending.updates <- actionStatusResult{update: update}:
	default:
	}
}

func (e *streamEnvironment) resolveActionResult(actionID string, result *protocolv1alpha2.ActionResult) {
	pending := e.getPendingAction(actionID)
	if pending == nil {
		return
	}
	select {
	case pending.results <- actionResult{result: result}:
	default:
	}
}

func (e *streamEnvironment) sendActionRequest(ctx context.Context, req *protocolv1alpha2.ActionRequest) error {
	message := &protocolv1alpha2.RuntimeMessage{
		MessageId: newMessageID("action"),
		Payload: &protocolv1alpha2.RuntimeMessage_Action{
			Action: req,
		},
	}
	if req.TaskSource == nil {
		return e.sendContext(ctx, message)
	}
	if e.taskAuthority == nil {
		return task.ErrWorldNotReady
	}
	err := e.sendGuarded(ctx, message, func(send func() error) error { return e.guardActionSend(ctx, req, send) })
	var notStarted transportNotStartedError
	if errors.As(err, &notStarted) {
		return e.recordUnsentTaskAction(ctx, req, err)
	}
	return err
}

func (e *streamEnvironment) send(msg *protocolv1alpha2.RuntimeMessage) error {
	return e.sendContext(context.Background(), msg)
}

func (e *streamEnvironment) sendContext(ctx context.Context, msg *protocolv1alpha2.RuntimeMessage) error {
	return e.sendGuarded(ctx, msg, nil)
}

func (e *streamEnvironment) sendGuarded(ctx context.Context, msg *protocolv1alpha2.RuntimeMessage, guard func(func() error) error) error {
	select {
	case <-ctx.Done():
		return transportNotStartedError{ctx.Err()}
	case <-e.closed:
		return transportNotStartedError{io.EOF}
	case e.sendSlot <- struct{}{}:
	}
	// At most one transport send is active. Shutdown releases its caller so the
	// handler can finish task persistence; returning the handler releases gRPC I/O.
	result := make(chan error, 1)
	start := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.closed:
			return io.EOF
		default:
		}
		go func() { defer func() { <-e.sendSlot }(); result <- e.stream.Send(msg) }()
		return nil
	}
	var err error
	if guard != nil {
		err = guard(start)
	} else {
		err = start()
	}
	if err != nil {
		<-e.sendSlot
		return transportNotStartedError{err}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.closed:
		return io.EOF
	case err := <-result:
		return err
	}
}

func (e *streamEnvironment) close() {
	e.closeOnce.Do(func() { close(e.closed) })
}

func (e *streamEnvironment) failAllPending(err error) {
	e.pendingMu.Lock()

	observationChs := make([]chan observeResult, 0, len(e.pendingObservations))
	for id, pending := range e.pendingObservations {
		observationChs = append(observationChs, pending.ch)
		delete(e.pendingObservations, id)
	}

	actionWaiters := make([]*pendingAction, 0, len(e.pendingActions))
	for id, pending := range e.pendingActions {
		actionWaiters = append(actionWaiters, pending)
		delete(e.pendingActions, id)
	}

	e.pendingMu.Unlock()

	for _, ch := range observationChs {
		select {
		case ch <- observeResult{err: err}:
		default:
		}
	}

	for _, pending := range actionWaiters {
		select {
		case pending.updates <- actionStatusResult{err: err}:
		default:
		}
		select {
		case pending.results <- actionResult{err: err}:
		default:
		}
	}
}

func newPendingAction() *pendingAction {
	return &pendingAction{
		updates: make(chan actionStatusResult, 8),
		results: make(chan actionResult, 1),
	}
}

func (e *streamEnvironment) setPendingAction(actionID string, pending *pendingAction) {
	e.pendingMu.Lock()
	e.pendingActions[actionID] = pending
	e.pendingMu.Unlock()
}

func (e *streamEnvironment) getPendingAction(actionID string) *pendingAction {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	return e.pendingActions[actionID]
}

func (e *streamEnvironment) deletePendingAction(actionID string) {
	e.pendingMu.Lock()
	delete(e.pendingActions, actionID)
	e.pendingMu.Unlock()
}

func isActionStartStatus(status protocolv1alpha2.ActionStatus) bool {
	return status == protocolv1alpha2.ActionStatus_ACTION_STATUS_ACCEPTED ||
		status == protocolv1alpha2.ActionStatus_ACTION_STATUS_RUNNING
}

type observationScopeMismatchError struct {
	requestedWorldID  string
	requestedEntityID string
	actualWorldID     string
	actualEntityID    string
}

func (e observationScopeMismatchError) Error() string {
	return fmt.Sprintf(
		"observation_scope_mismatch: requested world_id=%q entity_id=%q, got world_id=%q entity_id=%q",
		e.requestedWorldID,
		e.requestedEntityID,
		e.actualWorldID,
		e.actualEntityID,
	)
}

func (e observationScopeMismatchError) FailureReason() string {
	return "observation_scope_mismatch"
}

type adapterError struct {
	code    string
	message string
}

func (e adapterError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("adapter error %s", e.code)
	}
	return fmt.Sprintf("adapter error %s: %s", e.code, e.message)
}

func (e adapterError) FailureReason() string {
	if e.code == "" {
		return "adapter_error"
	}
	return e.code
}
