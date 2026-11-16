package tool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
)

// EventToolStarted and EventToolExecuted are the pipeline event names.
const (
	EventToolStarted  = "tool/started"
	EventToolExecuted = "tool/executed"
)

// PipelineOptions configures a Pipeline.
type PipelineOptions struct {
	Logger         *slog.Logger
	Events         *kernel.Events
	Timeout        time.Duration
	MaxOutputBytes int
}

// Pipeline executes tool calls with guards, timeouts, panic recovery, output
// limits and event emission. Execute always returns a Result: tool failures are
// reported to the model as IsError results instead of aborting the agent loop.
type Pipeline struct {
	registry       *Registry
	logger         *slog.Logger
	events         *kernel.Events
	timeout        time.Duration
	maxOutputBytes int

	mu     sync.RWMutex
	guards []Guard
}

// NewPipeline creates a pipeline around a registry.
func NewPipeline(registry *Registry, options PipelineOptions) *Pipeline {
	if registry == nil {
		registry = NewRegistry()
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxOutput := options.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = 128 << 10
	}
	return &Pipeline{
		registry:       registry,
		logger:         logger,
		events:         options.Events,
		timeout:        timeout,
		maxOutputBytes: maxOutput,
	}
}

// Registry returns the wrapped registry.
func (p *Pipeline) Registry() *Registry { return p.registry }

// AddGuard appends a guard. Guards run in registration order and may veto a
// call by returning an error.
func (p *Pipeline) AddGuard(guard Guard) {
	if guard == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.guards = append(p.guards, guard)
}

// Execute runs one tool call and never fails: every failure becomes an error
// Result the model can react to.
func (p *Pipeline) Execute(ctx context.Context, call Call) Result {
	started := time.Now()
	p.emit(EventToolStarted, call)

	result := p.execute(ctx, call)
	elapsed := time.Since(started)

	result.Content = p.clampOutput(result.Content)
	p.emit(EventToolExecuted, ExecutionEvent{
		Call:     call,
		Result:   result,
		Duration: elapsed.Milliseconds(),
	})
	p.logger.Debug("tool executed",
		"tool", call.Name,
		"error", result.IsError,
		"duration_ms", elapsed.Milliseconds(),
		"output_bytes", len(result.Content),
	)
	return result
}

func (p *Pipeline) execute(ctx context.Context, call Call) (result Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{IsError: true, Content: fmt.Sprintf("tool %q panicked: %v", call.Name, recovered)}
		}
	}()

	tool, ok := p.registry.Get(call.Name)
	if !ok {
		return Result{IsError: true, Content: fmt.Sprintf("tool %q is not registered", call.Name)}
	}
	for _, guard := range p.snapshotGuards() {
		if err := guard(ctx, call); err != nil {
			return Result{IsError: true, Content: fmt.Sprintf("tool %q blocked: %v", call.Name, err)}
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	result, err := tool.Execute(callCtx, call.Arguments)
	if err != nil {
		message := err.Error()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			message = fmt.Sprintf("timed out after %s", p.timeout)
		}
		return Result{IsError: true, Content: fmt.Sprintf("tool %q failed: %s", call.Name, message)}
	}
	return result
}

func (p *Pipeline) snapshotGuards() []Guard {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Guard(nil), p.guards...)
}

func (p *Pipeline) clampOutput(content string) string {
	if p.maxOutputBytes <= 0 || len(content) <= p.maxOutputBytes {
		return content
	}
	cut := p.maxOutputBytes
	// Avoid splitting a UTF-8 rune at the cut point.
	for cut > 0 && content[cut]&0xC0 == 0x80 {
		cut--
	}
	return strings.TrimRight(content[:cut], "") + fmt.Sprintf("\n...[truncated %d bytes]", len(content)-cut)
}

func (p *Pipeline) emit(name string, payload any) {
	if p.events == nil {
		return
	}
	p.events.Emit(name, payload)
}
