package config_test

import (
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
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
	if got, want := config.FilePath(), filepath.Join(home, ".deekseekharness-go", "config.json"); got != want {
		t.Fatalf("FilePath = %q, want %q", got, want)
	}
	cfg := config.Default()
	if want := filepath.Join(home, ".deekseekharness-go", "sessions"); cfg.SessionDir != want {
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
