//go:build windows

package secret

import (
	"testing"

	"golang.org/x/sys/windows"
)

// assertOwnerOnly checks the property the package promises on this platform:
// inherited access is gone. x/sys does not expose ACE enumeration, so the
// protected flag is what is checked, and it is precisely the flag that records
// whether inherited entries still apply.
func assertOwnerOnly(t *testing.T, path string, isDir bool) {
	t.Helper()

	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read security info of %s: %v", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read control flags of %s: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s still inherits its permissions from the parent directory", path)
	}
}
