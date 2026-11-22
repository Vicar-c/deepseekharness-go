package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// failingClient fails the first failures calls and then answers, so a test can
// assert exactly how many attempts the loop made.
type failingClient struct {
	failures []error
	response llm.Response
	calls    int
}

func (c *failingClient) Model() string { return "test-model" }

func (c *failingClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.calls++
	if len(c.failures) > 0 {
		err := c.failures[0]
		c.failures = c.failures[1:]
		return llm.Response{}, err
	}
	return c.response, nil
}

// streamingFailClient emits one delta and then fails, like a provider that drops
// the connection mid-answer.
type streamingFailClient struct {
	calls int
	err   error
}

func (c *streamingFailClient) Model() string { return "test-model" }

func (c *streamingFailClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.calls++
	return llm.Response{}, c.err
}

func (c *streamingFailClient) ChatStream(_ context.Context, _ llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	c.calls++
	if err := onChunk(llm.Chunk{Content: "partial"}); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{}, c.err
}

// fastRetryPolicy keeps the test fast while exercising the real policy logic.
func fastRetryPolicy(maxRetries int) *llm.RetryPolicy {
	policy := llm.DefaultRetryPolicy()
	policy.MaxRetries = maxRetries
	policy.InitialDelay = time.Millisecond
	policy.MaxDelay = 4 * time.Millisecond
	policy.JitterRatio = 0
	return policy
}

