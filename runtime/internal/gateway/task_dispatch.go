package gateway

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type wakeService interface {
	MarkEnqueued(context.Context, task.Binding, string, string) error
	ReleaseClaim(context.Context, task.Binding, string, string, time.Time) error
	BeginWake(context.Context, task.Binding, string, string) (task.ExecutionContext, task.Record, error)
	InspectWake(context.Context, task.Binding, string) (task.WakeInspection, error)
}
type WakeHandler func(context.Context, task.ExecutionContext, task.Record) error

func (s *Server) StartTaskDispatcher(ctx context.Context, config task.DispatcherConfig, handler, reconcile WakeHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.worlds == nil || s.dispatcher != nil {
		return task.ErrWorldNotReady
	}
	s.worlds.dispatchConfig = config
	if handler == nil {
		if loop, ok := s.agentLoop.(interface {
			HandleTaskWake(context.Context, agent.Environment, agent.ConnectionContext, *protocol.EntityRef, *tool.EnvironmentToolCatalog, task.ExecutionContext, task.Record) error
		}); ok {
			handler = func(ctx context.Context, exec task.ExecutionContext, record task.Record) error {
				entry, ok := s.worlds.Current(exec.Binding.World)
				if !ok || entry.Head.Binding != exec.Binding {
					return nil
				}
				env := entry.Environment.(*streamEnvironment)
				slot := s.worlds.slot(exec.Binding.World, false)
				slot.mu.Lock()
				if slot.owner == nil || slot.owner.env != env || slot.head.Binding != exec.Binding {
					slot.mu.Unlock()
					return nil
				}
				catalog, hello := slot.owner.catalog, slot.owner.hello
				slot.mu.Unlock()
				if record.NeedsReconcile {
					ctx = context.WithValue(ctx, taskReconcileQueryKey{}, true)
				}
				err := loop.HandleTaskWake(ctx, env, agent.ConnectionContext{GameID: hello.GameId, SessionID: hello.SessionId}, entry.Entities[exec.Owner.EntityID], catalog, exec, record)
				if err != nil {
					log.Printf("task turn: %s", logSafeError(err))
				}
				return env.finishTaskExecution(ctx, exec, err)
			}
			reconcile = func(ctx context.Context, exec task.ExecutionContext, record task.Record) error {
				entry, ok := s.worlds.Current(exec.Binding.World)
				if !ok || entry.Head.Binding != exec.Binding {
					return nil
				}
				return entry.Environment.(*streamEnvironment).finishTaskExecution(ctx, exec, taskActionFailure{task.ErrTaskChanged})
			}
		}
	}
	admission := newTaskDispatch(s.worlds, s.worlds.service, config, handler, reconcile)
	dispatcher, err := task.NewDispatcher(ctx, s.worlds.service, config, task.DispatcherCallbacks{
		ReadyWorlds: s.worlds.ReadyWorlds, EnqueueWake: admission.EnqueueWake, PauseWorld: admission.PauseWorld, Updates: s.worlds.updates,
	})
	if err != nil {
		return err
	}
	s.dispatcher = dispatcher
	return nil
}

type taskDispatch struct {
	worlds            *WorldRegistry
	service           wakeService
	config            task.DispatcherConfig
	handle, reconcile WakeHandler
}

func newTaskDispatch(r *WorldRegistry, s wakeService, c task.DispatcherConfig, h, reconcile WakeHandler) *taskDispatch {
	return &taskDispatch{worlds: r, service: s, config: c, handle: h, reconcile: reconcile}
}

// The enqueue caller owns recovery until the barrier opens. Run or Abort then
// acquires the same lock and becomes the sole owner of this delivery identity.
type wakeDelivery struct {
	mu              sync.Mutex
	dispatch        *taskDispatch
	binding         task.Binding
	wake            task.Wake
	entry           WorldEntry
	lane            *session.ExecutionLane
	admitted        chan struct{}
	barrier         sync.Once
	eligible, ended bool
	failures        int
	observedRunning *task.WakeInspection
}

