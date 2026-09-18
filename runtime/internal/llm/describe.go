package llm

import "strings"

// ConfigSummary describes a model configuration for a client to display. It
// exists so the client can show "which model is configured" without ever
// receiving the credential: a client may read this struct, never Config.
type ConfigSummary struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`

	// APIKeyEnvName is the environment variable the configuration points at,
	// and only when it points at one. A credential written into the file
	// directly has no name to show, so it is reported as configured without
	// being echoed.
	APIKeyEnvName string `json:"api_key_env_name,omitempty"`
	// APIKeyConfigured reports whether the credential resolves right now.
	APIKeyConfigured bool `json:"api_key_configured"`
}

// DescribeConfig reads a model configuration file and summarizes it.
func DescribeConfig(path string) (ConfigSummary, error) {
	config, err := LoadConfig(path)
	if err != nil {
		return ConfigSummary{}, err
	}

	summary := ConfigSummary{
		Provider: config.Provider,
		Model:    config.Model,
		BaseURL:  config.BaseURL,
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
