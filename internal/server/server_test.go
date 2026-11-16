package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/server"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newServerWithStore(t *testing.T, store *session.Store, runner server.ChatRunner) *httptest.Server {
	t.Helper()
	srv, err := server.New(server.Options{
		Config: config.Default(),
		Store:  store,
		Runner: runner,
		Logger: testLogger(),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func newTestServer(t *testing.T, runner server.ChatRunner) (*httptest.Server, *session.Store) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	return newServerWithStore(t, store, runner), store
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return response.StatusCode, payload
}

func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return response.StatusCode, payload
}

func TestHealthz(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	status, payload := getJSON(t, ts.URL+"/healthz")
	if status != http.StatusOK || payload["status"] != "ok" {
		t.Fatalf("status=%d payload=%v", status, payload)
	}
	if payload["chatEnabled"] != false {
		t.Fatalf("chatEnabled = %v", payload["chatEnabled"])
	}
}

func TestSessionsLifecycle(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	status, created := postJSON(t, ts.URL+"/api/sessions", `{"title":"demo"}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d payload=%v", status, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created = %v", created)
	}

	status, list := getJSON(t, ts.URL+"/api/sessions")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	sessions, ok := list["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions = %v", list["sessions"])
	}

	status, loaded := getJSON(t, ts.URL+"/api/sessions/"+id)
	if status != http.StatusOK || loaded["title"] != "demo" {
		t.Fatalf("get status=%d payload=%v", status, loaded)
	}
}

func TestChatWithoutRunnerReturns503(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	status, payload := postJSON(t, ts.URL+"/api/chat", `{"input":"hello"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d payload=%v", status, payload)
	}
}

func TestChatWithRunnerPersistsMessages(t *testing.T) {
	store := session.NewStore(t.TempDir())
	runner := func(_ context.Context, sess *session.Session, input string) (string, error) {
		if err := store.Append(sess, llm.TextMessage(llm.RoleUser, input)); err != nil {
			return "", err
		}
		return "ok:" + input, nil
	}
	ts := newServerWithStore(t, store, runner)
	status, payload := postJSON(t, ts.URL+"/api/chat", `{"input":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d payload=%v", status, payload)
	}
	if payload["reply"] != "ok:hi" {
		t.Fatalf("reply = %v", payload["reply"])
	}
	id, _ := payload["sessionId"].(string)
	if id == "" {
		t.Fatalf("sessionId = %v", payload["sessionId"])
	}

	status, loaded := getJSON(t, ts.URL+"/api/sessions/"+id)
	if status != http.StatusOK {
		t.Fatalf("get session status = %d", status)
	}
	messages, _ := loaded["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
}

func TestIndexPageAndValidation(t *testing.T) {
	runner := func(context.Context, *session.Session, string) (string, error) { return "ok", nil }
	ts, _ := newTestServer(t, runner)

	response, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body := new(strings.Builder)
	_, _ = io.Copy(body, response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), "DeepSeek Harness") {
		t.Fatalf("index status=%d", response.StatusCode)
	}

	if status, _ := postJSON(t, ts.URL+"/api/chat", `{"input":"  "}`); status != http.StatusBadRequest {
		t.Fatalf("empty input status = %d", status)
	}
	if status, _ := postJSON(t, ts.URL+"/api/chat", `{"input":"x","sessionId":"../escape"}`); status != http.StatusNotFound {
		t.Fatalf("traversal session status = %d", status)
	}
}
