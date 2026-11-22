package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

type slowTool struct{ delay time.Duration }

func (t *slowTool) Definition() tool.Definition {
	return tool.Definition{Name: "slow", Description: "sleeps past the deadline", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (t *slowTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	select {
	case <-time.After(t.delay):
		return tool.Result{Content: "late"}, nil
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
}

type panicTool struct{}

func (*panicTool) Definition() tool.Definition {
	return tool.Definition{Name: "boom", Description: "panics", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (*panicTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	panic("boom")
}

// TestResultCodes pins the machine-readable codes (MIGRATION_MAP §6-8): the log
// and the UI must be able to tell a timeout from a policy rejection without
// parsing prose.
func TestResultCodes(t *testing.T) {
	registry := tool.NewRegistry()
	for _, candidate := range []tool.Tool{&slowTool{delay: 200 * time.Millisecond}, &panicTool{}} {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Timeout: 20 * time.Millisecond})
	pipeline.AddGuard(func(ctx context.Context, call tool.Call) error {
		if call.Name == "slow" && strings.Contains(string(call.Arguments), "veto") {
			return errors.New("policy says no")
		}
		return nil
	})

	cases := []struct {
		name string
		call tool.Call
		want string
	}{
		{"timeout", tool.Call{Name: "slow", Arguments: json.RawMessage(`{}`)}, tool.CodeTimeout},
		{"unknown tool", tool.Call{Name: "missing", Arguments: json.RawMessage(`{}`)}, tool.CodeNotFound},
		{"guard veto", tool.Call{Name: "slow", Arguments: json.RawMessage(`{"veto":true}`)}, tool.CodeBlocked},
		{"panic", tool.Call{Name: "boom", Arguments: json.RawMessage(`{}`)}, tool.CodePanicked},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := pipeline.Execute(context.Background(), testCase.call)
			if !result.IsError {
				t.Fatalf("result = %+v, want an error", result)
			}
			if result.Code != testCase.want {
				t.Fatalf("code = %q, want %q", result.Code, testCase.want)
			}
		})
	}
}

func TestBatchReportsAbortedCode(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&slowTool{delay: time.Millisecond}); err != nil {
		t.Fatalf("register: %v", err)
	}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := pipeline.ExecuteBatch(ctx, []tool.Call{{Name: "slow", Arguments: json.RawMessage(`{}`)}}, 2)
	if len(results) != 1 || results[0].Code != tool.CodeAborted {
		t.Fatalf("results = %+v, want ABORTED_BEFORE_DISPATCH", results)
	}
}
