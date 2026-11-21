package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// TestLoopPersistsV4EventStream is the wiring contract: one agent turn must leave
// a valid v4 log behind, with the turn/step/tool markers around the surface
// events, and that log must project back to the same model messages.
func TestLoopPersistsV4EventStream(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{
		{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{{
					ID:       "call-1",
					Type:     "function",
					Function: llm.FunctionCall{Name: "echo", Arguments: `{"x":1}`},
				}},
			},
			FinishReason: "tool_calls",
		},
		llm.TextResponse("done", "stop"),
	}}
	loop, sess, store := buildLoop(t, client, registry, kernel.NewEvents(), 4)
	if _, err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	path := filepath.Join(store.Dir(), sess.ID+".jsonl")
	recording, err := session.ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	if recording.Header.Version != session.FormatVersion {
		t.Fatalf("header version = %d", recording.Header.Version)
	}

	want := []string{
		"session/title",
		"turn/start", "user/message",
		"step/start", "assistant/message", "tool/call", "tool/result", "step/end",
		"step/start", "assistant/message", "step/end",
		"turn/end",
	}
	got := make([]string, len(recording.Events))
	surface := 0
	for index, event := range recording.Events {
		got[index] = event.Type
		if event.SurfaceOp != nil {
			surface++
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event sequence:\n got %v\nwant %v", got, want)
	}
	if surface != 4 {
		t.Fatalf("surface events = %d, want 4", surface)
	}

	messages, err := recording.Project()
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("projected messages = %d, want 4", len(messages))
	}
	if messages[0].Role != llm.RoleUser || messages[0].Content != "hello" {
		t.Fatalf("first message = %+v", messages[0])
	}
	if messages[1].Role != llm.RoleAssistant || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", messages[1])
	}
	if messages[2].Role != llm.RoleTool || messages[2].ToolCallID != "call-1" {
		t.Fatalf("tool message = %+v", messages[2])
	}
	if messages[3].Role != llm.RoleAssistant || messages[3].Content != "done" {
		t.Fatalf("final message = %+v", messages[3])
	}

	var call session.ToolCallData
	for _, event := range recording.Events {
		if event.Type == "tool/call" {
			if err := json.Unmarshal(event.Data, &call); err != nil {
				t.Fatalf("decode tool/call: %v", err)
			}
		}
	}
	if call.Name != "echo" || call.Arguments != `{"x":1}` || call.CallID != "call-1" {
		t.Fatalf("tool/call payload = %+v", call)
	}

	var end session.TurnEndData
	for _, event := range recording.Events {
		if event.Type == "turn/end" {
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if end.Turn != 1 || end.Reason.Kind != "completed" {
		t.Fatalf("turn/end payload = %+v", end)
	}
}