func (d *taskDispatch) EnqueueWake(ctx context.Context, b task.Binding, w task.Wake) error {
	entry, ok := d.worlds.Current(b.World)
	if !ok {
		return task.ErrWorldNotReady
	}
	if entry.Head.Binding != b {
		return task.ErrGenerationStale
	}
	if w.Owner.GameID != b.World.GameID || w.Owner.WorldID != b.World.WorldID || entry.Entities[w.Owner.EntityID] == nil || w.Generation != b.Generation || w.Status != "claimed" || w.Validate() != nil {
		d.PauseWorld(ctx, b, w, task.ErrInvalidTaskSpec)
		return nil
	}
	delivery := &wakeDelivery{dispatch: d, binding: b, wake: w, entry: entry, admitted: make(chan struct{}), eligible: true}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	lane, err := entry.Lanes.GetOrCreate(w.Owner)
	delivery.lane = lane
	if err == nil {
		err = lane.Enqueue(session.Task{ID: w.ID, Admitted: delivery.admitted, Run: delivery.run, Abort: delivery.abort})
	}
	if err != nil {
		delivery.eligible = false
		delivery.release(ctx)
		return nil
	}
	delivery.mark(ctx)
	return nil
}

func (d *taskDispatch) PauseWorld(ctx context.Context, b task.Binding, w task.Wake, err error) {
	slot := d.worlds.slot(b.World, false)
	if slot == nil {
		return
	}
	slot.mu.Lock()
	if slot.head.Binding != b || slot.owner == nil {
		slot.mu.Unlock()
		return
	}
	slot.ready = false
	slot.authorityEpoch++
	slot.head.Status = "paused"
	slot.head.Reason = "task_dispatch_" + taskDispatchCode(err)
	reason := slot.head.Reason
	slot.mu.Unlock()
	// Admission is already fenced. Persist its reason independently of the lane
	// cancellation that follows fencing, while keeping shutdown bounded.
	pauseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	pauseErr := d.worlds.service.DeactivateWorld(pauseCtx, b, reason)
	log.Printf("task dispatch paused game=%q world=%q wake=%q claim=%q code=%q durable_pause=%q", b.World.GameID, b.World.WorldID, w.ID, w.ClaimID, taskDispatchCode(err), taskDispatchCode(pauseErr))
}
func taskDispatchCode(err error) string {
	if err == nil {
		return "ok"
	}
	var e *task.Error
	if errors.As(err, &e) {
		return string(e.Code)
	}
	return string(task.CodeTaskConflict)
}
func (w *wakeDelivery) finish() {
	w.eligible = false
	w.ended = true
	w.barrier.Do(func() { close(w.admitted) })
}
func (w *wakeDelivery) valid() bool {
	entry, ok := w.dispatch.worlds.Current(w.binding.World)
	if !ok || entry.Head.Binding != w.binding || entry.AuthorityEpoch != w.entry.AuthorityEpoch || entry.Lanes != w.entry.Lanes {
		return false
	}
	return true
}
func (w *wakeDelivery) guard(fn func() error) error {
	env, ok := w.entry.Environment.(*streamEnvironment)
	if !ok {
		return task.ErrWorldNotReady
	}
	return w.dispatch.worlds.Guard(env, w.binding, func(entry WorldEntry) error {
		if entry.AuthorityEpoch != w.entry.AuthorityEpoch || entry.Lanes != w.entry.Lanes {
			return task.ErrGenerationStale
		}
		return fn()
	})
}
func (w *wakeDelivery) pause(ctx context.Context, err error) {
	w.finish()
	w.dispatch.PauseWorld(ctx, w.binding, w.wake, err)
}

