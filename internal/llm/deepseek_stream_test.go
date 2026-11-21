package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

var _ llm.StreamingClient = (*llm.DeepSeek)(nil)

func streamServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func newStreamClient(t *testing.T, url string) *llm.DeepSeek {
	t.Helper()
	client, err := llm.NewDeepSeek(llm.DeepSeekOptions{BaseURL: url, APIKey: "test-key", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("NewDeepSeek: %v", err)
	}
	return client
}

func TestChatStreamDeliversDeltasAndAssemblesResponse(t *testing.T) {
	var seenBody map[string]any
	var seenAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAccept = r.Header.Get("Accept")
		_ = json.NewDecoder(r.Body).Decode(&seenBody)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, frame := range []string{
			`{"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"lo"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var chunks []llm.Chunk
	response, err := newStreamClient(t, server.URL).ChatStream(context.Background(), llm.Request{
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")},
	}, func(chunk llm.Chunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if seenBody["stream"] != true {
		t.Fatalf("request stream = %v, want true", seenBody["stream"])
	}
	if !strings.Contains(seenAccept, "text/event-stream") {
		t.Fatalf("Accept = %q", seenAccept)
	}

	var text strings.Builder
	var finish string
	for _, chunk := range chunks {
		text.WriteString(chunk.Content)
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("streamed text = %q", text.String())
	}
	if finish != "stop" {
		t.Fatalf("finish reason chunk = %q", finish)
	}
	if response.Message.Content != "Hello" {
		t.Fatalf("assembled content = %q", response.Message.Content)
	}
	if response.FinishReason != "stop" {
		t.Fatalf("assembled finish = %q", response.FinishReason)
	}
}

func TestChatStreamAssemblesToolCallDeltas(t *testing.T) {
	server := streamServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read","arguments":"{\"pa"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	defer server.Close()

	var sawToolDelta bool
	response, err := newStreamClient(t, server.URL).ChatStream(context.Background(), llm.Request{
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "read a")},
	}, func(chunk llm.Chunk) error {
		if chunk.ToolCallID != "" || chunk.ToolArgs != "" {
			sawToolDelta = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if !sawToolDelta {
		t.Fatal("no tool-call delta reached the callback")
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.Message.ToolCalls))
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "read" {
		t.Fatalf("tool call = %+v", call)
	}
	if call.Function.Arguments != `{"path":"a"}` {
		t.Fatalf("arguments = %q", call.Function.Arguments)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", response.FinishReason)
	}
}

func TestChatStreamReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := newStreamClient(t, server.URL).ChatStream(context.Background(), llm.Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestChatStreamStopsWhenCallbackFails(t *testing.T) {
	server := streamServer(t, []string{
		`{"choices":[{"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"b"},"finish_reason":null}]}`,
	})
	defer server.Close()

	stop := errors.New("stop now")
	_, err := newStreamClient(t, server.URL).ChatStream(context.Background(), llm.Request{}, func(llm.Chunk) error {
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the callback error", err)
	}
}

func TestChatStreamRequiresAPIKey(t *testing.T) {
	client, err := llm.NewDeepSeek(llm.DeepSeekOptions{BaseURL: "http://127.0.0.1:1", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("NewDeepSeek: %v", err)
	}
	if _, err := client.ChatStream(context.Background(), llm.Request{}, nil); !errors.Is(err, llm.ErrMissingAPIKey) {
		t.Fatalf("err = %v, want ErrMissingAPIKey", err)
	}
}
