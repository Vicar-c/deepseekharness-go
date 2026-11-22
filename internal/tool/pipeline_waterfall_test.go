package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// namedTool is a tool that records how often it ran.
type namedTool struct {
	name string
	runs int
	err  error
}

func (t *namedTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name, Description: "test tool", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (t *namedTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	t.runs++
	if t.err != nil {
		return tool.Result{}, t.err
	}
	return tool.Result{Content: "ran " + t.name}, nil
}

func newWaterfallPipeline(t *testing.T) (*tool.Pipeline, *namedTool, *kernel.Events) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := tool.NewRegistry()
	subject := &namedTool{name: "probe"}
	if err := registry.Register(subject); err != nil {
		t.Fatalf("Register: %v", err)
	}
	events := kernel.NewEvents()
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: logger, Events: events})
	return pipeline, subject, events
}

// TestPreExecuteName pins the extension point name: it is the upstream one, so a
// later policy or approval layer plugs into the same seam.
func TestPreExecuteName(t *testing.T) {
	if tool.EventToolPreExecute != "tools/pre-execute" {
		t.Fatalf("EventToolPreExecute = %q, want tools/pre-execute", tool.EventToolPreExecute)
	}
}

// TestGuardAllowsAndBlocks keeps the guard contract: a guard sees the call, a
// blocking guard prevents the tool from running, and a later guard never runs.
func TestGuardAllowsAndBlocks(t *testing.T) {
	pipeline, subject, _ := newWaterfallPipeline(t)
	var seen []string
	pipeline.AddGuard(func(_ context.Context, call tool.Call) error {
		seen = append(seen, "first:"+call.Name)
		return nil
	})
	pipeline.AddGuard(func(_ context.Context, _ tool.Call) error {
		seen = append(seen, "second")
		return errors.New("not allowed")
	})
	pipeline.AddGuard(func(_ context.Context, _ tool.Call) error {
		seen = append(seen, "third")
		return nil
	})

	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if !result.IsError || result.Code != tool.CodeBlocked {
		t.Fatalf("result = %+v, want a blocked error", result)
	}
	if !strings.Contains(result.Content, "not allowed") {
		t.Fatalf("content = %q, want the guard reason", result.Content)
	}
	if subject.runs != 0 {
		t.Fatalf("tool ran %d times, want 0", subject.runs)
	}
	if strings.Join(seen, ",") != "first:probe,second" {
		t.Fatalf("guards ran %v, want the first two only", seen)
	}
}

// TestPreExecuteLayerVetoesWithAResult lets a layer decide the result itself,
// which is how an approval or sandbox layer will deny a call.
func TestPreExecuteLayerVetoesWithAResult(t *testing.T) {
	pipeline, subject, _ := newWaterfallPipeline(t)
	pipeline.AddPreExecute(func(_ context.Context, payload any, _ func(context.Context) (any, error)) (any, error) {
		call := payload.(tool.Call)
		return tool.Result{IsError: true, Code: tool.CodeBlocked, Content: "denied " + call.Name}, nil
	})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if result.Code != tool.CodeBlocked || result.Content != "denied probe" {
		t.Fatalf("result = %+v, want the layer decision verbatim", result)
	}
	if subject.runs != 0 {
		t.Fatalf("tool ran %d times, want 0", subject.runs)
	}
}

// TestPreExecuteLayerWrapsTheCall proves the layer runs before and after the
// tool, which is what a policy layer that inspects the outcome needs.
func TestPreExecuteLayerWrapsTheCall(t *testing.T) {
	pipeline, subject, _ := newWaterfallPipeline(t)
	var trace []string
	pipeline.AddPreExecute(func(ctx context.Context, _ any, next func(context.Context) (any, error)) (any, error) {
		trace = append(trace, "before")
		value, err := next(ctx)
		decision, _ := value.(tool.Result)
		trace = append(trace, "after:"+decision.Content)
		return value, err
	})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "ran probe" {
		t.Fatalf("result = %+v, want the tool result", result)
	}
	if subject.runs != 1 {
		t.Fatalf("tool ran %d times, want 1", subject.runs)
	}
	if strings.Join(trace, ",") != "before,after:ran probe" {
		t.Fatalf("trace = %v", trace)
	}
}

