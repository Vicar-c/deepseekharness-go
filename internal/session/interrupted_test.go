package session_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

const testHeader = `{"type":"session","version":4,"id":"%s","createdAt":1763000000000,"isSeeded":false}`

// eventLine renders one v4 event; surface events carry the append operation.
func eventLine(seq int, eventType, data string, surface bool) string {
	surfaceOp := ""
	if surface {
		surfaceOp = `,"surfaceOp":"append"`
	}
	return fmt.Sprintf(`{"type":%q,"data":%s,"seq":%d,"time":1763000000000%s}`, eventType, data, seq, surfaceOp)
}

func decodeLog(t *testing.T, id string, events []string) *session.Recording {
	t.Helper()
	lines := append([]string{fmt.Sprintf(testHeader, id)}, events...)
	recording, err := session.Decode(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return recording
}

const (
	userMessageJSON       = `{"turn":1,"step":0,"message":{"id":"u1","role":"user","content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}}}`
	assistantToolCallJSON = `{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"tool-call","id":"call-1","name":"read","arguments":"{}"}],"source":{"kind":"model"}},"stream":[]}`
	toolCallJSON          = `{"turn":1,"step":1,"callId":"call-1","name":"read","arguments":"{}"}`
	toolResultJSON        = `{"turn":1,"step":1,"message":{"id":"t1","role":"tool","toolCallId":"call-1","content":[{"type":"text","text":"ok"}],"source":{"kind":"tool","callId":"call-1"}}}`
)

// crashAfterToolCall is the shape that broke resume in the audit: the call was
// recorded, the result never reached the log.
func crashAfterToolCall() []string {
	return []string{
		eventLine(0, "turn/start", `{"turn":1}`, false),
		eventLine(1, "user/message", userMessageJSON, true),
		eventLine(2, "step/start", `{"turn":1,"step":1}`, false),
		eventLine(3, "assistant/message", assistantToolCallJSON, true),
		eventLine(4, "tool/call", toolCallJSON, false),
	}
}

func TestInterruptedTurnClosersShapes(t *testing.T) {
	cases := []struct {
		name       string
		events     []string
		wantTypes  []string
		wantCode   string
		wantCallID string
	}{
		{
			name:       "crash after tool/call",
			events:     crashAfterToolCall(),
			wantTypes:  []string{"tool/result", "step/end", "turn/end"},
			wantCode:   tool.CodeOutcomeUnknown,
			wantCallID: "call-1",
		},
		{
			name: "crash after assistant message",
			events: []string{
				eventLine(0, "turn/start", `{"turn":1}`, false),
				eventLine(1, "user/message", userMessageJSON, true),
				eventLine(2, "step/start", `{"turn":1,"step":1}`, false),
				eventLine(3, "assistant/message", assistantToolCallJSON, true),
			},
			wantTypes:  []string{"tool/result", "step/end", "turn/end"},
			wantCode:   tool.CodeNotStarted,
			wantCallID: "call-1",
		},
		{
			name: "crash after user message",
			events: []string{
				eventLine(0, "turn/start", `{"turn":1}`, false),
				eventLine(1, "user/message", userMessageJSON, true),
				eventLine(2, "step/start", `{"turn":1,"step":1}`, false),
			},
			wantTypes: []string{"step/end", "turn/end"},
		},
		{
			name: "closed turn needs nothing",
			events: []string{
				eventLine(0, "turn/start", `{"turn":1}`, false),
				eventLine(1, "user/message", userMessageJSON, true),
				eventLine(2, "step/start", `{"turn":1,"step":1}`, false),
				eventLine(3, "assistant/message", `{"turn":1,"step":1,"message":{"id":"a2","role":"assistant","content":[{"type":"text","text":"done"}],"source":{"kind":"model"}},"stream":[]}`, true),
				eventLine(4, "step/end", `{"turn":1,"step":1}`, false),
				eventLine(5, "turn/end", `{"turn":1,"reason":{"kind":"completed"}}`, false),
			},
			wantTypes: nil,
		},
		{
			name: "one of two calls answered",
			events: []string{
				eventLine(0, "turn/start", `{"turn":1}`, false),
				eventLine(1, "user/message", userMessageJSON, true),
				eventLine(2, "step/start", `{"turn":1,"step":1}`, false),
				eventLine(3, "assistant/message", `{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"tool-call","id":"call-1","name":"read","arguments":"{}"},{"type":"tool-call","id":"call-2","name":"grep","arguments":"{}"}],"source":{"kind":"model"}},"stream":[]}`, true),
				eventLine(4, "tool/call", `{"turn":1,"step":1,"callId":"call-1","name":"read","arguments":"{}"}`, false),
				eventLine(5, "tool/call", `{"turn":1,"step":1,"callId":"call-2","name":"grep","arguments":"{}"}`, false),
				eventLine(6, "tool/result", toolResultJSON, true),
			},
			wantTypes:  []string{"tool/result", "step/end", "turn/end"},
			wantCode:   tool.CodeOutcomeUnknown,
			wantCallID: "call-2",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recording := decodeLog(t, "s1", testCase.events)
			closers := session.InterruptedTurnClosers(recording)
			types := make([]string, 0, len(closers))
			for _, closer := range closers {
				types = append(types, closer.Type)
			}
			if strings.Join(types, ",") != strings.Join(testCase.wantTypes, ",") {
				t.Fatalf("closers = %v, want %v", types, testCase.wantTypes)
			}
			if testCase.wantCode == "" {
				return
			}
			data, err := json.Marshal(closers[0].Data)
			if err != nil {
				t.Fatalf("marshal closer: %v", err)
			}
			var result session.ToolResultData
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("decode closer: %v", err)
			}
			if result.Code != testCase.wantCode {
				t.Fatalf("closer code = %q, want %q", result.Code, testCase.wantCode)
			}
			if result.Message.ToolCallID != testCase.wantCallID {
				t.Fatalf("closer call = %q, want %q", result.Message.ToolCallID, testCase.wantCallID)
			}
			if !result.Message.IsError {
				t.Fatal("recovered result must be an error result")
			}
			if closer := closers[len(closers)-1]; closer.Type == "turn/end" {
				turnEnd, ok := closer.Data.(session.TurnEndData)
				if !ok || turnEnd.Reason.Kind != session.TurnEndInterrupted {
					t.Fatalf("turn/end closer = %+v, want reason interrupted", closer.Data)
				}
			}
		})
	}
}

