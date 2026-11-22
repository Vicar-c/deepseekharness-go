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
	// DefaultMaxParallelToolCalls is how many concurrency-safe tool calls may
	// overlap inside one model step. 1 forces serial execution.
	DefaultMaxParallelToolCalls = 10
	// DefaultMaxRetries is how often one failed model call is repeated by
	// default. It mirrors llm.DefaultMaxRetries; a test keeps the two in step.
	DefaultMaxRetries = 5
	// LogFileOff disables the log file.
	LogFileOff = "off"
	// DataDirName is the directory under the user's home that holds the config
	// file and the sessions. It is kept out of ~/.dsh so the Go rewrite never
	// touches the installed desktop app's data.
	DataDirName = ".deepseekharness-go"
	// legacyDataDirName is the data directory used before the project was
	// renamed. It is migrated once at startup, see MigrateLegacyDataDir.
	legacyDataDirName = ".deekseekharness-go"
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
	// MaxParallelToolCalls bounds how many concurrency-safe tool calls run at
	// the same time inside one step; 1 makes every batch serial.
	MaxParallelToolCalls int    `json:"maxParallelToolCalls"`
	LogLevel             string `json:"logLevel"`
	// LogFile receives the structured log in addition to stderr; "off" disables
	// the file.
	LogFile string `json:"logFile"`
	// MaxRetries bounds how often one failed model call is repeated.
	MaxRetries int `json:"maxRetries"`

	// sources records the layer each setting came from (default, file, env,
	// flag, bundle, profile, home patch, --patch). It is never serialized.
	sources map[string]string
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
	cfg := Config{
		Model:                DefaultModel,
		BaseURL:              DefaultBaseURL,
		APIKeyEnv:            DefaultAPIKeyEnv,
		Workspace:            workspace,
		SessionDir:           defaultSessionDir(),
		MaxIterations:        12,
		RequestTimeoutSec:    120,
		ToolTimeoutSec:       60,
		MaxToolOutputBytes:   128 * 1024,
		MaxParallelToolCalls: DefaultMaxParallelToolCalls,
		LogLevel:             "info",
		LogFile:              defaultLogFile(),
		MaxRetries:           DefaultMaxRetries,
	}
	cfg.markDefaults()
	return cfg
}

// defaultLogFile is the log file used when nothing else is configured.
func defaultLogFile() string {
	return filepath.Join(HomeDir(), "logs", "dsh.log")
}

