package bootstrap

import (
	"context"
	"errors"
	"time"

	"gameagent/runtime/internal/task"
)

var (
	ErrConsoleRuntimeChanged  = errors.New("runtime_changed")
	ErrConsoleRuntimeNotReady = errors.New("runtime_not_ready")
)

func (r *Runtime) consoleScope(ctx context.Context, q task.ConsoleQuery) (*runtimeBundle, context.Context, func(), error) {
	r.mu.RLock()
	bundle := r.bundle
	if r.closed || !r.snapshot.Ready || bundle == nil {
		r.mu.RUnlock()
		return nil, nil, nil, ErrConsoleRuntimeNotReady
	}
	if bundle.id != q.InstanceID || r.snapshot.LoadedGame == nil || r.snapshot.LoadedGame.ID != q.GameID {
		r.mu.RUnlock()
		return nil, nil, nil, ErrConsoleRuntimeChanged
	}
	readContext := bundle.readContext
	r.mu.RUnlock()
	queryContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	stop := context.AfterFunc(readContext, cancel)
	return bundle, queryContext, func() { stop(); cancel() }, nil
}

func (r *Runtime) consoleCurrent(bundle *runtimeBundle, queryErr error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || !r.snapshot.Ready || r.bundle != bundle {
		return ErrConsoleRuntimeChanged
	}
	return queryErr
}

func overlayConsoleWorld(bundle *runtimeBundle, world *task.ConsoleWorld) map[string]string {
	live := bundle.gateway.ConsoleWorld(world.Head.Binding.World)
	if live.Head.Binding != world.Head.Binding || live.Head.CheckpointID != world.Head.CheckpointID {
		return nil
	}
	if world.Availability != "recovery_blocked" {
		if live.Saving && world.SaveBarrierActive {
			world.Availability = "saving"
		} else if live.Ready && !world.SaveBarrierActive {
			world.Availability = "ready"
		}
	}
	return live.Names
}

func (r *Runtime) ReadTaskWorlds(ctx context.Context, q task.ConsoleQuery) (task.ConsoleWorlds, error) {
	if err := q.Validate("worlds"); err != nil {
		return task.ConsoleWorlds{}, err
	}
	bundle, ctx, done, err := r.consoleScope(ctx, q)
	if err != nil {
		return task.ConsoleWorlds{}, err
	}
	defer done()
	result := task.ConsoleWorlds{InstanceID: q.InstanceID, GameID: q.GameID, ObservedAt: time.Now().UTC(), Worlds: []task.ConsoleWorld{}}
	if bundle.taskStore != nil {
		result, err = bundle.taskStore.ConsoleWorlds(ctx, q)
		if err == nil {
			for i := range result.Worlds {
				overlayConsoleWorld(bundle, &result.Worlds[i])
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	return result, r.consoleCurrent(bundle, err)
}

func (r *Runtime) ReadTaskPage(ctx context.Context, q task.ConsoleQuery, detail bool) (task.ConsolePage, error) {
	kind := "tasks"
	if detail {
		kind = "detail"
	}
	if err := q.Validate(kind); err != nil {
		return task.ConsolePage{}, err
	}
	bundle, ctx, done, err := r.consoleScope(ctx, q)
	if err != nil {
		return task.ConsolePage{}, err
	}
	defer done()
	result := task.ConsolePage{InstanceID: q.InstanceID, GameID: q.GameID, WorldID: q.WorldID, ObservedAt: time.Now().UTC(), Items: []task.ConsoleTask{}, Counts: map[task.State]int{}}
	if bundle.taskStore != nil {
		result, err = bundle.taskStore.ConsoleTasks(ctx, q, detail)
		if err == nil {
			names := overlayConsoleWorld(bundle, &result.World)
			for i := range result.Items {
				result.Items[i].EntityDisplayName = names[result.Items[i].EntityID]
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	return result, r.consoleCurrent(bundle, err)
}
