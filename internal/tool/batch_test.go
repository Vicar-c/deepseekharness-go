package tool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// probeTool records overlap so the batch scheduler's concurrency can be asserted
// without timing guesses beyond the sleeps it is told to perform.
type probeTool struct {
	name  string
	safe  bool
	delay time.Duration
	probe *probe
}

func (t *probeTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        t.name,
		Description: "test probe",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

func (t *probeTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	t.probe.enter(t.name)
	defer t.probe.leave()
	select {
	case <-time.After(t.delay):
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	return tool.Result{Content: "done " + t.name}, nil
}

// ConcurrencySafe returns the probe's classification: false makes even a tool
// that implements the interface exclusive.
func (t *probeTool) ConcurrencySafe() bool { return t.safe }

// exclusiveProbe is a probe tool without the ConcurrencySafe method.
type exclusiveProbe struct {
	name  string
	delay time.Duration
	probe *probe
}

func (t *exclusiveProbe) Definition() tool.Definition {
	return tool.Definition{Name: t.name, Description: "exclusive probe", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (t *exclusiveProbe) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	t.probe.enter(t.name)
	defer t.probe.leave()
	select {
	case <-time.After(t.delay):
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	return tool.Result{Content: "done " + t.name}, nil
}

type probe struct {
	mu         sync.Mutex
	running    int
	maxRunning int
	started    []string
}

func (p *probe) enter(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running++
	p.started = append(p.started, name)
	if p.running > p.maxRunning {
		p.maxRunning = p.running
	}
}

func (p *probe) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running--
}

func (p *probe) peak() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxRunning
}

func (p *probe) order() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.started...)
}

func newBatchPipeline(t *testing.T, tools ...tool.Tool) *tool.Pipeline {
	t.Helper()
	registry := tool.NewRegistry()
	for _, candidate := range tools {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("Register(%s): %v", candidate.Definition().Name, err)
		}
	}
	return tool.NewPipeline(registry, tool.PipelineOptions{})
}

func callOf(name string) tool.Call {
	return tool.Call{ID: "call-" + name, Name: name, Arguments: json.RawMessage(`{}`)}
}

// TestExecuteBatchKeepsModelOrder makes the last call finish first and still
// expects the results in the order the model asked for them.
func TestExecuteBatchKeepsModelOrder(t *testing.T) {
	shared := &probe{}
	pipeline := newBatchPipeline(t,
		&probeTool{name: "slow", safe: true, delay: 60 * time.Millisecond, probe: shared},
		&probeTool{name: "medium", safe: true, delay: 30 * time.Millisecond, probe: shared},
		&probeTool{name: "fast", safe: true, delay: 1 * time.Millisecond, probe: shared},
	)

	results := pipeline.ExecuteBatch(context.Background(),
		[]tool.Call{callOf("slow"), callOf("medium"), callOf("fast")}, 3)

	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	for index, want := range []string{"done slow", "done medium", "done fast"} {
		if results[index].Content != want {
			t.Fatalf("results[%d] = %q, want %q (results must stay in model order)", index, results[index].Content, want)
		}
	}
	t.Logf("start order = %v (scheduling is not asserted, only result order)", shared.order())
	if peak := shared.peak(); peak < 2 {
		t.Fatalf("peak concurrency = %d, want the safe calls to overlap", peak)
	}
}

func TestExecuteBatchRespectsParallelLimit(t *testing.T) {
	shared := &probe{}
	tools := make([]tool.Tool, 0, 6)
	calls := make([]tool.Call, 0, 6)
	for index := 0; index < 6; index++ {
		name := fmt.Sprintf("safe%d", index)
		tools = append(tools, &probeTool{name: name, safe: true, delay: 25 * time.Millisecond, probe: shared})
		calls = append(calls, callOf(name))
	}
	pipeline := newBatchPipeline(t, tools...)

	results := pipeline.ExecuteBatch(context.Background(), calls, 2)
	if len(results) != 6 {
		t.Fatalf("results = %d", len(results))
	}
	if peak := shared.peak(); peak != 2 {
		t.Fatalf("peak concurrency = %d, want exactly the limit 2", peak)
	}
}

func TestExecuteBatchSerialWhenLimitIsOne(t *testing.T) {
	shared := &probe{}
	pipeline := newBatchPipeline(t,
		&probeTool{name: "a", safe: true, delay: 10 * time.Millisecond, probe: shared},
		&probeTool{name: "b", safe: true, delay: 10 * time.Millisecond, probe: shared},
	)
	pipeline.ExecuteBatch(context.Background(), []tool.Call{callOf("a"), callOf("b")}, 1)
	if peak := shared.peak(); peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1 (serial)", peak)
	}
	if order := shared.order(); strings.Join(order, ",") != "a,b" {
		t.Fatalf("order = %v, want a,b", order)
	}
}

