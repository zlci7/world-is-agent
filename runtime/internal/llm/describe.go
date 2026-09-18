package llm

import "strings"

// ConfigSummary describes a model configuration for a client to display. It
// exists so the client can show "which model is configured" without ever
// receiving the credential: a client may read this struct, never Config.
type ConfigSummary struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// APIKeyEnvName is the environment variable the configuration points at,
	// and only when it points at one. A credential written into the file
	// directly has no name to show, so it is reported as configured without
	// being echoed.
	APIKeyEnvName string `json:"api_key_env_name,omitempty"`
	// APIKeyConfigured reports whether the credential resolves right now.
	APIKeyConfigured bool `json:"api_key_configured"`
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
	// Only an env: reference has a name that is safe to display. Anything else
	// is the credential itself and must not leave this package.
	if name, ok := strings.CutPrefix(config.APIKey, "env:"); ok {
		summary.APIKeyEnvName = strings.TrimSpace(name)
	}
	if _, err := resolveAPIKey(config); err == nil {
		summary.APIKeyConfigured = true
	}
	return summary, nil
}
