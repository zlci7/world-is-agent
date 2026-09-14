package gateway

import (
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

type worldTaskAuthority struct {
	registry *WorldRegistry
	env      *streamEnvironment
	world    task.WorldKey
}

func (e *streamEnvironment) TaskRuntime() (*task.Service, tool.TaskWorld) {
	if e.taskAuthority == nil {
		return nil, nil
	}
	return e.taskAuthority.registry.service, e.taskAuthority
}

func (e *historyMaintenanceEnvironment) TaskRuntime() (*task.Service, tool.TaskWorld) {
	provider, ok := e.Environment.(interface {
		TaskRuntime() (*task.Service, tool.TaskWorld)
	})
	if !ok {
		return nil, nil
	}
	return provider.TaskRuntime()
}

func (a *worldTaskAuthority) Current() (task.Head, uint64, bool) {
	slot := a.registry.slot(a.world, false)
	if slot == nil {
		return task.Head{}, 0, false
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if !slot.ready || slot.saving || slot.owner == nil || slot.owner.env != a.env {
		return task.Head{}, 0, false
	}
	return slot.head, slot.authorityEpoch, true
}

func (a *worldTaskAuthority) GuardOwner(binding task.Binding, epoch uint64, entityID string, fn func(task.Head) error) error {
	return a.guardEntry(binding, epoch, func(entry WorldEntry) error {
		if entry.Entities[entityID] == nil {
			return task.ErrSourceInvalid
		}
		return fn(entry.Head)
	})
}

func (a *worldTaskAuthority) Guard(binding task.Binding, epoch uint64, fn func(task.Head) error) error {
	return a.guardEntry(binding, epoch, func(entry WorldEntry) error { return fn(entry.Head) })
}

func (a *worldTaskAuthority) guardEntry(binding task.Binding, epoch uint64, fn func(WorldEntry) error) error {
	if binding.World != a.world {
		return task.ErrWorldMismatch
	}
	return a.registry.Guard(a.env, binding, func(entry WorldEntry) error {
		if entry.AuthorityEpoch != epoch {
			return task.ErrGenerationStale
		}
		return fn(entry)
	})
}
