package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"deekseekharness-go/internal/kernel"
	"deekseekharness-go/internal/tool"
)

type fakeTool struct {
	name    string
	content string
	run     func(context.Context) (tool.Result, error)
}

func (t *fakeTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        t.name,
		Description: "fake tool for tests",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

func (t *fakeTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	if t.run != nil {
		return t.run(ctx)
	}
	return tool.Result{Content: t.content}, nil
}

func newPipeline(t *testing.T, options tool.PipelineOptions) *tool.Pipeline {
	t.Helper()
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return tool.NewPipeline(tool.NewRegistry(), options)
}

func TestPipelineExecutesRegisteredTool(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&fakeTool{name: "echo", content: "hello"}); err != nil {
		t.Fatal(err)
	}
	events := kernel.NewEvents()
	var executed []tool.ExecutionEvent
	events.On(tool.EventToolExecuted, func(event kernel.Event) {
		payload, ok := event.Payload.(tool.ExecutionEvent)
		if ok {
			executed = append(executed, payload)
		}
	})
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Events: events, Logger: testLogger(t)})
	result := pipeline.Execute(context.Background(), tool.Call{ID: "1", Name: "echo", Arguments: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "hello" {
		t.Fatalf("result = %+v", result)
	}
	if len(executed) != 1 || executed[0].Call.Name != "echo" {
		t.Fatalf("executed events = %+v", executed)
	}
}

func TestPipelineUnknownToolIsAnErrorResult(t *testing.T) {
	pipeline := newPipeline(t, tool.PipelineOptions{})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "missing"})
	if !result.IsError || !strings.Contains(result.Content, "not registered") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPipelineGuardVetoesCall(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&fakeTool{name: "write", content: "done"}); err != nil {
		t.Fatal(err)
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger(t)})
	pipeline.AddGuard(func(context.Context, tool.Call) error { return errors.New("approval denied") })
	result := pipeline.Execute(context.Background(), tool.Call{Name: "write"})
	if !result.IsError || !strings.Contains(result.Content, "approval denied") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPipelineTimeout(t *testing.T) {
	registry := tool.NewRegistry()
	slow := &fakeTool{name: "slow", run: func(ctx context.Context) (tool.Result, error) {
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	}}
	if err := registry.Register(slow); err != nil {
		t.Fatal(err)
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger(t), Timeout: 10 * time.Millisecond})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "slow"})
	if !result.IsError || !strings.Contains(result.Content, "timed out") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPipelineTruncatesLargeOutput(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&fakeTool{name: "big", content: strings.Repeat("x", 100)}); err != nil {
		t.Fatal(err)
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger(t), MaxOutputBytes: 10})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "big"})
	if !strings.Contains(result.Content, "truncated") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestPipelineRecoversPanics(t *testing.T) {
	registry := tool.NewRegistry()
	exploding := &fakeTool{name: "panic", run: func(context.Context) (tool.Result, error) { panic("kaboom") }}
	if err := registry.Register(exploding); err != nil {
		t.Fatal(err)
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger(t)})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "panic"})
	if !result.IsError || !strings.Contains(result.Content, "panicked") {
		t.Fatalf("result = %+v", result)
	}
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
