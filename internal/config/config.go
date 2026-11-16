// Package config loads harness settings from a JSON file and environment
// variable overrides. Defaults are chosen so `dsh` starts without a config file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultModel is the DeepSeek chat model used when nothing is configured.
	DefaultModel = "deepseek-chat"
	// DefaultBaseURL is the official DeepSeek OpenAI-compatible endpoint.
	DefaultBaseURL = "https://api.deepseek.com"
	// DefaultAPIKeyEnv is the environment variable holding the API key.
	DefaultAPIKeyEnv = "DEEPSEEK_API_KEY"
)

// Config is the persisted harness configuration. Field names map 1:1 to the
// JSON file written by `dsh config init`.
type Config struct {
	Model              string `json:"model"`
	BaseURL            string `json:"baseUrl"`
	APIKeyEnv          string `json:"apiKeyEnv"`
	Workspace          string `json:"workspace"`
	SessionDir         string `json:"sessionDir"`
	MaxIterations      int    `json:"maxIterations"`
	RequestTimeoutSec  int    `json:"requestTimeoutSec"`
	ToolTimeoutSec     int    `json:"toolTimeoutSec"`
	MaxToolOutputBytes int    `json:"maxToolOutputBytes"`
	LogLevel           string `json:"logLevel"`
}

// Default returns the built-in configuration.
func Default() Config {
	workspace, err := os.Getwd()
	if err != nil || workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err == nil {
		workspace = abs
	}
	return Config{
		Model:              DefaultModel,
		BaseURL:            DefaultBaseURL,
		APIKeyEnv:          DefaultAPIKeyEnv,
		Workspace:          workspace,
		SessionDir:         defaultSessionDir(),
		MaxIterations:      12,
		RequestTimeoutSec:  120,
		ToolTimeoutSec:     60,
		MaxToolOutputBytes: 128 * 1024,
		LogLevel:           "info",
	}
}

// FilePath resolves the config file location: $DSH_CONFIG wins, otherwise
// ~/.dsh/config.json.
func FilePath() string {
	if explicit := strings.TrimSpace(os.Getenv("DSH_CONFIG")); explicit != "" {
		return explicit
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "dsh-config.json"
	}
	return filepath.Join(home, ".dsh", "config.json")
}

// Load reads the configuration from path (empty means FilePath). A missing file
// is not an error: defaults are used. Environment variables are applied last.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = FilePath()
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Defaults are intentional: the harness must start on a fresh machine.
	default:
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg.applyEnv()
	if err := cfg.Normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Normalize fills empty fields with defaults and validates the rest.
func (c *Config) Normalize() error {
	base := Default()
	if c.Model == "" {
		c.Model = base.Model
	}
	if c.BaseURL == "" {
		c.BaseURL = base.BaseURL
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = base.APIKeyEnv
	}
	if c.Workspace == "" {
		c.Workspace = base.Workspace
	}
	if abs, err := filepath.Abs(c.Workspace); err == nil {
		c.Workspace = abs
	}
	if c.SessionDir == "" {
		c.SessionDir = base.SessionDir
	}
	if c.MaxIterations <= 0 {
		c.MaxIterations = base.MaxIterations
	}
	if c.RequestTimeoutSec <= 0 {
		c.RequestTimeoutSec = base.RequestTimeoutSec
	}
	if c.ToolTimeoutSec <= 0 {
		c.ToolTimeoutSec = base.ToolTimeoutSec
	}
	if c.MaxToolOutputBytes <= 0 {
		c.MaxToolOutputBytes = base.MaxToolOutputBytes
	}
	if c.LogLevel == "" {
		c.LogLevel = base.LogLevel
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("config: model must not be empty")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("config: baseUrl must not be empty")
	}
	return nil
}

// Save writes the configuration as indented JSON, creating parent directories.
func (c Config) Save(path string) error {
	if path == "" {
		path = FilePath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: create directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	return nil
}

// APIKey returns the configured API key, or "" when the environment variable is
// unset.
func (c Config) APIKey() string {
	return strings.TrimSpace(os.Getenv(c.APIKeyEnv))
}

// ApplyFlags merges explicit CLI overrides into the configuration.
func (c *Config) ApplyFlags(model, baseURL, workspace, logLevel string) {
	if model != "" {
		c.Model = model
	}
	if baseURL != "" {
		c.BaseURL = baseURL
	}
	if workspace != "" {
		c.Workspace = workspace
	}
	if logLevel != "" {
		c.LogLevel = logLevel
	}
}

func (c *Config) applyEnv() {
	if value := os.Getenv("DSH_MODEL"); value != "" {
		c.Model = value
	}
	if value := os.Getenv("DSH_BASE_URL"); value != "" {
		c.BaseURL = value
	}
	if value := os.Getenv("DSH_API_KEY_ENV"); value != "" {
		c.APIKeyEnv = value
	}
	if value := os.Getenv("DSH_WORKSPACE"); value != "" {
		c.Workspace = value
	}
	if value := os.Getenv("DSH_SESSION_DIR"); value != "" {
		c.SessionDir = value
	}
	if value := os.Getenv("DSH_LOG_LEVEL"); value != "" {
		c.LogLevel = value
	}
	if value, err := strconv.Atoi(os.Getenv("DSH_MAX_ITERATIONS")); err == nil && value > 0 {
		c.MaxIterations = value
	}
}

func defaultSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".dsh", "sessions")
	}
	return filepath.Join(home, ".dsh", "sessions")
}
