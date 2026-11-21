package builtin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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

func execute(t *testing.T, tool tool.Tool, payload string) tool.Result {
	t.Helper()
	result, err := tool.Execute(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("%s.Execute: %v", tool.Definition().Name, err)
	}
	return result
}

func TestFsToolsStayInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
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

func TestEditToolReplacesUniqueString(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	edit := mustTool(t, registry, "edit")

	result := execute(t, edit, `{"path":"note.txt","old_string":"world","new_string":"Go"}`)
	if !strings.Contains(result.Content, "replaced 1 occurrence") {
		t.Fatalf("edit result = %q", result.Content)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "hello Go\n" {
		t.Fatalf("content = %q, err=%v", raw, err)
	}

	if _, err := edit.Execute(context.Background(), json.RawMessage(`{"path":"note.txt","old_string":"missing","new_string":"x"}`)); err == nil {
		t.Fatal("expected a missing old_string to fail")
	}
	if err := os.WriteFile(path, []byte("dup dup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Execute(context.Background(), json.RawMessage(`{"path":"note.txt","old_string":"dup","new_string":"x"}`)); err == nil {
		t.Fatal("expected an ambiguous old_string to fail")
	}
	execute(t, edit, `{"path":"note.txt","old_string":"dup","new_string":"x","replace_all":true}`)
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != "x x\n" {
		t.Fatalf("replace_all content = %q, err=%v", raw, err)
	}
}

func TestWorkingDirectoryMovesEveryTool(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sub", "note.txt"), []byte("inside sub"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	cwd := mustTool(t, registry, "working_directory")
	read := mustTool(t, registry, "read")

	result := execute(t, cwd, `{"path":"sub"}`)
	if !strings.Contains(result.Content, "set to sub") {
		t.Fatalf("working_directory result = %q", result.Content)
	}
	result = execute(t, read, `{"path":"note.txt"}`)
	if !strings.Contains(result.Content, "inside sub") {
		t.Fatalf("read after chdir = %q", result.Content)
	}
	if _, err := read.Execute(context.Background(), json.RawMessage(`{"path":"../escape.txt"}`)); err == nil {
		t.Fatal("expected a relative path above the workspace root to fail")
	}
	if _, err := cwd.Execute(context.Background(), json.RawMessage(`{"path":"../../outside"}`)); err == nil {
		t.Fatal("expected the working directory to stay inside the workspace")
	}
}

func TestGlobTool(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"a.go", "sub/b.go", "sub/c.txt"} {
		full := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	glob := mustTool(t, registry, "glob")

	result := execute(t, glob, `{"pattern":"**/*.go"}`)
	if !strings.Contains(result.Content, "a.go") || !strings.Contains(result.Content, "sub/b.go") {
		t.Fatalf("glob result = %q", result.Content)
	}
	if strings.Contains(result.Content, "c.txt") {
		t.Fatalf("glob must not return non-matching files: %q", result.Content)
	}
	result = execute(t, glob, `{"pattern":"*.txt"}`)
	if !strings.Contains(result.Content, "sub/c.txt") {
		t.Fatalf("bare-name glob should match nested files: %q", result.Content)
	}
}

func TestGrepTool(t *testing.T) {
	workspace := t.TempDir()
	content := "line one\nKey: value\nanother key here\n"
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	grep := mustTool(t, registry, "grep")

	result := execute(t, grep, `{"pattern":"key"}`)
	if !strings.Contains(result.Content, "note.txt:3:") {
		t.Fatalf("case-sensitive grep result = %q", result.Content)
	}
	if strings.Contains(result.Content, "note.txt:2:") {
		t.Fatalf("case-sensitive grep matched an uppercase line: %q", result.Content)
	}
	result = execute(t, grep, `{"pattern":"key","ignore_case":true}`)
	if !strings.Contains(result.Content, "note.txt:2:") || !strings.Contains(result.Content, "note.txt:3:") {
		t.Fatalf("case-insensitive grep result = %q", result.Content)
	}
	result = execute(t, grep, `{"pattern":"absent"}`)
	if !strings.Contains(result.Content, "no matches") {
		t.Fatalf("empty grep result = %q", result.Content)
	}
}

func TestReadImageTool(t *testing.T) {
	workspace := t.TempDir()
	var buffer bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 3, 2))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dot.png"), buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	readImage := mustTool(t, registry, "read_image")

	result := execute(t, readImage, `{"path":"dot.png"}`)
	if !strings.Contains(result.Content, "3x2") {
		t.Fatalf("read_image result = %q", result.Content)
	}
	if !strings.Contains(result.Content, "data:image/png;base64,") {
		t.Fatalf("read_image data URI missing: %q", result.Content)
	}
	if _, err := readImage.Execute(context.Background(), json.RawMessage(`{"path":"note.txt"}`)); err == nil {
		t.Fatal("expected a non-image file to be rejected")
	}
}

func TestTodoWriteTool(t *testing.T) {
	registry := applyPlugin(t, builtin.NewTodoPlugin())
	todo := mustTool(t, registry, "todo_write")

	result := execute(t, todo, `{"todos":[{"content":"read docs","status":"completed"},{"content":"write tests"}]}`)
	if !strings.Contains(result.Content, "[x] read docs") || !strings.Contains(result.Content, "[ ] write tests") {
		t.Fatalf("todo result = %q", result.Content)
	}
	result = execute(t, todo, `{"todos":[{"content":"in flight","status":"in_progress"}]}`)
	if !strings.Contains(result.Content, "[~] in flight") {
		t.Fatalf("todo update result = %q", result.Content)
	}
	result = execute(t, todo, `{"todos":[]}`)
	if !strings.Contains(result.Content, "cleared") {
		t.Fatalf("todo clear result = %q", result.Content)
	}
	if _, err := todo.Execute(context.Background(), json.RawMessage(`{"todos":[{"content":"x","status":"doing"}]}`)); err == nil {
		t.Fatal("expected an unknown status to fail")
	}
}

func TestShellToolRunsCommands(t *testing.T) {
	registry := applyPlugin(t, builtin.NewShellPlugin(builtin.NewWorkspace(t.TempDir())))
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
