//go:build windows

package secret

// restrictPlatform does nothing on Windows, deliberately.
//
// There is no equivalent of a POSIX mode to set: os.Chmod here only toggles the
// read-only flag. A real guarantee would mean replacing the file's DACL, which
// was removed from this design on purpose. Rewriting the ACL buys nothing against
// the threat this is meant to cover: a process already running as this user can
// read the file, this process's memory, and anything else the user can read. What
// matters is that the credential is not in the configuration file, not in a
// response, not in browser storage, and not readable by another OS user -- and
// the per-user profile directory the data root lives in already gives the last
// one.
//
// The consequence is stated rather than hidden: the "owner-only or do not save"
// guarantee this package keeps is a POSIX guarantee. On Windows the credential's
// protection is the permission the data root already has.
func restrictPlatform(path string, isDir bool) error {
	return nil
}
