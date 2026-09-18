// Package dataroot resolves the single data root the Runtime writes to, and the
// runtime-owned paths that hang off it.
//
// The Runtime must not depend on the process working directory: a released
// binary can be started from anywhere. Every path the Runtime owns is therefore
// either absolute, or relative to the data root.
package dataroot

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// EnvName is the environment variable that overrides the platform default.
	EnvName = "WIA_DATA_ROOT"

	// FlagName is the command line flag that takes precedence over EnvName.
	FlagName = "data-root"

	configDirName  = "config"
	dataDirName    = "data"
	secretsDirName = "secrets"

	traceFileName = "traces.jsonl"

	windowsAppDir = "WorldIsAgent"
	darwinAppDir  = "WorldIsAgent"
	linuxAppDir   = "wia"
)

// Env carries the ambient facts root resolution depends on, so that resolution
// is testable without touching the real process environment.
type Env struct {
	GOOS    string
	Getenv  func(string) string
	HomeDir func() (string, error)
}

// OS returns the Env of the running process.
func OS() Env {
	return Env{GOOS: runtime.GOOS, Getenv: os.Getenv, HomeDir: os.UserHomeDir}
}

// Resolve returns the absolute data root, applying the frozen precedence:
// explicit flag, then WIA_DATA_ROOT, then the platform default.
func Resolve(explicit string, env Env) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return absolute(value)
	}
	if value := strings.TrimSpace(env.Getenv(EnvName)); value != "" {
		return absolute(value)
	}
	return Default(env)
}

// Default returns the platform default data root. Windows uses LOCALAPPDATA
// rather than APPDATA because SQLite stores and traces must not roam.
func Default(env Env) (string, error) {
	switch env.GOOS {
	case "windows":
		if dir := strings.TrimSpace(env.Getenv("LOCALAPPDATA")); dir != "" {
			return filepath.Join(dir, windowsAppDir), nil
		}
		home, err := env.HomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve data root: %w", err)
		}
		return filepath.Join(home, "AppData", "Local", windowsAppDir), nil
	case "darwin":
		home, err := env.HomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve data root: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", darwinAppDir), nil
	default:
		if dir := strings.TrimSpace(env.Getenv("XDG_DATA_HOME")); dir != "" {
			return filepath.Join(dir, linuxAppDir), nil
		}
		home, err := env.HomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve data root: %w", err)
		}
		return filepath.Join(home, ".local", "share", linuxAppDir), nil
	}
}

// ResolvePath interprets a configured path value against the root: an absolute
// value is used as-is, a relative value is relative to the root, and an empty
// value is the root itself. The data root is the basis for defaults and
// relative values, not a rule that moves already-absolute paths.
func ResolvePath(root, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return filepath.Clean(root)
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	return filepath.Join(root, trimmed)
}

func absolute(value string) (string, error) {
	path, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	return path, nil
}

// Layout is the directory structure under one data root.
type Layout struct {
	root string
}

// New returns the layout of an absolute data root.
func New(root string) Layout {
	return Layout{root: filepath.Clean(root)}
}

// Root returns the absolute data root.
func (l Layout) Root() string { return l.root }

// ConfigDir holds the Runtime configuration files.
func (l Layout) ConfigDir() string { return filepath.Join(l.root, configDirName) }

// ConfigPath returns the path of a named configuration file.
func (l Layout) ConfigPath(name string) string { return filepath.Join(l.ConfigDir(), name) }

// DataDir holds everything the Runtime writes while running.
func (l Layout) DataDir() string { return filepath.Join(l.root, dataDirName) }

// TracePath returns the default trace file.
func (l Layout) TracePath() string { return filepath.Join(l.DataDir(), traceFileName) }

// SecretsDir is reserved for credentials that never leave the backend.
func (l Layout) SecretsDir() string { return filepath.Join(l.root, secretsDirName) }

// Ensure creates the directories the Runtime always owns. Per-store directories
// are created by the stores themselves, because their location is configurable.
func (l Layout) Ensure() error {
	for _, dir := range []string{l.root, l.ConfigDir(), l.DataDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create data root directory %s: %w", dir, err)
		}
	}
	if err := os.MkdirAll(l.SecretsDir(), 0o700); err != nil {
		return fmt.Errorf("create data root directory %s: %w", l.SecretsDir(), err)
	}
	return nil
}
