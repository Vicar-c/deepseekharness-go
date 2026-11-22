package tool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
)

// EventToolStarted and EventToolExecuted are the pipeline event names.
// EventToolPreExecute is the waterfall every call passes before it is
// dispatched: a layer may veto the call (returning without calling next) or
// hand the call on with next. The name is the upstream extension point, so a
// future policy, approval or sandbox layer plugs into the same seam.
const (
	EventToolPreExecute = "tools/pre-execute"
	EventToolStarted    = "tool/started"
	EventToolExecuted   = "tool/executed"
)

// PipelineOptions configures a Pipeline.
type PipelineOptions struct {
	Logger         *slog.Logger
	Events         *kernel.Events
	Timeout        time.Duration
	MaxOutputBytes int
	// MaxParallel bounds how many concurrency-safe calls overlap inside one
	// batch. Zero uses the default (10); 1 forces serial execution.
	MaxParallel int
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
	maxParallel    int
}

// defaultMaxParallel mirrors the upstream agent-loop default.
const defaultMaxParallel = 10

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
	maxParallel := options.MaxParallel
	if maxParallel <= 0 {
		maxParallel = defaultMaxParallel
	}
	events := options.Events
	if events == nil {
		// A pipeline without a shared bus still owns its guards, so it keeps a
		// private one instead of checking for nil at every dispatch.
		events = kernel.NewEvents()
	}
	return &Pipeline{
		registry:       registry,
		logger:         logger,
		events:         events,
		timeout:        timeout,
		maxOutputBytes: maxOutput,
		maxParallel:    maxParallel,
	}
}

// Registry returns the wrapped registry.
func (p *Pipeline) Registry() *Registry { return p.registry }

// AddPreExecute registers one layer of the pre-execute waterfall. Layers run in
// registration order before a call is dispatched; a layer that returns without
// calling next vetoes the call and its value (normally an error Result) is
// returned instead. The unsubscribe function is idempotent.
func (p *Pipeline) AddPreExecute(handler kernel.WaterfallHandler) (unsubscribe func()) {
	return p.events.OnWaterfall(EventToolPreExecute, handler)
}

// AddGuard appends a guard. Guards run in registration order and may veto a
// call by returning an error. A guard is the simple case of a pre-execute
// layer: it sees the call and either allows it or blocks it.
func (p *Pipeline) AddGuard(guard Guard) {
	if guard == nil {
		return
	}
	p.AddPreExecute(func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		call, ok := payload.(Call)
		if !ok {
			return next(ctx)
		}
		if err := guard(ctx, call); err != nil {
			return blockedResult(call, err), nil
		}
		return next(ctx)
	})
}

// blockedResult is the result of a call a guard or waterfall layer vetoed.
func blockedResult(call Call, err error) Result {
	return Result{IsError: true, Code: CodeBlocked, Content: fmt.Sprintf("tool %q blocked: %v", call.Name, err)}
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
			result = Result{
				IsError: true,
				Code:    CodePanicked,
				Content: fmt.Sprintf("tool %q panicked: %v", call.Name, recovered),
			}
		}
	}()

	value, err := p.events.Waterfall(ctx, EventToolPreExecute, call, func(ctx context.Context) (any, error) {
		return p.dispatch(ctx, call), nil
	})
	if err != nil {
		var panicked *kernel.PanicError
		if errors.As(err, &panicked) {
			return Result{
				IsError: true,
				Code:    CodePanicked,
				Content: fmt.Sprintf("tool %q panicked: %v", call.Name, panicked.Value),
			}
		}
		return blockedResult(call, err)
	}
	dispatched, ok := value.(Result)
	if !ok {
		return Result{
			IsError: true,
			Code:    CodeBlocked,
			Content: fmt.Sprintf("tool %q: a pre-execute layer returned %T instead of a tool result", call.Name, value),
		}
	}
	return dispatched
}

// dispatch runs the tool itself: lookup, deadline and error mapping.
func (p *Pipeline) dispatch(ctx context.Context, call Call) Result {
	tool, ok := p.registry.Get(call.Name)
	if !ok {
		return Result{IsError: true, Code: CodeNotFound, Content: fmt.Sprintf("tool %q is not registered", call.Name)}
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	result, err := tool.Execute(callCtx, call.Arguments)
	if err != nil {
		message := err.Error()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Result{
				IsError: true,
				Code:    CodeTimeout,
				Content: fmt.Sprintf("tool %q timed out after %s", call.Name, p.timeout),
			}
		}
		return Result{IsError: true, Content: fmt.Sprintf("tool %q failed: %s", call.Name, message)}
	}
	return result
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
