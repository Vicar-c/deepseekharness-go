package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// blockingClient holds the model call open until the test releases it, so a
// second turn really overlaps the first.
type blockingClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingClient) Model() string { return "test-model" }

func (c *blockingClient) Chat(ctx context.Context, _ llm.Request) (llm.Response, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		return llm.TextResponse("done", "stop"), nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

// cancellingClient cancels the turn context from inside the model call, which is
// what a signal-driven shutdown looks like to the loop.
type cancellingClient struct{ cancel context.CancelFunc }

func (*cancellingClient) Model() string { return "test-model" }

func (c *cancellingClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	c.cancel()
	return llm.Response{}, context.Canceled
}

func newLoopFor(t *testing.T, store *session.Store, sess *session.Session, client llm.Client) *agent.Loop {
	t.Helper()
	loop, err := agent.New(agent.Options{
		Client:   client,
		Pipeline: tool.NewPipeline(tool.NewRegistry(), tool.PipelineOptions{Logger: testLogger()}),
		Session:  sess,
		Store:    store,
		Events:   kernel.NewEvents(),
		Logger:   testLogger(),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop
}

func TestLoopRejectsConcurrentTurn(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("lease")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	client := &blockingClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	first := newLoopFor(t, store, sess, client)
	done := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background(), "first")
		done <- err
	}()
	<-client.started

	second := newLoopFor(t, store, sess, client)
	if _, err := second.Run(context.Background(), "second"); !errors.Is(err, session.ErrSessionOwned) {
		t.Fatalf("second turn error = %v, want ErrSessionOwned", err)
	}

	close(client.release)
	if err := <-done; err != nil {
		t.Fatalf("first turn: %v", err)
	}
}

func TestLoopRecordsCancelledTurn(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("cancel")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := newLoopFor(t, store, sess, &cancellingClient{cancel: cancel})

	if _, err := loop.Run(ctx, "stop me"); err == nil {
		t.Fatal("expected the run to fail after cancellation")
	}

	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	var reasons []session.TurnEndReason
	for _, event := range recording.Events {
		if event.Type != "turn/end" {
			continue
		}
		var data session.TurnEndData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode turn/end: %v", err)
		}
		reasons = append(reasons, data.Reason)
	}
	if len(reasons) != 1 || reasons[0].Kind != session.TurnEndAborted {
		t.Fatalf("turn/end reasons = %+v, want one %q", reasons, session.TurnEndAborted)
	}
	if reasons[0].Reason == nil || reasons[0].Reason.Kind != session.CancelCauseUser {
		t.Fatalf("cancel cause = %+v, want %q", reasons[0].Reason, session.CancelCauseUser)
	}
}

// TestLoopReleasesLeaseOnFailure keeps the lease from leaking after a failed turn.
func TestLoopReleasesLeaseOnFailure(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("release")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	client := &scriptedClient{err: errors.New("model down")}
	loop := newLoopFor(t, store, sess, client)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := loop.Run(context.Background(), "again"); err == nil {
			t.Fatal("expected a model error")
		}
	}
	release, err := store.BeginTurn(sess.ID)
	if err != nil {
		t.Fatalf("lease was not released: %v", err)
	}
	release()
}
