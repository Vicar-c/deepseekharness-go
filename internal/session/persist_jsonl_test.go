package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testUserData struct {
	ID      string           `json:"id"`
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func newTestHeader(t *testing.T) Header {
	t.Helper()
	depth := 0
	return Header{
		ID:              "sess-test",
		CreatedAt:       1700000000000,
		Cwd:             "/tmp/workspace",
		IsSeeded:        false,
		DelegationDepth: &depth,
		AgentPreset:     "standard",
	}
}

func testUserMessage(id, text string) testUserData {
	return testUserData{
		ID:      id,
		Role:    "user",
		Content: []map[string]any{{"type": "text", "text": text}},
	}
}

func TestCreateSessionFileWritesHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatalf("CreateSessionFile: %v", err)
	}
	defer writer.Close()

	recording, err := ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	if recording.Header.ID != "sess-test" || recording.Header.Version != FormatVersion {
		t.Fatalf("header = %+v", recording.Header)
	}
	if recording.Header.Type != HeaderType {
		t.Fatalf("header type = %q", recording.Header.Type)
	}
	if recording.Header.Cwd != "/tmp/workspace" || recording.Header.AgentPreset != "standard" {
		t.Fatalf("header metadata lost: %+v", recording.Header)
	}
	if len(recording.Events) != 0 {
		t.Fatalf("events = %d, want 0", len(recording.Events))
	}
	if writer.Seq() != 0 {
		t.Fatalf("seq = %d, want 0", writer.Seq())
	}
}

func TestWriterAppendsSequentialEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"one", "two"} {
		if _, err := writer.Append("user/message", testUserMessage("u", text)); err != nil {
			t.Fatalf("Append %d: %v", index, err)
		}
	}
	if writer.Seq() != 2 {
		t.Fatalf("seq = %d, want 2", writer.Seq())
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recording, err := ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	if len(recording.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(recording.Events))
	}
	for index, event := range recording.Events {
		if event.Seq != int64(index) {
			t.Errorf("event %d seq = %d", index, event.Seq)
		}
		if event.Time <= 0 {
			t.Errorf("event %d has no timestamp", index)
		}
		if !event.Known() {
			t.Errorf("event %d type %q is unknown", index, event.Type)
		}
	}
}

func TestOpenWriterContinuesSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s3.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Append("user/message", testUserMessage("u1", "one")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if reopened.Seq() != 1 {
		t.Fatalf("seq after reopen = %d, want 1", reopened.Seq())
	}
	if reopened.Header().ID != "sess-test" {
		t.Fatalf("header after reopen = %+v", reopened.Header())
	}
	if _, err := reopened.Append("user/message", testUserMessage("u2", "two")); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	recording, err := ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	if len(recording.Events) != 2 || recording.Events[1].Seq != 1 {
		t.Fatalf("events after reopen = %+v", recording.Events)
	}
}

func TestSecondWriterIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s4.jsonl")
	first, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWriter(path); !errors.Is(err, ErrWriterExists) {
		t.Fatalf("second OpenWriter err = %v, want ErrWriterExists", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter after Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterRejectsUnknownEventType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s5.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Append("plugin:demo/unknown", map[string]any{}); !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("err = %v, want ErrUnknownEventType", err)
	}
}

func TestAppendEventKeepsSurfaceOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s6.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	appendOp := Append
	if _, err := writer.AppendEvent("user/message", testUserMessage("u1", "hi"), &appendOp, nil); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recording, err := ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	op := recording.Events[0].SurfaceOp
	if op == nil || op.Replace {
		t.Fatalf("surfaceOp = %+v, want append", op)
	}
}

func TestRepairTailDropsPartialTrailingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s7.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Append("user/message", testUserMessage("u1", "one")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte(`{"type":"user/message","data":{"id":"u2"`)
	if _, err := file.Write(partial); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	dropped, err := RepairTail(path)
	if err != nil {
		t.Fatalf("RepairTail: %v", err)
	}
	if dropped != int64(len(partial)) {
		t.Fatalf("dropped = %d, want %d", dropped, len(partial))
	}
	recording, err := ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording after repair: %v", err)
	}
	if len(recording.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(recording.Events))
	}
	if dropped, err := RepairTail(path); err != nil || dropped != 0 {
		t.Fatalf("second repair = %d, %v; want 0, nil", dropped, err)
	}
}

func TestRepairTailDropsInvalidCompleteRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s8.jsonl")
	writer, err := CreateSessionFile(path, newTestHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	broken := []byte("{\"type\":\"user/message\",\"data\":\n")
	if _, err := file.Write(broken); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	dropped, err := RepairTail(path)
	if err != nil {
		t.Fatalf("RepairTail: %v", err)
	}
	if dropped != int64(len(broken)) {
		t.Fatalf("dropped = %d, want %d", dropped, len(broken))
	}
	if recording, err := ReadRecording(path); err != nil || len(recording.Events) != 0 {
		t.Fatalf("ReadRecording after repair = %+v, %v", recording, err)
	}
}
