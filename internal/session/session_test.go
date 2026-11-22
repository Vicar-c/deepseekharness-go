package session_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

func TestStoreCreateAppendLoad(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })

	sess, err := store.Create("demo session")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("empty session id")
	}
	if err := store.Append(sess, llm.TextMessage(llm.RoleUser, "hi")); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if err := store.Append(sess, llm.TextMessage(llm.RoleAssistant, "hello")); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if len(sess.Messages) != 2 {
		t.Fatalf("in-memory messages = %d", len(sess.Messages))
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != sess.ID || loaded.Title != "demo session" {
		t.Fatalf("loaded session = %+v", loaded)
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "hello" {
		t.Fatalf("loaded messages = %+v", loaded.Messages)
	}

	raw, err := os.ReadFile(filepath.Join(dir, sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if lines := strings.Count(string(raw), "\n"); lines != 4 {
		t.Fatalf("jsonl lines = %d, want 4 (header + title + 2 events)", lines)
	}
}

func TestStoreListNewestFirst(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Create("first")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := store.Create("second")
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %+v", summaries)
	}
	if summaries[0].ID != second.ID || summaries[1].ID != first.ID {
		t.Fatalf("unexpected order: %+v", summaries)
	}
}

func TestStoreRejectsInvalidIDs(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("expected error for path traversal id")
	}
	sess, err := store.Create("demo")
	if err != nil {
		t.Fatal(err)
	}
	sess.ID = "../escape"
	if err := store.Append(sess, llm.TextMessage(llm.RoleUser, "x")); err == nil {
		t.Fatal("expected error appending with invalid id")
	}
}

func TestStoreLoadMissingSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Load("does-not-exist"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreTruncatesLongTitle(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	long := strings.Repeat("x", 300)
	sess, err := store.Create(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Title) != 120 {
		t.Fatalf("title length = %d, want 120", len(sess.Title))
	}
}

// TestRecordingReadsWithoutRepairing is the read path B-31 restores from: it must
// tolerate a torn trailing record without truncating the file a writer may own.
func TestRecordingReadsWithoutRepairing(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("recording")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	todo := map[string]any{"turn": 1, "step": 1, "todos": []any{map[string]any{"content": "x", "status": "pending"}}}
	if err := store.AppendEvent(sess, "todo/write", todo, nil, nil); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	path := filepath.Join(store.Dir(), sess.ID+".jsonl")
	// A crash can leave a partial line without its newline.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := file.WriteString(`{"type":"todo/write","data":{`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	recording, err := store.Recording(sess.ID)
	if err != nil {
		t.Fatalf("Recording: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Recording repaired the log: %d bytes before, %d after", len(before), len(after))
	}
	var found bool
	for _, event := range recording.Events {
		if event.Type == "todo/write" {
			found = true
		}
	}
	if !found {
		t.Fatal("Recording did not return the committed todo/write event")
	}

	if _, err := store.Recording("missing-session"); err == nil {
		t.Fatal("expected a missing session to be reported")
	}
}
