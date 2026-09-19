package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileMode is the permission of the configuration files the Runtime writes. They
// are not credentials, so they stay readable; the credential itself is written by
// the secret package, which has a stricter guarantee to keep.
const FileMode = 0o644

// WriteFile replaces a configuration file atomically.
//
// Callers write configuration while the Runtime is running, so a reader must
// never observe half of it: the new content is written beside the destination and
// renamed over it.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()

	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", temporary, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync %s: %w", temporary, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", temporary, err)
	}
	if err := os.Chmod(temporary, FileMode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", temporary, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