// TestExecuteBatchExclusiveFormsBarrier runs safe, exclusive, safe and asserts
// the exclusive call never overlapped anything.
func TestExecuteBatchExclusiveFormsBarrier(t *testing.T) {
	shared := &probe{}
	pipeline := newBatchPipeline(t,
		&probeTool{name: "left", safe: true, delay: 30 * time.Millisecond, probe: shared},
		&exclusiveProbe{name: "exclusive", delay: 30 * time.Millisecond, probe: shared},
		&probeTool{name: "right", safe: true, delay: 30 * time.Millisecond, probe: shared},
	)

	results := pipeline.ExecuteBatch(context.Background(),
		[]tool.Call{callOf("left"), callOf("exclusive"), callOf("right")}, 4)
	for index, want := range []string{"done left", "done exclusive", "done right"} {
		if results[index].Content != want {
			t.Fatalf("results[%d] = %q, want %q", index, results[index].Content, want)
		}
	}
	if peak := shared.peak(); peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1: the exclusive call must form a barrier", peak)
	}
	if order := shared.order(); strings.Join(order, ",") != "left,exclusive,right" {
		t.Fatalf("order = %v, want left,exclusive,right", order)
	}
}

// TestExecuteBatchGroupsAdjacentSafeCalls checks that safe calls on both sides of
// a barrier overlap inside their own group.
func TestExecuteBatchGroupsAdjacentSafeCalls(t *testing.T) {
	shared := &probe{}
	pipeline := newBatchPipeline(t,
		&probeTool{name: "a", safe: true, delay: 30 * time.Millisecond, probe: shared},
		&probeTool{name: "b", safe: true, delay: 30 * time.Millisecond, probe: shared},
		&exclusiveProbe{name: "mid", delay: 5 * time.Millisecond, probe: shared},
		&probeTool{name: "c", safe: true, delay: 1 * time.Millisecond, probe: shared},
	)
	pipeline.ExecuteBatch(context.Background(),
		[]tool.Call{callOf("a"), callOf("b"), callOf("mid"), callOf("c")}, 4)
	if peak := shared.peak(); peak != 2 {
		t.Fatalf("peak concurrency = %d, want 2 (a+b overlap, mid alone, c alone)", peak)
	}
}

func TestExecuteBatchUnknownToolIsExclusiveAndReported(t *testing.T) {
	shared := &probe{}
	pipeline := newBatchPipeline(t,
		&probeTool{name: "safe", safe: true, delay: time.Millisecond, probe: shared},
	)
	results := pipeline.ExecuteBatch(context.Background(),
		[]tool.Call{callOf("missing"), callOf("safe")}, 4)
	if !results[0].IsError || !strings.Contains(results[0].Content, "not registered") {
		t.Fatalf("unknown tool result = %+v", results[0])
	}
	if results[1].IsError || results[1].Content != "done safe" {
		t.Fatalf("second result = %+v", results[1])
	}
}

func TestExecuteBatchReportsSkippedCallsAfterCancel(t *testing.T) {
	shared := &probe{}
	pipeline := newBatchPipeline(t,
		&probeTool{name: "safe", safe: true, delay: time.Millisecond, probe: shared},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := pipeline.ExecuteBatch(ctx, []tool.Call{callOf("safe"), callOf("safe")}, 4)
	for index, result := range results {
		if !result.IsError || !strings.Contains(result.Content, "was not started") {
			t.Fatalf("results[%d] = %+v, want a skipped error result", index, result)
		}
	}
	if started := len(shared.order()); started != 0 {
		t.Fatalf("started %d calls after cancellation, want 0", started)
	}
}

func TestExecuteBatchEmpty(t *testing.T) {
	pipeline := newBatchPipeline(t)
	if results := pipeline.ExecuteBatch(context.Background(), nil, 4); len(results) != 0 {
		t.Fatalf("results = %v", results)
	}
}

// TestExecuteBatchRunsConcurrently proves the wall clock overlaps: six 40ms
// calls with a limit of 6 must finish in far less than the 240ms a serial run
// would need.
func TestExecuteBatchRunsConcurrently(t *testing.T) {
	shared := &probe{}
	var tools []tool.Tool
	var calls []tool.Call
	for index := 0; index < 6; index++ {
		name := fmt.Sprintf("safe%d", index)
		tools = append(tools, &probeTool{name: name, safe: true, delay: 40 * time.Millisecond, probe: shared})
		calls = append(calls, callOf(name))
	}
	pipeline := newBatchPipeline(t, tools...)

	start := time.Now()
	pipeline.ExecuteBatch(context.Background(), calls, 6)
	elapsed := time.Since(start)
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("elapsed = %s, want the batch to overlap (serial would be ~240ms)", elapsed)
	}
	if peak := shared.peak(); peak != 6 {
		t.Fatalf("peak concurrency = %d, want 6", peak)
	}
}
