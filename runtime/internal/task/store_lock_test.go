package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const storeLockHelperEnv = "GAMEAGENT_TASK_STORE_LOCK_HELPER"

func TestStoreLockRejectsSecondWriterAndAllowsDifferentPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.sqlite")
	first := openTaskTestStore(t, StoreOptions{Path: path})

	_, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: filepath.Clean(path)})
	if !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("same-path second open error = %v, want ErrStoreInUse", err)
	}
	assertTaskErrorSanitized(t, err, dir, "LockFileEx", "Flock")

	other := openTaskTestStore(t, StoreOptions{Path: filepath.Join(dir, "other.sqlite")})
	if first == other {
		t.Fatal("different paths returned same store")
	}
}

func TestStoreLockCanonicalizesRelativeAndAbsolutePaths(t *testing.T) {
	absPath := filepath.Join(t.TempDir(), "tasks.sqlite")
	originalDirectory := mustWorkingDirectory(t)
	if err := os.Chdir(filepath.Dir(absPath)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	store := openTaskTestStore(t, StoreOptions{Path: absPath})
	if _, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: filepath.Base(absPath)}); !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("relative alias open error = %v, want ErrStoreInUse", err)
	}
	if store.path != filepath.Clean(absPath) {
		t.Fatalf("resolved store path = %q, want %q", store.path, filepath.Clean(absPath))
	}
}

func TestStoreLockHelperProcessExclusionAndCrashRecovery(t *testing.T) {
	if os.Getenv(storeLockHelperEnv) != "" {
		runStoreLockHelperProcess()
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("tasks-%d.sqlite", os.Getpid()))
	readyPath := filepath.Join(dir, fmt.Sprintf("ready-%d", os.Getpid()))
	command := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelperProcessExclusionAndCrashRecovery$")
	command.Env = append(os.Environ(), storeLockHelperEnv+"=1", "GAMEAGENT_TASK_STORE_PATH="+path, "GAMEAGENT_TASK_STORE_READY="+readyPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	terminated := false
	t.Cleanup(func() {
		if !terminated && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	waitForHelperReady(t, command, readyPath)

	if _, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: path}); !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("live helper exclusion error = %v, want ErrStoreInUse", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("forced helper termination unexpectedly reported success")
	}
	terminated = true

	reopened := openTaskTestStore(t, StoreOptions{Path: path})
	if reopened == nil {
		t.Fatal("reopen after helper crash returned nil")
	}
}

func runStoreLockHelperProcess() {
	path := os.Getenv("GAMEAGENT_TASK_STORE_PATH")
	readyPath := os.Getenv("GAMEAGENT_TASK_STORE_READY")
	store, err := OpenSQLiteStore(context.Background(), StoreOptions{Path: path})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "helper open failed")
		os.Exit(2)
	}
	if err := os.WriteFile(readyPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		_ = store.Close()
		os.Exit(3)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForHelperReady(t *testing.T, command *exec.Cmd, readyPath string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(readyPath); err == nil && len(data) > 0 {
			return
		}
		if command.ProcessState != nil {
			t.Fatalf("helper exited before ready: %v", command.ProcessState)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	t.Fatalf("helper did not become ready on %s", runtime.GOOS)
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
