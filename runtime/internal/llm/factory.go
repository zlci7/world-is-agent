package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gameagent/runtime/internal/llm/deepseek"
	"gameagent/runtime/internal/llm/fake"
	"gameagent/runtime/internal/llm/openai"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/secret"
)

// ConfigEnvName overrides the model configuration path. The Runtime resolves a
// relative value against its data root.
const ConfigEnvName = "GAMEAGENT_MODEL_CONFIG"

type Config struct {
	model.WindowLimits
	Provider string `json:"provider"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, err
	}

	config.Provider = strings.TrimSpace(config.Provider)
	config.Model = strings.TrimSpace(config.Model)
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")

	return config, nil
}

// NewProviderFromConfigFile 根据模型配置文件创建具体 Provider。
func NewProviderFromConfigFile(path string) (model.Provider, Config, error) {
	config, err := LoadConfig(path)
	if err != nil {
		return nil, Config{}, err
	}

	// The configuration's own directory resolves a relative file: credential, so
	// the data root can move without breaking it and this package never has to
	// know what a data root is.
	provider, err := newProvider(config, filepath.Dir(path))
	if err != nil {
		return nil, Config{}, err
	}

	return provider, config, nil
}

// NewProvider builds a provider from an in-memory configuration, which has no
// location. A relative file: credential therefore cannot be resolved here and is
// reported as unusable; configurations read from disk go through
// NewProviderFromConfigFile.
func NewProvider(config Config) (model.Provider, error) {
	return newProvider(config, "")
}

func newProvider(config Config, configDir string) (model.Provider, error) {
	if err := config.WindowLimits.Validate(); err != nil {
		return nil, err
	}
	switch config.Provider {
	case "", "fake":
		return fake.NewProvider(), nil

	case "openai":
		apiKey, err := resolveAPIKey(config, configDir)
		if err != nil {
			return nil, err
		}

		modelName := config.Model
		if modelName == "" {
			modelName = "gpt-5-mini"
		}

		return openai.NewProvider(apiKey, modelName, openai.WithBaseURL(config.BaseURL), openai.WithModelWindow(config.WindowLimits)), nil

	case "deepseek":
		apiKey, err := resolveAPIKey(config, configDir)
		if err != nil {
			return nil, err
		}

		modelName := config.Model
		if modelName == "" {
			modelName = "deepseek-v4-flash"
		}

		return deepseek.NewProvider(apiKey, modelName, deepseek.WithBaseURL(config.BaseURL), deepseek.WithModelWindow(config.WindowLimits)), nil

	default:
		return nil, fmt.Errorf("unsupported model provider %q", config.Provider)
	}
}

// The two accepted forms of a stored credential. A credential written into the
// configuration directly is not one of them: it would sit with the
// configuration, under the same permissions, and be copied along with it.
const (
	apiKeyEnvPrefix  = "env:"
	apiKeyFilePrefix = "file:"
)

func resolveAPIKey(config Config, configDir string) (string, error) {
	switch {
	case config.APIKey == "":
		return "", fmt.Errorf("api_key is required for provider %q; use %sVARIABLE_NAME or %sPATH", config.Provider, apiKeyEnvPrefix, apiKeyFilePrefix)
	case strings.HasPrefix(config.APIKey, apiKeyEnvPrefix):
		return resolveEnvAPIKey(config)
	case strings.HasPrefix(config.APIKey, apiKeyFilePrefix):
		return resolveFileAPIKey(config, configDir)
	default:
		return "", fmt.Errorf("api_key for provider %q must reference an environment variable or a file, for example %sDEEPSEEK_API_KEY or %ssecrets/model.key", config.Provider, apiKeyEnvPrefix, apiKeyFilePrefix)
	}
}

func resolveEnvAPIKey(config Config) (string, error) {
	envName := strings.TrimSpace(strings.TrimPrefix(config.APIKey, apiKeyEnvPrefix))
	if envName == "" {
		return "", fmt.Errorf("api_key env reference is empty for provider %q", config.Provider)
	}

	apiKey := strings.TrimSpace(os.Getenv(envName))
	if apiKey == "" {
		return "", fmt.Errorf("%s is required for provider %q", envName, config.Provider)
	}

	return apiKey, nil
}

// resolveFileAPIKey reads a credential from a file. A relative reference is
// resolved against the configuration's own directory.
//
// The messages name the reference the user wrote, never the path it resolved to:
// they reach the local client, and the client is not told where the credential
// lives.
func resolveFileAPIKey(config Config, configDir string) (string, error) {
	reference := strings.TrimSpace(strings.TrimPrefix(config.APIKey, apiKeyFilePrefix))
	if reference == "" {
		return "", fmt.Errorf("api_key file reference is empty for provider %q", config.Provider)
	}

	path := reference
	if !filepath.IsAbs(path) {
		if configDir == "" {
			return "", fmt.Errorf("api_key %s%s cannot be resolved without the location of the configuration file", apiKeyFilePrefix, reference)
		}
		path = filepath.Join(configDir, path)
	}

	apiKey, err := secret.Read(path)
	if err != nil {
		return "", fmt.Errorf("api_key %s%s: %w", apiKeyFilePrefix, reference, err)
	}
	if apiKey == "" {
		return "", fmt.Errorf("api_key %s%s: %w", apiKeyFilePrefix, reference, secret.ErrEmpty)
	}
	return apiKey, nil
}
