// Package session stores conversations as append-only JSONL files, mirroring
// the persistence model of the upstream DSH JSONL session format closely enough
// for future migration tooling.
package session

import (
	"bufio"
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

	"deekseekharness-go/internal/llm"
)

// Entry types persisted in one session JSONL file.
const (
	EntryMeta    = "meta"
	EntryMessage = "message"
)

// Entry is one JSONL line of a session file.
type Entry struct {
	Type      string       `json:"type"`
	Time      time.Time    `json:"time"`
	ID        string       `json:"id,omitempty"`
	Title     string       `json:"title,omitempty"`
	CreatedAt *time.Time   `json:"createdAt,omitempty"`
	Message   *llm.Message `json:"message,omitempty"`
}

// Summary is the list projection of one session.
type Summary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Messages  int       `json:"messages"`
}

// Session is one conversation held in memory.
type Session struct {
	ID        string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []llm.Message
}

// Summary projects the session into its list view.
func (s *Session) Summary() Summary {
	return Summary{ID: s.ID, Title: s.Title, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Messages: len(s.Messages)}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Store persists sessions under one directory.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates a store rooted at dir. The directory is created lazily.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir reports the session directory.
func (s *Store) Dir() string { return s.dir }

// Create writes a new session file and returns the in-memory session.
func (s *Store) Create(title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled session"
	}
	if len(title) > 120 {
		title = title[:120]
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:        newID(now),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create directory: %w", err)
	}
	entry := Entry{Type: EntryMeta, Time: now, ID: sess.ID, Title: sess.Title, CreatedAt: &sess.CreatedAt}
	if err := s.appendEntryLocked(sess.ID, entry); err != nil {
		return nil, err
	}
	return sess, nil
}

// Append appends one message to the session file and to the in-memory view.
func (s *Store) Append(sess *Session, message llm.Message) error {
	if sess == nil {
		return errors.New("session: nil session")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendEntryLocked(sess.ID, Entry{Type: EntryMessage, Time: now, Message: &message}); err != nil {
		return err
	}
	sess.Messages = append(sess.Messages, message)
	sess.UpdatedAt = now
	return nil
}

func (s *Store) appendEntryLocked(id string, entry Entry) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("session: invalid id %q", id)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("session: create directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(s.dir, id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("session: open %s: %w", id, err)
	}
	defer file.Close()
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("session: encode entry: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("session: write entry: %w", err)
	}
	return nil
}

// Load reads one session file back into memory.
func (s *Store) Load(id string) (*Session, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}
	path := filepath.Join(s.dir, id+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session: %s not found", id)
		}
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer file.Close()

	sess := &Session{ID: id, Title: "Untitled session"}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("session: decode %s: %w", path, err)
		}
		switch entry.Type {
		case EntryMeta:
			sess.ID = entry.ID
			sess.Title = entry.Title
			if entry.CreatedAt != nil {
				sess.CreatedAt = *entry.CreatedAt
			}
			sess.UpdatedAt = entry.Time
		case EntryMessage:
			if entry.Message != nil {
				sess.Messages = append(sess.Messages, *entry.Message)
				sess.UpdatedAt = entry.Time
			}
		default:
			// Unknown entry types are ignored for forward compatibility.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: scan %s: %w", path, err)
	}
	return sess, nil
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

func newID(now time.Time) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%s-%d", now.Format("20060102-150405"), now.UnixNano()%1_000_000)
	}
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(random))
}
