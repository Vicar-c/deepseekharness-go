package session

import (
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

func TestProjectUpstreamFixture(t *testing.T) {
	messages, err := decodeFixture(t).Project()
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(messages) != 16 {
		t.Fatalf("messages = %d, want 16 surface events", len(messages))
	}

	first := messages[0]
	if first.Role != llm.RoleUser {
		t.Fatalf("first role = %q, want user", first.Role)
	}
	if !strings.Contains(first.Content, "Show how multiple Turns") {
		t.Fatalf("first content = %q", first.Content)
	}

	assistant := messages[1]
	if assistant.Role != llm.RoleAssistant {
		t.Fatalf("second role = %q, want assistant", assistant.Role)
	}
	if len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant tool calls = %d, want 2", len(assistant.ToolCalls))
	}
	call := assistant.ToolCalls[0]
	if call.ID != "preview-spacing-read" || call.Function.Name != "read" {
		t.Fatalf("first tool call = %+v", call)
	}
	if !strings.Contains(call.Function.Arguments, "PREVIEW.md") {
		t.Fatalf("tool call arguments = %q", call.Function.Arguments)
	}
	if assistant.Content != "" {
		t.Fatalf("assistant content = %q, want empty: the sample only has reasoning blocks", assistant.Content)
	}

	result := messages[2]
	if result.Role != llm.RoleTool || result.ToolCallID != "preview-spacing-read" {
		t.Fatalf("tool message = %+v", result)
	}
	if result.Name != "read" {
		t.Fatalf("tool message name = %q, want read (from tool/call)", result.Name)
	}
	if !strings.Contains(result.Content, "<path>PREVIEW.md</path>") {
		t.Fatalf("tool content = %.120q", result.Content)
	}

	// Log-only events must never become messages.
	for _, message := range messages {
		switch message.Role {
		case llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleSystem:
		default:
			t.Fatalf("unexpected role %q", message.Role)
		}
	}
}

func TestProjectAppliesReplaceSurfaceOp(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"user/message","data":{"id":"u1","role":"user","content":[{"type":"text","text":"first"}]},"surfaceOp":"append","seq":0,"time":1}` + "\n" +
		`{"type":"assistant/message","data":{"message":{"id":"a1","role":"assistant","content":[{"type":"text","text":"second"}]}},"surfaceOp":"append","seq":1,"time":2}` + "\n" +
		`{"type":"user/message","data":{"id":"u2","role":"user","content":[{"type":"text","text":"third"}]},"surfaceOp":"append","seq":2,"time":3}` + "\n" +
		`{"type":"system/message","data":{"message":{"id":"s1","role":"system","content":[{"type":"text","text":"summary"}]}},"surfaceOp":{"op":"replace","startSeq":0,"endSeq":1},"seq":3,"time":4}` + "\n"

	recording, err := Decode(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	messages, err := recording.Project()
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if messages[0].Role != llm.RoleSystem || messages[0].Content != "summary" {
		t.Fatalf("replacement message = %+v", messages[0])
	}
	if messages[1].Role != llm.RoleUser || messages[1].Content != "third" {
		t.Fatalf("surviving message = %+v", messages[1])
	}
}

func TestProjectRejectsOutOfRangeReplace(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"user/message","data":{"id":"u1","role":"user","content":[{"type":"text","text":"only"}]},"surfaceOp":"append","seq":0,"time":1}` + "\n" +
		`{"type":"system/message","data":{"message":{"id":"s1","role":"system","content":[{"type":"text","text":"summary"}]}},"surfaceOp":{"op":"replace","startSeq":5,"endSeq":6},"seq":1,"time":2}` + "\n"
	recording, err := Decode(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, err := recording.Project(); err == nil {
		t.Fatal("expected an out-of-range replace to fail")
	}
}

func TestProjectMapsDeveloperMessageToSystem(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"developer/message","data":{"message":{"id":"d1","role":"developer","content":[{"type":"text","text":"be brief"}]}},"surfaceOp":"append","seq":0,"time":1}` + "\n"
	recording, err := Decode(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	messages, err := recording.Project()
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != llm.RoleSystem || messages[0].Content != "be brief" {
		t.Fatalf("developer message projection = %+v", messages)
	}
}

func TestProjectIgnoresLogOnlyEvents(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"turn/start","data":{"turn":1},"seq":0,"time":1}` + "\n" +
		`{"type":"tool/call","data":{"turn":1,"step":1,"callId":"c1","name":"read","arguments":"{}"},"seq":1,"time":2}` + "\n" +
		`{"type":"user/message","data":{"id":"u1","role":"user","content":[{"type":"text","text":"hi"}]},"surfaceOp":"append","seq":2,"time":3}` + "\n" +
		`{"type":"step/end","data":{"turn":1,"step":1},"seq":3,"time":4}` + "\n"
	recording, err := Decode(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	messages, err := recording.Project()
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", messages)
	}
}