func buildRetryLoop(t *testing.T, client llm.Client, policy *llm.RetryPolicy, events *kernel.Events) (*agent.Loop, *session.Session, *session.Store) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("retry test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	pipeline := tool.NewPipeline(tool.NewRegistry(), tool.PipelineOptions{Logger: testLogger()})
	loop, err := agent.New(agent.Options{
		Client:      client,
		Pipeline:    pipeline,
		Session:     sess,
		Store:       store,
		Events:      events,
		Logger:      testLogger(),
		RetryPolicy: policy,
		Random:      func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop, sess, store
}

// recordedEvent is one decoded session event in test order.
type recordedEvent struct {
	Type string
	Data json.RawMessage
}

func readEvents(t *testing.T, store *session.Store, sess *session.Session) []recordedEvent {
	t.Helper()
	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	events := make([]recordedEvent, 0, len(recording.Events))
	for _, event := range recording.Events {
		events = append(events, recordedEvent{Type: event.Type, Data: event.Data})
	}
	return events
}

// retryPayload decodes the fields this test cares about in an llm/retry event.
type retryPayload struct {
	RetryID    string `json:"retryId"`
	Turn       int    `json:"turn"`
	Step       int    `json:"step"`
	Provider   string `json:"provider"`
	Mode       string `json:"mode"`
	PolicyKey  string `json:"policyKey"`
	Retry      int    `json:"retry"`
	MaxRetries int    `json:"maxRetries"`
	DelayMs    int64  `json:"delayMs"`
	Failure    struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"failure"`
}

// TestLoopRetriesARateLimitedModelCall proves a transient failure is repeated,
// that both retry transitions are durable, and that the retry counter advances.
func TestLoopRetriesARateLimitedModelCall(t *testing.T) {
	rateLimited := &llm.Failure{Message: "deepseek HTTP 429: slow down", Code: llm.CodeRateLimit, Status: 429}
	client := &failingClient{
		failures: []error{rateLimited, rateLimited},
		response: llm.TextResponse("finally", "stop"),
	}
	loop, sess, store := buildRetryLoop(t, client, fastRetryPolicy(3), kernel.NewEvents())

	reply, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply != "finally" {
		t.Fatalf("reply = %q, want finally", reply)
	}
	if client.calls != 3 {
		t.Fatalf("model calls = %d, want 3", client.calls)
	}

	var retries []retryPayload
	var order []string
	var end session.TurnEndData
	for _, event := range readEvents(t, store, sess) {
		switch event.Type {
		case "llm/retry":
			var payload retryPayload
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatalf("decode llm/retry: %v", err)
			}
			retries = append(retries, payload)
			order = append(order, event.Type)
		case "llm/retry-started":
			order = append(order, event.Type)
		case "turn/end":
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if len(retries) != 2 {
		t.Fatalf("llm/retry events = %d, want 2", len(retries))
	}
	wantOrder := "llm/retry,llm/retry-started,llm/retry,llm/retry-started"
	if strings.Join(order, ",") != wantOrder {
		t.Fatalf("retry event order = %v, want %s", order, wantOrder)
	}
	for index, payload := range retries {
		if payload.Retry != index+1 {
			t.Errorf("retry %d counted itself as %d", index+1, payload.Retry)
		}
		if payload.Turn != 1 || payload.Step != 1 {
			t.Errorf("retry %d recorded turn/step %d/%d, want 1/1", index+1, payload.Turn, payload.Step)
		}
		if payload.Mode != llm.RetryModeNormal || payload.MaxRetries != 3 {
			t.Errorf("retry %d policy = %s/%d, want normal/3", index+1, payload.Mode, payload.MaxRetries)
		}
		if payload.Failure.Code != llm.CodeRateLimit {
			t.Errorf("retry %d failure code = %q, want RATE_LIMIT", index+1, payload.Failure.Code)
		}
		if wantDelay := int64(1) << index; payload.DelayMs != wantDelay {
			t.Errorf("retry %d delay = %dms, want %dms", index+1, payload.DelayMs, wantDelay)
		}
		if payload.PolicyKey == "" {
			t.Errorf("retry %d recorded no policy key", index+1)
		}
		if payload.RetryID != retries[0].RetryID {
			t.Errorf("retry %d used a different retry id", index+1)
		}
	}
	if end.Reason.Kind != session.TurnEndCompleted {
		t.Fatalf("turn end = %+v, want completed", end.Reason)
	}
}

// TestLoopDoesNotRetryAHopelessFailure keeps a failure a repeat cannot fix from
// burning the retry budget.
func TestLoopDoesNotRetryAHopelessFailure(t *testing.T) {
	client := &failingClient{failures: []error{
		&llm.Failure{Message: "deepseek HTTP 401: bad key", Code: llm.CodeAuth, Status: 401},
	}}
	loop, sess, store := buildRetryLoop(t, client, fastRetryPolicy(5), kernel.NewEvents())

	if _, err := loop.Run(context.Background(), "go"); err == nil {
		t.Fatal("expected the run to fail")
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	var end session.TurnEndData
	for _, event := range readEvents(t, store, sess) {
		if event.Type == "llm/retry" {
			t.Fatal("a non-retryable failure must not schedule a retry")
		}
		if event.Type == "turn/end" {
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if end.Reason.Kind != session.TurnEndError {
		t.Fatalf("turn end = %+v, want error", end.Reason)
	}
	if end.Reason.Error == nil || end.Reason.Error.Code != llm.CodeAuth {
		t.Fatalf("turn end error = %+v, want code AUTH", end.Reason.Error)
	}
}

// TestLoopStopsWhenRetriesAreExhausted proves the bound is honoured.
func TestLoopStopsWhenRetriesAreExhausted(t *testing.T) {
	rateLimited := &llm.Failure{Message: "rate limited", Code: llm.CodeRateLimit, Status: 429}
	client := &failingClient{failures: []error{rateLimited, rateLimited, rateLimited}}
	loop, sess, store := buildRetryLoop(t, client, fastRetryPolicy(1), kernel.NewEvents())

	if _, err := loop.Run(context.Background(), "go"); err == nil {
		t.Fatal("expected the run to fail")
	}
	if client.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (first attempt plus one retry)", client.calls)
	}
	events := readEvents(t, store, sess)
	retries := 0
	for _, event := range events {
		if event.Type == "llm/retry" {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("llm/retry events = %d, want 1", retries)
	}
	var end session.TurnEndData
	if err := json.Unmarshal(events[len(events)-1].Data, &end); err != nil {
		t.Fatalf("decode turn/end: %v", err)
	}
	if end.Reason.Kind != session.TurnEndError || end.Reason.Error.Code != llm.CodeRateLimit {
		t.Fatalf("turn end = %+v, want error RATE_LIMIT", end.Reason)
	}
}

// TestLoopDoesNotRetryAfterStreamingStarted keeps a repeated attempt from
// duplicating text the user already saw.
func TestLoopDoesNotRetryAfterStreamingStarted(t *testing.T) {
	client := &streamingFailClient{err: &llm.Failure{Message: "stream broke", Code: llm.CodeTransport}}
	events := kernel.NewEvents()
	var streamed []string
	events.On(agent.EventAgentStream, func(event kernel.Event) {
		if payload, ok := event.Payload.(agent.StreamEvent); ok && payload.Content != "" {
			streamed = append(streamed, payload.Content)
		}
	})
	loop, sess, store := buildRetryLoop(t, client, fastRetryPolicy(5), events)

	if _, err := loop.Run(context.Background(), "go"); err == nil {
		t.Fatal("expected the run to fail")
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	if strings.Join(streamed, ",") != "partial" {
		t.Fatalf("streamed content = %v, want exactly one partial delta", streamed)
	}
	for _, event := range readEvents(t, store, sess) {
		if event.Type == "llm/retry" {
			t.Fatal("a retry after streamed output would duplicate the delta")
		}
	}
}

// TestLoopCancelsARetryWait proves an interrupt during the wait ends the turn as
// aborted instead of sleeping through it.
func TestLoopCancelsARetryWait(t *testing.T) {
	client := &failingClient{failures: []error{
		&llm.Failure{Message: "rate limited", Code: llm.CodeRateLimit},
		&llm.Failure{Message: "rate limited", Code: llm.CodeRateLimit},
	}}
	policy := fastRetryPolicy(5)
	policy.InitialDelay = time.Hour
	policy.MaxDelay = time.Hour
	events := kernel.NewEvents()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events.On(agent.EventAgentRetry, func(event kernel.Event) {
		if _, ok := event.Payload.(agent.RetryEvent); ok {
			cancel()
		}
	})
	loop, sess, store := buildRetryLoop(t, client, policy, events)

	if _, err := loop.Run(ctx, "go"); err == nil {
		t.Fatal("expected the cancelled run to fail")
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	var end session.TurnEndData
	for _, event := range readEvents(t, store, sess) {
		if event.Type == "turn/end" {
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if end.Reason.Kind != session.TurnEndAborted {
		t.Fatalf("turn end = %+v, want aborted", end.Reason)
	}
	if end.Reason.Reason == nil || end.Reason.Reason.Kind != session.CancelCauseUser {
		t.Fatalf("cancel cause = %+v, want user", end.Reason.Reason)
	}
}

// TestLoopRecordsMaxTokensTurn distinguishes a truncated answer from a complete
// one, so a reader can tell the model ran out of output budget.
func TestLoopRecordsMaxTokensTurn(t *testing.T) {
	client := &failingClient{response: llm.TextResponse("cut off", "length")}
	loop, sess, store := buildRetryLoop(t, client, fastRetryPolicy(0), kernel.NewEvents())

	reply, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply != "cut off" {
		t.Fatalf("reply = %q, want cut off", reply)
	}
	var end session.TurnEndData
	for _, event := range readEvents(t, store, sess) {
		if event.Type == "turn/end" {
			if err := json.Unmarshal(event.Data, &end); err != nil {
				t.Fatalf("decode turn/end: %v", err)
			}
		}
	}
	if end.Reason.Kind != session.TurnEndMaxTokens {
		t.Fatalf("turn end = %+v, want max-tokens", end.Reason)
	}
}

// TestLoopRetriesAnEmptyResponse covers the degenerate completion the provider
// sometimes sends: it produced nothing, so repeating the attempt is safe.
func TestLoopRetriesAnEmptyResponse(t *testing.T) {
	client := &failingClient{
		failures: []error{&llm.Failure{Message: "empty", Code: llm.CodeEmptyResponse}},
		response: llm.TextResponse("second try", "stop"),
	}
	loop, _, _ := buildRetryLoop(t, client, fastRetryPolicy(2), kernel.NewEvents())
	reply, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reply != "second try" || client.calls != 2 {
		t.Fatalf("reply = %q calls = %d, want second try after 2 calls", reply, client.calls)
	}
}

// TestLoopAlwaysModeRetriesUntilSuccess covers the unbounded policy.
func TestLoopAlwaysModeRetriesUntilSuccess(t *testing.T) {
	failures := make([]error, 4)
	for index := range failures {
		failures[index] = &llm.Failure{Message: "provider down", Code: llm.CodeServer, Status: 503}
	}
	client := &failingClient{failures: failures, response: llm.TextResponse("ok", "stop")}
	policy := fastRetryPolicy(0)
	policy.Mode = llm.RetryModeAlways
	loop, _, _ := buildRetryLoop(t, client, policy, kernel.NewEvents())
	if _, err := loop.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 5 {
		t.Fatalf("model calls = %d, want 5", client.calls)
	}
}

// TestLoopReportsRetryEvents checks the transient bus event a user interface
// listens to while a retry is pending.
func TestLoopReportsRetryEvents(t *testing.T) {
	client := &failingClient{
		failures: []error{&llm.Failure{Message: "rate limited", Code: llm.CodeRateLimit}},
		response: llm.TextResponse("ok", "stop"),
	}
	events := kernel.NewEvents()
	var retries []agent.RetryEvent
	events.On(agent.EventAgentRetry, func(event kernel.Event) {
		if payload, ok := event.Payload.(agent.RetryEvent); ok {
			retries = append(retries, payload)
		}
	})
	loop, _, _ := buildRetryLoop(t, client, fastRetryPolicy(2), events)
	if _, err := loop.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(retries) != 1 {
		t.Fatalf("retry events = %d, want 1", len(retries))
	}
	if retries[0].Code != llm.CodeRateLimit || retries[0].Retry != 1 || retries[0].MaxRetries != 2 {
		t.Fatalf("retry event = %+v", retries[0])
	}
	if retries[0].Delay != time.Millisecond {
		t.Fatalf("retry delay = %s, want 1ms", retries[0].Delay)
	}
	if retries[0].SessionID != loop.Session().ID {
		t.Fatalf("retry event session = %q, want %q", retries[0].SessionID, loop.Session().ID)
	}
}

// TestSleepContextRejectsAnExpiredContext documents the helper the retry wait
// uses: a cancelled context must win over the timer.
func TestLoopRetryWaitRespectsDeadline(t *testing.T) {
	client := &failingClient{failures: []error{
		&llm.Failure{Message: "rate limited", Code: llm.CodeRateLimit},
	}}
	policy := fastRetryPolicy(5)
	policy.InitialDelay = time.Hour
	policy.MaxDelay = time.Hour
	loop, _, _ := buildRetryLoop(t, client, policy, kernel.NewEvents())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := loop.Run(ctx, "go")
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("run waited %s despite the deadline", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the context error", err)
	}
}
