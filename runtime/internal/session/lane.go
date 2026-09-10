package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const DefaultQueueSize = 1

var (
	ErrLaneClosed = errors.New("execution lane is closed")
	ErrLaneFull   = errors.New("execution lane queue is full")
)

type AbortReason string

const AbortReasonConnectionClosed AbortReason = "connection_closed"

type Task struct {
	ID       string
	Admitted <-chan struct{}
	Run      func(context.Context)
	Abort    func(AbortReason)
}

type ExecutionLane struct {
	ctx    context.Context
	cancel context.CancelFunc

	queueSize int
	queue     chan Task
	ready     chan struct{}
	done      chan struct{}

	mu                sync.Mutex
	closed            bool
	maintenance       *Task
	activeMaintenance bool
}

func NewExecutionLane(parent context.Context, queueSize int) (*ExecutionLane, error) {
	if queueSize <= 0 {
		return nil, fmt.Errorf("queue size must be positive")
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	lane := &ExecutionLane{
		ctx:       ctx,
		cancel:    cancel,
		queueSize: queueSize,
		queue:     make(chan Task, queueSize+1),
		ready:     make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	go lane.run()

	return lane, nil
}

func (l *ExecutionLane) Enqueue(task Task) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || l.ctx.Err() != nil {
		return ErrLaneClosed
	}

	limit := l.queueSize
	if l.activeMaintenance {
		// Maintenance preserves the active-player slot until a player is selected.
		limit++
	}
	if len(l.queue) >= limit {
		return ErrLaneFull
	}
	l.queue <- task
	select {
	case l.ready <- struct{}{}:
	default:
	}
	return nil
}

// EnqueueMaintenance keeps one pending maintenance task outside player capacity.
// The latest task replaces the pending task, silently discarding its Run and
// Abort callbacks. A selected task keeps its admission wait and is never replaced.
// Pending tasks aborted on lane cancellation receive AbortReasonConnectionClosed.
func (l *ExecutionLane) EnqueueMaintenance(task Task) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || l.ctx.Err() != nil {
		return ErrLaneClosed
	}

	l.maintenance = &task
	select {
	case l.ready <- struct{}{}:
	default:
	}
	return nil
}

func (l *ExecutionLane) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
}

func (l *ExecutionLane) Done() <-chan struct{} {
	return l.done
}

func (l *ExecutionLane) run() {
	defer func() {
		l.closeAdmission()
		l.drainQueued(AbortReasonConnectionClosed)
		close(l.done)
	}()

	for {
		if l.ctx.Err() != nil {
			return
		}
		task, ok := l.dequeue()
		if !ok {
			// Wakeups carry no tasks; selection always happens under the admission lock.
			select {
			case <-l.ctx.Done():
				return
			case <-l.ready:
			}
			continue
		}
		if !l.waitAdmitted(task) || l.ctx.Err() != nil {
			task.abort(AbortReasonConnectionClosed)
			return
		}
		task.run(l.ctx)
	}
}

func (l *ExecutionLane) dequeue() (Task, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Player admission and selection share mu, so queued players win atomically.
	l.activeMaintenance = false
	select {
	case task := <-l.queue:
		return task, true
	default:
	}
	if l.maintenance != nil {
		task := *l.maintenance
		l.maintenance = nil
		l.activeMaintenance = true
		return task, true
	}
	return Task{}, false
}

func (l *ExecutionLane) waitAdmitted(task Task) bool {
	if task.Admitted == nil {
		return true
	}

	select {
	case <-task.Admitted:
		return true
	case <-l.ctx.Done():
		return false
	}
}

func (l *ExecutionLane) drainQueued(reason AbortReason) {
	for {
		task, ok := l.dequeue()
		if !ok {
			return
		}
		task.abort(reason)
	}
}

func (l *ExecutionLane) closeAdmission() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
}

func (t Task) run(ctx context.Context) {
	if t.Run != nil {
		t.Run(ctx)
	}
}

func (t Task) abort(reason AbortReason) {
	if t.Abort != nil {
		t.Abort(reason)
	}
}

type LaneStore struct {
	ctx context.Context

	queueSize int
	lanes     map[AgentSessionKey]*ExecutionLane

	mu     sync.Mutex
	closed bool
}

func NewLaneStore(parent context.Context, queueSize int) (*LaneStore, error) {
	if queueSize <= 0 {
		return nil, fmt.Errorf("queue size must be positive")
	}
	if parent == nil {
		parent = context.Background()
	}

	return &LaneStore{
		ctx:       parent,
		queueSize: queueSize,
		lanes:     make(map[AgentSessionKey]*ExecutionLane),
	}, nil
}

func (s *LaneStore) GetOrCreate(key AgentSessionKey) (*ExecutionLane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrLaneClosed
	}

	if lane := s.lanes[key]; lane != nil {
		return lane, nil
	}

	lane, err := NewExecutionLane(s.ctx, s.queueSize)
	if err != nil {
		return nil, err
	}
	s.lanes[key] = lane
	return lane, nil
}

func (s *LaneStore) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true

	lanes := make([]*ExecutionLane, 0, len(s.lanes))
	for _, lane := range s.lanes {
		lanes = append(lanes, lane)
	}
	s.mu.Unlock()

	for _, lane := range lanes {
		lane.Close()
	}
}

// CloseAndWait waits for active task cleanup and queued aborts after cancellation.
func (s *LaneStore) CloseAndWait() {
	s.Close()
	s.mu.Lock()
	lanes := make([]*ExecutionLane, 0, len(s.lanes))
	for _, lane := range s.lanes {
		lanes = append(lanes, lane)
	}
	s.mu.Unlock()
	for _, lane := range lanes {
		<-lane.Done()
	}
}
