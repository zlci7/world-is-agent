//go:build !windows

package secret

import (
	"os"
	"testing"
)

func assertOwnerOnly(t *testing.T, path string, isDir bool) {
	t.Helper()

	expected := os.FileMode(FileMode)
	if isDir {
		expected = DirMode
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != expected {
		t.Fatalf("permissions of %s are %04o, want %04o", path, got, expected)
	}
}
