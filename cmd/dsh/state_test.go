package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

// newStateRuntime builds the part of the runtime the restore path needs: a store
// plus the shared workspace and todo list the builtin tools use.
func newStateRuntime(t *testing.T, workspaceRoot string) (*runtime, *session.Store, *session.Session) {
	t.Helper()
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("restore")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	rt := &runtime{
		store:     store,
		workspace: builtin.NewWorkspace(workspaceRoot),
		todos:     builtin.NewTodoStore(),
	}
	return rt, store, sess
}

func appendState(t *testing.T, store *session.Store, sess *session.Session, eventType string, data any) {
	t.Helper()
	if err := store.AppendEvent(sess, eventType, data, nil, nil); err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
}

// TestRestoreSessionReplaysState is B-31 end to end: the todo list and working
// directory a session recorded come back when it is resumed.
func TestRestoreSessionReplaysState(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "sub"), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	rt, store, sess := newStateRuntime(t, workspaceRoot)
	appendState(t, store, sess, "todo/write", builtin.TodoWriteData{
		Turn: 1, Step: 1, Todos: []builtin.TodoItem{{Content: "first", Status: "completed"}, {Content: "second", Status: "in_progress"}},
	})
	appendState(t, store, sess, "working-directory/change", builtin.WorkingDirectoryData{
		Turn: 1, Step: 1, Directory: "sub",
	})

	if err := rt.restoreSession(sess); err != nil {
		t.Fatalf("restoreSession: %v", err)
	}
	items := rt.todos.Items()
	if len(items) != 2 || items[0].Content != "first" || items[1].Status != "in_progress" {
		t.Fatalf("restored todos = %+v", items)
	}
	if got := rt.workspace.Cwd(); got != "sub" {
		t.Fatalf("restored cwd = %q, want sub", got)
	}

	// A later change wins, exactly like replaying the log in order.
	appendState(t, store, sess, "todo/write", builtin.TodoWriteData{
		Turn: 2, Step: 1, Todos: []builtin.TodoItem{{Content: "only", Status: "pending"}},
	})
	appendState(t, store, sess, "working-directory/change", builtin.WorkingDirectoryData{
		Turn: 2, Step: 1, Directory: ".",
	})
	if err := rt.restoreSession(sess); err != nil {
		t.Fatalf("restoreSession: %v", err)
	}
	if items := rt.todos.Items(); len(items) != 1 || items[0].Content != "only" {
		t.Fatalf("restored todos after replay = %+v", items)
	}
	if got := rt.workspace.Cwd(); got != "" {
		t.Fatalf("restored cwd after replay = %q, want the root", got)
	}
}

// TestRestoreSessionResetsForANewSession keeps one session from inheriting what
// another one changed: a fresh session starts at the root with no todos.
func TestRestoreSessionResetsForANewSession(t *testing.T) {
	rt, store, old := newStateRuntime(t, t.TempDir())
	appendState(t, store, old, "todo/write", builtin.TodoWriteData{Todos: []builtin.TodoItem{{Content: "old", Status: "pending"}}})
	if err := rt.restoreSession(old); err != nil {
		t.Fatalf("restoreSession: %v", err)
	}
	if len(rt.todos.Items()) != 1 {
		t.Fatalf("precondition: todos = %+v", rt.todos.Items())
	}
	fresh, err := store.Create("fresh")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := rt.restoreSession(fresh); err != nil {
		t.Fatalf("restoreSession: %v", err)
	}
	if items := rt.todos.Items(); len(items) != 0 {
		t.Fatalf("a new session inherited todos %+v", items)
	}
	if got := rt.workspace.Cwd(); got != "" {
		t.Fatalf("a new session inherited cwd %q", got)
	}
}

// TestRestoreSessionToleratesADeletedDirectory keeps a resume working when the
// recorded directory no longer exists: the session continues from the root.
func TestRestoreSessionToleratesADeletedDirectory(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "gone"), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.Remove(filepath.Join(workspaceRoot, "gone")); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	rt, store, sess := newStateRuntime(t, workspaceRoot)
	appendState(t, store, sess, "working-directory/change", builtin.WorkingDirectoryData{Directory: "gone"})
	if err := rt.restoreSession(sess); err != nil {
		t.Fatalf("restoreSession: %v", err)
	}
	if got := rt.workspace.Cwd(); got != "" {
		t.Fatalf("cwd = %q, want the root after a missing directory", got)
	}
}
