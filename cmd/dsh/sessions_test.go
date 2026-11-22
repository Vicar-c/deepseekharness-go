package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

// seedSessions creates two stored sessions and returns their ids: one finished
// with an error, one still empty.
func seedSessions(t *testing.T, directory string) (failed, empty string) {
	t.Helper()
	store := session.NewStore(directory)
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Create("failed session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.AppendEvent(first, "turn/start", session.TurnMarker{Turn: 1}, nil, nil); err != nil {
		t.Fatalf("append turn/start: %v", err)
	}
	end := session.TurnEndData{
		Turn: 1,
		Reason: session.TurnEndReason{
			Kind:  session.TurnEndError,
			Error: &session.TurnEndFailure{Message: "bad key", Code: "AUTH", Status: 401},
		},
	}
	if err := store.AppendEvent(first, "turn/end", end, nil, nil); err != nil {
		t.Fatalf("append turn/end: %v", err)
	}
	second, err := store.Create("empty session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return first.ID, second.ID
}

// TestSessionsListsStoredSessions covers B-32's list view: id, title, turns and
// the last turn outcome, with and without --json.
func TestSessionsListsStoredSessions(t *testing.T) {
	home := isolate(t)
	directory := filepath.Join(home, "sessions")
	t.Setenv("DSH_SESSION_DIR", directory)
	failedID, emptyID := seedSessions(t, directory)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sessions"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"ID", failedID, emptyID, "failed session", "empty session", "error [AUTH]"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output is missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"sessions", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var rows []sessionRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("decode JSON list: %v\n%s", err, stdout.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byID := map[string]sessionRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	failed := byID[failedID]
	if failed.LastTurn != session.TurnEndError || failed.LastErrorCode != "AUTH" {
		t.Fatalf("failed session row = %+v", failed)
	}
	if failed.Turns != 1 || failed.Events != 3 {
		t.Fatalf("failed session turns/events = %d/%d, want 1/3 (title + turn/start + turn/end)", failed.Turns, failed.Events)
	}
	if byID[emptyID].LastTurn != "" || byID[emptyID].Turns != 0 {
		t.Fatalf("empty session row = %+v", byID[emptyID])
	}
}

// TestSessionsShowsOneSession covers the detail view.
func TestSessionsShowsOneSession(t *testing.T) {
	home := isolate(t)
	directory := filepath.Join(home, "sessions")
	t.Setenv("DSH_SESSION_DIR", directory)
	failedID, _ := seedSessions(t, directory)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sessions", failedID}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"id: " + failedID,
		"title: failed session",
		"turns: 1",
		"last turn: error [AUTH]",
		"path: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail output is missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"sessions", "--json", failedID}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var row sessionRow
	if err := json.Unmarshal(stdout.Bytes(), &row); err != nil {
		t.Fatalf("decode JSON detail: %v\n%s", err, stdout.String())
	}
	if row.ID != failedID || row.LastErrorCode != "AUTH" {
		t.Fatalf("row = %+v", row)
	}
}

// TestSessionsRejectsAnUnknownID fails loudly instead of printing an empty view.
func TestSessionsRejectsAnUnknownID(t *testing.T) {
	home := isolate(t)
	t.Setenv("DSH_SESSION_DIR", filepath.Join(home, "sessions"))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sessions", "no-such-session"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr = %q, want a not-found error", stderr.String())
	}
}

// TestSessionsReportsNoSessions keeps an empty store a success.
func TestSessionsReportsNoSessions(t *testing.T) {
	home := isolate(t)
	t.Setenv("DSH_SESSION_DIR", filepath.Join(home, "empty-sessions"))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sessions"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "no sessions" {
		t.Fatalf("stdout = %q, want no sessions", stdout.String())
	}
}
