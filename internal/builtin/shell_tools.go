package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

type shellTool struct{ workspace *Workspace }

// shellToolName mirrors the upstream catalog, which exposes a dedicated bash
// tool on POSIX hosts and a pwsh tool on Windows.
func shellToolName() string {
	if runtime.GOOS == "windows" {
		return "pwsh"
	}
	return "bash"
}

func (t *shellTool) Definition() tool.Definition {
	description := "Run a bash command in the workspace and return its combined output and exit code. Use it for builds, tests and repository inspection."
	if runtime.GOOS == "windows" {
		description = "Run a PowerShell command in the workspace and return its combined output and exit code. Use it for builds, tests and repository inspection."
	}
	return tool.Definition{
		Name:        shellToolName(),
		Description: description,
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":     map[string]any{"type": "string", "description": "Command line to execute."},
				"timeout_sec": map[string]any{"type": "integer", "description": "Optional timeout in seconds; defaults to the tool pipeline timeout."},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		}),
	}
}

func (t *shellTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return tool.Result{}, errors.New("command is required")
	}
	if args.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutSec)*time.Second)
		defer cancel()
	}
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(ctx, windowsPowerShell(), "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", args.Command)
	} else {
		command = exec.CommandContext(ctx, posixShell(), "-c", args.Command)
	}
	command.Dir = t.workspace.Dir()
	command.Env = os.Environ()
	output := &limitedBuffer{limit: 1 << 20}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	content := output.String()
	if output.dropped > 0 {
		content += fmt.Sprintf("\n...[captured output truncated after %d bytes]", output.limit)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			content += fmt.Sprintf("\n(exit code %d)", exitErr.ExitCode())
			return tool.Result{Content: content, IsError: true}, nil
		}
		return tool.Result{}, fmt.Errorf("run command: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}
	return tool.Result{Content: content + "\n(exit code 0)"}, nil
}

// windowsPowerShell returns the best available PowerShell host: pwsh 7+ when
// installed, otherwise the inbox Windows PowerShell 5.1.
func windowsPowerShell() string {
	for _, candidate := range []string{"pwsh.exe", "pwsh", "powershell.exe"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return "powershell.exe"
}

// posixShell prefers bash, which is what the upstream `bash` tool exposes.
func posixShell() string {
	if path, err := exec.LookPath("bash"); err == nil {
		return path
	}
	return "/bin/sh"
}

type limitedBuffer struct {
	buffer  bytes.Buffer
	limit   int
	dropped int
}

func (b *limitedBuffer) Write(chunk []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.dropped += len(chunk)
		return len(chunk), nil
	}
	if len(chunk) > remaining {
		b.dropped += len(chunk) - remaining
		chunk = chunk[:remaining]
	}
	b.buffer.Write(chunk)
	return len(chunk), nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }
