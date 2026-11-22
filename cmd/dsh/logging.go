package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
)

// newLogger builds the process logger. The structured log goes to stderr and,
// unless logFile is empty or "off", is appended to that file as well, so a
// failure a user reports can be re-read after the terminal scrolled.
//
// A log file that cannot be opened is not fatal: the harness still has to run,
// so the problem is reported through warn and logging continues on stderr.
// The returned function closes the file and is safe to call more than once.
func newLogger(level, logFile string, warn io.Writer) (*slog.Logger, func()) {
	options := &slog.HandlerOptions{Level: parseLogLevel(level)}
	sinks := []io.Writer{os.Stderr}
	closeLog := func() {}
	path := strings.TrimSpace(logFile)
	if path != "" && !strings.EqualFold(path, config.LogFileOff) {
		file, err := openLogFile(path)
		if err != nil {
			fmt.Fprintf(warn, "dsh: warning: cannot write the log file: %v\n", err)
		} else {
			sinks = append(sinks, file)
			closeLog = func() { _ = file.Close() }
		}
	}
	logger := slog.New(slog.NewTextHandler(io.MultiWriter(sinks...), options))
	slog.SetDefault(logger)
	return logger, closeLog
}

// parseLogLevel maps the configured level name onto a slog level. An unknown
// name falls back to info rather than failing: the level is a debugging aid, not
// a correctness setting.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// openLogFile creates the log file and its directory. The file is append-only
// and private: it may carry prompts and tool output.
func openLogFile(path string) (*os.File, error) {
	if directory := filepath.Dir(path); directory != "" && directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