// HomeDir reports the directory that holds the configuration, the profiles, the
// logs and (unless sessionDir says otherwise) the sessions. It is deliberately
// not the installed desktop app's ~/.dsh: the rewrite never reads or writes the
// upstream profile or session data.
func HomeDir() string {
	if explicit := strings.TrimSpace(os.Getenv("DSH_CONFIG")); explicit != "" {
		return filepath.Dir(explicit)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return filepath.Join(home, DataDirName)
}

// FilePath resolves the config file location: $DSH_CONFIG wins, otherwise
// ~/.deepseekharness-go/config.json. The directory is intentionally separate
// from the installed DeepSeek Harness desktop app data in ~/.dsh.
func FilePath() string {
	if explicit := strings.TrimSpace(os.Getenv("DSH_CONFIG")); explicit != "" {
		return explicit
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "dsh-config.json"
	}
	return filepath.Join(home, DataDirName, "config.json")
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
		data = StripBOM(data)
		var present map[string]json.RawMessage
		if err := json.Unmarshal(data, &present); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		for key := range present {
			if IsField(key) {
				cfg.mark(key, SourceFilePrefix+path)
			}
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
		c.mark(FieldModel, SourceDefault)
	}
	if c.BaseURL == "" {
		c.BaseURL = base.BaseURL
		c.mark(FieldBaseURL, SourceDefault)
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = base.APIKeyEnv
		c.mark(FieldAPIKeyEnv, SourceDefault)
	}
	if c.Workspace == "" {
		c.Workspace = base.Workspace
		c.mark(FieldWorkspace, SourceDefault)
	}
	if abs, err := filepath.Abs(c.Workspace); err == nil {
		c.Workspace = abs
	}
	if c.SessionDir == "" {
		c.SessionDir = base.SessionDir
		c.mark(FieldSessionDir, SourceDefault)
	}
	if c.MaxIterations <= 0 {
		c.MaxIterations = base.MaxIterations
		c.mark(FieldMaxIterations, SourceDefault)
	}
	if c.RequestTimeoutSec <= 0 {
		c.RequestTimeoutSec = base.RequestTimeoutSec
		c.mark(FieldRequestTimeoutSec, SourceDefault)
	}
	if c.ToolTimeoutSec <= 0 {
		c.ToolTimeoutSec = base.ToolTimeoutSec
		c.mark(FieldToolTimeoutSec, SourceDefault)
	}
	if c.MaxToolOutputBytes <= 0 {
		c.MaxToolOutputBytes = base.MaxToolOutputBytes
		c.mark(FieldMaxToolOutputBytes, SourceDefault)
	}
	if c.MaxParallelToolCalls <= 0 {
		c.MaxParallelToolCalls = base.MaxParallelToolCalls
		c.mark(FieldMaxParallelToolCalls, SourceDefault)
	}
	if c.LogLevel == "" {
		c.LogLevel = base.LogLevel
		c.mark(FieldLogLevel, SourceDefault)
	}
	if strings.TrimSpace(c.LogFile) == "" {
		c.LogFile = base.LogFile
		c.mark(FieldLogFile, SourceDefault)
	}
	// A negative retry budget is meaningless rather than merely unset: it is
	// rejected so a typo fails loudly. Zero disables retries and is only
	// replaced by the default when no layer supplied it.
	if c.MaxRetries < 0 {
		return fmt.Errorf("config: maxRetries must not be negative, got %d", c.MaxRetries)
	}
	if c.MaxRetries == 0 {
		switch c.Source(FieldMaxRetries) {
		case "", SourceDefault:
			c.MaxRetries = base.MaxRetries
			c.mark(FieldMaxRetries, SourceDefault)
		}
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
		c.mark(FieldModel, SourceFlagPrefix+"--model")
	}
	if baseURL != "" {
		c.BaseURL = baseURL
		c.mark(FieldBaseURL, SourceFlagPrefix+"--base-url")
	}
	if workspace != "" {
		c.Workspace = workspace
		c.mark(FieldWorkspace, SourceFlagPrefix+"--workspace")
	}
	if logLevel != "" {
		c.LogLevel = logLevel
		c.mark(FieldLogLevel, SourceFlagPrefix+"--log-level")
	}
}

func (c *Config) applyEnv() {
	if value := os.Getenv("DSH_MODEL"); value != "" {
		c.Model = value
		c.mark(FieldModel, SourceEnvPrefix+"DSH_MODEL")
	}
	if value := os.Getenv("DSH_BASE_URL"); value != "" {
		c.BaseURL = value
		c.mark(FieldBaseURL, SourceEnvPrefix+"DSH_BASE_URL")
	}
	if value := os.Getenv("DSH_API_KEY_ENV"); value != "" {
		c.APIKeyEnv = value
		c.mark(FieldAPIKeyEnv, SourceEnvPrefix+"DSH_API_KEY_ENV")
	}
	if value := os.Getenv("DSH_WORKSPACE"); value != "" {
		c.Workspace = value
		c.mark(FieldWorkspace, SourceEnvPrefix+"DSH_WORKSPACE")
	}
	if value := os.Getenv("DSH_SESSION_DIR"); value != "" {
		c.SessionDir = value
		c.mark(FieldSessionDir, SourceEnvPrefix+"DSH_SESSION_DIR")
	}
	if value := os.Getenv("DSH_LOG_LEVEL"); value != "" {
		c.LogLevel = value
		c.mark(FieldLogLevel, SourceEnvPrefix+"DSH_LOG_LEVEL")
	}
	if value := os.Getenv("DSH_LOG_FILE"); value != "" {
		c.LogFile = value
		c.mark(FieldLogFile, SourceEnvPrefix+"DSH_LOG_FILE")
	}
	if value, err := strconv.Atoi(os.Getenv("DSH_MAX_RETRIES")); err == nil && value >= 0 && os.Getenv("DSH_MAX_RETRIES") != "" {
		c.MaxRetries = value
		c.mark(FieldMaxRetries, SourceEnvPrefix+"DSH_MAX_RETRIES")
	}
	if value, err := strconv.Atoi(os.Getenv("DSH_MAX_ITERATIONS")); err == nil && value > 0 {
		c.MaxIterations = value
		c.mark(FieldMaxIterations, SourceEnvPrefix+"DSH_MAX_ITERATIONS")
	}
	if value, err := strconv.Atoi(os.Getenv("DSH_MAX_PARALLEL_TOOL_CALLS")); err == nil && value > 0 {
		c.MaxParallelToolCalls = value
		c.mark(FieldMaxParallelToolCalls, SourceEnvPrefix+"DSH_MAX_PARALLEL_TOOL_CALLS")
	}
}

func defaultSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(DataDirName, "sessions")
	}
	return filepath.Join(home, DataDirName, "sessions")
}

// MigrateLegacyDataDir renames the data directory used before the project was
// renamed (~/.deekseekharness-go) to the current one (~/.deepseekharness-go).
//
// It reports whether a migration happened. The rename only runs when the legacy
// directory exists and the current one does not, so existing data is never
// overwritten and the call is a no-op on fresh installs and on every run after
// the first migration.
func MigrateLegacyDataDir() (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false, nil
	}
	return migrateDataDir(filepath.Join(home, legacyDataDirName), filepath.Join(home, DataDirName))
}

// migrateDataDir is the testable core of MigrateLegacyDataDir.
func migrateDataDir(legacy, current string) (bool, error) {
	if _, err := os.Stat(legacy); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("config: inspect %s: %w", legacy, err)
	}
	if _, err := os.Stat(current); err == nil {
		// Already migrated, or the user already has data in the new location.
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("config: inspect %s: %w", current, err)
	}
	if err := os.Rename(legacy, current); err != nil {
		return false, fmt.Errorf("config: migrate %s to %s: %w", legacy, current, err)
	}
	return true, nil
}
