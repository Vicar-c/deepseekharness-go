package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

// isolate points every path of the run at a temporary home so a test never
// touches the real configuration directory. The variables a .env layer is
// expected to supply are removed rather than blanked: an exported-but-empty
// value still outranks a file value, on purpose.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("DSH_CONFIG", filepath.Join(home, "config.json"))
	for _, name := range []string{
		"DEEPSEEK_API_KEY", "DSH_MODEL", "DSH_SESSION_DIR", "DSH_LOG_FILE", "DSH_MAX_RETRIES", "DSH_HOME",
	} {
		name := name
		previous, present := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, previous)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
	return home
}

// TestReportStartupFailureSavesReport proves a startup failure leaves a file a
// user can attach to a bug report, and prints where it is.
func TestReportStartupFailureSavesReport(t *testing.T) {
	home := isolate(t)
	var stderr bytes.Buffer
	failure := errors.New("config: parse /tmp/config.json: unexpected end of JSON input")
	reportStartupFailure(&stderr, diagnosticContext{
		Home:       home,
		Version:    "test-version",
		Profile:    "headless",
		ConfigPath: "/tmp/config.json",
	}, failure)

	out := stderr.String()
	if !strings.Contains(out, "Full diagnostics: ") {
		t.Fatalf("stderr = %q, want the diagnostics path", out)
	}
	if !strings.Contains(out, failure.Error()) {
		t.Fatalf("stderr = %q, want the failure message", out)
	}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Full diagnostics: ") {
			path = strings.TrimSpace(strings.TrimPrefix(line, "Full diagnostics: "))
		}
	}
	if path == "" {
		t.Fatal("no diagnostics path in the output")
	}
	if !strings.HasPrefix(path, filepath.Join(home, "logs")) {
		t.Fatalf("diagnostics path = %q, want it under %s", path, filepath.Join(home, "logs"))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	for _, want := range []string{
		diagnosticsWarning,
		"dshVersion: test-version",
		"profile: headless",
		"config: /tmp/config.json",
		failure.Error(),
		"goVersion: ",
		"platform: ",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("report %s is missing %q", path, want)
		}
	}
}

// TestReportStartupFailureFallsBackToStderr keeps the failure visible even when
// the report cannot be written.
func TestReportStartupFailureFallsBackToStderr(t *testing.T) {
	home := isolate(t)
	// A file where the log directory belongs makes the report unwritable.
	if err := os.WriteFile(filepath.Join(home, "logs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var stderr bytes.Buffer
	reportStartupFailure(&stderr, diagnosticContext{Home: home, Version: "v"}, errors.New("boot failed"))
	out := stderr.String()
	if !strings.Contains(out, "could not write startup diagnostics") {
		t.Fatalf("stderr = %q, want the write warning", out)
	}
	if !strings.Contains(out, "Full diagnostics:\n") {
		t.Fatalf("stderr = %q, want the inline report header", out)
	}
	if !strings.Contains(out, diagnosticsWarning) || !strings.Contains(out, "boot failed") {
		t.Fatalf("stderr = %q, want the report inline", out)
	}
}

// TestReportTimestampIsFileSystemSafe keeps the report name portable: a colon is
// not a legal file-name character on Windows.
func TestReportTimestampIsFileSystemSafe(t *testing.T) {
	stamp := reportTimestamp(time.Now().UTC())
	if strings.Contains(stamp, ":") {
		t.Fatalf("timestamp %q contains a colon", stamp)
	}
	if len(randomSuffix()) != 8 {
		t.Fatalf("random suffix = %q, want 8 hex characters", randomSuffix())
	}
}

// TestRunReportsDiagnosticsForABrokenConfig is the end-to-end proof: a config
// file that cannot be parsed exits non-zero and leaves a report.
func TestRunReportsDiagnosticsForABrokenConfig(t *testing.T) {
	home := isolate(t)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Full diagnostics: ") {
		t.Fatalf("stderr = %q, want a diagnostics path", stderr.String())
	}
	reports, err := filepath.Glob(filepath.Join(home, "logs", "startup-*.log"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %v, want exactly one", reports)
	}
}

// TestApplyEnvFilesAppliesTheProjectLayer proves a .env value reaches the
// process environment before the configuration is read.
func TestApplyEnvFilesAppliesTheProjectLayer(t *testing.T) {
	isolate(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("DEEPSEEK_API_KEY=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Chdir(project)

	var stderr bytes.Buffer
	layers, err := applyEnvFiles(&stderr)
	if err != nil {
		t.Fatalf("applyEnvFiles: %v", err)
	}
	if os.Getenv("DEEPSEEK_API_KEY") != "from-dotenv" {
		t.Fatalf("API key = %q, want the .env value", os.Getenv("DEEPSEEK_API_KEY"))
	}
	if len(layers) != 1 || layers[0].Source != "project-env" {
		t.Fatalf("layers = %+v, want one project layer", layers)
	}
}

// TestConfigCommandReportsEnvFiles shows the applied layers in the config
// summary, so a surprising value can be traced to its file.
func TestConfigCommandReportsEnvFiles(t *testing.T) {
	isolate(t)
	project := t.TempDir()
	dotenv := filepath.Join(project, ".env")
	if err := os.WriteFile(dotenv, []byte("DEEPSEEK_API_KEY=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Chdir(project)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "env files: "+dotenv) {
		t.Fatalf("stdout = %q, want the .env path", out)
	}
	if !strings.Contains(out, "api key: set (DEEPSEEK_API_KEY)") {
		t.Fatalf("stdout = %q, want the key to be reported as set", out)
	}
}

// TestConfigCommandRefusesABootstrapNameInDotEnv keeps a cloned repository from
// changing how the process starts.
func TestConfigCommandRefusesABootstrapNameInDotEnv(t *testing.T) {
	isolate(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("PATH=/attacker/bin\n"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Chdir(project)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "only the launching environment may set") {
		t.Fatalf("stderr = %q, want the refusal", stderr.String())
	}
	if os.Getenv("PATH") == "/attacker/bin" {
		t.Fatal("the refused value reached the environment")
	}
	if !strings.Contains(stderr.String(), "Full diagnostics: ") {
		t.Fatalf("stderr = %q, want diagnostics for the refusal", stderr.String())
	}
}

// TestDumpConfigShowsTheNewSettings keeps the resolver output complete.
func TestDumpConfigShowsTheNewSettings(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "--dump-config"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"maxRetries", "logFile"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("dump is missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestRetryPolicyFollowsTheConfiguredBudget keeps the CLI wiring in step with
// the setting: the configured budget is the only value the CLI overrides.
func TestRetryPolicyFollowsTheConfiguredBudget(t *testing.T) {
	cfg := config.Default()
	policy := retryPolicy(cfg)
	if policy.MaxRetries != cfg.MaxRetries {
		t.Fatalf("policy max retries = %d, want %d", policy.MaxRetries, cfg.MaxRetries)
	}
	if policy.Mode != llm.RetryModeNormal {
		t.Fatalf("policy mode = %q, want normal", policy.Mode)
	}
	if policy.MaxDelay != llm.DefaultMaxDelay || policy.InitialDelay != llm.DefaultInitialDelay {
		t.Fatalf("backoff = %s/%s, want the provider defaults", policy.InitialDelay, policy.MaxDelay)
	}
	cfg.MaxRetries = 0
	policy = retryPolicy(cfg)
	if !policy.Exhausted(0) {
		t.Fatal("a zero budget must not retry")
	}
	if !policy.Retryable(llm.CodeRateLimit) {
		t.Fatal("the default retryable codes must survive the budget override")
	}
}
