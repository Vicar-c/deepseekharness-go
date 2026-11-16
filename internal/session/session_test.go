package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"deekseekharness-go/internal/llm"
	"deekseekharness-go/internal/session"
)

func TestStoreCreateAppendLoad(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)

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
	if lines := strings.Count(string(raw), "\n"); lines != 3 {
		t.Fatalf("jsonl lines = %d, want 3 (meta + 2 messages)", lines)
	}
}

func TestStoreListNewestFirst(t *testing.T) {
	store := session.NewStore(t.TempDir())
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
	if _, err := store.Load("does-not-exist"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreTruncatesLongTitle(t *testing.T) {
	store := session.NewStore(t.TempDir())
	long := strings.Repeat("x", 300)
	sess, err := store.Create(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Title) != 120 {
		t.Fatalf("title length = %d, want 120", len(sess.Title))
	}
}
