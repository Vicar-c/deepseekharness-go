package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrWriterExists reports that another Writer in this process already holds the
// session file. Cross-process locking needs platform specific file locks and is
// deliberately left to the sandbox step.
var ErrWriterExists = errors.New("session: a writer already holds this session file")

// ErrWriterClosed reports a write after Close.
var ErrWriterClosed = errors.New("session: writer is closed")

// activeWriters guards the in-process single-writer invariant.
var (
	activeWritersMu sync.Mutex
	activeWriters   = map[string]struct{}{}
)

func acquireWriter(path string) error {
	key, err := filepath.Abs(path)
	if err != nil {
		key = path
	}
	activeWritersMu.Lock()
	defer activeWritersMu.Unlock()
	if _, taken := activeWriters[key]; taken {
		return ErrWriterExists
	}
	activeWriters[key] = struct{}{}
	return nil
}

func releaseWriter(path string) {
	key, err := filepath.Abs(path)
	if err != nil {
		key = path
	}
	activeWritersMu.Lock()
	defer activeWritersMu.Unlock()
	delete(activeWriters, key)
}

// Writer appends v4 events to one session file. It assigns sequences and
// timestamps, and serialises every write behind one mutex, which makes it the
// single writer the format requires inside this process.
type Writer struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	header Header
	seq    int64
	closed bool
}

// CreateSessionFile writes a new session log at path with header as its first
// line. The header is written to a temporary file and renamed into place, so a
// crash never leaves a half written header behind.
func CreateSessionFile(path string, header Header) (*Writer, error) {
	if header.ID == "" {
		return nil, errors.New("session: header id is required")
	}
	header.Type = HeaderType
	header.Version = FormatVersion
	if header.CreatedAt == 0 {
		header.CreatedAt = time.Now().UnixMilli()
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("session: create directory: %w", err)
	}
	line, err := encodeJSONLine(header)
	if err != nil {
		return nil, fmt.Errorf("session: encode header: %w", err)
	}
	temp, err := os.CreateTemp(directory, ".session-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("session: create temporary file: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if _, err := temp.Write(line); err != nil {
		cleanup()
		return nil, fmt.Errorf("session: write header: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return nil, fmt.Errorf("session: sync header: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("session: close header: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("session: publish session file: %w", err)
	}
	return openWriterAt(path, header, 0)
}

// OpenWriter opens an existing session log for appending. A partial trailing
// record left by a crash is repaired first, then the next sequence number is
// derived from the last committed event.
func OpenWriter(path string) (*Writer, error) {
	if _, err := RepairTail(path); err != nil {
		return nil, err
	}
	recording, err := ReadRecording(path)
	if err != nil {
		return nil, err
	}
	next := int64(0)
	if len(recording.Events) > 0 {
		next = recording.Events[len(recording.Events)-1].Seq + 1
	}
	return openWriterAt(path, recording.Header, next)
}

func openWriterAt(path string, header Header, seq int64) (*Writer, error) {
	if err := acquireWriter(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		releaseWriter(path)
		return nil, fmt.Errorf("session: open for append: %w", err)
	}
	return &Writer{path: path, file: file, header: header, seq: seq}, nil
}

// Header reports the session header this writer opened.
func (w *Writer) Header() Header { return w.header }

// Seq reports the sequence number the next event will receive.
func (w *Writer) Seq() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// Path reports the session file path.
func (w *Writer) Path() string { return w.path }

// Append adds one log-only event.
func (w *Writer) Append(eventType string, data any) (Event, error) {
	return w.AppendEvent(eventType, data, nil, nil)
}

// AppendEvent adds one event, optionally carrying a surface operation and cited
// source sequences. The write is a single append of one JSON line.
func (w *Writer) AppendEvent(eventType string, data any, surfaceOp *SurfaceOp, sources []int64) (Event, error) {
	if !IsKnownEventType(eventType) {
		return Event{}, fmt.Errorf("%w: %q", ErrUnknownEventType, eventType)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("session: encode %s data: %w", eventType, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Event{}, ErrWriterClosed
	}
	event := Event{
		Type:            eventType,
		Data:            payload,
		SurfaceOp:       surfaceOp,
		SourceEventSeqs: sources,
		Seq:             w.seq,
		Time:            time.Now().UnixMilli(),
	}
	line, err := encodeJSONLine(event)
	if err != nil {
		return Event{}, fmt.Errorf("session: encode %s event: %w", eventType, err)
	}
	if _, err := w.file.Write(line); err != nil {
		return Event{}, fmt.Errorf("session: append %s event: %w", eventType, err)
	}
	w.seq++
	return event, nil
}

// Sync flushes the file to stable storage.
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	return w.file.Sync()
}

// Close syncs and closes the file and releases the single-writer slot.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	syncErr := w.file.Sync()
	closeErr := w.file.Close()
	releaseWriter(w.path)
	return errors.Join(syncErr, closeErr)
}

// ReadRecording opens path and decodes it as a v4 session log.
func ReadRecording(path string) (*Recording, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer file.Close()
	recording, err := Decode(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return recording, nil
}

// RepairTail truncates a trailing record that a crash left incomplete. A record
// counts as committed only when it ends with a newline and parses as JSON;
// anything after the last valid line is dropped, and the number of dropped
// bytes is returned.
func RepairTail(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("session: read %s: %w", path, err)
	}
	keep := committedPrefix(data)
	if keep == len(data) {
		return 0, nil
	}
	return int64(len(data) - keep), truncateFile(path, int64(keep))
}

// committedPrefix reports how many leading bytes form the complete, parseable
// prefix of a log. A trailing record that a crash left incomplete, or a complete
// record that does not parse, is excluded from the prefix.
func committedPrefix(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	if data[len(data)-1] != '\n' {
		return bytes.LastIndexByte(data, '\n') + 1
	}
	start := bytes.LastIndexByte(data[:len(data)-1], '\n') + 1
	last := bytes.TrimSpace(data[start:])
	if start == 0 {
		// A one-line file may carry a BOM; it is part of the header, not a
		// reason to truncate the only record in the log.
		last = bytes.TrimSpace(bytes.TrimPrefix(last, utf8BOM))
	}
	if len(last) == 0 || json.Valid(last) {
		return len(data)
	}
	return start
}

// readRecordingLenient decodes the committed prefix of path without touching the
// file. Readers must not repair: a concurrent writer may be halfway through an
// append, and truncating what looks like a torn tail would delete a line it owns.
func readRecordingLenient(path string) (*Recording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	recording, err := Decode(bytes.NewReader(data[:committedPrefix(data)]))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return recording, nil
}

func truncateFile(path string, size int64) error {
	if err := os.Truncate(path, size); err != nil {
		return fmt.Errorf("session: truncate %s: %w", path, err)
	}
	return nil
}

// encodeJSONLine marshals one value as a single JSON line, keeping "<", ">" and
// "&" unescaped to stay close to the upstream writer's JSON.stringify output.
func encodeJSONLine(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
