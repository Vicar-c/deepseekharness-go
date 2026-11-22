// Package session stores conversations as append-only JSONL files in the DSH v4
// session format: one header line followed by sequenced events. The wire format
// lives in format.go, the event-to-message projection in projection.go and the
// writer in persist_jsonl.go; this file is the store the CLI, the web server and
// the agent loop share.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

// Summary is the list projection of one session.
type Summary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Messages  int       `json:"messages"`
}

// Session is one conversation held in memory. Messages is the projection of the
// persisted event log; Turn is the last turn number seen.
type Session struct {
	ID        string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Turn      int
	Messages  []llm.Message
}

// Summary projects the session into its list view.
func (s *Session) Summary() Summary {
	return Summary{ID: s.ID, Title: s.Title, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Messages: len(s.Messages)}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ErrSessionOwned reports a second concurrent turn on a session that already has
// one running.
var ErrSessionOwned = errors.New("session: another turn is already running on this session")

// Store persists sessions as v4 JSONL files under one directory.
type Store struct {
	dir       string
	workspace string
	mu        sync.Mutex
	writers   map[string]*Writer
	turns     map[string]struct{}
	// repairMu serializes whole-session repairs: two callers resuming the same
	// crashed log must not both append the same closers.
	repairMu sync.Mutex
}

// NewStore creates a store rooted at dir. The directory is created lazily.
func NewStore(dir string) *Store {
	return &Store{dir: dir, writers: make(map[string]*Writer), turns: make(map[string]struct{})}
}

// Dir reports the session directory.
func (s *Store) Dir() string { return s.dir }

// SetWorkspace records the workspace path stamped into new session headers.
func (s *Store) SetWorkspace(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspace = path
}

// Close closes every writer held by the store.
func (s *Store) Close() error {
	s.mu.Lock()
	writers := make([]*Writer, 0, len(s.writers))
	for id, writer := range s.writers {
		writers = append(writers, writer)
		delete(s.writers, id)
	}
	s.mu.Unlock()
	var errs []error
	for _, writer := range writers {
		errs = append(errs, writer.Close())
	}
	return errors.Join(errs...)
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".jsonl") }

// Create writes a new v4 session file (header plus a session/title event) and
// returns the in-memory session.
func (s *Store) Create(title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled session"
	}
	if len(title) > 120 {
		title = title[:120]
	}
	now := time.Now().UTC()
	sess := &Session{ID: newID(now), Title: title, CreatedAt: now, UpdatedAt: now}

	s.mu.Lock()
	workspace := s.workspace
	s.mu.Unlock()
	zero := 0
	header := Header{
		ID:              sess.ID,
		CreatedAt:       now.UnixMilli(),
		Cwd:             workspace,
		IsSeeded:        false,
		DelegationDepth: &zero,
	}
	writer, err := CreateSessionFile(s.path(sess.ID), header)
	if err != nil {
		return nil, err
	}
	titleData := map[string]any{"title": title, "messageSeqs": []int64{}, "source": map[string]string{"kind": "user"}}
	if _, err := writer.Append("session/title", titleData); err != nil {
		_ = writer.Close()
		return nil, err
	}
	s.mu.Lock()
	s.writers[sess.ID] = writer
	s.mu.Unlock()
	return sess, nil
}

func (s *Store) writerFor(id string) (*Writer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if writer, ok := s.writers[id]; ok {
		return writer, nil
	}
	writer, err := OpenWriter(s.path(id))
	if err != nil {
		return nil, err
	}
	s.writers[id] = writer
	return writer, nil
}

// AppendEvent records one v4 event in the session log. The in-memory message
// list is not touched: callers that keep a Session in memory update it
// themselves. A nil surfaceOp means the event is log-only.
func (s *Store) AppendEvent(sess *Session, eventType string, data any, surfaceOp *SurfaceOp, sources []int64) error {
	if sess == nil {
		return errors.New("session: nil session")
	}
	if !idPattern.MatchString(sess.ID) {
		return fmt.Errorf("session: invalid id %q", sess.ID)
	}
	writer, err := s.writerFor(sess.ID)
	if err != nil {
		return err
	}
	event, err := writer.AppendEvent(eventType, data, surfaceOp, sources)
	if err != nil {
		return err
	}
	sess.UpdatedAt = event.At()
	return nil
}

