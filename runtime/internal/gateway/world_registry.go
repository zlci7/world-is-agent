package gateway

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

const taskExtension = "gameagent.tasks.v1"

// WorldEntry is a defensive snapshot; Environment and Lanes are fenced lifecycle handles.
type WorldEntry struct {
	Head           task.Head
	AuthorityEpoch uint64
	ConnectionID   string
	Entities       map[string]*protocol.EntityRef
	Catalog        tool.TurnToolView
	Environment    agent.Environment
	Lanes          *session.LaneStore
}

type worldConnection struct {
	lifecycle sync.Mutex
	id        string
	hello     *protocol.AdapterHello
	env       *streamEnvironment
	transport *streamEnvironment
	lanes     *session.LaneStore
	catalog   *tool.EnvironmentToolCatalog
	world     *task.WorldKey
}

type worldSlot struct {
	lifecycle      sync.Mutex
	mu             sync.Mutex
	owner          *worldConnection
	head           task.Head
	entities       map[string]*protocol.EntityRef
	ready          bool
	saving         bool
	failed         bool
	authorityEpoch uint64
}

type WorldRegistry struct {
	updates        chan struct{}
	mu             sync.Mutex
	worlds         map[task.WorldKey]*worldSlot
	service        *task.Service
	stopped        bool
	interactions   map[string]struct{}
	interactionMu  sync.Mutex
	dispatchConfig task.DispatcherConfig
}

func NewWorldRegistry(service *task.Service) *WorldRegistry {
	return &WorldRegistry{worlds: make(map[task.WorldKey]*worldSlot), service: service, updates: make(chan struct{}, 1), interactions: make(map[string]struct{}), dispatchConfig: task.DispatcherConfig{RetryMin: 10 * time.Millisecond, RetryMax: 100 * time.Millisecond}}
}

func (r *WorldRegistry) notify() {
	select {
	case r.updates <- struct{}{}:
	default:
	}
}

func (r *WorldRegistry) slot(world task.WorldKey, create bool) *worldSlot {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot := r.worlds[world]
	if slot == nil && create && !r.stopped {
		slot = &worldSlot{}
		r.worlds[world] = slot
	}
	return slot
}

func (s *worldSlot) snapshot() WorldEntry {
	entities := make(map[string]*protocol.EntityRef, len(s.entities))
	for id, entity := range s.entities {
		entities[id] = cloneEntityRef(entity)
	}
	return WorldEntry{Head: s.head, AuthorityEpoch: s.authorityEpoch, ConnectionID: s.owner.id, Entities: entities, Catalog: s.owner.catalog.Snapshot(), Environment: s.owner.env, Lanes: s.owner.lanes}
}

