package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secretPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets", "model.key")
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	path := secretPath(t)

	if err := Write(path, "  sk-round-trip  "); err != nil {
		t.Fatalf("Write: %v", err)
	}
	value, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if value != "sk-round-trip" {
		t.Fatalf("value = %q, want the trimmed credential", value)
	}
}

func TestWriteCreatesTheSecretsDirectory(t *testing.T) {
	path := secretPath(t)

	if err := Write(path, "sk-value"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("stat secrets directory: %v", err)
	}
}

// The point of the package: the credential is written where only its owner can
// read it, and that is checked rather than assumed.
func TestWriteLeavesTheCredentialOwnerOnly(t *testing.T) {
	path := secretPath(t)

	if err := Write(path, "sk-value"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	assertOwnerOnly(t, filepath.Dir(path), true)
	assertOwnerOnly(t, path, false)
}

// Tightening an existing directory matters as much as tightening a new one: the
// directory may already exist with looser permissions.
func TestWriteTightensAnExistingLooseDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "model.key")

	if err := Write(path, "sk-value"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	assertOwnerOnly(t, dir, true)
}

func TestWriteReplacesAnExistingCredential(t *testing.T) {
	path := secretPath(t)

	if err := Write(path, "sk-old"); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := Write(path, "sk-new"); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	value, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if value != "sk-new" {
		t.Fatalf("value = %q, want the replacement", value)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read secrets directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("secrets directory holds %d entries, want just the credential", len(entries))
	}
}

func TestWriteRefusesAnEmptyValue(t *testing.T) {
	path := secretPath(t)

	for _, value := range []string{"", "   ", "\n\t"} {
		if err := Write(path, value); !errors.Is(err, ErrEmpty) {
			t.Fatalf("Write(%q) error = %v, want ErrEmpty", value, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty value still produced a file")
	}
}

// A credential that could not be made owner-only must not survive as though it
// had been. This is the hard line the design rests on, so it is covered directly
// rather than left to the platform behaving.
func TestFailedTighteningDoesNotKeepTheCredential(t *testing.T) {
	path := secretPath(t)
	original := restrict
	restrict = func(string, bool) error { return errors.New("tightening failed") }
	defer func() { restrict = original }()

	err := Write(path, "sk-must-not-survive")

	if err == nil {
		t.Fatal("Write reported success after tightening failed")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("the credential survived a failed tightening")
	}
}

func TestReadReportsWhatIsWrongWithoutThePath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "secrets", "model.key")

	if _, err := Read(missing); !errors.Is(err, ErrMissing) {
		t.Fatalf("Read of a missing file = %v, want ErrMissing", err)
	}

	// A path of the wrong kind is its own answer.
	if _, err := Read(dir); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Read of a directory = %v, want ErrNotRegular", err)
	}

	oversized := filepath.Join(dir, "big.key")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", MaxBytes+1)), 0o600); err != nil {
		t.Fatalf("write oversized file: %v", err)
	}
	if _, err := Read(oversized); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read of an oversized file = %v, want ErrTooLarge", err)
	}
}

// These messages can reach the local client, which is not told where the
// credential lives.
func TestReadErrorsDoNotCarryTheResolvedPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "secrets", "model.key")

	_, err := Read(missing)
	if err == nil {
		t.Fatal("Read of a missing file reported success")
	}
	for _, leak := range []string{dir, "model.key", "secrets"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q carries %q", err.Error(), leak)
		}
	}
}

func TestReadReturnsAnEmptyFileAsEmpty(t *testing.T) {
	path := secretPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	value, err := Read(path)

	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if value != "" {
		t.Fatalf("value = %q, want empty so the caller can call it unconfigured", value)
	}
}
