package hosted

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gameagent/runtime/internal/accounts"
)

type Config struct {
	Mail                   SMTPConfig `json:"mail"`
	GameID                 string     `json:"game_id"`
	Origin                 string     `json:"origin"`
	GatewayURL             string     `json:"gateway_url"`
	HTTPAddr               string     `json:"http_addr"`
	GRPCAddr               string     `json:"grpc_addr"`
	ModelAddr              string     `json:"model_addr"`
	DataRoot               string     `json:"data_root"`
	SecretEnv              string     `json:"secret_env"`
	WorkerImage            string     `json:"worker_image"`
	WorkerNetwork          string     `json:"worker_network"`
	WorkerMemoryMB         int        `json:"worker_memory_mb"`
	WorkerCPUs             string     `json:"worker_cpus"`
	MaxWorkers             int        `json:"max_workers"`
	ModelPrivateURL        string     `json:"model_private_url"`
	UpstreamURL            string     `json:"upstream_url"`
	Model                  string     `json:"model"`
	ModelKeyEnv            string     `json:"model_key_env"`
	ContextTokens          int        `json:"context_tokens"`
	OutputTokens           int        `json:"output_tokens"`
	DailyTokens            int64      `json:"daily_tokens"`
	GlobalDailyTokens      int64      `json:"global_daily_tokens"`
	GlobalModelConcurrency int        `json:"global_model_concurrency"`
	ModelTimeoutSeconds    int        `json:"model_timeout_seconds"`
	SessionHours           int        `json:"session_hours"`
	KeyDays                int        `json:"key_days"`
	MinAdapterVersion      string     `json:"min_adapter_version"`
}

var safeID = regexp.MustCompile(`^usr_[a-f0-9]{32}$`)
var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$`)

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var c Config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.DailyTokens > 1_000_000_000_000 || c.GlobalDailyTokens < 0 || c.GlobalDailyTokens > 100_000_000_000_000 {
		return errors.New("daily token limits are outside the supported range")
	}
	if err := c.Mail.Validate(); err != nil {
		return err
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("origin must be an HTTPS origin")
	}
	g, err := url.Parse(c.GatewayURL)
	if err != nil || g.Scheme != "https" || g.Host == "" || g.User != nil || g.RawQuery != "" || (g.Path != "" && g.Path != "/") {
		return errors.New("gateway_url must be an HTTPS origin")
	}
	u, err = url.Parse(c.UpstreamURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return errors.New("upstream_url must be HTTPS without credentials or query")
	}
	if !filepath.IsAbs(c.DataRoot) || c.SecretEnv == "" || c.ModelKeyEnv == "" || c.Model == "" || c.HTTPAddr == "" || c.GRPCAddr == "" || c.ModelAddr == "" || !safeName.MatchString(c.GameID) {
		return errors.New("required hosted configuration is missing")
	}
	if !safeName.MatchString(c.WorkerImage) || !strings.Contains(c.WorkerImage, ":") || !safeName.MatchString(c.WorkerNetwork) || c.MaxWorkers < 1 || c.MaxWorkers > 100 || c.WorkerMemoryMB < 256 || !regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`).MatchString(c.WorkerCPUs) {
		return errors.New("explicit worker resource budgets and a fixed image tag are required")
	}
	u, err = url.Parse(c.ModelPrivateURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return errors.New("private model URL is required")
	}
	if c.ContextTokens < 2048 || c.OutputTokens < 1 || c.OutputTokens >= c.ContextTokens || c.DailyTokens < int64(c.ContextTokens) || c.GlobalModelConcurrency < 1 || c.GlobalModelConcurrency > 32 || c.ModelTimeoutSeconds < 1 || c.ModelTimeoutSeconds > 120 || c.SessionHours < 1 || c.SessionHours > 168 || c.KeyDays < 1 || c.KeyDays > 365 || c.MinAdapterVersion == "" {
		return errors.New("explicit model and credential budgets are required")
	}
	return nil
}

func (c Config) GlobalTokenLimit() int64 {
	if c.GlobalDailyTokens > 0 {
		return c.GlobalDailyTokens
	}
	return c.DailyTokens * int64(max(1, c.MaxWorkers))
}

func OpenAccounts(c Config) (*accounts.Store, error) {
	secret := os.Getenv(c.SecretEnv)
	if len(secret) < 32 || strings.HasPrefix(secret, "replace-") {
		return nil, errors.New("hosted account secret is missing or too short")
	}
	return accounts.Open(filepath.Join(c.DataRoot, "accounts.sqlite"), []byte(secret))
}
