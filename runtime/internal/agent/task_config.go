package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"gameagent/runtime/internal/task"
)

// TaskConfig is process-wide; entity definitions cannot change storage ownership.
type TaskConfig struct {
	Enabled       bool
	DBPath        string
	ScanInterval  time.Duration
	DispatchBatch int
	RetryMin      time.Duration
	RetryMax      time.Duration
	StoreOptions  task.StoreOptions
}

func DefaultTaskConfig() TaskConfig {
	return TaskConfig{DBPath: "runtime/.local/tasks/tasks.sqlite", ScanInterval: time.Second, DispatchBatch: 32, RetryMin: time.Second, RetryMax: 30 * time.Second}
}

func (c TaskConfig) WithDefaults() TaskConfig {
	if c == (TaskConfig{}) {
		return DefaultTaskConfig()
	}
	return c
}

func (c TaskConfig) Validate() error {
	c = c.WithDefaults()
	if c.ScanInterval <= 0 || c.DispatchBatch <= 0 || c.RetryMin <= 0 || c.RetryMax <= 0 || c.RetryMin > c.RetryMax || c.Enabled && strings.TrimSpace(c.DBPath) == "" {
		return fmt.Errorf("invalid task configuration")
	}
	options := c.StoreOptions
	options.Path = "validation"
	if _, err := options.Resolve(); err != nil {
		return fmt.Errorf("invalid task store options: %w", err)
	}
	return nil
}

func (c *TaskConfig) UnmarshalJSON(data []byte) error {
	defaults := DefaultTaskConfig()
	raw := struct {
		Enabled bool   `json:"enabled"`
		DBPath  string `json:"db_path"`
		Scan    int64  `json:"scan_interval_ms"`
		Batch   int    `json:"dispatch_batch"`
		Min     int64  `json:"retry_min_ms"`
		Max     int64  `json:"retry_max_ms"`
		Store   struct {
			Busy     int64 `json:"busy_timeout_ms"`
			Task     int   `json:"max_task_bytes"`
			Snapshot int   `json:"max_snapshot_bytes"`
			Count    int   `json:"max_tasks_per_world"`
		} `json:"store_options"`
	}{DBPath: defaults.DBPath, Scan: 1000, Batch: 32, Min: 1000, Max: 30000}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Scan <= 0 || raw.Min <= 0 || raw.Max <= 0 || raw.Batch <= 0 {
		return fmt.Errorf("task durations and dispatch batch must be positive")
	}
	for _, ms := range []int64{raw.Scan, raw.Min, raw.Max, raw.Store.Busy} {
		if ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
			return fmt.Errorf("task duration out of range")
		}
	}
	*c = TaskConfig{Enabled: raw.Enabled, DBPath: raw.DBPath, ScanInterval: time.Duration(raw.Scan) * time.Millisecond, DispatchBatch: raw.Batch, RetryMin: time.Duration(raw.Min) * time.Millisecond, RetryMax: time.Duration(raw.Max) * time.Millisecond, StoreOptions: task.StoreOptions{BusyTimeout: time.Duration(raw.Store.Busy) * time.Millisecond, MaxTaskBytes: raw.Store.Task, MaxSnapshotBytes: raw.Store.Snapshot, MaxTasksPerWorld: raw.Store.Count}}
	return c.Validate()
}