func (w *wakeDelivery) invalidate() {
	w.finish()
	if w.valid() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.dispatch.PauseWorld(ctx, w.binding, w.wake, task.ErrWorldNotReady)
	}
}
func (w *wakeDelivery) failed(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		w.invalidate()
		return false
	}
	if task.BindingRecoveryError(err) {
		w.finish()
		return false
	}
	w.failures++
	if w.failures >= 3 {
		w.pause(ctx, err)
		return false
	}
	if !task.WaitTechnicalRetry(ctx, w.dispatch.config, w.failures) {
		w.invalidate()
		return false
	}
	return true
}
func (w *wakeDelivery) inspect(ctx context.Context) (task.WakeInspection, bool) {
	for {
		inspection, err := w.dispatch.service.InspectWake(ctx, w.binding, w.wake.ID)
		if err != nil {
			if !w.failed(ctx, err) {
				return task.WakeInspection{}, false
			}
			continue
		}
		if inspection.Head.Status != "ready" || !w.valid() {
			w.finish()
			return inspection, false
		}
		switch inspection.Wake.Status {
		case "pending", "consumed":
			w.finish()
			return inspection, false
		}
		if inspection.Wake.ClaimID != w.wake.ClaimID || inspection.Wake.ClaimedBy != w.wake.ClaimedBy {
			w.finish()
			return inspection, false
		}
		return inspection, true
	}
}
func (w *wakeDelivery) mark(ctx context.Context) {
	for !w.ended {
		err := w.guard(func() error { return w.dispatch.service.MarkEnqueued(ctx, w.binding, w.wake.ID, w.wake.ClaimID) })
		if err == nil {
			w.failures = 0
			w.barrier.Do(func() { close(w.admitted) })
			return
		}
		if task.BindingRecoveryError(err) || ctx.Err() != nil {
			w.finish()
			return
		}
		if !task.TechnicalFailure(err) {
			w.eligible = false
			w.barrier.Do(func() { close(w.admitted) })
			w.release(ctx)
			return
		}
		if !w.failed(ctx, err) {
			return
		}
		inspection, ok := w.inspect(ctx)
		if !ok {
			return
		}
		switch inspection.Wake.Status {
		case "claimed":
			continue
		case "enqueued", "running":
			if inspection.Wake.Status == "running" {
				w.observedRunning = &inspection
			}
			w.failures = 0
			w.barrier.Do(func() { close(w.admitted) })
			return
		default:
			w.pause(ctx, task.ErrInvalidTaskSpec)
			return
		}
	}
}
func (w *wakeDelivery) release(ctx context.Context) {
	for !w.ended {
		err := w.guard(func() error {
			return w.dispatch.service.ReleaseClaim(ctx, w.binding, w.wake.ID, w.wake.ClaimID, time.Now().Add(task.RetryDelay(w.dispatch.config, 1)))
		})
		if err == nil {
			w.finish()
			return
		}
		if !w.failed(ctx, err) {
			return
		}
		inspection, ok := w.inspect(ctx)
		if !ok {
			return
		}
		if inspection.Wake.Status != "claimed" {
			w.pause(ctx, task.ErrWorldNotReady)
			return
		}
	}
}
func (w *wakeDelivery) run(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended || !w.eligible {
		return
	}
	if w.dispatch.handle == nil && w.observedRunning == nil {
		w.pause(ctx, task.ErrTaskConflict)
		return
	}
	for !w.ended {
		if ctx.Err() != nil || !w.valid() {
			w.invalidate()
			return
		}
		if w.observedRunning != nil {
			w.recoverRunning(ctx, *w.observedRunning)
			return
		}
		var exec task.ExecutionContext
		var record task.Record
		err := w.guard(func() error {
			var e error
			exec, record, e = w.dispatch.service.BeginWake(ctx, w.binding, w.wake.ID, w.wake.ClaimID)
			return e
		})
		if err == nil {
			w.finish()
			if ctx.Err() != nil || !w.valid() {
				w.invalidate()
				return
			}
			if w.dispatch.handle != nil {
				if err := w.dispatch.handle(ctx, exec, record); err != nil {
					w.dispatch.PauseWorld(ctx, w.binding, w.wake, err)
				}
			}
			return
		}
		if !w.failed(ctx, err) {
			return
		}
		inspection, ok := w.inspect(ctx)
		if !ok {
			return
		}
		switch inspection.Wake.Status {
		case "enqueued":
			continue
		case "running":
			w.recoverRunning(ctx, inspection)
			return
		default:
			w.finish()
			return
		}
	}
}

func (w *wakeDelivery) recoverRunning(ctx context.Context, inspection task.WakeInspection) {
	w.finish()
	if ctx.Err() != nil || !w.valid() {
		return
	}
	exec := task.ExecutionContext{Owner: inspection.Task.Owner, Binding: inspection.Head.Binding, Clock: inspection.Head.Clock, TaskID: inspection.Task.ID, WakeID: inspection.Wake.ID, ExpectedRevision: inspection.Task.Revision, Source: task.SourceRef{Kind: task.SourceKindTaskWake, CallID: inspection.Wake.ID}}
	if w.dispatch.reconcile == nil {
		w.dispatch.PauseWorld(ctx, w.binding, w.wake, task.ErrTaskConflict)
		return
	}
	if err := w.dispatch.reconcile(ctx, exec, inspection.Task); err != nil {
		w.dispatch.PauseWorld(ctx, w.binding, w.wake, err)
	}
}
func (w *wakeDelivery) abort(session.AbortReason) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		return
	}
	w.finish()
	// Lane closure fences execution. Rebind/disconnect normalizes enqueued wakes;
	// a standalone closed lane explicitly pauses admission for that recovery.
	if w.valid() {
		w.dispatch.PauseWorld(context.Background(), w.binding, w.wake, task.ErrWorldNotReady)
	}
}
