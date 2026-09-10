package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gameagent/runtime/internal/llm/deepseek"
	"gameagent/runtime/internal/llm/fake"
	"gameagent/runtime/internal/llm/openai"
	"gameagent/runtime/internal/model"
)

const (
	defaultConfigPath = "runtime/config/model.json"
	configEnvName     = "GAMEAGENT_MODEL_CONFIG"
)

type Config struct {
	model.WindowLimits
	Provider string `json:"provider"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
}

func ConfigPathFromEnv() string {
	if path := strings.TrimSpace(os.Getenv(configEnvName)); path != "" {
		return path
	}
	return defaultConfigPath
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

	provider, err := NewProvider(config)
	if err != nil {
		return nil, Config{}, err
	}

	return provider, config, nil
}

func NewProvider(config Config) (model.Provider, error) {
	if err := config.WindowLimits.Validate(); err != nil {
		return nil, err
	}
	switch config.Provider {
	case "", "fake":
		return fake.NewProvider(), nil

	case "openai":
		apiKey, err := resolveAPIKey(config)
		if err != nil {
			return nil, err
		}

		modelName := config.Model
		if modelName == "" {
			modelName = "gpt-5-mini"
		}

		return openai.NewProvider(apiKey, modelName, openai.WithBaseURL(config.BaseURL), openai.WithModelWindow(config.WindowLimits)), nil

	case "deepseek":
		apiKey, err := resolveAPIKey(config)
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

func resolveAPIKey(config Config) (string, error) {
	if config.APIKey == "" {
		return "", fmt.Errorf("api_key is required for provider %q; use env:VARIABLE_NAME", config.Provider)
	}

	if !strings.HasPrefix(config.APIKey, "env:") {
		return "", fmt.Errorf("api_key for provider %q must reference an environment variable, for example env:DEEPSEEK_API_KEY", config.Provider)
	}

	envName := strings.TrimSpace(strings.TrimPrefix(config.APIKey, "env:"))
	if envName == "" {
		return "", fmt.Errorf("api_key env reference is empty for provider %q", config.Provider)
	}

	apiKey := strings.TrimSpace(os.Getenv(envName))
	if apiKey == "" {
		return "", fmt.Errorf("%s is required for provider %q", envName, config.Provider)
	}

	return apiKey, nil
}
