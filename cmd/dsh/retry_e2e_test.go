package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

// fakeDeepSeek answers every chat completion, but rejects the first rateLimited
// requests with 429 and a rate-limit error body, which is what a busy provider
// looks like to the client.
func fakeDeepSeek(t *testing.T, rateLimited int32) (*httptest.Server, *int32) {
	t.Helper()
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		count := atomic.AddInt32(&requests, 1)
		if count <= rateLimited {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"Rate limit reached for requests","type":"rate_limit_error"}}`))
			return
		}
		if !payload.Stream {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered after retry"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`data: {"choices":[{"delta":{"content":"recovered "}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"after retry"},"finish_reason":"stop"}]}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, frame := range frames {
			_, _ = io.WriteString(writer, frame)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// TestChatRetriesAgainstARealEndpoint is the regression net for B-27: a real HTTP
// round trip through the CLI repeats a rate-limited call, waits between attempts,
// records both retries in the session log, and ends the turn as completed.
func TestChatRetriesAgainstARealEndpoint(t *testing.T) {
	home := isolate(t)
	sessionDir := filepath.Join(home, "sessions")
	t.Setenv("DSH_SESSION_DIR", sessionDir)
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-key")
	t.Setenv("DSH_MAX_RETRIES", "2")
	server, requests := fakeDeepSeek(t, 2)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"chat", "--prompt", "say hi", "--base-url", server.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if got := atomic.LoadInt32(requests); got != 3 {
		t.Fatalf("requests = %d, want 3 (two rate limits then success)", got)
	}
	if !strings.Contains(stdout.String(), "recovered after retry") {
		t.Fatalf("stdout = %q, want the streamed answer", stdout.String())
	}
	retryLines := strings.Count(stderr.String(), "[retry] attempt")
	if retryLines != 2 {
		t.Fatalf("retry lines = %d, want 2:\n%s", retryLines, stderr.String())
	}
	if !strings.Contains(stderr.String(), "(RATE_LIMIT)") {
		t.Fatalf("stderr = %q, want the failure code", stderr.String())
	}

	files, err := filepath.Glob(filepath.Join(sessionDir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session files = %v, err = %v", files, err)
	}
	recording, err := session.ReadRecording(files[0])
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	retries := 0
	started := 0
	var end session.TurnEndData
	var lastRetry struct {
		Code    string `json:"code"`
		DelayMs int64  `json:"delayMs"`
		Retry   int    `json:"retry"`
	}
	for _, event := range recording.Events {
		switch event.Type {
		case "llm/retry":
			retries++
			var payload struct {
				Retry   int   `json:"retry"`
				DelayMs int64 `json:"delayMs"`
				Failure struct {
					Code string `json:"code"`
				} `json:"failure"`
			}
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatalf("decode llm/retry: %v", err)
			}
			lastRetry.Retry = payload.Retry
			lastRetry.DelayMs = payload.DelayMs
			lastRetry.Code = payload.Failure.Code
		case "llm/retry-started":
			started++
		case "turn/end":
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if retries != 2 || started != 2 {
		t.Fatalf("retry events = %d/%d, want 2 llm/retry and 2 llm/retry-started", retries, started)
	}
	if lastRetry.Retry != 2 || lastRetry.Code != "RATE_LIMIT" {
		t.Fatalf("second retry = %+v", lastRetry)
	}
	// The delay doubles from the 500ms base, with the policy's ±10% jitter; the
	// range also proves the wait is scheduled rather than ignored.
	if lastRetry.DelayMs < 900 || lastRetry.DelayMs > 1100 {
		t.Fatalf("second retry delay = %dms, want about 1000ms", lastRetry.DelayMs)
	}
	if end.Reason.Kind != session.TurnEndCompleted {
		t.Fatalf("turn end = %+v, want completed", end.Reason)
	}
}
