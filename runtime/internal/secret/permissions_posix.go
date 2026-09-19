//go:build !windows

package secret

import (
	"fmt"
	"os"
)

// restrict makes path readable only by its owner.
//
// The result is checked rather than assumed: chmod can report success and still
// leave a different mode on a filesystem that cannot represent the requested one.
func restrictPlatform(path string, isDir bool) error {
	mode := os.FileMode(FileMode)
	if isDir {
		mode = DirMode
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set owner-only permissions: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect permissions: %w", err)
	}
	if info.Mode().Perm() != mode {
		return fmt.Errorf("permissions are %04o, want %04o", info.Mode().Perm(), mode)
	}
	return nil
}
