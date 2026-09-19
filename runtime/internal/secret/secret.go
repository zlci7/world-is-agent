// Package secret stores and reads the one credential the Runtime keeps on the
// user's machine.
//
// The value is written where only its owner can read it, and a failure to make
// that true is a failure to save. Writing a credential into a file anyone can read
// while reporting success would be worse than not saving it at all.
//
// That guarantee is a POSIX one. On Windows the platform file has nothing to set
// and says so; there, the credential's protection is the permission the data root
// directory already has.
//
// Errors never carry the resolved path. These messages reach the local client,
// and the client is not told where the credential lives; the reference the user
// wrote in their configuration identifies it well enough.
package secret

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxBytes bounds a secret file. A credential is short, so a larger file is a
	// mistake or an attack, and neither belongs in memory as one.
	MaxBytes = 8 << 10

	// DirMode and FileMode are the POSIX permissions of the secrets directory and
	// of the files inside it. On Windows the same intent is expressed as an ACL.
	DirMode  = 0o700
	FileMode = 0o600
)

var (
	// ErrMissing reports that a referenced secret file is not there.
	ErrMissing = errors.New("the secret file does not exist")
	// ErrEmpty reports a secret with no value, which is treated as unconfigured
	// rather than as a credential.
	ErrEmpty = errors.New("the secret file is empty")
	// ErrTooLarge reports a file that is not a credential.
	ErrTooLarge = fmt.Errorf("the secret file is larger than %d bytes", MaxBytes)
	// ErrNotRegular reports a path that is not a regular file.
	ErrNotRegular = errors.New("the secret path is not a regular file")
)

// restrict is a variable so a test can make the owner-only step fail. "A failure
// there must not leave the credential behind" is a guarantee worth covering, and
// it cannot be provoked on a filesystem that behaves.
var restrict = restrictPlatform

// Read returns the credential stored at path, trimmed. A missing file reports
// ErrMissing so the caller can say what is missing rather than what failed.
func Read(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrMissing
		}
		// Deliberately not wrapped: the underlying error carries the path.
		return "", errors.New("the secret file could not be read")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", errors.New("the secret file could not be read")
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotRegular
	}

	// Bounded rather than read-then-check: an oversized file should never be
	// loaded to discover that it is oversized.
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return "", errors.New("the secret file could not be read")
	}
	if len(data) > MaxBytes {
		return "", ErrTooLarge
	}
	return strings.TrimSpace(string(data)), nil
}

// Write stores value at path so that only its owner can read it.
//
// The file is written next to its destination and renamed, so a process that
// stops mid-write leaves the previous credential rather than half of a new one:
// a truncated credential is worse than a missing one, because it looks
// configured.
//
// This is intentionally not shared with the configuration seeder, which writes
// atomically for an unrelated reason (an interrupted seed must be retried). The
// two have different guarantees to keep and should be able to change separately.
func Write(path, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ErrEmpty
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("create the secret directory: %w", err)
	}
	// The directory is tightened first. Writing a credential into a directory
	// another user can write to would let them replace it afterwards, so the file
	// permissions alone would not be enough.
	if err := restrict(dir, true); err != nil {
		return err
	}
	if err := writeAtomically(path, trimmed); err != nil {
		return err
	}
	if err := restrict(path, false); err != nil {
		// A credential that is not owner-only must not survive as though it were.
		_ = os.Remove(path)
		return err
	}
	return nil
}

func writeAtomically(path, value string) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create a temporary secret file: %w", err)
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()

	if _, err := file.WriteString(value); err != nil {
		file.Close()
		return fmt.Errorf("write the secret file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync the secret file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close the secret file: %w", err)
	}
	// The temporary file starts owner-only where the platform expresses that as a
	// mode, so it is never briefly world-readable.
	if err := os.Chmod(temporary, FileMode); err != nil {
		return fmt.Errorf("set owner-only permissions: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace the secret file: %w", err)
	}
	return nil
}
