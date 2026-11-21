package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
)

// TestLoadRecordsSettingSources is the provenance contract: every setting can
// explain whether it came from the built-in defaults, the file or the
// environment.
func TestLoadRecordsSettingSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"file-model","maxIterations":3}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DSH_MODEL", "env-model")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Source(config.FieldModel); got != config.SourceEnvPrefix+"DSH_MODEL" {
		t.Fatalf("model source = %q, want the environment", got)
	}
	if got := cfg.Source(config.FieldMaxIterations); got != config.SourceFilePrefix+path {
		t.Fatalf("maxIterations source = %q", got)
	}
	if got := cfg.Source(config.FieldBaseURL); got != config.SourceDefault {
		t.Fatalf("untouched setting source = %q, want default", got)
	}
	if got := cfg.Source(config.FieldMaxParallelToolCalls); got != config.SourceDefault {
		t.Fatalf("maxParallelToolCalls source = %q", got)
	}
	if cfg.MaxParallelToolCalls != config.DefaultMaxParallelToolCalls {
		t.Fatalf("maxParallelToolCalls default = %d", cfg.MaxParallelToolCalls)
	}
}

func TestNormalizeRestoresDefaultSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"maxIterations":0,"maxParallelToolCalls":0}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, field := range []string{config.FieldMaxIterations, config.FieldMaxParallelToolCalls} {
		if got := cfg.Source(field); got != config.SourceDefault {
			t.Fatalf("%s source = %q, want default after normalization", field, got)
		}
	}
}

func TestSetRecordsLayerAndRejectsBadInput(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Set(config.FieldToolTimeoutSec, float64(5), "patch:x.json"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if cfg.ToolTimeoutSec != 5 || cfg.Source(config.FieldToolTimeoutSec) != "patch:x.json" {
		t.Fatalf("toolTimeoutSec = %d source = %q", cfg.ToolTimeoutSec, cfg.Source(config.FieldToolTimeoutSec))
	}
	if err := cfg.Set(config.FieldMaxIterations, 8, "patch:x.json"); err != nil {
		t.Fatalf("Set int: %v", err)
	}
	if cfg.MaxIterations != 8 {
		t.Fatalf("maxIterations = %d", cfg.MaxIterations)
	}

	cases := []struct {
		name    string
		field   string
		value   any
		wantErr string
	}{
		{"unknown field", "nope", 1, "unknown setting"},
		{"wrong type", config.FieldMaxIterations, "many", "must be an integer"},
		{"non-positive", config.FieldMaxParallelToolCalls, 0, "positive integer"},
		{"float", config.FieldMaxIterations, 1.5, "must be an integer"},
		{"string setting given a number", config.FieldModel, 1, "must be a string"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := cfg.Set(testCase.field, testCase.value, "patch:x.json")
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}
}

func TestIsFieldCoversEveryField(t *testing.T) {
	for _, field := range config.Fields() {
		if !config.IsField(field) {
			t.Fatalf("IsField(%q) = false", field)
		}
	}
	if config.IsField("nope") {
		t.Fatal("IsField(nope) = true")
	}
}

// TestLoadToleratesUTF8BOM covers the Windows editor default: a hand-written
// config saved with a byte order mark must not fail to parse.
func TestLoadToleratesUTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"model":"bom-model"}`)...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DSH_MODEL", "")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "bom-model" {
		t.Fatalf("model = %q", cfg.Model)
	}
	if got := config.StripBOM([]byte(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Fatalf("StripBOM changed a file without a BOM: %q", got)
	}
}
