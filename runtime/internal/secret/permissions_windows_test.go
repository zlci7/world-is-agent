//go:build windows

package secret

import (
	"os"
	"testing"
)

// There is no permission to check on Windows: restrictPlatform is a documented
// no-op, and the credential's protection is the permission the data root
// directory already has. What is checked here is that writing still happened
// where it was asked to, so the POSIX-only guarantee cannot quietly become "no
// file at all" on this platform.
func assertOwnerOnly(t *testing.T, path string, isDir bool) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.IsDir() != isDir {
		t.Fatalf("%s: isDir = %v, want %v", path, info.IsDir(), isDir)
	}
}