// Append records one model message as the matching surface event and updates the
// in-memory session. Callers that model turns themselves should use AppendEvent.
func (s *Store) Append(sess *Session, message llm.Message) error {
	op := Append
	switch message.Role {
	case llm.RoleUser:
		if err := s.AppendEvent(sess, "user/message", NewUserMessage(message.Content), &op, nil); err != nil {
			return err
		}
	case llm.RoleAssistant:
		data := AssistantMessageData{Turn: sess.Turn, Step: 1, Message: NewAssistantMessage(message), Stream: []any{}}
		if err := s.AppendEvent(sess, "assistant/message", data, &op, nil); err != nil {
			return err
		}
	case llm.RoleTool:
		data := ToolResultData{Turn: sess.Turn, Step: 1, Message: NewToolResultMessage(message.ToolCallID, message.Content, false)}
		if err := s.AppendEvent(sess, "tool/result", data, &op, nil); err != nil {
			return err
		}
	case llm.RoleSystem:
		data := SystemMessageData{Turn: sess.Turn, Step: 1, Message: NewSystemMessage(message.Content)}
		if err := s.AppendEvent(sess, "system/message", data, &op, nil); err != nil {
			return err
		}
	default:
		return fmt.Errorf("session: unsupported role %q", message.Role)
	}
	sess.Messages = append(sess.Messages, message)
	return nil
}

// BeginTurn reserves the session for one running turn and returns an idempotent
// release function. A second concurrent turn on the same session is rejected
// instead of interleaving its turn/start and step events with the first one.
func (s *Store) BeginTurn(id string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = make(map[string]struct{})
	}
	if _, running := s.turns[id]; running {
		return nil, fmt.Errorf("%w: %s", ErrSessionOwned, id)
	}
	s.turns[id] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.turns, id)
		})
	}, nil
}

// Resume opens an existing session for writing and closes a turn that a crash
// left open, appending the recovery events (missing tool results, step/end and
// turn/end{interrupted}) as an ordinary batch. The returned messages already
// include them, so the next request sees a paired tool history.
//
// Load stays read-only: a viewer must never mutate a log.
func (s *Store) Resume(id string) (*Session, error) {
	// The whole read-repair-append sequence is serialized: a second concurrent
	// resume must observe the repaired log, not append a second copy.
	s.repairMu.Lock()
	defer s.repairMu.Unlock()

	// Resuming owns the write path, so it repairs the physical tail on disk
	// before computing any closers. Load itself never writes.
	if _, err := os.Stat(s.path(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session: %s not found", id)
		}
		return nil, fmt.Errorf("session: open %s: %w", s.path(id), err)
	}
	if _, err := RepairTail(s.path(id)); err != nil {
		return nil, err
	}
	sess, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	recording, err := ReadRecording(s.path(id))
	if err != nil {
		return nil, err
	}
	closers := InterruptedTurnClosers(recording)
	if len(closers) == 0 {
		return sess, nil
	}
	writer, err := s.writerFor(id)
	if err != nil {
		return nil, err
	}
	for _, closer := range closers {
		if _, err := writer.AppendEvent(closer.Type, closer.Data, closer.SurfaceOp, nil); err != nil {
			return nil, fmt.Errorf("session: repair %s: %w", id, err)
		}
	}
	if err := writer.Sync(); err != nil {
		return nil, fmt.Errorf("session: flush repair %s: %w", id, err)
	}
	return s.Load(id)
}

// Load reads one session file and projects it back into model messages. A crash
// torn trailing record is repaired before reading.
func (s *Store) Load(id string) (*Session, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}
	path := s.path(id)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session: %s not found", id)
		}
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	recording, err := readRecordingLenient(path)
	if err != nil {
		return nil, err
	}
	// The file name is the identity callers use; a mismatch means a renamed or
	// copied file, and silently trusting the header would make every later write
	// target a path the caller never asked for.
	if recording.Header.ID != id {
		return nil, fmt.Errorf("session: %s declares id %q, want %q", path, recording.Header.ID, id)
	}
	messages, err := recording.Project()
	if err != nil {
		return nil, err
	}
	title := titleOf(recording)
	if title == "" {
		title = "Untitled session"
	}
	return &Session{
		ID:        recording.Header.ID,
		Title:     title,
		CreatedAt: recording.Header.Created(),
		UpdatedAt: lastEventTime(recording),
		Turn:      lastTurn(recording),
		Messages:  messages,
	}, nil
}

// List returns every stored session summary, newest first.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: list directory: %w", err)
	}
	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		sess, err := s.Load(id)
		if err != nil {
			continue
		}
		summaries = append(summaries, sess.Summary())
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt) })
	return summaries, nil
}

func titleOf(recording *Recording) string {
	for _, event := range recording.Events {
		if event.Type != "session/title" {
			continue
		}
		var data struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(event.Data, &data); err == nil && data.Title != "" {
			return data.Title
		}
	}
	return ""
}

func lastEventTime(recording *Recording) time.Time {
	if len(recording.Events) == 0 {
		return recording.Header.Created()
	}
	return recording.Events[len(recording.Events)-1].At()
}

func lastTurn(recording *Recording) int {
	turn := 0
	for _, event := range recording.Events {
		if event.Type != "turn/start" {
			continue
		}
		var marker TurnMarker
		if err := json.Unmarshal(event.Data, &marker); err == nil && marker.Turn > turn {
			turn = marker.Turn
		}
	}
	return turn
}

func newID(now time.Time) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%s-%d", now.Format("20060102-150405"), now.UnixNano()%1_000_000)
	}
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(random))
}