func (r *WorldRegistry) Current(world task.WorldKey) (WorldEntry, bool) {
	slot := r.slot(world, false)
	if slot == nil {
		return WorldEntry{}, false
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if !slot.ready || slot.saving || slot.owner == nil {
		return WorldEntry{}, false
	}
	return slot.snapshot(), true
}

func (r *WorldRegistry) ReadyWorlds() []task.Head {
	r.mu.Lock()
	slots := make([]*worldSlot, 0, len(r.worlds))
	for _, slot := range r.worlds {
		slots = append(slots, slot)
	}
	r.mu.Unlock()
	heads := make([]task.Head, 0, len(slots))
	for _, slot := range slots {
		slot.mu.Lock()
		if slot.ready && !slot.saving {
			heads = append(heads, slot.head)
		}
		slot.mu.Unlock()
	}
	sort.Slice(heads, func(i, j int) bool {
		a, b := heads[i].Binding.World, heads[j].Binding.World
		if a.GameID != b.GameID {
			return a.GameID < b.GameID
		}
		return a.WorldID < b.WorldID
	})
	return heads
}

func (s *worldSlot) check(env *streamEnvironment, binding task.Binding) error {
	if s.owner == nil || s.owner.env != env || s.head.Binding != binding {
		return task.ErrGenerationStale
	}
	if !s.ready {
		return task.ErrWorldNotReady
	}
	if s.saving {
		return task.ErrSaveInProgress
	}
	return nil
}

// Guard serializes short authority checks and durable mutations. The callback must
// finish without model calls, network waits, or re-entering the registry.
func (r *WorldRegistry) Guard(env *streamEnvironment, binding task.Binding, fn func(WorldEntry) error) error {
	slot := r.slot(binding.World, false)
	if slot == nil {
		return task.ErrWorldNotReady
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if err := slot.check(env, binding); err != nil {
		return err
	}
	return fn(slot.snapshot())
}

func (r *WorldRegistry) SetSaveBarrier(env *streamEnvironment, binding task.Binding, saving bool) error {
	slot := r.slot(binding.World, false)
	if slot == nil {
		return task.ErrWorldNotReady
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.owner == nil || slot.owner.env != env || slot.head.Binding != binding {
		return task.ErrGenerationStale
	}
	if !slot.ready {
		return task.ErrWorldNotReady
	}
	if saving && !slot.saving {
		slot.authorityEpoch++
	}
	slot.saving = saving
	return nil
}

func validateWorldDirectory(value taskWorldBinding) (map[string]*protocol.EntityRef, error) {
	if len(value.Entities) == 0 {
		return nil, task.ErrInvalidTaskSpec
	}
	result := make(map[string]*protocol.EntityRef, len(value.Entities))
	for _, entity := range value.Entities {
		for _, id := range []string{entity.EntityId, entity.EntityType, entity.DefinitionId} {
			if id == "" || strings.TrimSpace(id) != id {
				return nil, task.ErrInvalidTaskSpec
			}
		}
		if _, exists := result[entity.EntityId]; exists {
			return nil, task.ErrInvalidTaskSpec
		}
		result[entity.EntityId] = cloneEntityRef(entity)
	}
	return result, nil
}

func (r *WorldRegistry) bind(ctx context.Context, conn *worldConnection, request *protocol.WorldBinding) (task.Head, error) {
	conn.lifecycle.Lock()
	defer conn.lifecycle.Unlock()
	if conn.world != nil {
		if current := r.slot(*conn.world, false); current != nil {
			current.mu.Lock()
			if current.owner == conn {
				current.ready = false
			}
			current.mu.Unlock()
		}
	}
	value, err := taskWorldBindingFromProtocol(request)
	if err != nil {
		return task.Head{}, err
	}
	if value.Binding.World.GameID != conn.hello.GameId {
		return task.Head{}, task.ErrWorldMismatch
	}
	entities, err := validateWorldDirectory(value)
	if err != nil {
		return task.Head{}, err
	}
	ref := task.CheckpointRef{Status: "absent", World: value.Binding.World}
	if value.Checkpoint != nil {
		ref = *value.Checkpoint
	}
	if ref.World != value.Binding.World {
		return task.Head{}, task.ErrCheckpointInvalid
	}
	if conn.world != nil && *conn.world != value.Binding.World {
		return task.Head{}, task.ErrWorldMismatch
	}
	slot := r.slot(value.Binding.World, true)
	if slot == nil {
		return task.Head{}, task.ErrWorldNotReady
	}
	slot.lifecycle.Lock()
	defer slot.lifecycle.Unlock()
	slot.mu.Lock()
	if slot.failed {
		slot.mu.Unlock()
		return task.Head{}, task.ErrWorldNotReady
	}
	rebinding := slot.owner == conn
	persisted, headErr := r.service.ReadHead(ctx, value.Binding.World)
	if headErr != nil && !errors.Is(headErr, task.ErrTaskNotFound) {
		if rebinding {
			slot.failed = true
			slot.authorityEpoch++
		}
		slot.mu.Unlock()
		if rebinding {
			conn.env.close()
			conn.lanes.CloseAndWait()
		}
		return task.Head{}, headErr
	}
	if value.Binding.Generation != 0 && (headErr != nil || value.Binding.Generation != persisted.Binding.Generation || value.Binding.RunID != persisted.Binding.RunID) {
		slot.mu.Unlock()
		return task.Head{}, task.ErrGenerationStale
	}
	if slot.owner != nil && slot.owner != conn && value.Binding.Generation == 0 {
		slot.mu.Unlock()
		return task.Head{}, task.ErrWorldNotReady
	}
	if rebinding && value.Binding.RunID != slot.head.Binding.RunID {
		slot.mu.Unlock()
		return task.Head{}, task.ErrGenerationStale
	}
	if slot.saving {
		slot.mu.Unlock()
		return task.Head{}, task.ErrSaveInProgress
	}
	oldHead, oldOwner := slot.head, slot.owner
	slot.authorityEpoch++
	slot.ready = false
	slot.mu.Unlock()
	if oldOwner != nil {
		oldOwner.env.close()
		if oldOwner != conn {
			oldOwner.transport.close()
		}
		oldOwner.lanes.CloseAndWait()
		reason := "world_rebind"
		if oldHead.Status == "paused" && oldHead.Reason != "" {
			reason = oldHead.Reason
		}
		if err := r.service.DeactivateWorld(ctx, oldHead.Binding, reason); err != nil {
			slot.mu.Lock()
			slot.failed = true
			slot.mu.Unlock()
			return task.Head{}, err
		}
	}
	if rebinding {
		nextEnv := newStreamEnvironment(conn.env.stream)
		nextEnv.sendSlot = conn.transport.sendSlot
		nextLanes, err := session.NewLaneStore(ctx, session.DefaultQueueSize)
		if err != nil {
			return task.Head{}, err
		}
		slot.mu.Lock()
		conn.env, conn.lanes = nextEnv, nextLanes
		slot.mu.Unlock()
	}
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return task.Head{}, task.ErrWorldNotReady
	}
	head, err := r.service.ActivateWorld(ctx, value.Binding.World, value.Binding.RunID, value.Clock, ref)
	if err != nil {
		return head, err
	}
	if head.Status != "ready" {
		return head, task.ErrWorldNotReady
	}
	slot.mu.Lock()
	slot.owner = conn
	slot.head = head
	conn.env.taskAuthority = &worldTaskAuthority{registry: r, env: conn.env, world: head.Binding.World}
	slot.entities = entities
	slot.saving = false
	world := value.Binding.World
	conn.world = &world
	slot.mu.Unlock()
	return head, nil
}

func (r *WorldRegistry) markReady(conn *worldConnection, head task.Head) error {
	slot := r.slot(head.Binding.World, false)
	if slot == nil {
		return task.ErrWorldNotReady
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if r.stopped || slot.owner != conn || slot.head != head || slot.failed {
		return task.ErrWorldNotReady
	}
	slot.ready = true
	r.notify()
	return nil
}

func (r *WorldRegistry) UpdateClock(ctx context.Context, env *streamEnvironment, update *protocol.WorldClockUpdate) error {
	binding, err := taskBindingFromProtocol(update.GetScope())
	if err != nil {
		return err
	}
	clock, err := taskClockFromProtocol(update.GetClock())
	if err != nil {
		return err
	}
	// Distinguish a foreign world from an expired owner of the correct world.
	r.mu.Lock()
	slots := make([]*worldSlot, 0, len(r.worlds))
	for _, s := range r.worlds {
		slots = append(slots, s)
	}
	r.mu.Unlock()
	for _, slot := range slots {
		slot.mu.Lock()
		owns := slot.owner != nil && slot.owner.env == env
		world := slot.head.Binding.World
		slot.mu.Unlock()
		if owns && world != binding.World {
			return task.ErrWorldMismatch
		}
	}
	slot := r.slot(binding.World, false)
	if slot == nil {
		return task.ErrWorldNotReady
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if err := slot.check(env, binding); err != nil {
		return err
	}
	current := slot.head.Clock
	if clock.ID != current.ID {
		return task.ErrClockMismatch
	}
	if clock.Sequence == current.Sequence && clock != current {
		return r.pauseClockViolation(ctx, slot, task.ErrClockMismatch)
	}
	if clock.Sequence < current.Sequence || clock.Tick < current.Tick {
		return r.pauseClockViolation(ctx, slot, task.ErrClockRewound)
	}
	head, err := r.service.UpdateClock(ctx, binding, clock)
	if err != nil {
		return err
	}
	slot.head = head
	r.notify()
	return nil
}

// The caller holds slot.mu so authority closes before another Task mutation.
func (r *WorldRegistry) pauseClockViolation(ctx context.Context, slot *worldSlot, cause error) error {
	slot.authorityEpoch++
	slot.ready = false
	slot.head.Status = "paused"
	slot.head.Reason = taskDispatchCode(cause)
	pauseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(cause, r.service.DeactivateWorld(pauseCtx, slot.head.Binding, slot.head.Reason))
}

func (r *WorldRegistry) disconnect(ctx context.Context, conn *worldConnection, reason string) error {
	conn.lifecycle.Lock()
	defer conn.lifecycle.Unlock()
	conn.transport.close()
	if conn.world == nil {
		conn.env.close()
		conn.lanes.CloseAndWait()
		return nil
	}
	slot := r.slot(*conn.world, false)
	if slot == nil {
		return nil
	}
	slot.lifecycle.Lock()
	defer slot.lifecycle.Unlock()
	slot.mu.Lock()
	if slot.owner != conn {
		slot.mu.Unlock()
		conn.env.close()
		conn.lanes.CloseAndWait()
		return nil
	}
	slot.ready = false
	head := slot.head
	slot.authorityEpoch++
	slot.mu.Unlock()
	conn.env.close()
	conn.lanes.CloseAndWait()
	if head.Status == "paused" && head.Reason != "" {
		reason = head.Reason
	}
	err := r.service.DeactivateWorld(ctx, head.Binding, reason)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if err != nil {
		slot.failed = true
		log.Printf("task world deactivation game=%q world=%q: %s", head.Binding.World.GameID, head.Binding.World.WorldID, logSafeError(err))
		return err
	}
	slot.owner = nil
	slot.head.Status = "paused"
	slot.head.Reason = reason
	return nil
}

func (r *WorldRegistry) StopAdmission() {
	r.mu.Lock()
	r.stopped = true
	slots := make([]*worldSlot, 0, len(r.worlds))
	for _, s := range r.worlds {
		slots = append(slots, s)
	}
	r.mu.Unlock()
	for _, slot := range slots {
		slot.mu.Lock()
		slot.ready = false
		slot.mu.Unlock()
	}
}

func (r *WorldRegistry) Close(ctx context.Context) error {
	r.StopAdmission()
	r.mu.Lock()
	slots := make([]*worldSlot, 0, len(r.worlds))
	for _, s := range r.worlds {
		slots = append(slots, s)
	}
	r.mu.Unlock()
	var result error
	for _, slot := range slots {
		slot.mu.Lock()
		conn := slot.owner
		slot.mu.Unlock()
		if conn != nil {
			result = errors.Join(result, r.disconnect(ctx, conn, "runtime_shutdown"))
		}
	}
	return result
}
