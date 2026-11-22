package session_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

// TestLoadNeverRepairsOnDisk pins the read/write split: a viewer must not rewrite
// a log, because a writer may be halfway through an append. Load returns the
// committed prefix and leaves the file byte-identical.
func TestLoadNeverRepairsOnDisk(t *testing.T) {
	dir := t.TempDir()
	id := "20261122-555555-ffffffff"
	path := filepath.Join(dir, id+".jsonl")
	content := fmt.Sprintf(testHeader, id) + "\n" +
		eventLine(0, "turn/start", `{"turn":1}`, false) + "\n" +
		`{"type":"turn/end","data":{"turn":1,"re` // torn tail, no newline
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sess.Turn != 1 {
		t.Fatalf("turn = %d, want the committed prefix's 1", sess.Turn)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("Load rewrote the log: %d -> %d bytes", before.Size(), after.Size())
	}

	// Only the write path repairs, and it drops exactly the torn bytes.
	dropped, err := session.RepairTail(path)
	if err != nil {
		t.Fatalf("RepairTail: %v", err)
	}
	if dropped == 0 {
		t.Fatal("RepairTail should have dropped the torn tail")
	}
}

// TestLoadWhileWriting keeps the projection consistent when readers race an
// active writer: no reader may truncate a line, so the final log stays contiguous.
func TestLoadWhileWriting(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("race")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const events = 40
	done := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		defer close(done)
		for index := 0; index < events; index++ {
			eventType := "turn/start"
			data := session.TurnMarker{Turn: index + 1}
			if err := store.AppendEvent(sess, eventType, data, nil, nil); err != nil {
				t.Errorf("append: %v", err)
				return
			}
		}
	}()

	var readers sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := store.Load(sess.ID); err != nil {
					t.Errorf("Load during write: %v", err)
					return
				}
			}
		}()
	}
	writers.Wait()
	readers.Wait()

	recording, err := session.ReadRecording(filepath.Join(dir, sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v (a reader truncated a live log)", err)
	}
	// Create writes one session/title event, then the loop appends `events` more.
	if len(recording.Events) != events+1 {
		t.Fatalf("events = %d, want %d", len(recording.Events), events+1)
	}
	for index, event := range recording.Events {
		if event.Seq != int64(index) {
			t.Fatalf("event %d has seq %d, log is no longer contiguous", index, event.Seq)
		}
	}
}

// TestConcurrentSessionsUnderRace is the P1 acceptance criterion: 100 sessions
// written, read and listed at the same time must stay isolated. Run with -race in
// CI to make it meaningful.
func TestConcurrentSessionsUnderRace(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })

	const sessionCount = 100
	const turns = 3

	var writers sync.WaitGroup
	errors := make(chan error, sessionCount)
	for index := 0; index < sessionCount; index++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			sess, err := store.Create(fmt.Sprintf("session %d", index))
			if err != nil {
				errors <- err
				return
			}
			for turn := 1; turn <= turns; turn++ {
				events := []struct {
					eventType string
					data      any
				}{
					{"turn/start", session.TurnMarker{Turn: turn}},
					{"user/message", session.SystemMessageData{Turn: turn, Message: session.NewUserMessage("hi")}},
					{"step/start", session.StepMarker{Turn: turn, Step: 1}},
					{"assistant/message", session.AssistantMessageData{Turn: turn, Step: 1, Message: session.NewAssistantMessage(llm.Message{Role: llm.RoleAssistant, Content: "ok"}), Stream: []any{}}},
					{"step/end", session.StepMarker{Turn: turn, Step: 1}},
					{"turn/end", session.TurnEndData{Turn: turn, Reason: session.TurnEndReason{Kind: session.TurnEndCompleted}}},
				}
				for _, event := range events {
					var surfaceOp *session.SurfaceOp
					if event.eventType == "user/message" || event.eventType == "assistant/message" {
						op := session.Append
						surfaceOp = &op
					}
					if err := store.AppendEvent(sess, event.eventType, event.data, surfaceOp, nil); err != nil {
						errors <- err
						return
					}
				}
			}
			loaded, err := store.Load(sess.ID)
			if err != nil {
				errors <- err
				return
			}
			if len(loaded.Messages) != turns*2 {
				errors <- fmt.Errorf("session %d: %d messages, want %d", index, len(loaded.Messages), turns*2)
				return
			}
			if _, err := store.List(); err != nil {
				errors <- err
			}
		}(index)
	}

	var lister sync.WaitGroup
	listed := make(chan struct{})
	lister.Add(1)
	go func() {
		defer lister.Done()
		for {
			select {
			case <-listed:
				return
			default:
			}
			if _, err := store.List(); err != nil {
				errors <- err
				return
			}
		}
	}()

	writers.Wait()
	close(listed)
	lister.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent session work: %v", err)
		}
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != sessionCount {
		t.Fatalf("sessions = %d, want %d", len(summaries), sessionCount)
	}
	_ = turns
}
