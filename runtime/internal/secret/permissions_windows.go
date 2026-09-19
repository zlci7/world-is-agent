//go:build windows

package secret

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// restrict replaces the DACL with a single entry for the current user and marks
// it protected, which is what removes inherited access.
//
// Inherited access is the part that matters. A file under a per-user directory
// may already be owner-only, but nothing guarantees that once a parent directory
// is loosened, and the Runtime cannot tell the difference by looking at the file.
func restrictPlatform(path string, isDir bool) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}

	inheritance := uint32(windows.NO_INHERITANCE)
	if isDir {
		// Children must inherit owner-only access. A protected directory whose
		// entry grants nothing inheritable would leave files created inside it
		// with no DACL at all, which is everyone's access rather than the
		// owner's.
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		// The generic mask is deliberate. A container ends up with two entries for
		// the same trustee -- the mapped rights plus the generic mask -- which is
		// redundant rather than permissive, since no other principal appears. A
		// hand-computed specific mask would collapse that to one entry and could
		// just as easily grant too little.
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build an owner-only ACL: %w", err)
	}

	const information = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("set owner-only permissions: %w", err)
	}
	return verifyOwnerOnly(path)
}

// verifyOwnerOnly reads the descriptor back. The call above can report success
// while inherited entries are still present, and their absence is the property
// this package promises.
func verifyOwnerOnly(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read back permissions: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read back permissions: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("permissions still inherit from the parent directory")
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("open the process token: %w", err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read the current user: %w", err)
	}
	return user.User.Sid, nil
}
