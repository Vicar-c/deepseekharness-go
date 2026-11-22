// Package server exposes the minimal HTTP surface of the Go rewrite: health,
// session management and one chat endpoint that drives the agent loop.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/version"
)

//go:embed index.html
var staticFiles embed.FS

// ChatRunner executes one chat turn inside a session. It is supplied by the
// composition root and is nil when the model is not configured.
type ChatRunner func(ctx context.Context, sess *session.Session, input string) (string, error)

// Options configures the HTTP server.
type Options struct {
	Config config.Config
	Store  *session.Store
	Runner ChatRunner
	// Events carries the transient streaming deltas of the agent loop. When it
	// is nil the streaming endpoint still works but only sends the final frame.
	Events *kernel.Events
	Logger *slog.Logger
}

// Server serves the Web UI and the JSON API.
type Server struct {
	opts Options
	mux  *http.ServeMux
}

// New validates dependencies and registers routes.
func New(options Options) (*Server, error) {
	if options.Store == nil {
		return nil, errors.New("server: session store is required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	server := &Server{opts: options, mux: http.NewServeMux()}
	server.routes()
	return server, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.recoverMiddleware(s.mux) }

// Run serves on address until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, address string) error {
	httpServer := &http.Server{
		Addr:              address,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	s.opts.Logger.Info("web server listening", "address", address)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("POST /api/chat", s.handleChat)
	s.mux.HandleFunc("GET /", s.handleIndex)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"version":     version.Version,
		"commit":      version.Commit,
		"model":       s.opts.Config.Model,
		"workspace":   s.opts.Config.Workspace,
		"chatEnabled": s.opts.Runner != nil,
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.opts.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if sessions == nil {
		sessions = []session.Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sess, err := s.opts.Store.Create(request.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess.Summary())
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.opts.Store.Load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        sess.ID,
		"title":     sess.Title,
		"createdAt": sess.CreatedAt,
		"updatedAt": sess.UpdatedAt,
		"messages":  sess.Messages,
	})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string `json:"sessionId"`
		Input     string `json:"input"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input := strings.TrimSpace(request.Input)
	if input == "" {
		writeError(w, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	if len(input) > 64*1024 {
		writeError(w, http.StatusBadRequest, errors.New("input exceeds 64 KiB"))
		return
	}
	if s.opts.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("model is not configured: set DEEPSEEK_API_KEY (or run `dsh config init`)"))
		return
	}

	var sess *session.Session
	var err error
	if id := strings.TrimSpace(request.SessionID); id != "" {
		// Resume, not Load: a chat continues the session and therefore owns the
		// write path, which also closes a turn a crash left open.
		sess, err = s.opts.Store.Resume(id)
	} else {
		sess, err = s.opts.Store.Create("New session")
	}
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	if wantsEventStream(r) {
		s.streamChat(w, r, sess, input)
		return
	}

	reply, err := s.opts.Runner(r.Context(), sess, input)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": sess.ID, "reply": reply})
}

// chatStreamFrame is one server-sent event of the streaming chat endpoint.
type chatStreamFrame struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Reply     string `json:"reply,omitempty"`
	Message   string `json:"message,omitempty"`
}

// wantsEventStream reports whether the client asked for server-sent events.
func wantsEventStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// streamChat drives one turn and forwards the transient agent deltas as SSE
// frames. A write failure cancels the turn: nobody is listening any more.
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, sess *session.Session, input string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported by this connection"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var writeErr error
	send := func(frame chatStreamFrame) {
		if writeErr != nil {
			return
		}
		data, err := json.Marshal(frame)
		if err != nil {
			writeErr = err
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			writeErr = err
			return
		}
		flusher.Flush()
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	unsubscribe := func() {}
	if s.opts.Events != nil {
		unsubscribe = s.opts.Events.On(agent.EventAgentStream, func(event kernel.Event) {
			payload, ok := event.Payload.(agent.StreamEvent)
			if !ok || payload.SessionID != sess.ID || payload.Content == "" {
				return
			}
			send(chatStreamFrame{Type: "delta", Text: payload.Content})
			if writeErr != nil {
				cancel()
			}
		})
	}
	defer unsubscribe()

	reply, err := s.opts.Runner(ctx, sess, input)
	if err != nil {
		send(chatStreamFrame{Type: "error", Message: err.Error()})
		return
	}
	send(chatStreamFrame{Type: "done", SessionID: sess.ID, Reply: reply})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("index.html")
	if err != nil {
		http.Error(w, "web UI asset is missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.opts.Logger.Error("panic serving request", "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, errors.New("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
