package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModelConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}
	return path
}

func TestDescribeConfigReportsAnEnvReferenceWithoutTheKey(t *testing.T) {
	t.Setenv("DESCRIBE_TEST_API_KEY", "sk-secret-value")
	path := writeModelConfig(t, `{
		"provider": "deepseek",
		"model": "deepseek-v4-flash",
		"api_key": "env:DESCRIBE_TEST_API_KEY",
		"base_url": "https://api.deepseek.com"
	}`)

	summary, err := DescribeConfig(path)
	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	if summary.Provider != "deepseek" || summary.Model != "deepseek-v4-flash" {
		t.Errorf("provider/model = %q/%q", summary.Provider, summary.Model)
	}
	if summary.APIKeyEnvName != "DESCRIBE_TEST_API_KEY" {
		t.Errorf("env name = %q, want DESCRIBE_TEST_API_KEY", summary.APIKeyEnvName)
	}
	if !summary.APIKeyConfigured {
		t.Error("api_key_configured = false, want true")
	}
	assertNoSecret(t, summary, "sk-secret-value")
}

func TestDescribeConfigReportsAMissingCredential(t *testing.T) {
	path := writeModelConfig(t, `{"provider":"deepseek","api_key":"env:DESCRIBE_MISSING_KEY"}`)

	summary, err := DescribeConfig(path)
	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	if summary.APIKeyConfigured {
		t.Error("api_key_configured = true, want false when the variable is unset")
	}
	if summary.APIKeyEnvName != "DESCRIBE_MISSING_KEY" {
		t.Errorf("env name = %q, want DESCRIBE_MISSING_KEY: the client has to know what to set", summary.APIKeyEnvName)
	}
}

// The client must never be able to read a credential through the summary. This
// is the invariant the first-run flow will be built on.
func TestDescribeConfigNeverEchoesAnInlineCredential(t *testing.T) {
	path := writeModelConfig(t, `{"provider":"deepseek","api_key":"sk-inline-do-not-leak"}`)

	summary, err := DescribeConfig(path)
	if err != nil {
		t.Fatalf("DescribeConfig: %v", err)
	}
	if summary.APIKeyEnvName != "" {
		t.Errorf("env name = %q, want empty for a credential that is not an env reference", summary.APIKeyEnvName)
	}
	if summary.APIKeyConfigured {
		t.Error("api_key_configured = true, want false: inline credentials are rejected today")
	}
	assertNoSecret(t, summary, "sk-inline-do-not-leak")
}

func assertNoSecret(t *testing.T, summary ConfigSummary, secret string) {
	t.Helper()
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("summary %s contains the credential", data)
	}
}