// TestPreExecuteLayersRunInRegistrationOrder documents the outermost-first order
// a policy stack relies on.
func TestPreExecuteLayersRunInRegistrationOrder(t *testing.T) {
	pipeline, _, _ := newWaterfallPipeline(t)
	var order []string
	for _, name := range []string{"outer", "inner"} {
		label := name
		pipeline.AddPreExecute(func(ctx context.Context, _ any, next func(context.Context) (any, error)) (any, error) {
			order = append(order, label)
			return next(ctx)
		})
	}
	if result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)}); result.IsError {
		t.Fatalf("result = %+v", result)
	}
	if strings.Join(order, ",") != "outer,inner" {
		t.Fatalf("order = %v, want outer,inner", order)
	}
}

// TestPreExecuteUnsubscribe removes a layer again.
func TestPreExecuteUnsubscribe(t *testing.T) {
	pipeline, _, _ := newWaterfallPipeline(t)
	calls := 0
	unsubscribe := pipeline.AddPreExecute(func(ctx context.Context, _ any, next func(context.Context) (any, error)) (any, error) {
		calls++
		return next(ctx)
	})
	pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	unsubscribe()
	unsubscribe()
	pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if calls != 1 {
		t.Fatalf("layer ran %d times, want 1", calls)
	}
}

// TestPreExecuteLayerPanicIsReportedAsAPanic keeps a buggy layer from being
// mistaken for a policy denial.
func TestPreExecuteLayerPanicIsReportedAsAPanic(t *testing.T) {
	pipeline, subject, _ := newWaterfallPipeline(t)
	pipeline.AddPreExecute(func(context.Context, any, func(context.Context) (any, error)) (any, error) {
		panic("layer bug")
	})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if result.Code != tool.CodePanicked {
		t.Fatalf("code = %q, want %q", result.Code, tool.CodePanicked)
	}
	if !strings.Contains(result.Content, "layer bug") {
		t.Fatalf("content = %q", result.Content)
	}
	if subject.runs != 0 {
		t.Fatalf("tool ran %d times, want 0", subject.runs)
	}
}

// TestPreExecuteLayerErrorBlocks covers a layer that fails instead of deciding.
func TestPreExecuteLayerErrorBlocks(t *testing.T) {
	pipeline, subject, _ := newWaterfallPipeline(t)
	pipeline.AddPreExecute(func(context.Context, any, func(context.Context) (any, error)) (any, error) {
		return nil, errors.New("policy unavailable")
	})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if result.Code != tool.CodeBlocked || !strings.Contains(result.Content, "policy unavailable") {
		t.Fatalf("result = %+v", result)
	}
	if subject.runs != 0 {
		t.Fatalf("tool ran %d times, want 0", subject.runs)
	}
}

// TestPreExecuteLayerMustReturnAResult rejects a layer that returns the wrong
// type instead of silently executing the tool.
func TestPreExecuteLayerMustReturnAResult(t *testing.T) {
	pipeline, subject, _ := newWaterfallPipeline(t)
	pipeline.AddPreExecute(func(context.Context, any, func(context.Context) (any, error)) (any, error) {
		return "not a result", nil
	})
	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if result.Code != tool.CodeBlocked || !strings.Contains(result.Content, "instead of a tool result") {
		t.Fatalf("result = %+v", result)
	}
	if subject.runs != 0 {
		t.Fatalf("tool ran %d times, want 0", subject.runs)
	}
}

// TestPreExecuteSurvivesAClosedBus keeps tool execution working after the app
// shuts the event bus down.
func TestPreExecuteSurvivesAClosedBus(t *testing.T) {
	pipeline, subject, events := newWaterfallPipeline(t)
	pipeline.AddPreExecute(func(context.Context, any, func(context.Context) (any, error)) (any, error) {
		return tool.Result{IsError: true, Code: tool.CodeBlocked, Content: "blocked by a layer"}, nil
	})
	events.Close()
	result := pipeline.Execute(context.Background(), tool.Call{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "ran probe" {
		t.Fatalf("result = %+v, want the tool to run after Close", result)
	}
	if subject.runs != 1 {
		t.Fatalf("tool ran %d times, want 1", subject.runs)
	}
}
