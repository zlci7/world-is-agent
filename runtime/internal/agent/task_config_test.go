package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gameagent/runtime/internal/agent"
)

func TestTaskConfigRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":false,"db_path":"","scan_interval_ms":0,"dispatch_batch":0,"retry_min_ms":0,"retry_max_ms":0}`,
		`{"enabled":true,"db_path":""}`, `{"scan_interval_ms":-1}`, `{"scan_interval_ms":0}`,
		`{"scan_interval_ms":9223372036854775807}`, `{"dispatch_batch":0}`, `{"dispatch_batch":-1}`,
		`{"retry_min_ms":0}`, `{"retry_max_ms":-1}`, `{"retry_min_ms":40000,"retry_max_ms":30000}`,
		`{"store_options":{"busy_timeout_ms":-1}}`, `{"store_options":{"max_task_bytes":-1}}`,
		`{"store_options":{"max_snapshot_bytes":-1}}`, `{"store_options":{"max_tasks_per_world":-1}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(path, []byte(`{"task":`+raw+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := agent.LoadConfigFile(path); err == nil {
				t.Fatal("invalid task configuration was accepted")
			}
		})
	}
}

func TestTaskConfigDefaultsAndOverrides(t *testing.T) {
	for _, content := range []string{`{}`, `{"task":{}}`} {
		path := filepath.Join(t.TempDir(), "agent.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		config, err := agent.LoadConfigFile(path)
		if err != nil {
			t.Fatal(err)
		}
		task := config.Task
		// The default is relative to the Runtime data root, so it must stay relative.
		if !task.Enabled || task.DBPath != "data/tasks/tasks.sqlite" || filepath.IsAbs(task.DBPath) || task.ScanInterval != time.Second || task.DispatchBatch != 32 || task.RetryMin != time.Second || task.RetryMax != 30*time.Second {
			t.Fatalf("defaults=%+v", task)
		}
		options := task.StoreOptions
		options.Path = task.DBPath
		resolved, err := options.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		if resolved.BusyTimeout != 5*time.Second || resolved.MaxTaskBytes != 256<<10 || resolved.MaxSnapshotBytes != 32<<20 || resolved.MaxTasksPerWorld != 1024 {
			t.Fatalf("store defaults=%+v", resolved)
		}
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"task":{"enabled":true,"db_path":"owned.sqlite","scan_interval_ms":23,"dispatch_batch":4,"retry_min_ms":5,"retry_max_ms":20,"store_options":{"busy_timeout_ms":6,"max_task_bytes":300,"max_snapshot_bytes":900,"max_tasks_per_world":8}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := agent.LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	task := config.Task
	if !task.Enabled || task.DBPath != "owned.sqlite" || task.ScanInterval != 23*time.Millisecond || task.DispatchBatch != 4 || task.RetryMin != 5*time.Millisecond || task.RetryMax != 20*time.Millisecond || task.StoreOptions.BusyTimeout != 6*time.Millisecond || task.StoreOptions.MaxTaskBytes != 300 || task.StoreOptions.MaxSnapshotBytes != 900 || task.StoreOptions.MaxTasksPerWorld != 8 {
		t.Fatalf("overrides=%+v", task)
	}
}

func TestTaskConfigExplicitDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"task":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := agent.LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Task.Enabled {
		t.Fatal("explicit task disable was ignored")
	}
}