func TestClosersAreIdempotentAfterRepair(t *testing.T) {
	events := crashAfterToolCall()
	recording := decodeLog(t, "s1", events)
	closers := session.InterruptedTurnClosers(recording)
	if len(closers) == 0 {
		t.Fatal("expected closers for a crashed turn")
	}
	// Replaying the appended closers must leave nothing to do.
	repaired := append([]string{}, events...)
	for index, closer := range closers {
		data, err := json.Marshal(closer.Data)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		repaired = append(repaired, eventLine(len(events)+index, closer.Type, string(data), closer.SurfaceOp != nil))
	}
	reloaded := decodeLog(t, "s1", repaired)
	if again := session.InterruptedTurnClosers(reloaded); len(again) != 0 {
		t.Fatalf("second pass produced %v", again)
	}
}

func writeSessionFile(t *testing.T, dir, id string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, id+".jsonl")
	content := strings.Join(append([]string{fmt.Sprintf(testHeader, id)}, lines...), "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return path
}

func TestResumeRepairsInterruptedTurn(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-000000-aaaaaaaa"
	path := writeSessionFile(t, dir, id, crashAfterToolCall())

	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })

	resumed, err := store.Resume(id)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	recording, err := session.ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	types := make([]string, 0, len(recording.Events))
	for _, event := range recording.Events {
		types = append(types, event.Type)
	}
	want := "turn/start,user/message,step/start,assistant/message,tool/call,tool/result,step/end,turn/end"
	if strings.Join(types, ",") != want {
		t.Fatalf("events = %s, want %s", strings.Join(types, ","), want)
	}
	last := recording.Events[len(recording.Events)-1]
	var turnEnd session.TurnEndData
	if err := json.Unmarshal(last.Data, &turnEnd); err != nil {
		t.Fatalf("decode turn/end: %v", err)
	}
	if turnEnd.Reason.Kind != session.TurnEndInterrupted {
		t.Fatalf("turn end kind = %q", turnEnd.Reason.Kind)
	}
	if len(resumed.Messages) != 3 {
		t.Fatalf("resumed messages = %d, want user + assistant + the recovered tool result", len(resumed.Messages))
	}
	if got := resumed.Messages[2].Role; got != "tool" {
		t.Fatalf("last resumed message role = %q, want tool", got)
	}

	before := len(recording.Events)
	if _, err := store.Resume(id); err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	again, err := session.ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	if len(again.Events) != before {
		t.Fatalf("second resume appended %d events", len(again.Events)-before)
	}
}

func TestLoadRejectsMismatchedHeaderID(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-111111-bbbbbbbb"
	writeSessionFile(t, dir, id, crashAfterToolCall())
	if err := os.Rename(filepath.Join(dir, id+".jsonl"), filepath.Join(dir, "renamed.jsonl")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	_, err := store.Load("renamed")
	if err == nil || !strings.Contains(err.Error(), "declares id") {
		t.Fatalf("error = %v, want it to name the id mismatch", err)
	}
}

func TestSessionLogToleratesBOM(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-222222-cccccccc"
	path := filepath.Join(dir, id+".jsonl")
	content := "\xef\xbb\xbf" + fmt.Sprintf(testHeader, id) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A single-line BOM file must not look like a torn tail.
	dropped, err := session.RepairTail(path)
	if err != nil {
		t.Fatalf("RepairTail: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("RepairTail dropped %d bytes of a valid BOM file", dropped)
	}
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Load(id); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Appending on top of a BOM'd file must keep the log readable.
	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.AppendEvent(sess, "turn/start", session.TurnMarker{Turn: 1}, nil, nil); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	recording, err := session.ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording after append: %v", err)
	}
	if len(recording.Events) != 1 || recording.Events[0].Seq != 0 {
		t.Fatalf("events = %+v", recording.Events)
	}
}

// TestConcurrentResumeRepairsOnce keeps two resumes of the same crashed log from
// appending the closers twice.
func TestConcurrentResumeRepairsOnce(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-333333-dddddddd"
	path := writeSessionFile(t, dir, id, crashAfterToolCall())
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })

	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.Resume(id); err != nil {
				errs <- err
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Resume: %v", err)
	}

	recording, err := session.ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	results := 0
	turnEnds := 0
	for _, event := range recording.Events {
		switch event.Type {
		case "tool/result":
			results++
		case "turn/end":
			turnEnds++
		}
	}
	if results != 1 || turnEnds != 1 {
		t.Fatalf("repaired log has %d tool/result and %d turn/end, want exactly 1 each", results, turnEnds)
	}
}

func TestBeginTurnRejectsConcurrentTurn(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	release, err := store.BeginTurn("s1")
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if _, err := store.BeginTurn("s1"); err == nil {
		t.Fatal("second concurrent turn should be rejected")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error = %v", err)
	}
	release()
	release() // idempotent
	second, err := store.BeginTurn("s1")
	if err != nil {
		t.Fatalf("BeginTurn after release: %v", err)
	}
	second()
}
