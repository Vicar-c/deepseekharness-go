package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/server"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

func newStreamingServer(t *testing.T, runner server.ChatRunner, events *kernel.Events) *httptest.Server {
	t.Helper()
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	srv, err := server.New(server.Options{
		Config: config.Default(),
		Store:  store,
		Runner: runner,
		Events: events,
		Logger: testLogger(),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func postStream(t *testing.T, url, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url+"/api/chat", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestChatStreamsDeltasThenDone(t *testing.T) {
	events := kernel.NewEvents()
	runner := func(ctx context.Context, sess *session.Session, input string) (string, error) {
		events.Emit(agent.EventAgentStream, agent.StreamEvent{SessionID: sess.ID, Content: "Hel"})
		events.Emit(agent.EventAgentStream, agent.StreamEvent{SessionID: sess.ID, Content: "lo"})
		return "Hello", nil
	}
	srv := newStreamingServer(t, runner, events)

	response := postStream(t, srv.URL, `{"input":"hi"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if count := strings.Count(text, `"type":"delta"`); count != 2 {
		t.Fatalf("delta frames = %d, want 2; body:\n%s", count, text)
	}
	if !strings.Contains(text, `"text":"Hel"`) || !strings.Contains(text, `"text":"lo"`) {
		t.Fatalf("delta payloads missing:\n%s", text)
	}
	if !strings.Contains(text, `"type":"done"`) || !strings.Contains(text, `"reply":"Hello"`) {
		t.Fatalf("done frame missing:\n%s", text)
	}
}

func TestChatStreamIgnoresOtherSessions(t *testing.T) {
	events := kernel.NewEvents()
	runner := func(ctx context.Context, sess *session.Session, input string) (string, error) {
		events.Emit(agent.EventAgentStream, agent.StreamEvent{SessionID: "other-session", Content: "LEAK"})
		events.Emit(agent.EventAgentStream, agent.StreamEvent{SessionID: sess.ID, Content: "ok"})
		return "ok", nil
	}
	srv := newStreamingServer(t, runner, events)

	body, err := io.ReadAll(postStream(t, srv.URL, `{"input":"hi"}`).Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "LEAK") {
		t.Fatalf("delta from another session leaked:\n%s", body)
	}
	if !strings.Contains(string(body), `"text":"ok"`) {
		t.Fatalf("own delta missing:\n%s", body)
	}
}

func TestChatStreamReportsRunnerError(t *testing.T) {
	events := kernel.NewEvents()
	runner := func(ctx context.Context, sess *session.Session, input string) (string, error) {
		return "", errors.New("model exploded")
	}
	srv := newStreamingServer(t, runner, events)

	body, err := io.ReadAll(postStream(t, srv.URL, `{"input":"hi"}`).Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `"type":"error"`) || !strings.Contains(string(body), "model exploded") {
		t.Fatalf("error frame missing:\n%s", body)
	}
}

// TestChatWithoutAcceptStaysJSON pins the backwards compatible path: an ordinary
// client keeps getting one JSON object.
func TestChatWithoutAcceptStaysJSON(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	runner := func(ctx context.Context, sess *session.Session, input string) (string, error) {
		return "plain", nil
	}
	srv, err := server.New(server.Options{Config: config.Default(), Store: store, Runner: runner, Logger: testLogger()})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	response, err := http.Post(httpServer.URL+"/api/chat", "application/json", strings.NewReader(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), `"reply":"plain"`) {
		t.Fatalf("body = %s", body)
	}
}
