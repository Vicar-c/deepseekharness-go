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

func recordingOf(t *testing.T, store *session.Store, id string) *session.Recording {
	t.Helper()
	recording, err := session.ReadRecording(filepath.Join(store.Dir(), id+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	return recording
}

// TestLoopRecordsRequestHeaderPerEnvelope covers B-07: the first request writes a
// self-contained header (call config plus tool schemas) and its context; a later
// step with the same envelope inherits it instead of duplicating the schemas.
func TestLoopRecordsRequestHeaderPerEnvelope(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{
		toolCallResponse(functionCall("call-1", "echo")),
		llm.TextResponse("done", "stop"),
	}}
	loop, sess, store := buildLoop(t, client, registry, kernel.NewEvents(), 4)
	if _, err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recording := recordingOf(t, store, sess.ID)
	var headers []session.RequestHeaderData
	var contexts []session.RequestContextData
	for _, event := range recording.Events {
		switch event.Type {
		case "request/header":
			var data session.RequestHeaderData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decode request/header: %v", err)
			}
			headers = append(headers, data)
		case "request/context":
			var data session.RequestContextData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decode request/context: %v", err)
			}
			contexts = append(contexts, data)
		}
	}
	if len(headers) != 1 {
		t.Fatalf("request/header events = %d, want one per envelope (two steps share it)", len(headers))
	}
	header := headers[0]
	if header.Reason != session.RequestHeaderInitial {
		t.Fatalf("header reason = %q, want initial", header.Reason)
	}
	if header.Header.Config.Model != "test-model" || header.Header.Config.Provider != "deepseek" {
		t.Fatalf("header config = %+v", header.Header.Config)
	}
	if len(header.Header.Tools) != 1 || header.Header.Tools[0].Name != "echo" {
		t.Fatalf("header tools = %+v, want the echo schema", header.Header.Tools)
	}
	if !json.Valid(header.Header.Tools[0].Parameters) {
		t.Fatalf("header tool parameters are not valid JSON: %s", header.Header.Tools[0].Parameters)
	}
	if len(contexts) != 1 || contexts[0].Model != "test-model" || contexts[0].Provider != "deepseek" {
		t.Fatalf("request/context = %+v", contexts)
	}
}

// TestLoopWritesResumeHeaderOnExistingHistory pins the resume reason: a loop that
// starts on a session which already has turns records a resume header.
func TestLoopWritesResumeHeaderOnExistingHistory(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{
		llm.TextResponse("first", "stop"),
		llm.TextResponse("second", "stop"),
	}}
	loop, sess, store := buildLoop(t, client, registry, kernel.NewEvents(), 2)
	if _, err := loop.Run(context.Background(), "one"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := loop.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	var reasons []string
	for _, event := range recordingOf(t, store, sess.ID).Events {
		if event.Type != "request/header" {
			continue
		}
		var data session.RequestHeaderData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode: %v", err)
		}
		reasons = append(reasons, data.Reason)
	}
	if len(reasons) != 1 || reasons[0] != session.RequestHeaderInitial {
		t.Fatalf("same loop wrote %v, want one initial header inherited by turn 2", reasons)
	}

	// A fresh loop over the same session is a resume.
	resumed, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	second, err := agent.New(agent.Options{
		Client:   &scriptedClient{responses: []llm.Response{llm.TextResponse("third", "stop")}},
		Pipeline: tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger()}),
		Session:  resumed,
		Store:    store,
		Logger:   testLogger(),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if _, err := second.Run(context.Background(), "three"); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	reasons = nil
	for _, event := range recordingOf(t, store, sess.ID).Events {
		if event.Type != "request/header" {
			continue
		}
		var data session.RequestHeaderData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode: %v", err)
		}
		reasons = append(reasons, data.Reason)
	}
	if len(reasons) != 2 || reasons[1] != session.RequestHeaderResume {
		t.Fatalf("headers = %v, want [initial resume]", reasons)
	}
}

// TestLoopRecordsSystemMessageOnce covers B-08: the rendered prompt is recorded
// once and is not appended again on later turns.
func TestLoopRecordsSystemMessageOnce(t *testing.T) {
	loop, sess, store := buildLoop(t, &scriptedClient{responses: []llm.Response{
		llm.TextResponse("first", "stop"),
		llm.TextResponse("second", "stop"),
	}}, tool.NewRegistry(), kernel.NewEvents(), 2)
	for _, input := range []string{"one", "two"} {
		if _, err := loop.Run(context.Background(), input); err != nil {
			t.Fatalf("Run(%s): %v", input, err)
		}
	}

	systemMessages := 0
	for _, event := range recordingOf(t, store, sess.ID).Events {
		if event.Type == "system/message" {
			systemMessages++
		}
	}
	if systemMessages != 1 {
		t.Fatalf("system/message events = %d, want 1", systemMessages)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Messages) == 0 || loaded.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("history does not start with the system prompt: %+v", loaded.Messages)
	}
	if loaded.Messages[0].Content != agent.DefaultSystemPrompt {
		t.Fatalf("system prompt was not recorded verbatim")
	}
}

// TestLoopFlushesAtStepBoundaries pins the durability checkpoint: one flush after
// every step and one after the turn, so a crash can only lose the turn in
// progress. A turn with one tool round trip has two steps, hence three flushes.
func TestLoopFlushesAtStepBoundaries(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{
		toolCallResponse(functionCall("call-1", "echo")),
		llm.TextResponse("done", "stop"),
	}}
	loop, sess, store := buildLoop(t, client, registry, kernel.NewEvents(), 4)
	if _, err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := store.SyncCount(sess.ID); got != 3 {
		t.Fatalf("syncs = %d, want 3 (two step ends plus the turn end)", got)
	}
}
