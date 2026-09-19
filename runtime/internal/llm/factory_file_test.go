package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gameagent/runtime/internal/secret"
)

// A relative file: reference is resolved against the configuration's own
// directory, which is what lets a data root move without breaking it.
func TestFileCredentialResolvesAgainstTheConfigurationDirectory(t *testing.T) {
	// The shipped layout: <root>/config/model.json referring to
	// <root>/secrets/model.key as ../secrets/model.key.
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	secrets := filepath.Join(root, "secrets")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "model.key"), []byte("sk-from-file\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	path := filepath.Join(configDir, "model.json")
	config := `{"provider":"deepseek","model":"test-model","api_key":"file:../secrets/model.key"}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}

	provider, _, err := NewProviderFromConfigFile(path)

	if err != nil {
		t.Fatalf("NewProviderFromConfigFile: %v", err)
	}
	if provider == nil {
		t.Fatal("no provider was built from a file credential")
	}
}

// The same configuration read from somewhere else must still resolve, because
// the reference follows the file rather than the working directory.
func TestFileCredentialDoesNotDependOnTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "model.key"), []byte("sk-absolute"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	path := filepath.Join(dir, "model.json")
	absolute := filepath.ToSlash(filepath.Join(dir, "secrets", "model.key"))
	config := `{"provider":"deepseek","api_key":"file:` + absolute + `"}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}

	provider, _, err := NewProviderFromConfigFile(path)

	if err != nil {
		t.Fatalf("NewProviderFromConfigFile with an absolute reference: %v", err)
	}
	if provider == nil {
		t.Fatal("no provider was built from an absolute file reference")
	}
}

func TestFileCredentialReportsWhatIsWrong(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.json")
	config := `{"provider":"deepseek","api_key":"file:secrets/missing.key"}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}

	_, _, err := NewProviderFromConfigFile(path)

	if err == nil {
		t.Fatal("a missing secret file produced a provider")
	}
	if !strings.Contains(err.Error(), "secrets/missing.key") {
		t.Fatalf("error %q does not name the reference the user wrote", err.Error())
	}
	// The reference is the user's own text; the resolved path is not theirs to be
	// told, because this message can reach the local client.
	if strings.Contains(err.Error(), filepath.ToSlash(dir)) || strings.Contains(err.Error(), dir) {
		t.Fatalf("error %q carries the resolved path", err.Error())
	}
}

func TestEmptySecretFileIsNotACredential(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "model.key"), []byte("  \n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	path := filepath.Join(dir, "model.json")
	config := `{"provider":"deepseek","api_key":"file:secrets/model.key"}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}

	_, _, err := NewProviderFromConfigFile(path)

	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want an empty secret to be reported as unconfigured", err)
	}
}

// A configuration with no location cannot resolve a relative reference, and
// guessing a directory would be worse than saying so.
func TestRelativeFileCredentialNeedsAConfigurationLocation(t *testing.T) {
	_, err := NewProvider(Config{Provider: "deepseek", APIKey: "file:secrets/model.key"})

	if err == nil {
		t.Fatal("a relative file reference resolved without a configuration location")
	}
	if !strings.Contains(err.Error(), "without the location") {
		t.Fatalf("error = %v, want it to say the location is missing", err)
	}
}

func TestDescribeConfigReportsAFileCredentialWithoutItsPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "model.key"), []byte("sk-file-do-not-leak"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	path := filepath.Join(dir, "model.json")
	config := `{"provider":"deepseek","model":"test-model","api_key":"file:secrets/model.key"}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}

	summary, err := DescribeConfig(path)

	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	if !summary.APIKeyConfigured {
		t.Fatalf("api_key_configured = false, want true: %+v", summary)
	}
	if summary.APIKeyEnvName != "" {
		t.Fatalf("env name = %q, want empty for a file credential", summary.APIKeyEnvName)
	}
	assertNoSecret(t, summary, "sk-file-do-not-leak")
	assertNoSecret(t, summary, "secrets/model.key")
	assertNoSecret(t, summary, dir)
}

func TestDescribeConfigExplainsAMissingFileCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.json")
	config := `{"provider":"deepseek","api_key":"file:secrets/missing.key"}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}

	summary, err := DescribeConfig(path)

	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	if summary.APIKeyConfigured {
		t.Fatal("api_key_configured = true for a missing file")
	}
	if summary.APIKeyProblem == "" {
		t.Fatal("a missing credential must say why, or the client sends the user looking in the wrong place")
	}
	assertNoSecret(t, summary, dir)
}

// The stored forms are the credential's home; writing one into the configuration
// itself stays rejected.
func TestInlineCredentialsRemainRejected(t *testing.T) {
	path := writeModelConfig(t, `{"provider":"deepseek","api_key":"sk-inline"}`)

	_, _, err := NewProviderFromConfigFile(path)

	if err == nil {
		t.Fatal("an inline credential in the configuration was accepted")
	}
	if !strings.Contains(err.Error(), "environment variable or a file") {
		t.Fatalf("error = %v, want it to name the accepted forms", err)
	}
}

// A reference that is not a path at all is reported rather than silently
// treated as a file name.
func TestEmptyFileReferenceIsRejected(t *testing.T) {
	path := writeModelConfig(t, `{"provider":"deepseek","api_key":"file:"}`)

	_, _, err := NewProviderFromConfigFile(path)

	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want an empty file reference to be rejected", err)
	}
}

// The written secret must be the one the provider uses, not a trivially
// truncated or extended version of it.
func TestWrittenSecretIsReadBackExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets", "model.key")
	if err := secret.Write(path, "sk-written-by-the-wizard"); err != nil {
		t.Fatalf("secret.Write: %v", err)
	}

	value, err := secret.Read(path)
	if err != nil {
		t.Fatalf("secret.Read: %v", err)
	}
	if value != "sk-written-by-the-wizard" {
		t.Fatalf("value = %q", value)
	}

	// And the resolved credential reaches the factory through the summary path
	// without appearing in it.
	modelPath := filepath.Join(dir, "model.json")
	body, err := json.Marshal(map[string]string{
		"provider": "deepseek", "model": "test-model", "api_key": "file:secrets/model.key",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(modelPath, body, 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}
	if _, _, err := NewProviderFromConfigFile(modelPath); err != nil {
		t.Fatalf("NewProviderFromConfigFile: %v", err)
	}
	summary, err := DescribeConfig(modelPath)
	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	assertNoSecret(t, summary, "sk-written-by-the-wizard")
}
