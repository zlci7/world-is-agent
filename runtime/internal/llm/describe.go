package llm

import (
	"path/filepath"
	"strings"
)

// ConfigSummary describes a model configuration for a client to display. It
// exists so the client can show "which model is configured" without ever
// receiving the credential: a client may read this struct, never Config.
type ConfigSummary struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// APIKeyEnvName is the environment variable the configuration points at, and
	// only when it points at one. The other accepted form names a file, and a
	// file path identifies where the credential lives, so it is not reported.
	APIKeyEnvName string `json:"api_key_env_name,omitempty"`
	// APIKeyConfigured reports whether the credential resolves right now.
	APIKeyConfigured bool `json:"api_key_configured"`
	// APIKeyProblem explains why it does not, when it does not. The reason names
	// the reference the user wrote, never the path it resolved to.
	APIKeyProblem string `json:"api_key_problem,omitempty"`
}

// DescribeConfig reads a model configuration file and summarizes it.
//
// The summary carries no credential and no field that can carry one. base_url is
// deliberately absent: a URL may embed userinfo or a token in its query, so
// echoing it would break the invariant that no route returns a credential. A
// client that needs to show the endpoint should be given a sanitized origin
// (scheme and host) instead of the configured value.
func DescribeConfig(path string) (ConfigSummary, error) {
	config, err := LoadConfig(path)
	if err != nil {
		return ConfigSummary{}, err
	}

	summary := ConfigSummary{
		Provider: config.Provider,
		Model:    config.Model,
	}
	if name, ok := strings.CutPrefix(config.APIKey, apiKeyEnvPrefix); ok {
		summary.APIKeyEnvName = strings.TrimSpace(name)
	}

	if _, err := resolveAPIKey(config, filepath.Dir(path)); err != nil {
		// Why a credential is unusable is worth saying: "no credential" sends the
		// user looking in the wrong place when the real answer is that the file
		// they referenced is not there.
		summary.APIKeyProblem = err.Error()
	} else {
		summary.APIKeyConfigured = true
	}
	return summary, nil
}
