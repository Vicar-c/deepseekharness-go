package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureHeader = `{"type":"session","version":4,"id":"fixture","createdAt":1000,"isSeeded":false}`

func decodeFixture(t *testing.T) *Recording {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "upstream-v4-turn-spacing.jsonl"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()
	recording, err := Decode(file)
	if err != nil {
		t.Fatalf("Decode(fixture): %v", err)
	}
	return recording
}

// TestDecodeUpstreamV4Fixture is the interop contract: the decoder must ingest a
// real upstream file without translation and reproduce its header and every
// event, sequence numbers included.
func TestDecodeUpstreamV4Fixture(t *testing.T) {
	recording := decodeFixture(t)

	header := recording.Header
	if header.Type != HeaderType || header.Version != FormatVersion {
		t.Fatalf("header type/version = %q/%d", header.Type, header.Version)
	}
	if header.ID != "preview-turn-spacing" {
		t.Errorf("header id = %q", header.ID)
	}
	if header.Cwd != "/dsh/workspace" {
		t.Errorf("header cwd = %q", header.Cwd)
	}
	if header.IsSeeded {
		t.Error("header isSeeded = true, want false")
	}
	if header.DelegationDepth == nil || *header.DelegationDepth != 0 {
		t.Errorf("header delegationDepth = %v, want pointer to 0", header.DelegationDepth)
	}
	if header.AgentPreset != "standard" {
		t.Errorf("header agentPreset = %q", header.AgentPreset)
	}
	if header.CreatedAt != 1787472300000 {
		t.Errorf("header createdAt = %d", header.CreatedAt)
	}

	if len(recording.Events) != 43 {
		t.Fatalf("events = %d, want 43", len(recording.Events))
	}
	appends := 0
	for index, event := range recording.Events {
		if event.Seq != int64(index) {
			t.Fatalf("event %d seq = %d", index, event.Seq)
		}
		if !event.Known() {
			t.Errorf("event %d type %q is not in the known vocabulary", index, event.Type)
		}
		if event.SurfaceOp != nil {
			appends++
			if event.SurfaceOp.Replace {
				t.Errorf("event %d unexpectedly replaces a surface range", index)
			}
		}
	}
	if appends != 16 {
		t.Errorf("events with surfaceOp = %d, want 16", appends)
	}
	if recording.Events[0].Type != "turn/start" {
		t.Errorf("first event type = %q", recording.Events[0].Type)
	}
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	log := `{"type":"session","version":3,"id":"old","createdAt":1,"isSeeded":false}` + "\n"
	_, err := Decode(strings.NewReader(log))
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestDecodeRejectsUnknownRequiredType(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"plugin:demo/unknown","data":{},"seq":0,"time":2}` + "\n"
	_, err := Decode(strings.NewReader(log))
	if !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("err = %v, want ErrUnknownEventType", err)
	}
}

func TestDecodeKeepsIgnorableUnknownType(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"plugin:demo/unknown","data":{"note":"kept"},"seq":0,"time":2,"ignorable":true}` + "\n"
	recording, err := Decode(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(recording.Events) != 1 {
		t.Fatalf("events = %d", len(recording.Events))
	}
	if recording.Events[0].Known() {
		t.Fatal("unknown event type reported as known")
	}
	if !recording.Events[0].Ignorable {
		t.Fatal("ignorable marker was lost")
	}
}

func TestDecodeRejectsSequenceGap(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"turn/start","data":{"turn":1},"seq":1,"time":2}` + "\n"
	_, err := Decode(strings.NewReader(log))
	if !errors.Is(err, ErrMalformedLog) {
		t.Fatalf("err = %v, want ErrMalformedLog", err)
	}
}

func TestDecodeRejectsMalformedLine(t *testing.T) {
	log := fixtureHeader + "\n" + "{not json}" + "\n"
	_, err := Decode(strings.NewReader(log))
	if !errors.Is(err, ErrMalformedLog) {
		t.Fatalf("err = %v, want ErrMalformedLog", err)
	}
}

// TestEncodeRoundTrip re-encodes the upstream fixture and decodes it again: the
// header, every event and every raw event payload must survive unchanged.
func TestEncodeRoundTrip(t *testing.T) {
	original := decodeFixture(t)
	var buffer bytes.Buffer
	if err := original.Encode(&buffer); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	if len(lines) != 1+len(original.Events) {
		t.Fatalf("encoded lines = %d, want %d", len(lines), 1+len(original.Events))
	}
	if !strings.HasPrefix(lines[0], `{"type":"session","version":4,`) {
		t.Fatalf("header line = %q", lines[0])
	}
	if !strings.Contains(lines[1], `"seq":0`) {
		t.Fatalf("first event line has no seq: %q", lines[1])
	}

	redecoded, err := Decode(bytes.NewReader(buffer.Bytes()))
	if err != nil {
		t.Fatalf("Decode(encoded): %v", err)
	}
	if !reflect.DeepEqual(redecoded.Header, original.Header) {
		t.Fatalf("header changed:\n got %+v\nwant %+v", redecoded.Header, original.Header)
	}
	if len(redecoded.Events) != len(original.Events) {
		t.Fatalf("events = %d, want %d", len(redecoded.Events), len(original.Events))
	}
	for index := range original.Events {
		want, got := original.Events[index], redecoded.Events[index]
		if got.Type != want.Type || got.Seq != want.Seq || got.Time != want.Time {
			t.Fatalf("event %d changed: got %+v want %+v", index, got, want)
		}
		if !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("event %d data changed:\n got %s\nwant %s", index, got.Data, want.Data)
		}
		if !reflect.DeepEqual(got.SurfaceOp, want.SurfaceOp) {
			t.Fatalf("event %d surfaceOp changed: got %+v want %+v", index, got.SurfaceOp, want.SurfaceOp)
		}
	}
}

func TestSurfaceOpReplaceRoundTrip(t *testing.T) {
	log := fixtureHeader + "\n" +
		`{"type":"tool/result","data":{"ok":true},"surfaceOp":{"op":"replace","startSeq":1,"endSeq":2},"seq":0,"time":2}` + "\n"
	recording, err := Decode(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	op := recording.Events[0].SurfaceOp
	if op == nil || !op.Replace || op.StartSeq != 1 || op.EndSeq != 2 {
		t.Fatalf("surfaceOp = %+v", op)
	}

	var buffer bytes.Buffer
	if err := recording.Encode(&buffer); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(buffer.String(), `"surfaceOp":{"op":"replace","startSeq":1,"endSeq":2}`) {
		t.Fatalf("encoded replace op = %s", buffer.String())
	}
	again, err := Decode(bytes.NewReader(buffer.Bytes()))
	if err != nil {
		t.Fatalf("Decode(encoded): %v", err)
	}
	if !reflect.DeepEqual(again.Events[0].SurfaceOp, op) {
		t.Fatalf("replace op changed: got %+v want %+v", again.Events[0].SurfaceOp, op)
	}
}
