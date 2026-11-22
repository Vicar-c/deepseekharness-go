package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// FormatVersion is the session format version this package reads and writes. It
// mirrors SESSION_FORMAT_VERSION in the upstream TypeScript implementation.
const FormatVersion = 4

// HeaderType is the value of the header record's "type" field.
const HeaderType = "session"

// maxLineBytes bounds one JSONL line. Session events are small; the limit only
// protects against a corrupt or hostile file.
const maxLineBytes = 8 << 20

// utf8BOM is the byte order mark some Windows editors prepend. Decode strips it
// from the header line and RepairTail ignores it when validating the last
// record, so a hand-edited log neither fails to read nor gets truncated.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Errors returned by Decode. They are wrapped with the failing line number, so
// callers can use errors.Is while still printing a precise message.
var (
	// ErrMalformedLog reports a log that is not valid v4 JSONL.
	ErrMalformedLog = errors.New("session: malformed session log")
	// ErrUnsupportedFormat reports a header whose version this build cannot read.
	ErrUnsupportedFormat = errors.New("session: unsupported session format version")
	// ErrUnknownEventType reports an unrecognized event type without the
	// ignorable marker: such a log may reconstruct a wrong session, so the
	// reader refuses it instead of silently dropping the event.
	ErrUnknownEventType = errors.New("session: unknown event type without the ignorable marker")
)

// Header is the first line of a v4 session log. Storage metadata lives here and
// stays outside the conversation event log.
type Header struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	ID              string `json:"id"`
	CreatedAt       int64  `json:"createdAt"`
	Cwd             string `json:"cwd,omitempty"`
	ParentSession   string `json:"parentSession,omitempty"`
	IsSeeded        bool   `json:"isSeeded"`
	Origin          string `json:"origin,omitempty"`
	DelegationDepth *int   `json:"delegationDepth,omitempty"`
	AgentPreset     string `json:"agentPreset,omitempty"`
}

// Created reports the header timestamp as a UTC time.
func (h Header) Created() time.Time { return time.UnixMilli(h.CreatedAt).UTC() }

// Event is one immutable entry after the header.
type Event struct {
	Type            string          `json:"type"`
	Data            json.RawMessage `json:"data"`
	SurfaceOp       *SurfaceOp      `json:"surfaceOp,omitempty"`
	SourceEventSeqs []int64         `json:"sourceEventSeqs,omitempty"`
	Seq             int64           `json:"seq"`
	Time            int64           `json:"time"`
	Ignorable       bool            `json:"ignorable,omitempty"`
}

// At reports the event timestamp as a UTC time.
func (e Event) At() time.Time { return time.UnixMilli(e.Time).UTC() }

// Known reports whether this build recognizes the event type.
func (e Event) Known() bool { return IsKnownEventType(e.Type) }

// SurfaceOp records how a message-producing event entered the ordered surface.
// It is encoded either as the string "append" or as
// {"op":"replace","startSeq":N,"endSeq":M}.
type SurfaceOp struct {
	Replace  bool
	StartSeq int64
	EndSeq   int64
}

// Append is the surface operation of a normally appended event.
var Append = SurfaceOp{}

