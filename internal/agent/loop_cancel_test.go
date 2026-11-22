package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// partialThenCancelClient streams one delta and then cancels the turn, which is
// what an interrupt during an answer looks like to the loop.
type partialThenCancelClient struct {
	cancel context.CancelFunc
	text   string
}

func (c *partialThenCancelClient) Model() string { return "test-model" }

func (c *partialThenCancelClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, context.Canceled
}

func (c *partialThenCancelClient) ChatStream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	if err := onChunk(llm.Chunk{Content: c.text}); err != nil {
		return llm.Response{}, err
	}
	c.cancel()
	return llm.Response{}, context.Canceled
}

// partialThenFailClient streams one delta and then fails without cancelling.
type partialThenFailClient struct {
	text string
	err  error
}

func (c *partialThenFailClient) Model() string { return "test-model" }

func (c *partialThenFailClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, c.err
}

func (c *partialThenFailClient) ChatStream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	if err := onChunk(llm.Chunk{Content: c.text}); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{}, c.err
}

func buildCancelLoop(t *testing.T, client llm.Client) (*agent.Loop, *session.Session, *session.Store) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("cancel")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	pipeline := tool.NewPipeline(tool.NewRegistry(), tool.PipelineOptions{Logger: testLogger()})
	loop, err := agent.New(agent.Options{
		Client:   client,
		Pipeline: pipeline,
		Session:  sess,
		Store:    store,
		Events:   kernel.NewEvents(),
		Logger:   testLogger(),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop, sess, store
}

// TestCancelledTurnKeepsTheStreamedPrefix is B-34: the text the user saw before
// the interrupt must be in the log, in the right place, and in the history the
// next request is built from.
func TestCancelledTurnKeepsTheStreamedPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &partialThenCancelClient{cancel: cancel, text: "half an answer"}
	loop, sess, store := buildCancelLoop(t, client)

	if _, err := loop.Run(ctx, "go"); err == nil {
		t.Fatal("expected the cancelled turn to fail")
	}

	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	var assistantText string
	var assistantCount int
	var assistantIndex, endIndex = -1, -1
	var end session.TurnEndData
	for index, event := range recording.Events {
		switch event.Type {
		case "assistant/message":
			assistantCount++
			assistantIndex = index
			var data session.AssistantMessageData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decode assistant/message: %v", err)
			}
			for _, block := range data.Message.Content {
				if block.Type == "text" {
					assistantText = block.Text
				}
			}
		case "turn/end":
			endIndex = index
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if assistantCount != 1 {
		t.Fatalf("assistant/message events = %d, want the partial answer recorded once", assistantCount)
	}
	if assistantText != "half an answer" {
		t.Fatalf("recorded partial text = %q, want the streamed prefix", assistantText)
	}
	if end.Reason.Kind != session.TurnEndAborted {
		t.Fatalf("turn end = %+v, want aborted", end.Reason)
	}
	if assistantIndex < 0 || endIndex < 0 || assistantIndex > endIndex {
		t.Fatalf("event order: assistant at %d, turn/end at %d; want the partial answer first", assistantIndex, endIndex)
	}

	if len(sess.Messages) == 0 {
		t.Fatal("session history is empty")
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != llm.RoleAssistant || last.Content != "half an answer" {
		t.Fatalf("last history message = %+v, want the partial assistant answer", last)
	}
}

// TestFailedAttemptWithoutCancellationRecordsNoAnswer is the deliberate other
// half of B-34: a stream that breaks without an interrupt is reported as a
// failure and does not fabricate an assistant turn in the history.
func TestFailedAttemptWithoutCancellationRecordsNoAnswer(t *testing.T) {
	client := &partialThenFailClient{
		text: "doomed prefix",
		err:  &llm.Failure{Message: "transport broke", Code: llm.CodeTransport},
	}
	loop, sess, store := buildCancelLoop(t, client)

	if _, err := loop.Run(context.Background(), "go"); err == nil {
		t.Fatal("expected the failed turn to report an error")
	}

	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	var end session.TurnEndData
	for _, event := range recording.Events {
		if event.Type == "assistant/message" {
			t.Fatalf("a non-cancelled failure recorded an assistant message: %s", event.Data)
		}
		if event.Type == "turn/end" {
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if end.Reason.Kind != session.TurnEndError || end.Reason.Error == nil || end.Reason.Error.Code != llm.CodeTransport {
		t.Fatalf("turn end = %+v, want error TRANSPORT", end.Reason)
	}
	for _, message := range sess.Messages {
		if message.Role == llm.RoleAssistant {
			t.Fatalf("history contains an assistant message after a failed attempt: %+v", message)
		}
	}
}
