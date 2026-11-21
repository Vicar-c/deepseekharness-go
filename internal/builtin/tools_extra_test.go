package builtin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// TestEveryRegisteredToolIsWellFormed pins the catalogue: the P1 tool set is
// registered exactly once, every definition carries a description and a valid
// JSON schema.
func TestEveryRegisteredToolIsWellFormed(t *testing.T) {
	workspace := t.TempDir()
	registry := tool.NewRegistry()
	services := kernel.NewServices()
	kernel.Provide(services, registry)
	appContext := &kernel.Context{Logger: testLogger(), Events: kernel.NewEvents(), Services: services}
	for _, plugin := range []kernel.Plugin{
		builtin.NewFsPlugin(builtin.NewWorkspace(workspace)),
		builtin.NewShellPlugin(builtin.NewWorkspace(workspace)),
		builtin.NewTimePlugin(),
		builtin.NewTodoPlugin(),
	} {
		if err := plugin.Apply(appContext); err != nil {
			t.Fatalf("Apply(%s): %v", plugin.Name(), err)
		}
	}

	definitions := registry.Definitions()
	if len(definitions) != 11 {
		t.Fatalf("definitions = %d, want 11", len(definitions))
	}
	names := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Description) == "" {
			t.Errorf("tool %q has an empty name or description", definition.Name)
		}
		if !json.Valid(definition.Parameters) {
			t.Errorf("tool %q has invalid JSON parameters: %s", definition.Name, definition.Parameters)
		}
		if names[definition.Name] {
			t.Errorf("tool %q is registered twice", definition.Name)
		}
		names[definition.Name] = true
	}
	want := []string{"read", "write", "list_dir", "edit", "glob", "grep", "read_image", "working_directory", "todo_write", "now"}
	if runtime.GOOS == "windows" {
		want = append(want, "pwsh")
	} else {
		want = append(want, "bash")
	}
	for _, name := range want {
		if !names[name] {
			t.Errorf("tool %q is missing", name)
		}
	}
}

