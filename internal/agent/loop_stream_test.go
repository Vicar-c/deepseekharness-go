package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// streamingClient implements llm.StreamingClient: it replays fixed text deltas
// and returns the assembled response, like the DeepSeek client does.
type streamingClient struct {
	scriptedClient
	deltas []string
}

func (c *streamingClient) ChatStream(_ context.Context, request llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	c.requests = append(c.requests, request)
	for _, delta := range c.deltas {
		if err := onChunk(llm.Chunk{Content: delta}); err != nil {
			return llm.Response{}, err
		}
	}
	if err := onChunk(llm.Chunk{FinishReason: "stop"}); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: strings.Join(c.deltas, "")},
		FinishReason: "stop",
	}, nil
}

func TestLoopStreamsDeltasAndPersistsOnce(t *testing.T) {
	client := &streamingClient{deltas: []string{"Hel", "lo", " world"}}
	events := kernel.NewEvents()
	var streamed string
	events.On(agent.EventAgentStream, func(event kernel.Event) {
		payload, ok := event.Payload.(agent.StreamEvent)
		if ok {
			streamed += payload.Content
		}
	})

	loop, sess, store := buildLoop(t, client, tool.NewRegistry(), events, 2)
	reply, err := loop.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply != "Hello world" {
		t.Fatalf("reply = %q", reply)
	}
	if streamed != "Hello world" {
		t.Fatalf("streamed text = %q, want the deltas in order", streamed)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("persisted messages = %d, want user + assistant only", len(loaded.Messages))
	}
	if loaded.Messages[1].Role != llm.RoleAssistant || loaded.Messages[1].Content != "Hello world" {
		t.Fatalf("persisted assistant = %+v", loaded.Messages[1])
	}
}
