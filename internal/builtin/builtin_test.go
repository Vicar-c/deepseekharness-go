package builtin_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func applyPlugin(t *testing.T, plugin kernel.Plugin) *tool.Registry {
	t.Helper()
	registry := tool.NewRegistry()
	services := kernel.NewServices()
	kernel.Provide(services, registry)
	appContext := &kernel.Context{Logger: testLogger(), Events: kernel.NewEvents(), Services: services}
	if err := plugin.Apply(appContext); err != nil {
		t.Fatalf("Apply(%s): %v", plugin.Name(), err)
	}
	return registry
}

func mustTool(t *testing.T, registry *tool.Registry, name string) tool.Tool {
	t.Helper()
	found, ok := registry.Get(name)
	if !ok {
		t.Fatalf("tool %q is not registered", name)
	}
	return found
}

func TestFsToolsStayInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	registry := applyPlugin(t, builtin.NewFsPlugin(workspace))
	ctx := context.Background()

	write := mustTool(t, registry, "write")
	result, err := write.Execute(ctx, json.RawMessage(`{"path":"sub/note.txt","content":"hello harness"}`))
	if err != nil || result.IsError {
		t.Fatalf("write result=%+v err=%v", result, err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "sub", "note.txt"))
	if err != nil || string(raw) != "hello harness" {
		t.Fatalf("file content = %q, err=%v", raw, err)
	}

	read := mustTool(t, registry, "read")
	result, err = read.Execute(ctx, json.RawMessage(`{"path":"sub/note.txt"}`))
	if err != nil || result.IsError || !strings.Contains(result.Content, "hello harness") {
		t.Fatalf("read result=%+v err=%v", result, err)
	}

	list := mustTool(t, registry, "list_dir")
	result, err = list.Execute(ctx, json.RawMessage(`{"path":"sub"}`))
	if err != nil || !strings.Contains(result.Content, "note.txt") {
		t.Fatalf("list result=%+v err=%v", result, err)
	}

	if _, err := read.Execute(ctx, json.RawMessage(`{"path":"../../escape.txt"}`)); err == nil {
		t.Fatal("expected read outside workspace to fail")
	}
	if _, err := write.Execute(ctx, json.RawMessage(`{"path":"../escape.txt","content":"x"}`)); err == nil {
		t.Fatal("expected write outside workspace to fail")
	}
}

func TestShellToolRunsCommands(t *testing.T) {
	registry := applyPlugin(t, builtin.NewShellPlugin(t.TempDir()))
	name := "bash"
	if runtime.GOOS == "windows" {
		name = "pwsh"
	}
	shell := mustTool(t, registry, name)
	command := `echo harness-shell-ok`
	if runtime.GOOS == "windows" {
		// PowerShell dialect on purpose: cmd.exe would not understand this.
		command = `Write-Output "harness-shell-ok"`
	}
	payload, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.Execute(context.Background(), payload)
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "harness-shell-ok") {
		t.Fatalf("shell result = %+v", result)
	}
}

func TestClockTool(t *testing.T) {
	registry := applyPlugin(t, builtin.NewTimePlugin())
	clock := mustTool(t, registry, "now")
	result, err := clock.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(result.Content, "UTC:") {
		t.Fatalf("clock result=%+v err=%v", result, err)
	}
}
