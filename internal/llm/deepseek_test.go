package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"deekseekharness-go/internal/llm"
)

func TestDeepSeekChatSendsAuthAndParsesToolCalls(t *testing.T) {
	var seenPath, seenAuth string
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "",
					"tool_calls": [{"id": "call-1", "type": "function", "function": {"name": "read", "arguments": "{\"path\":\"README.md\"}"}}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
		}`))
	}))
	defer server.Close()

	client, err := llm.NewDeepSeek(llm.DeepSeekOptions{BaseURL: server.URL, APIKey: "test-key", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("NewDeepSeek: %v", err)
	}
	response, err := client.Chat(context.Background(), llm.Request{
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hello")},
		Tools: []llm.ToolDefinition{{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:       "read",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if seenAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", seenAuth)
	}
	if seenPath != "/chat/completions" {
		t.Fatalf("path = %q", seenPath)
	}
	if seenBody["model"] != "deepseek-chat" {
		t.Fatalf("model = %v", seenBody["model"])
	}
	tools, ok := seenBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools payload = %v", seenBody["tools"])
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "read" || !strings.Contains(call.Function.Arguments, "README.md") {
		t.Fatalf("parsed call = %+v", call)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestDeepSeekChatReportsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	}))
	defer server.Close()

	client, err := llm.NewDeepSeek(llm.DeepSeekOptions{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), llm.Request{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeepSeekMissingAPIKey(t *testing.T) {
	client, err := llm.NewDeepSeek(llm.DeepSeekOptions{BaseURL: "https://example.invalid", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), llm.Request{})
	if !errors.Is(err, llm.ErrMissingAPIKey) {
		t.Fatalf("error = %v, want ErrMissingAPIKey", err)
	}
}

func TestDeepSeekRejectsInvalidOptions(t *testing.T) {
	if _, err := llm.NewDeepSeek(llm.DeepSeekOptions{APIKey: "k", Model: "m"}); err == nil {
		t.Fatal("expected error for empty base URL")
	}
	if _, err := llm.NewDeepSeek(llm.DeepSeekOptions{BaseURL: "https://example.invalid"}); err == nil {
		t.Fatal("expected error for empty model")
	}
}
