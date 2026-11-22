package builtin_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
)

// TestTodoWriteReportsState is B-31 for the todo list: the tool hands the loop
// the payload to record, and the loop (not the tool) knows the turn and step.
func TestTodoWriteReportsState(t *testing.T) {
	store := builtin.NewTodoStore()
	registry := applyPlugin(t, builtin.NewTodoPluginWithStore(store))
	write := mustTool(t, registry, "todo_write")

	result := execute(t, write, `{"todos":[{"content":"ship it","status":"in_progress"},{"content":"rest"}]}`)
	if result.IsError {
		t.Fatalf("todo_write failed: %s", result.Content)
	}
	if len(result.State) != 1 || result.State[0].Type != "todo/write" {
		t.Fatalf("state = %+v, want one todo/write event", result.State)
	}
	data, ok := result.State[0].Data.(*builtin.TodoWriteData)
	if !ok {
		t.Fatalf("state payload = %T, want *builtin.TodoWriteData", result.State[0].Data)
	}
	if len(data.Todos) != 2 || data.Todos[0].Status != "in_progress" || data.Todos[1].Status != "pending" {
		t.Fatalf("todos = %+v", data.Todos)
	}
	if data.Turn != 0 || data.Step != 0 {
		t.Fatalf("turn/step = %d/%d, want them left for the loop", data.Turn, data.Step)
	}
	data.SetTurnStep(3, 2)
	if data.Turn != 3 || data.Step != 2 {
		t.Fatalf("after SetTurnStep = %d/%d, want 3/2", data.Turn, data.Step)
	}

	// An empty list clears the store but still has to be recorded, otherwise a
	// replay would restore the previous list.
	cleared := execute(t, write, `{"todos":[]}`)
	if len(cleared.State) != 1 {
		t.Fatalf("clearing produced state %+v, want one todo/write", cleared.State)
	}
	if items := store.Items(); len(items) != 0 {
		t.Fatalf("store after clearing = %+v", items)
	}
}

// TestTodoPluginSharesStore proves the host can restore a list before the tool
// runs: both constructors must end up in the same store.
func TestTodoPluginSharesStore(t *testing.T) {
	store := builtin.NewTodoStore()
	store.Replace([]builtin.TodoItem{{Content: "restored", Status: "pending"}})
	registry := applyPlugin(t, builtin.NewTodoPluginWithStore(store))
	result := execute(t, mustTool(t, registry, "todo_write"), `{"todos":[{"content":"next","status":"pending"}]}`)
	if result.IsError {
		t.Fatalf("todo_write failed: %s", result.Content)
	}
	items := store.Items()
	if len(items) != 1 || items[0].Content != "next" {
		t.Fatalf("shared store = %+v, want the tool's new list", items)
	}
}

// TestWorkingDirectoryReportsChanges keeps the query case silent: only an actual
// move is worth a session event.
func TestWorkingDirectoryReportsChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	workspace := builtin.NewWorkspace(root)
	registry := applyPlugin(t, builtin.NewFsPlugin(workspace))
	directory := mustTool(t, registry, "working_directory")

	query := execute(t, directory, `{}`)
	if len(query.State) != 0 {
		t.Fatalf("querying the directory produced state %+v", query.State)
	}

	moved := execute(t, directory, `{"path":"sub"}`)
	if moved.IsError {
		t.Fatalf("move failed: %s", moved.Content)
	}
	if len(moved.State) != 1 || moved.State[0].Type != "working-directory/change" {
		t.Fatalf("state = %+v, want one working-directory/change", moved.State)
	}
	data, ok := moved.State[0].Data.(*builtin.WorkingDirectoryData)
	if !ok {
		t.Fatalf("payload = %T, want *builtin.WorkingDirectoryData", moved.State[0].Data)
	}
	if data.Directory != "sub" {
		t.Fatalf("recorded directory = %q, want the root-relative path", data.Directory)
	}
	data.SetTurnStep(2, 1)
	if data.Turn != 2 || data.Step != 1 {
		t.Fatalf("after SetTurnStep = %d/%d", data.Turn, data.Step)
	}
}

// TestWorkspaceRestoreCwd replays a recorded directory without depending on the
// current one, and reports a directory that is gone.
func TestWorkspaceRestoreCwd(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	workspace := builtin.NewWorkspace(root)
	if err := workspace.RestoreCwd("sub"); err != nil {
		t.Fatalf("RestoreCwd: %v", err)
	}
	if got := workspace.Cwd(); got != "sub" {
		t.Fatalf("cwd = %q, want sub", got)
	}
	// A recorded root value must land at the root, not below the current directory.
	if err := workspace.RestoreCwd("."); err != nil {
		t.Fatalf("RestoreCwd(.): %v", err)
	}
	if got := workspace.Cwd(); got != "" {
		t.Fatalf("cwd = %q, want the root", got)
	}
	if err := workspace.RestoreCwd("../escape"); err == nil {
		t.Fatal("expected an escaping path to be rejected")
	}
	if err := workspace.RestoreCwd("missing"); err == nil {
		t.Fatal("expected a missing directory to be reported")
	}
}
