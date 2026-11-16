// Package builtin provides first-party plugins of the Go rewrite: workspace
// scoped file tools, a shell tool and a clock tool.
package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

func register(registry *tool.Registry, tools ...tool.Tool) error {
	var errs []error
	for _, candidate := range tools {
		if candidate == nil {
			errs = append(errs, errors.New("builtin: nil tool"))
			continue
		}
		if err := registry.Register(candidate); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func schema(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("builtin: encode tool schema: %v", err))
	}
	return data
}

// FsPlugin registers workspace-scoped file tools.
type FsPlugin struct{ workspace string }

// NewFsPlugin creates the workspace file plugin.
func NewFsPlugin(workspace string) *FsPlugin { return &FsPlugin{workspace: workspace} }

// Name implements kernel.Plugin.
func (p *FsPlugin) Name() string { return "builtin/fs" }

// Apply implements kernel.Plugin.
func (p *FsPlugin) Apply(ctx *kernel.Context) error {
	registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
	if !ok {
		return errors.New("builtin/fs: tool registry service is missing")
	}
	return register(registry,
		&readFileTool{root: p.workspace},
		&writeFileTool{root: p.workspace},
		&listDirTool{root: p.workspace},
	)
}

// ShellPlugin registers a command execution tool inside the workspace.
type ShellPlugin struct{ workspace string }

// NewShellPlugin creates the shell plugin.
func NewShellPlugin(workspace string) *ShellPlugin { return &ShellPlugin{workspace: workspace} }

// Name implements kernel.Plugin.
func (p *ShellPlugin) Name() string { return "builtin/shell" }

// Apply implements kernel.Plugin.
func (p *ShellPlugin) Apply(ctx *kernel.Context) error {
	registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
	if !ok {
		return errors.New("builtin/shell: tool registry service is missing")
	}
	return register(registry, &shellTool{root: p.workspace})
}

// TimePlugin registers the clock tool.
type TimePlugin struct{}

// NewTimePlugin creates the clock plugin.
func NewTimePlugin() *TimePlugin { return &TimePlugin{} }

// Name implements kernel.Plugin.
func (*TimePlugin) Name() string { return "builtin/time" }

// Apply implements kernel.Plugin.
func (*TimePlugin) Apply(ctx *kernel.Context) error {
	registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
	if !ok {
		return errors.New("builtin/time: tool registry service is missing")
	}
	return register(registry, &clockTool{})
}

func resolveWorkspacePath(root, requested string) (absolute string, relative string, err error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", "", errors.New("path is required")
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	absolute, err = filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	relative, err = filepath.Rel(rootAbsolute, absolute)
	if err != nil {
		return "", "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("path %q escapes the workspace", requested)
	}
	return absolute, relative, nil
}

type readFileTool struct{ root string }

func (t *readFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "read",
		Description: "Read a UTF-8 text file from the workspace and return its content.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path relative to the workspace."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		}),
	}
}

func (t *readFileTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	absolute, relative, err := resolveWorkspacePath(t.root, args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("stat %s: %w", relative, err)
	}
	if info.IsDir() {
		return tool.Result{}, fmt.Errorf("%s is a directory, use list_dir", relative)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read %s: %w", relative, err)
	}
	return tool.Result{Content: fmt.Sprintf("# %s (%d bytes)\n%s", relative, len(content), string(content))}, nil
}

type writeFileTool struct{ root string }

func (t *writeFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "write",
		Description: "Create or overwrite a UTF-8 text file inside the workspace.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path relative to the workspace."},
				"content": map[string]any{"type": "string", "description": "Full file content to write."},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		}),
	}
}

func (t *writeFileTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	absolute, relative, err := resolveWorkspacePath(t.root, args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return tool.Result{}, fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(absolute, []byte(args.Content), 0o644); err != nil {
		return tool.Result{}, fmt.Errorf("write %s: %w", relative, err)
	}
	return tool.Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), relative)}, nil
}

type listDirTool struct{ root string }

func (t *listDirTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "list_dir",
		Description: "List the entries of a directory inside the workspace.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path relative to the workspace; defaults to ."},
			},
			"additionalProperties": false,
		}),
	}
}

func (t *listDirTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}
	absolute, relative, err := resolveWorkspacePath(t.root, args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("list %s: %w", relative, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s (%d entries)\n", relative, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Fprintf(&builder, "d %s/\n", entry.Name())
			continue
		}
		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(&builder, "f %s\n", entry.Name())
			continue
		}
		fmt.Fprintf(&builder, "f %s (%d bytes)\n", entry.Name(), info.Size())
	}
	return tool.Result{Content: builder.String()}, nil
}

type shellTool struct{ root string }

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
	command.Dir = t.root
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

type clockTool struct{}

func (*clockTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "now",
		Description: "Return the current date and time in UTC and local time.",
		Parameters: schema(map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}),
	}
}

func (*clockTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	now := time.Now()
	return tool.Result{Content: fmt.Sprintf("UTC: %s\nLocal: %s", now.UTC().Format(time.RFC3339), now.Format(time.RFC3339))}, nil
}
