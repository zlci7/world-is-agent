package task

import (
	"testing"
	"time"
)

func TestStoreOptionsResolveAppliesExactDefaults(t *testing.T) {
	got, err := (StoreOptions{Path: "relative/tasks.sqlite"}).Resolve()
	if err != nil {
		t.Fatalf("StoreOptions.Resolve() returned error: %v", err)
	}
	want := StoreOptions{
		Path:             "relative/tasks.sqlite",
		BusyTimeout:      5 * time.Second,
		MaxTaskBytes:     256 << 10,
		MaxSnapshotBytes: 32 << 20,
		MaxTasksPerWorld: 1024,
	}
	if got != want {
		t.Fatalf("StoreOptions.Resolve() = %+v, want %+v", got, want)
	}
}

func TestStoreOptionsResolvePreservesExplicitValues(t *testing.T) {
	want := StoreOptions{
		Path:             "tasks.sqlite",
		BusyTimeout:      3 * time.Second,
		MaxTaskBytes:     17,
		MaxSnapshotBytes: 29,
		MaxTasksPerWorld: 7,
	}
	got, err := want.Resolve()
	if err != nil {
		t.Fatalf("StoreOptions.Resolve() returned error: %v", err)
	}
	if got != want {
		t.Fatalf("StoreOptions.Resolve() = %+v, want %+v", got, want)
	}
}

func TestStoreOptionsRejectsMissingPathAndNegativeValues(t *testing.T) {
	tests := []struct {
		name    string
		options StoreOptions
	}{
		{name: "empty path", options: StoreOptions{}},
		{name: "blank path", options: StoreOptions{Path: " \t\n"}},
		{name: "negative busy timeout", options: StoreOptions{Path: "tasks.sqlite", BusyTimeout: -time.Nanosecond}},
		{name: "negative max task bytes", options: StoreOptions{Path: "tasks.sqlite", MaxTaskBytes: -1}},
		{name: "negative max snapshot bytes", options: StoreOptions{Path: "tasks.sqlite", MaxSnapshotBytes: -1}},
		{name: "negative max tasks per world", options: StoreOptions{Path: "tasks.sqlite", MaxTasksPerWorld: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.options.Resolve(); err == nil {
				t.Fatal("StoreOptions.Resolve() returned nil error")
			}
		})
	}
}
