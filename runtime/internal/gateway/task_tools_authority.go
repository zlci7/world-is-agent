package gateway

import (
	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

// Event targets and binding seeds share one current-world identity directory.
// Identity admission does not grant player interaction or task execution rights.
func (a *worldTaskAuthority) RegisterEventTarget(event *protocol.GameEvent, target *protocol.EntityRef) *protocol.Error {
	slot := a.registry.slot(a.world, false)
	if slot == nil {
		return nil
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.owner == nil || slot.owner.env != a.env {
		return taskErrorToProtocol(task.ErrGenerationStale)
	}
	if !slot.ready || slot.saving {
		return nil
	}
	if event.GetWorldId() != a.world.WorldID {
		return taskErrorToProtocol(task.ErrWorldMismatch)
	}
	if source := event.GetInteractionSource(); source != nil {
		binding, err := taskBindingFromProtocol(source.GetScope())
		if err != nil || binding != slot.head.Binding {
			return taskErrorToProtocol(task.ErrSourceInvalid)
		}
	}
	if target.GetEntityId() == "" || target.GetEntityType() == "" || target.GetDefinitionId() == "" {
		return &protocol.Error{Code: "target_entity_invalid", Message: "target entity requires id, type and definition_id"}
	}
	if prior := slot.entities[target.EntityId]; prior != nil &&
		(prior.EntityType != target.EntityType || prior.DefinitionId != target.DefinitionId) {
		return &protocol.Error{Code: "target_entity_conflict", Message: "target entity conflicts with the current world identity"}
	}
	slot.entities[target.EntityId] = cloneEntityRef(target)
	return nil
}

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
