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
}

func (o StoreOptions) Resolve() (StoreOptions, error) {
	if strings.TrimSpace(o.Path) == "" ||
		o.BusyTimeout < 0 ||
		o.MaxTaskBytes < 0 ||
		o.MaxSnapshotBytes < 0 ||
		o.MaxTasksPerWorld < 0 {
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
	return o, nil
}
