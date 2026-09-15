package task

import (
	"strings"
	"time"
)

const (
	defaultBusyTimeout      = 5 * time.Second
	defaultMaxTaskBytes     = 256 << 10
	defaultMaxSnapshotBytes = 32 << 20
	defaultMaxTasksPerWorld = 1024
)

type StoreOptions struct {
	Path             string
	BusyTimeout      time.Duration
	MaxTaskBytes     int
	MaxSnapshotBytes int
	MaxTasksPerWorld int
	// CheckpointBarrierTimeout bounds how long a prepared save may hold the world before the
	// kernel retires the barrier. Production keeps the documented ten seconds; fault-window
	// tests shorten it to observe the same code path without waiting.
	CheckpointBarrierTimeout time.Duration
}

func (o StoreOptions) Resolve() (StoreOptions, error) {
	if strings.TrimSpace(o.Path) == "" ||
		o.BusyTimeout < 0 ||
		o.MaxTaskBytes < 0 ||
		o.MaxSnapshotBytes < 0 ||
		o.MaxTasksPerWorld < 0 ||
		o.CheckpointBarrierTimeout < 0 {
		return StoreOptions{}, ErrInvalidTaskSpec
	}
	if o.BusyTimeout == 0 {
		o.BusyTimeout = defaultBusyTimeout
	}
	if o.MaxTaskBytes == 0 {
		o.MaxTaskBytes = defaultMaxTaskBytes
	}
	if o.MaxSnapshotBytes == 0 {
		o.MaxSnapshotBytes = defaultMaxSnapshotBytes
	}
	if o.MaxTasksPerWorld == 0 {
		o.MaxTasksPerWorld = defaultMaxTasksPerWorld
	}
	if o.CheckpointBarrierTimeout == 0 {
		o.CheckpointBarrierTimeout = time.Duration(checkpointBarrierTimeoutMS) * time.Millisecond
	}
	return o, nil
}
