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

// TestLoopAppliesModelBounds proves the two optional bounds reach the request and
// the log: a completion budget must appear in the wire request, and an advertised
// context size must be recorded in request/context instead of being silently
// dropped.
func TestLoopAppliesModelBounds(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("bounds")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{llm.TextResponse("ok", "stop")}}
	pipeline := tool.NewPipeline(tool.NewRegistry(), tool.PipelineOptions{Logger: testLogger()})
	loop, err := agent.New(agent.Options{
		Client:        client,
		Pipeline:      pipeline,
		Session:       sess,
		Store:         store,
		Events:        kernel.NewEvents(),
		Logger:        testLogger(),
		MaxTokens:     512,
		ContextWindow: 65536,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if _, err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(client.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(client.requests))
	}
	if got := client.requests[0].MaxTokens; got != 512 {
		t.Fatalf("request max tokens = %d, want 512", got)
	}

	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	var context_ session.RequestContextData
	found := false
	for _, event := range recording.Events {
		if event.Type != "request/context" {
			continue
		}
		if err := json.Unmarshal(event.Data, &context_); err != nil {
			t.Fatalf("decode request/context: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatal("no request/context event")
	}
	if context_.ContextWindow != 65536 {
		t.Fatalf("context window = %d, want 65536", context_.ContextWindow)
	}
}

// TestLoopOmitsUnsetBounds keeps an unset budget out of the wire request and out
// of request/context, so a provider default is not mistaken for a configured one.
func TestLoopOmitsUnsetBounds(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("no bounds")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{llm.TextResponse("ok", "stop")}}
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
	if _, err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.requests[0].MaxTokens != 0 {
		t.Fatalf("request max tokens = %d, want the provider default (0)", client.requests[0].MaxTokens)
	}
	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	for _, event := range recording.Events {
		if event.Type != "request/context" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode request/context: %v", err)
		}
		if _, present := data["contextWindow"]; present {
			t.Fatalf("request/context recorded an unknown context window: %s", event.Data)
		}
	}
}
