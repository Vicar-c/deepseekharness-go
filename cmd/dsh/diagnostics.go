package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
)

// diagnosticsWarning heads every saved report. It is the upstream wording: the
// report exists for a bug report, so the reader has to know it may contain
// secrets before pasting it anywhere.
const diagnosticsWarning = "WARNING: Raw diagnostics may contain configuration or credential values from plugin errors. Review before sharing."

// diagnosticContext describes the failing run. It holds no configuration values
// or credentials: only where the process looked and which version it is.
type diagnosticContext struct {
	Home       string
	Version    string
	Profile    string
	ConfigPath string
}

// reportStartupFailure prints the concise failure and saves a full report under
// <home>/logs, then tells the user where it went. When the report cannot be
// saved the whole report is printed instead, because a diagnostics path that
// does not exist is worse than a long terminal message.
func reportStartupFailure(stderr io.Writer, context diagnosticContext, err error) {
	report := buildDiagnosticReport(context, err)
	fmt.Fprintf(stderr, "%v\n", err)
	path, writeErr := saveDiagnosticReport(context.Home, report)
	if writeErr != nil {
		fmt.Fprintf(stderr, "\ndsh: warning: could not write startup diagnostics: %v\nFull diagnostics:\n%s", writeErr, report)
		return
	}
	fmt.Fprintf(stderr, "\nFull diagnostics: %s\n", path)
}

// buildDiagnosticReport renders the report body: the warning, then the facts a
// maintainer needs to reproduce the failure.
func buildDiagnosticReport(context diagnosticContext, err error) string {
	var builder strings.Builder
	builder.WriteString(diagnosticsWarning)
	builder.WriteString("\n\n")
	fmt.Fprintf(&builder, "timestamp: %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(&builder, "dshVersion: %s\n", context.Version)
	fmt.Fprintf(&builder, "goVersion: %s\n", goruntime.Version())
	fmt.Fprintf(&builder, "platform: %s/%s\n", goruntime.GOOS, goruntime.GOARCH)
	fmt.Fprintf(&builder, "profile: %s\n", context.Profile)
	fmt.Fprintf(&builder, "home: %s\n", context.Home)
	fmt.Fprintf(&builder, "config: %s\n", context.ConfigPath)
	// The error comes last and verbatim: it is the only part that may contain
	// provider wording, and it must not be truncated.
	fmt.Fprintf(&builder, "error:\n%+v\n", err)
	return builder.String()
}

// saveDiagnosticReport writes one uniquely named report under <home>/logs and
// returns its path.
func saveDiagnosticReport(home, report string) (string, error) {
	if strings.TrimSpace(home) == "" {
		home = config.HomeDir()
	}
	directory := filepath.Join(home, "logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("startup-%s-%s.log", reportTimestamp(time.Now().UTC()), randomSuffix())
	path := filepath.Join(directory, name)
	// O_EXCL keeps two processes starting at the same moment from sharing a file.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(file, report); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// reportTimestamp renders a file-system safe timestamp: the colon separator of
// an ISO timestamp is not portable to every file system.
func reportTimestamp(at time.Time) string {
	return strings.ReplaceAll(at.Format("2006-01-02T15-04-05.000Z"), ":", "-")
}

// randomSuffix keeps two reports from the same second apart.
func randomSuffix() string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(random)
}
