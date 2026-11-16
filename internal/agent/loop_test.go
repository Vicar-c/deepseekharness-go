package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"deekseekharness-go/internal/agent"
	"deekseekharness-go/internal/kernel"
	"deekseekharness-go/internal/llm"
	"deekseekharness-go/internal/session"
	"deekseekharness-go/internal/tool"
)

type scriptedClient struct {
	responses []llm.Response
	err       error
	requests  []llm.Request
}

func (c *scriptedClient) Model() string { return "test-model" }

func (c *scriptedClient) Chat(_ context.Context, request llm.Request) (llm.Response, error) {
	c.requests = append(c.requests, request)
	if c.err != nil {
		return llm.Response{}, c.err
	}
	if len(c.responses) == 0 {
		return llm.Response{}, errors.New("no scripted response left")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

type echoTool struct {
	received []string
}

func (t *echoTool) Definition() tool.Definition {
	return tool.Definition{Name: "echo", Description: "echo arguments", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (t *echoTool) Execute(_ context.Context, arguments json.RawMessage) (tool.Result, error) {
	t.received = append(t.received, string(arguments))
	return tool.Result{Content: "echo:" + string(arguments)}, nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func buildLoop(t *testing.T, client llm.Client, registry *tool.Registry, events *kernel.Events, maxIterations int) (*agent.Loop, *session.Session, *session.Store) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	sess, err := store.Create("agent test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger()})
	loop, err := agent.New(agent.Options{
		Client:        client,
		Pipeline:      pipeline,
		Session:       sess,
		Store:         store,
		Events:        events,
		Logger:        testLogger(),
		MaxIterations: maxIterations,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop, sess, store
}

func TestLoopExecutesToolsThenAnswers(t *testing.T) {
	registry := tool.NewRegistry()
	echo := &echoTool{}
	if err := registry.Register(echo); err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{responses: []llm.Response{
		{Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "",
			ToolCalls: []llm.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"value":"hi"}`},
			}},
		}, FinishReason: "tool_calls"},
		llm.TextResponse("all done", "stop"),
	}}
	events := kernel.NewEvents()
	var steps []agent.StepEvent
	events.On(agent.EventAgentStep, func(event kernel.Event) {
		payload, ok := event.Payload.(agent.StepEvent)
		if ok {
			steps = append(steps, payload)
		}
	})
	loop, sess, store := buildLoop(t, client, registry, events, 5)

	reply, err := loop.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply != "all done" {
		t.Fatalf("reply = %q", reply)
	}
	if len(echo.received) != 1 || echo.received[0] != `{"value":"hi"}` {
		t.Fatalf("tool arguments = %v", echo.received)
	}
	if len(steps) != 1 || steps[0].ToolCalls != 1 {
		t.Fatalf("steps = %+v", steps)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(client.requests))
	}
	first := client.requests[0]
	if len(first.Tools) != 1 || first.Tools[0].Function.Name != "echo" {
		t.Fatalf("first request tools = %+v", first.Tools)
	}
	if len(first.Messages) != 2 || first.Messages[0].Role != llm.RoleSystem || first.Messages[1].Role != llm.RoleUser {
		t.Fatalf("first request messages = %+v", first.Messages)
	}
	second := client.requests[1]
	if len(second.Messages) != 4 || second.Messages[3].Role != llm.RoleTool || second.Messages[3].ToolCallID != "call-1" {
		t.Fatalf("second request messages = %+v", second.Messages)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("load persisted session: %v", err)
	}
	if len(loaded.Messages) != 4 {
		t.Fatalf("persisted messages = %d, want 4", len(loaded.Messages))
	}
}

func TestLoopStopsAtMaxIterations(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatal(err)
	}
	toolCall := llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:       "call-x",
			Function: llm.FunctionCall{Name: "echo", Arguments: "{}"},
		}},
	}
	client := &scriptedClient{responses: []llm.Response{{Message: toolCall, FinishReason: "tool_calls"}, {Message: toolCall, FinishReason: "tool_calls"}}}
	loop, _, _ := buildLoop(t, client, registry, kernel.NewEvents(), 2)
	_, err := loop.Run(context.Background(), "loop forever")
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoopSurfacesModelErrors(t *testing.T) {
	registry := tool.NewRegistry()
	client := &scriptedClient{err: errors.New("network down")}
	loop, _, _ := buildLoop(t, client, registry, nil, 3)
	_, err := loop.Run(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoopRejectsEmptyInput(t *testing.T) {
	registry := tool.NewRegistry()
	client := &scriptedClient{}
	loop, _, _ := buildLoop(t, client, registry, nil, 3)
	if _, err := loop.Run(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty input")
	}
}