func TestEditPreservesFileMode(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "secret.txt")
	if err := os.WriteFile(path, []byte("token=one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	edit := mustTool(t, registry, "edit")

	execute(t, edit, `{"path":"secret.txt","old_string":"one","new_string":"two"}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "token=two\n" {
		t.Fatalf("content = %q, err=%v", raw, err)
	}
}

func TestWorkingDirectoryReportsCurrent(t *testing.T) {
	workspace := t.TempDir()
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	cwd := mustTool(t, registry, "working_directory")

	result := execute(t, cwd, `{}`)
	if !strings.Contains(result.Content, "workspace:") || !strings.Contains(result.Content, "working directory: .") {
		t.Fatalf("report = %q", result.Content)
	}
}

func TestGlobHonoursPathLimitAndSkipsVCS(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"sub/a.go", "sub/nested/b.go", ".git/c.go"} {
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

	result := execute(t, glob, `{"pattern":"**/*.go","path":"sub"}`)
	if !strings.Contains(result.Content, "a.go") || !strings.Contains(result.Content, "nested/b.go") {
		t.Fatalf("path-scoped glob = %q", result.Content)
	}
	if strings.Contains(result.Content, "c.go") {
		t.Fatalf("glob must not descend into .git: %q", result.Content)
	}
	result = execute(t, glob, `{"pattern":"**/*.go","limit":1}`)
	if !strings.Contains(result.Content, "stopped at the 1 match limit") {
		t.Fatalf("limit note missing: %q", result.Content)
	}
}

func TestGrepFiltersBinaryAndLimits(t *testing.T) {
	workspace := t.TempDir()
	files := map[string]string{
		"a.txt":       "needle one\nneedle two\n",
		"b.go":        "needle three\n",
		"bin.dat":     "needle\x00binary\n",
		".git/config": "needle vcs\n",
	}
	for name, content := range files {
		full := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	grep := mustTool(t, registry, "grep")

	result := execute(t, grep, `{"pattern":"needle"}`)
	if !strings.Contains(result.Content, "a.txt:1:") || !strings.Contains(result.Content, "b.go:1:") {
		t.Fatalf("grep missed text files: %q", result.Content)
	}
	if strings.Contains(result.Content, "bin.dat") {
		t.Fatalf("grep must skip binary files: %q", result.Content)
	}
	if strings.Contains(result.Content, ".git/") {
		t.Fatalf("grep must skip .git: %q", result.Content)
	}

	result = execute(t, grep, `{"pattern":"needle","glob":"*.go"}`)
	if strings.Contains(result.Content, "a.txt") || !strings.Contains(result.Content, "b.go") {
		t.Fatalf("glob filter = %q", result.Content)
	}

	result = execute(t, grep, `{"pattern":"needle","max_matches":1}`)
	if !strings.Contains(result.Content, "stopped at the 1 match limit") {
		t.Fatalf("match limit note missing: %q", result.Content)
	}
}

func TestReadImageSkipsLargeInlineAndRejectsOversize(t *testing.T) {
	workspace := t.TempDir()
	random := rand.New(rand.NewSource(7))
	picture := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			picture.Set(x, y, color.RGBA{
				R: uint8(random.Intn(256)),
				G: uint8(random.Intn(256)),
				B: uint8(random.Intn(256)),
				A: 255,
			})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() <= 64*1024 {
		t.Fatalf("noise image is only %d bytes; the inline-skip case would not be exercised", buffer.Len())
	}
	if err := os.WriteFile(filepath.Join(workspace, "noise.png"), buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	oversize := make([]byte, (8<<20)+1)
	if err := os.WriteFile(filepath.Join(workspace, "huge.png"), oversize, 0o644); err != nil {
		t.Fatal(err)
	}

	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	readImage := mustTool(t, registry, "read_image")

	result := execute(t, readImage, `{"path":"noise.png"}`)
	if !strings.Contains(result.Content, "inline base64 skipped") {
		t.Fatalf("large inline payload was not skipped: %.120q", result.Content)
	}
	if strings.Contains(result.Content, "data:image/png;base64,") {
		t.Fatal("oversized image must not inline base64")
	}
	if _, err := readImage.Execute(context.Background(), json.RawMessage(`{"path":"huge.png"}`)); err == nil {
		t.Fatal("expected an image above the size limit to be rejected")
	}
}

// TestShellToolFollowsWorkingDirectory is the reason the two plugins share one
// Workspace: flipping the directory must move the shell too, not only the file
// tools.
func TestShellToolFollowsWorkingDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	shared := builtin.NewWorkspace(workspace)
	registry := tool.NewRegistry()
	services := kernel.NewServices()
	kernel.Provide(services, registry)
	appContext := &kernel.Context{Logger: testLogger(), Events: kernel.NewEvents(), Services: services}
	for _, plugin := range []kernel.Plugin{builtin.NewFsPlugin(shared), builtin.NewShellPlugin(shared)} {
		if err := plugin.Apply(appContext); err != nil {
			t.Fatalf("Apply(%s): %v", plugin.Name(), err)
		}
	}
	execute(t, mustTool(t, registry, "working_directory"), `{"path":"sub"}`)

	name := "bash"
	command := "pwd"
	if runtime.GOOS == "windows" {
		name = "pwsh"
		command = "Get-Location | ForEach-Object { $_.Path }"
	}
	payload, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	result, err := mustTool(t, registry, name).Execute(context.Background(), payload)
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if !strings.Contains(strings.ToLower(result.Content), "sub") {
		t.Fatalf("shell did not follow the shared working directory: %q", result.Content)
	}
}

// TestShellToolIsKilledWhenThePipelineTimesOut covers the timeout contract the
// agent loop relies on: a hanging command must come back as an IsError result,
// quickly, instead of blocking the turn.
func TestShellToolIsKilledWhenThePipelineTimesOut(t *testing.T) {
	registry := applyPlugin(t, builtin.NewShellPlugin(builtin.NewWorkspace(t.TempDir())))
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{
		Logger:  testLogger(),
		Timeout: time.Second,
	})
	name := "bash"
	command := "sleep 5"
	if runtime.GOOS == "windows" {
		name = "pwsh"
		command = "Start-Sleep -Seconds 5"
	}
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result := pipeline.Execute(context.Background(), tool.Call{ID: "1", Name: name, Arguments: arguments})
	elapsed := time.Since(started)
	if !result.IsError {
		t.Fatalf("hanging command must be an IsError result, got %+v", result)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("timeout took %s, want roughly one second", elapsed)
	}
	if !strings.Contains(result.Content, "timed out") && !strings.Contains(result.Content, "exit code") {
		t.Fatalf("unexpected timeout output: %q", result.Content)
	}
}

// TestPipelineGuardBlocksBuiltinTool proves a guard veto reaches the model as a
// normal IsError result, which is the seam a sandbox or approval policy will
// plug into later.
func TestPipelineGuardBlocksBuiltinTool(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{
		Logger:  testLogger(),
		Timeout: 5 * time.Second,
	})
	pipeline.AddGuard(func(ctx context.Context, call tool.Call) error {
		if call.Name == "read" {
			return errors.New("blocked by policy")
		}
		return nil
	})

	result := pipeline.Execute(context.Background(), tool.Call{ID: "1", Name: "read", Arguments: json.RawMessage(`{"path":"secret.txt"}`)})
	if !result.IsError || !strings.Contains(result.Content, "blocked by policy") {
		t.Fatalf("guard veto not surfaced: %+v", result)
	}
}

// TestBuiltinToolsBehaveThroughThePipeline exercises the path the agent loop
// actually uses: output clamping and error-to-IsError conversion live in the
// pipeline, not in the tools.
func TestBuiltinToolsBehaveThroughThePipeline(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "big.txt"), []byte(strings.Repeat("x", 5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := applyPlugin(t, builtin.NewFsPlugin(builtin.NewWorkspace(workspace)))
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{
		Logger:         testLogger(),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 200,
	})

	clamped := pipeline.Execute(context.Background(), tool.Call{ID: "1", Name: "read", Arguments: json.RawMessage(`{"path":"big.txt"}`)})
	if clamped.IsError {
		t.Fatalf("read through pipeline failed: %+v", clamped)
	}
	if !strings.Contains(clamped.Content, "[truncated") {
		t.Fatalf("pipeline did not clamp the output: %.120q", clamped.Content)
	}

	missing := pipeline.Execute(context.Background(), tool.Call{ID: "2", Name: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)})
	if !missing.IsError || !strings.Contains(missing.Content, "failed") {
		t.Fatalf("missing file must become an IsError result, got %+v", missing)
	}

	unknown := pipeline.Execute(context.Background(), tool.Call{ID: "3", Name: "nope", Arguments: json.RawMessage(`{}`)})
	if !unknown.IsError || !strings.Contains(unknown.Content, "not registered") {
		t.Fatalf("unknown tool must become an IsError result, got %+v", unknown)
	}
}

// TestBuiltinConcurrencyClassification pins which built-in tools may overlap in
// one model step. Only read-only tools opt in; a new tool stays exclusive unless
// it says otherwise.
func TestBuiltinConcurrencyClassification(t *testing.T) {
	workspace := t.TempDir()
	registry := tool.NewRegistry()
	services := kernel.NewServices()
	kernel.Provide(services, registry)
	appContext := &kernel.Context{Logger: testLogger(), Events: kernel.NewEvents(), Services: services}
	for _, plugin := range []kernel.Plugin{
		builtin.NewFsPlugin(builtin.NewWorkspace(workspace)),
		builtin.NewShellPlugin(builtin.NewWorkspace(workspace)),
		builtin.NewTimePlugin(),
		builtin.NewTodoPlugin(),
	} {
		if err := plugin.Apply(appContext); err != nil {
			t.Fatalf("Apply(%s): %v", plugin.Name(), err)
		}
	}

	wantSafe := map[string]bool{
		"read": true, "list_dir": true, "glob": true, "grep": true, "read_image": true, "now": true,
		"write": false, "edit": false, "working_directory": false, "todo_write": false,
	}
	// The shell tool is named after the platform dialect.
	if runtime.GOOS == "windows" {
		wantSafe["pwsh"] = false
	} else {
		wantSafe["bash"] = false
	}
	seen := make(map[string]bool, len(wantSafe))
	for _, registered := range registry.List() {
		name := registered.Definition().Name
		want, ok := wantSafe[name]
		if !ok {
			t.Fatalf("tool %q is not covered by the concurrency classification", name)
		}
		seen[name] = true
		safe, implements := registered.(tool.ConcurrencySafe)
		got := implements && safe.ConcurrencySafe()
		if got != want {
			t.Fatalf("tool %q concurrency-safe = %v, want %v", name, got, want)
		}
	}
	if len(seen) != len(wantSafe) {
		t.Fatalf("classified %d tools, registry has %d", len(seen), len(wantSafe))
	}
}