// UnmarshalJSON decodes both accepted surface operation shapes.
func (s *SurfaceOp) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte(`"append"`)) {
		*s = SurfaceOp{}
		return nil
	}
	var payload struct {
		Op       string `json:"op"`
		StartSeq *int64 `json:"startSeq"`
		EndSeq   *int64 `json:"endSeq"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("session: decode surfaceOp: %w", err)
	}
	if payload.Op != "replace" || payload.StartSeq == nil || payload.EndSeq == nil {
		return fmt.Errorf("session: unsupported surfaceOp %s", data)
	}
	if *payload.StartSeq < 0 || *payload.EndSeq < *payload.StartSeq {
		return fmt.Errorf("session: invalid replace range %d..%d", *payload.StartSeq, *payload.EndSeq)
	}
	*s = SurfaceOp{Replace: true, StartSeq: *payload.StartSeq, EndSeq: *payload.EndSeq}
	return nil
}

// MarshalJSON encodes the surface operation in its wire shape.
func (s SurfaceOp) MarshalJSON() ([]byte, error) {
	if !s.Replace {
		return []byte(`"append"`), nil
	}
	return json.Marshal(struct {
		Op       string `json:"op"`
		StartSeq int64  `json:"startSeq"`
		EndSeq   int64  `json:"endSeq"`
	}{Op: "replace", StartSeq: s.StartSeq, EndSeq: s.EndSeq})
}

// Recording is one decoded session log: the header plus every event in order.
type Recording struct {
	Header Header
	Events []Event
}

// Decode reads a v4 JSONL session. The first record must be the session header
// and every following event must carry the next sequence number, starting at 0.
func Decode(r io.Reader) (*Recording, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	recording := &Recording{}
	lineNumber := 0
	haveHeader := false
	nextSeq := int64(0)
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if lineNumber == 1 {
			// A hand-edited or Windows-saved file may start with a UTF-8 BOM;
			// encoding/json rejects it, and the header is the only place it can
			// appear.
			line = bytes.TrimPrefix(line, utf8BOM)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !haveHeader {
			header, err := decodeHeader(line)
			if err != nil {
				return nil, err
			}
			recording.Header = header
			haveHeader = true
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", ErrMalformedLog, lineNumber, err)
		}
		if event.Type == "" {
			return nil, fmt.Errorf("%w: line %d: event type is empty", ErrMalformedLog, lineNumber)
		}
		if !event.Known() && !event.Ignorable {
			return nil, fmt.Errorf("%w: line %d: %q", ErrUnknownEventType, lineNumber, event.Type)
		}
		if event.Seq != nextSeq {
			return nil, fmt.Errorf("%w: line %d: seq = %d, want %d", ErrMalformedLog, lineNumber, event.Seq, nextSeq)
		}
		nextSeq++
		recording.Events = append(recording.Events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedLog, err)
	}
	if !haveHeader {
		return nil, fmt.Errorf("%w: empty session log", ErrMalformedLog)
	}
	return recording, nil
}

func decodeHeader(line []byte) (Header, error) {
	var probe struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return Header{}, fmt.Errorf("%w: line 1: %v", ErrMalformedLog, err)
	}
	if probe.Type != HeaderType {
		return Header{}, fmt.Errorf("%w: line 1: first record type = %q, want %q", ErrMalformedLog, probe.Type, HeaderType)
	}
	if probe.Version != FormatVersion {
		return Header{}, fmt.Errorf("%w: %d (this build reads and writes %d)", ErrUnsupportedFormat, probe.Version, FormatVersion)
	}
	var header Header
	if err := json.Unmarshal(line, &header); err != nil {
		return Header{}, fmt.Errorf("%w: line 1: %v", ErrMalformedLog, err)
	}
	if header.ID == "" {
		return Header{}, fmt.Errorf("%w: line 1: header id is empty", ErrMalformedLog)
	}
	if header.CreatedAt < 0 {
		return Header{}, fmt.Errorf("%w: line 1: createdAt = %d", ErrMalformedLog, header.CreatedAt)
	}
	return header, nil
}

// Encode writes the recording as v4 JSONL: the header first, then every event.
// The encoder keeps "<", ">" and "&" unescaped, matching JSON.stringify, so the
// output stays close to what the upstream writer produces.
func (r *Recording) Encode(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(r.Header); err != nil {
		return fmt.Errorf("session: encode header: %w", err)
	}
	for index, event := range r.Events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("session: encode event %d: %w", index, err)
		}
	}
	return nil
}

// knownEventTypes is the event vocabulary this build understands, copied from
// packages/core/session/src/known-event-types.ts at upstream commit 39163dd30f.
// Unknown types are only tolerated when they carry ignorable: true.
var knownEventTypes = map[string]struct{}{
	"agent-preset/selected":                  {},
	"agent/inbox/spliced":                    {},
	"approval/asked":                         {},
	"approval/decided":                       {},
	"approval/policy":                        {},
	"assistant/attempt":                      {},
	"assistant/message":                      {},
	"command/done":                           {},
	"command/run":                            {},
	"compaction/end":                         {},
	"compaction/prune":                       {},
	"compaction/start":                       {},
	"compaction/summary":                     {},
	"deliverables/presented":                 {},
	"developer/message":                      {},
	"feedback/message-delete":                {},
	"feedback/message-put":                   {},
	"feedback/record":                        {},
	"goal/change":                            {},
	"hook/invoked":                           {},
	"hook/result":                            {},
	"image/offload":                          {},
	"job/registered":                         {},
	"job/settled":                            {},
	"llm/retry":                              {},
	"llm/retry-started":                      {},
	"model/selection":                        {},
	"permission/preset":                      {},
	"plan/mode":                              {},
	"request/context":                        {},
	"request/header":                         {},
	"sandbox/mode":                           {},
	"schedule/change":                        {},
	"session-log-deepseek/delivery-accepted": {},
	"session/end-seed":                       {},
	"session/title":                          {},
	"session/title-llm-request":              {},
	"step/end":                               {},
	"step/start":                             {},
	"subagent/catalog":                       {},
	"subagent/descriptor":                    {},
	"subagent/model-selection-policy":        {},
	"system/message":                         {},
	"team/member":                            {},
	"team/message/delivered":                 {},
	"team/message/queued":                    {},
	"team/mode":                              {},
	"team/task":                              {},
	"todo/write":                             {},
	"tool-workflow/agent-end":                {},
	"tool-workflow/agent-start":              {},
	"tool-workflow/run-end":                  {},
	"tool-workflow/run-start":                {},
	"tool/call":                              {},
	"tool/ptc-dispatch":                      {},
	"tool/ptc-dispatch-start":                {},
	"tool/result":                            {},
	"turn/end":                               {},
	"turn/start":                             {},
	"user/message":                           {},
	"web/deepseek-search-llm-request":        {},
	"working-directory/change":               {},
	"workspace/changes":                      {},
}

// IsKnownEventType reports whether name is part of the v4 event vocabulary.
func IsKnownEventType(name string) bool {
	_, ok := knownEventTypes[name]
	return ok
}
