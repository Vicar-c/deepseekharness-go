// Package builtin provides first-party plugins of the Go rewrite: workspace
// scoped file tools, search and image helpers, a shell tool, a todo list and a
// clock tool. Every file tool resolves paths through one shared Workspace, so
// none of them can escape the configured sandbox root.
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// FsPlugin registers the workspace scoped file, search and image tools.
type FsPlugin struct{ workspace *Workspace }

// NewFsPlugin creates the workspace tool plugin. The workspace is shared with
// the shell plugin so working_directory affects both.
func NewFsPlugin(workspace *Workspace) *FsPlugin { return &FsPlugin{workspace: workspace} }

// Name implements kernel.Plugin.
func (p *FsPlugin) Name() string { return "builtin/fs" }

// Apply implements kernel.Plugin.
func (p *FsPlugin) Apply(ctx *kernel.Context) error {
	registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
	if !ok {
		return errors.New("builtin/fs: tool registry service is missing")
	}
	return register(registry,
		&readFileTool{workspace: p.workspace},
		&writeFileTool{workspace: p.workspace},
		&listDirTool{workspace: p.workspace},
		&editFileTool{workspace: p.workspace},
		&globTool{workspace: p.workspace},
		&grepTool{workspace: p.workspace},
		&readImageTool{workspace: p.workspace},
		&workingDirectoryTool{workspace: p.workspace},
	)
}

// ShellPlugin registers a command execution tool inside the workspace.
type ShellPlugin struct{ workspace *Workspace }

// NewShellPlugin creates the shell plugin.
func NewShellPlugin(workspace *Workspace) *ShellPlugin {
	return &ShellPlugin{workspace: workspace}
}

// Name implements kernel.Plugin.
func (p *ShellPlugin) Name() string { return "builtin/shell" }

// Apply implements kernel.Plugin.
func (p *ShellPlugin) Apply(ctx *kernel.Context) error {
	registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
	if !ok {
		return errors.New("builtin/shell: tool registry service is missing")
	}
	return register(registry, &shellTool{workspace: p.workspace})
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
