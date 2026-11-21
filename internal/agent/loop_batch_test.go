package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// delayedTool is a concurrency-safe probe: it sleeps, records overlap and echoes
// its own name so the result order can be asserted.
type delayedTool struct {
	name  string
	delay time.Duration
	probe *overlapProbe
}

func (t *delayedTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name, Description: "delayed probe", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (t *delayedTool) ConcurrencySafe() bool { return true }

func (t *delayedTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	t.probe.enter()
	defer t.probe.leave()
	select {
	case <-time.After(t.delay):
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	return tool.Result{Content: "done " + t.name}, nil
}

type overlapProbe struct {
	mu         sync.Mutex
	running    int
	maxRunning int
}

func (p *overlapProbe) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running++
	if p.running > p.maxRunning {
		p.maxRunning = p.running
	}
}

func (p *overlapProbe) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running--
}

func (p *overlapProbe) peak() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxRunning
}

func toolCallResponse(calls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Message:      llm.Message{Role: llm.RoleAssistant, ToolCalls: calls},
		FinishReason: "tool_calls",
	}
}

func functionCall(id, name string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: `{}`}}
}

// TestLoopRunsToolBatchInModelOrder is the batch contract at the loop level: the
// faster call finishes first, but the log and the model history keep the order
// the model asked for, and every tool/call is recorded before any result.
func TestLoopRunsToolBatchInModelOrder(t *testing.T) {
	probe := &overlapProbe{}
	registry := tool.NewRegistry()
	for _, candidate := range []tool.Tool{
		&delayedTool{name: "slow", delay: 40 * time.Millisecond, probe: probe},
		&delayedTool{name: "fast", delay: time.Millisecond, probe: probe},
	} {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register %s: %v", candidate.Definition().Name, err)
		}
	}
	client := &scriptedClient{responses: []llm.Response{
		toolCallResponse(functionCall("call-slow", "slow"), functionCall("call-fast", "fast")),
		llm.TextResponse("done", "stop"),
	}}
	loop, sess, store := buildLoop(t, client, registry, kernel.NewEvents(), 4)
	if _, err := loop.Run(context.Background(), "run both"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if peak := probe.peak(); peak != 2 {
		t.Fatalf("peak concurrency = %d, want 2 (safe calls must overlap)", peak)
	}

	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	var callIDs []string
	var resultTexts []string
	firstResult := -1
	lastCall := -1
	for index, event := range recording.Events {
		switch event.Type {
		case "tool/call":
			var data session.ToolCallData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decode tool/call: %v", err)
			}
			callIDs = append(callIDs, data.CallID)
			lastCall = index
		case "tool/result":
			var data session.ToolResultData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decode tool/result: %v", err)
			}
			if len(data.Message.Content) != 1 {
				t.Fatalf("tool/result content = %+v", data.Message.Content)
			}
			resultTexts = append(resultTexts, data.Message.Content[0].Text)
			if firstResult == -1 {
				firstResult = index
			}
		}
	}
	if strings.Join(callIDs, ",") != "call-slow,call-fast" {
		t.Fatalf("tool/call order = %v, want model order", callIDs)
	}
	if strings.Join(resultTexts, ",") != "done slow,done fast" {
		t.Fatalf("tool/result order = %v, want model order despite completion order", resultTexts)
	}
	if firstResult < lastCall {
		t.Fatalf("a tool/result was recorded before the last tool/call (first result %d, last call %d)", firstResult, lastCall)
	}

	// The projected model history keeps the same order, so a resumed session
	// replays the batch exactly as it happened.
	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var toolTexts []string
	for _, message := range reloaded.Messages {
		if message.Role == llm.RoleTool {
			toolTexts = append(toolTexts, message.Content)
		}
	}
	if strings.Join(toolTexts, ",") != "done slow,done fast" {
		t.Fatalf("projected tool messages = %v", toolTexts)
	}
}
