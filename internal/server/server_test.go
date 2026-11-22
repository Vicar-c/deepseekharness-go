package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	t.Cleanup(func() { _ = store.Close() })
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
	t.Cleanup(func() { _ = store.Close() })
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

// TestChatRepairsAnInterruptedSession pins the wiring: a chat request continues
// the session through Resume, so a turn a crash left open is closed before the
// new turn starts instead of being sent to the model with a dangling tool call.
func TestChatRepairsAnInterruptedSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	id := "20261122-444444-eeeeeeee"
	log := `{"type":"session","version":4,"id":"` + id + `","createdAt":1763000000000,"isSeeded":false}
{"type":"session/title","data":{"title":"crashed"},"seq":0,"time":1763000000000}
{"type":"turn/start","data":{"turn":1},"seq":1,"time":1763000000000}
{"type":"user/message","data":{"turn":1,"step":0,"message":{"id":"u1","role":"user","content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}}},"surfaceOp":"append","seq":2,"time":1763000000000}
{"type":"step/start","data":{"turn":1,"step":1},"seq":3,"time":1763000000000}
{"type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"tool-call","id":"call-1","name":"read","arguments":"{}"}],"source":{"kind":"model"}},"stream":[]},"surfaceOp":"append","seq":4,"time":1763000000000}
{"type":"tool/call","data":{"turn":1,"step":1,"callId":"call-1","name":"read","arguments":"{}"},"seq":5,"time":1763000000000}
`
	path := filepath.Join(store.Dir(), id+".jsonl")
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	httpServer := newServerWithStore(t, store, func(ctx context.Context, sess *session.Session, input string) (string, error) {
		return "repaired", nil
	})
	status, payload := postJSON(t, httpServer.URL+"/api/chat", `{"sessionId":"`+id+`","input":"continue"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, payload)
	}

	recording, err := session.ReadRecording(path)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	var repaired bool
	for _, event := range recording.Events {
		if event.Type != "turn/end" {
			continue
		}
		var data session.TurnEndData
		if err := json.Unmarshal(event.Data, &data); err == nil && data.Reason.Kind == session.TurnEndInterrupted {
			repaired = true
		}
	}
	if !repaired {
		t.Fatalf("chat did not repair the interrupted turn: %+v", recording.Events)
	}
}
