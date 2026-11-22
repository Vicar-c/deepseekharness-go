package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("DSH_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != config.DefaultModel || cfg.MaxIterations <= 0 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadFileThenEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	base := config.Default()
	base.Model = "file-model"
	base.MaxIterations = 3
	base.SessionDir = filepath.Join(t.TempDir(), "sessions")
	if err := base.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Setenv("DSH_MODEL", "env-model")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "env-model" {
		t.Fatalf("model = %q, want env override", cfg.Model)
	}
	if cfg.MaxIterations != 3 {
		t.Fatalf("maxIterations = %d", cfg.MaxIterations)
	}
	if cfg.SessionDir != base.SessionDir {
		t.Fatalf("sessionDir = %q, want %q", cfg.SessionDir, base.SessionDir)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	t.Setenv("DSH_MODEL", "")
	t.Setenv("DSH_BASE_URL", "")
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg := config.Default()
	cfg.Model = "saved-model"
	cfg.MaxToolOutputBytes = 4096
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Model != "saved-model" || loaded.MaxToolOutputBytes != 4096 {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestAPIKeyFromEnvironment(t *testing.T) {
	cfg := config.Default()
	t.Setenv("DEEPSEEK_API_KEY", "  secret-key  ")
	if got := cfg.APIKey(); got != "secret-key" {
		t.Fatalf("APIKey = %q", got)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	if got := cfg.APIKey(); got != "" {
		t.Fatalf("APIKey = %q, want empty", got)
	}
}

func TestNormalizeRejectsBlankBaseURL(t *testing.T) {
	cfg := config.Default()
	cfg.BaseURL = "   "
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected error for blank base URL")
	}
}

func TestApplyFlags(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Default()
	cfg.ApplyFlags("flag-model", "http://localhost:1234", workspace, "debug")
	if cfg.Model != "flag-model" || cfg.BaseURL != "http://localhost:1234" || cfg.LogLevel != "debug" {
		t.Fatalf("flags not applied: %+v", cfg)
	}
	if cfg.Workspace != workspace {
		t.Fatalf("workspace = %q, want %q", cfg.Workspace, workspace)
	}
}

// TestNormalizeResolvesWorkspace keeps the original intent of TestApplyFlags:
// whatever the CLI passes in, the normalized configuration holds an absolute
// workspace path on every platform.
func TestNormalizeResolvesWorkspace(t *testing.T) {
	cfg := config.Default()
	cfg.ApplyFlags("", "", "relative-workspace", "")
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !filepath.IsAbs(cfg.Workspace) {
		t.Fatalf("workspace = %q, want absolute path", cfg.Workspace)
	}
	if got := filepath.Base(cfg.Workspace); got != "relative-workspace" {
		t.Fatalf("workspace baseline = %q, want relative-workspace", got)
	}
}

func TestDefaultDataDirectoryIsSeparatedFromUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("DSH_CONFIG", "")
	if got, want := config.FilePath(), filepath.Join(home, ".deepseekharness-go", "config.json"); got != want {
		t.Fatalf("FilePath = %q, want %q", got, want)
	}
	cfg := config.Default()
	if want := filepath.Join(home, ".deepseekharness-go", "sessions"); cfg.SessionDir != want {
		t.Fatalf("SessionDir = %q, want %q", cfg.SessionDir, want)
	}
}

func TestFilePathHonoursDSHConfig(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "custom.json")
	t.Setenv("DSH_CONFIG", explicit)
	if got := config.FilePath(); got != explicit {
		t.Fatalf("FilePath = %q, want %q", got, explicit)
	}
}

// TestMaxRetriesDefaultsAndExplicitZero keeps the retry budget configurable: a
// missing value uses the built-in default, while an explicit zero disables
// retries instead of being mistaken for "unset".
func TestMaxRetriesDefaultsAndExplicitZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("DSH_CONFIG", "")
	t.Setenv("DSH_MAX_RETRIES", "")
	if got := config.Default().MaxRetries; got != config.DefaultMaxRetries {
		t.Fatalf("default maxRetries = %d, want %d", got, config.DefaultMaxRetries)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"maxRetries":0}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxRetries != 0 {
		t.Fatalf("maxRetries = %d, want an explicit 0", cfg.MaxRetries)
	}
	if err := os.WriteFile(path, []byte(`{"maxRetries":-1}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected a negative maxRetries to be rejected")
	}
	t.Setenv("DSH_MAX_RETRIES", "2")
	cfg, err = config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxRetries != 2 {
		t.Fatalf("maxRetries = %d, want the environment override 2", cfg.MaxRetries)
	}
	if cfg.Source(config.FieldMaxRetries) != "env:DSH_MAX_RETRIES" {
		t.Fatalf("provenance = %q", cfg.Source(config.FieldMaxRetries))
	}
}

// TestMaxRetriesDefaultMatchesTheLLM keeps the documented default and the policy
// default from drifting apart.
func TestMaxRetriesDefaultMatchesTheLLM(t *testing.T) {
	if config.DefaultMaxRetries != llm.DefaultMaxRetries {
		t.Fatalf("config.DefaultMaxRetries = %d, llm.DefaultMaxRetries = %d",
			config.DefaultMaxRetries, llm.DefaultMaxRetries)
	}
}

// TestLogFileDefaultAndOverrides pins where the structured log lands.
func TestLogFileDefaultAndOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("DSH_CONFIG", "")
	t.Setenv("DSH_LOG_FILE", "")
	want := filepath.Join(home, ".deepseekharness-go", "logs", "dsh.log")
	if got := config.Default().LogFile; got != want {
		t.Fatalf("default logFile = %q, want %q", got, want)
	}
	t.Setenv("DSH_LOG_FILE", "off")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFile != config.LogFileOff {
		t.Fatalf("logFile = %q, want off", cfg.LogFile)
	}
}

// TestHomeDirFollowsTheConfigPath keeps logs, profiles and .env layers beside
// the configuration the process actually uses.
func TestHomeDirFollowsTheConfigPath(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("DSH_CONFIG", filepath.Join(directory, "config.json"))
	if got := config.HomeDir(); got != directory {
		t.Fatalf("HomeDir = %q, want %q", got, directory)
	}
}

// TestOptionalModelBounds covers maxTokens and contextWindow: both default to
// zero ("unset"), accept an explicit value from any layer, and reject a
// negative number instead of silently ignoring it.
func TestOptionalModelBounds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("DSH_CONFIG", "")
	t.Setenv("DSH_MAX_TOKENS", "")
	t.Setenv("DSH_CONTEXT_WINDOW", "")
	if got := config.Default().MaxTokens; got != 0 {
		t.Fatalf("default maxTokens = %d, want 0", got)
	}
	if got := config.Default().ContextWindow; got != 0 {
		t.Fatalf("default contextWindow = %d, want 0", got)
	}
	path := filepath.Join(home, "config.json")
	body := `{"maxTokens":512,"contextWindow":65536}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxTokens != 512 || cfg.ContextWindow != 65536 {
		t.Fatalf("bounds = %d/%d, want 512/65536", cfg.MaxTokens, cfg.ContextWindow)
	}
	if cfg.Source(config.FieldMaxTokens) == "" || cfg.Source(config.FieldContextWindow) == "" {
		t.Fatalf("provenance missing: %+v", cfg.Sources())
	}
	t.Setenv("DSH_MAX_TOKENS", "1024")
	t.Setenv("DSH_CONTEXT_WINDOW", "200000")
	cfg, err = config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxTokens != 1024 || cfg.ContextWindow != 200000 {
		t.Fatalf("env bounds = %d/%d", cfg.MaxTokens, cfg.ContextWindow)
	}
	if err := os.WriteFile(path, []byte(`{"maxTokens":-1}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DSH_MAX_TOKENS", "")
	t.Setenv("DSH_CONTEXT_WINDOW", "")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected a negative maxTokens to be rejected")
	}
	if err := os.WriteFile(path, []byte(`{"contextWindow":-5}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected a negative contextWindow to be rejected")
	}
}

// TestSetAcceptsZeroBounds keeps patch files able to clear a bound explicitly.
func TestSetAcceptsZeroBounds(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Set(config.FieldMaxTokens, 0, "patch:x"); err != nil {
		t.Fatalf("Set maxTokens: %v", err)
	}
	if err := cfg.Set(config.FieldContextWindow, 0, "patch:x"); err != nil {
		t.Fatalf("Set contextWindow: %v", err)
	}
	if err := cfg.Set(config.FieldMaxTokens, -1, "patch:x"); err == nil {
		t.Fatal("expected a negative maxTokens to be rejected")
	}
}
