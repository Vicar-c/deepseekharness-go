// Package agent runs the model-driven loop: call the LLM, execute the tools it
// requests, feed the results back, and repeat until the model answers.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/prompt"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// Events published by the loop.
const (
	EventAgentStarted  = "agent/started"
	EventAgentStep     = "agent/step"
	EventAgentStream   = "agent/stream"
	EventAgentRetry    = "agent/retry"
	EventAgentFinished = "agent/finished"
)

// DefaultSystemPrompt is used when Options.SystemPrompt is empty.
const DefaultSystemPrompt = prompt.Base

// Options configures an agent Loop.
type Options struct {
	Client        llm.Client
	Pipeline      *tool.Pipeline
	Session       *session.Session
	Store         *session.Store
	Events        *kernel.Events
	Logger        *slog.Logger
	SystemPrompt  string
	MaxIterations int
	Model         string
	// Provider names the model route recorded in request/context.
	Provider string
	// ContextWindow is the route's advertised context size, when known.
	ContextWindow int
	// MaxTokens bounds the completion length of one model call. Zero leaves the
	// provider default in place.
	MaxTokens int
	// MaxParallelToolCalls bounds the overlap of concurrency-safe tool calls in
	// one model step. Zero uses the tool pipeline default (10); 1 forces serial.
	MaxParallelToolCalls int
	// RetryPolicy decides which failed model calls are repeated. Nil uses the
	// built-in normal policy.
	RetryPolicy *llm.RetryPolicy
	// Random samples [0,1) for retry jitter; nil means the policy applies no
	// jitter, which keeps tests deterministic.
	Random func() float64
}

// StepEvent is the "agent/step" payload.
type StepEvent struct {
	Iteration int
	ToolCalls int
}

// StreamEvent is the "agent/stream" payload: one incremental delta of the model
// response. These events are transient (they are not persisted); the complete
// message is persisted once the call finishes.
type StreamEvent struct {
	SessionID    string
	Content      string
	ToolCallID   string
	ToolName     string
	ToolArgs     string
	FinishReason string
}

// FinishedEvent is the "agent/finished" payload.
type FinishedEvent struct {
	SessionID  string
	Iterations int
	Duration   int64 // milliseconds
	Err        error
}

// RetryEvent is the "agent/retry" payload for one scheduled model-call retry.
type RetryEvent struct {
	SessionID string
	Turn      int
	Step      int
	// Retry counts retries within the step, starting at 1.
	Retry      int
	MaxRetries int
	// Mode is the policy mode ("normal" or "always").
	Mode string
	Code string
	// Delay is how long the loop waits before the next attempt.
	Delay time.Duration
}

// retryEventData is the llm/retry payload. Its field names match the upstream
// LlmRetryEventData, so both implementations record the same event shape.
type retryEventData struct {
	RetryID    string       `json:"retryId"`
	Turn       int          `json:"turn"`
	Step       int          `json:"step"`
	Provider   string       `json:"provider"`
	Mode       string       `json:"mode"`
	PolicyKey  string       `json:"policyKey"`
	Retry      int          `json:"retry"`
	MaxRetries int          `json:"maxRetries,omitempty"`
	DelayMs    int64        `json:"delayMs"`
	Failure    *llm.Failure `json:"failure"`
}

// retryStartedData is the llm/retry-started payload: the durable transition
// written after the wait succeeded and before the next attempt starts.
type retryStartedData struct {
	RetryID string `json:"retryId"`
	Turn    int    `json:"turn"`
	Step    int    `json:"step"`
	Retry   int    `json:"retry"`
}

// Loop is one agent conversation bound to one session.
type Loop struct {
	opts        Options
	definitions []llm.ToolDefinition
	// headerFingerprint is the last written request envelope; unchanged steps
	// inherit it instead of duplicating the tool schemas into the log.
	headerFingerprint string
	headerWritten     bool
	contextRecorded   bool
}

// New validates dependencies and builds a loop.
func New(options Options) (*Loop, error) {
	if options.Client == nil {
		return nil, errors.New("agent: llm client is required")
	}
	if options.Pipeline == nil {
		return nil, errors.New("agent: tool pipeline is required")
	}
	if options.Session == nil {
		return nil, errors.New("agent: session is required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if strings.TrimSpace(options.SystemPrompt) == "" {
		options.SystemPrompt = DefaultSystemPrompt
	}
	if options.MaxIterations <= 0 {
		options.MaxIterations = 12
	}
	if options.Model == "" {
		options.Model = options.Client.Model()
	}
	loop := &Loop{opts: options}
	loop.definitions = toolDefinitions(options.Pipeline.Registry().Definitions())
	return loop, nil
}

// Run executes one user turn to completion and returns the final assistant text.
func (l *Loop) Run(ctx context.Context, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("agent: empty input")
	}
	// One turn at a time per session: without the lease, two concurrent runs
	// interleave turn/start and step events into one log.
	if l.opts.Store != nil {
		release, err := l.opts.Store.BeginTurn(l.opts.Session.ID)
		if err != nil {
			return "", err
		}
		defer release()
	}
	started := time.Now()
	turn := l.opts.Session.Turn + 1
	l.opts.Session.Turn = turn
	l.emit(EventAgentStarted, map[string]any{"sessionId": l.opts.Session.ID, "input": input})

	if err := l.record("turn/start", session.TurnMarker{Turn: turn}); err != nil {
		return "", err
	}

	openStep := 0
	var lastErr error
	cancelled := false
	for iteration := 1; iteration <= l.opts.MaxIterations; iteration++ {
		if err := l.record("step/start", session.StepMarker{Turn: turn, Step: iteration}); err != nil {
			return "", err
		}
		openStep = iteration
		// The system prompt and the user message belong to the first step: the
		// upstream format requires a surface event inside an open step, and the
		// first surface event must be the system message.
		if iteration == 1 {
			if !l.hasSystemMessage() {
				prompt := l.opts.SystemPrompt
				if err := l.appendSurface("system/message",
					session.SystemMessageData{Turn: turn, Step: 1, Message: session.NewSystemMessage(prompt)},
					llm.TextMessage(llm.RoleSystem, prompt)); err != nil {
					return "", err
				}
			}
			if err := l.appendSurface("user/message", session.NewUserMessage(input), llm.TextMessage(llm.RoleUser, input)); err != nil {
				return "", err
			}
		}
		request := llm.Request{
			Model:     l.opts.Model,
			Messages:  l.messages(),
			Tools:     l.definitions,
			MaxTokens: l.opts.MaxTokens,
		}
		if err := l.recordEnvelope(turn, iteration, request); err != nil {
			return "", err
		}
		response, partial, err := l.chatWithRetry(ctx, turn, iteration, request)
		if err != nil {
			cancelled = errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
			// Text the user already saw belongs in the log even though the turn
			// failed: otherwise the next request claims the model said nothing,
			// while the screen shows a half answer.
			if cancelled && strings.TrimSpace(partial) != "" {
				if recordErr := l.recordPartialAnswer(turn, iteration, partial); recordErr != nil {
					lastErr = errors.Join(fmt.Errorf("agent: model call: %w", err), recordErr)
					break
				}
			}
			lastErr = fmt.Errorf("agent: model call: %w", err)
			break
		}
		assistant := session.AssistantMessageData{
			Turn:    turn,
			Step:    iteration,
			Message: session.NewAssistantMessage(response.Message),
			Stream:  []any{},
		}
		if err := l.appendSurface("assistant/message", assistant, response.Message); err != nil {
			return "", err
		}
		if len(response.Message.ToolCalls) == 0 {
			if err := l.closeStep(turn, iteration); err != nil {
				return "", err
			}
			openStep = 0
			// A response cut off by the output-token ceiling still ends the turn,
			// but the log records why the answer stopped instead of claiming a
			// plain completion.
			reason := session.TurnEndReason{Kind: session.TurnEndCompleted}
			if response.FinishReason == finishReasonLength {
				reason.Kind = session.TurnEndMaxTokens
			}
			if err := l.closeTurn(turn, reason); err != nil {
				return "", err
			}
			l.emit(EventAgentFinished, FinishedEvent{
				SessionID:  l.opts.Session.ID,
				Iterations: iteration,
				Duration:   time.Since(started).Milliseconds(),
			})
			return response.Message.Content, nil
		}
		batch := make([]tool.Call, 0, len(response.Message.ToolCalls))
		for _, call := range response.Message.ToolCalls {
			arguments := normalizeArguments(call.Function.Arguments)
			toolCall := session.ToolCallData{
				Turn:      turn,
				Step:      iteration,
				CallID:    call.ID,
				Name:      call.Function.Name,
				Arguments: string(arguments),
			}
			if err := l.record("tool/call", toolCall); err != nil {
				return "", err
			}
			batch = append(batch, tool.Call{ID: call.ID, Name: call.Function.Name, Arguments: arguments})
		}
		// The batch is recorded before it runs: every tool/call is durable while
		// the work happens, and results are appended in model order afterwards —
		// completion order never leaks into the model history.
		results := l.opts.Pipeline.ExecuteBatch(ctx, batch, l.opts.MaxParallelToolCalls)
		for index, call := range response.Message.ToolCalls {
			result := results[index]
			// State a tool changed outside the conversation is recorded before its
			// result, so replaying the log applies the same state a reader expects
			// and the ordering is deterministic regardless of batch concurrency.
			for _, state := range result.State {
				if setter, ok := state.Data.(tool.TurnStepSetter); ok {
					setter.SetTurnStep(turn, iteration)
				}
				if err := l.record(state.Type, state.Data); err != nil {
					return "", err
				}
			}
			message := llm.ToolResultMessage(call.ID, call.Function.Name, result.Content)
			data := session.ToolResultData{
				Turn:    turn,
				Step:    iteration,
				Code:    result.Code,
				Message: session.NewToolResultMessage(call.ID, result.Content, result.IsError),
			}
			if err := l.appendSurface("tool/result", data, message); err != nil {
				return "", err
			}
		}
		if err := l.closeStep(turn, iteration); err != nil {
			return "", err
		}
		openStep = 0
		l.emit(EventAgentStep, StepEvent{Iteration: iteration, ToolCalls: len(response.Message.ToolCalls)})
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("agent: exceeded %d iterations without a final answer", l.opts.MaxIterations)
	}
	if openStep != 0 {
		if err := l.closeStep(turn, openStep); err != nil {
			lastErr = errors.Join(lastErr, err)
		}
	}
	if err := l.closeTurn(turn, turnEndReason(lastErr, cancelled)); err != nil {
		lastErr = errors.Join(lastErr, err)
	}
	l.emit(EventAgentFinished, FinishedEvent{
		SessionID:  l.opts.Session.ID,
		Iterations: l.opts.MaxIterations,
		Duration:   time.Since(started).Milliseconds(),
		Err:        lastErr,
	})
	return "", lastErr
}

// Session exposes the conversation this loop writes to.
func (l *Loop) Session() *session.Session { return l.opts.Session }

// finishReasonLength is the provider finish reason of a response cut off by the
// output-token ceiling.
const finishReasonLength = "length"

// chatWithRetry calls the model and repeats a failed attempt when the retry
// policy allows it. Every scheduled wait is durable before it starts: llm/retry
// is recorded first, then the cancellable wait runs, then llm/retry-started
// marks the attempt that follows.
//
// An attempt that already streamed a chunk is not repeated, because the deltas
// have been shown to the user and the event bus cannot take them back. The
// partial text of a failed attempt is returned so the caller can record what the
// user actually saw.
func (l *Loop) chatWithRetry(ctx context.Context, turn, step int, request llm.Request) (llm.Response, string, error) {
	policy := l.opts.RetryPolicy
	if policy == nil {
		policy = llm.DefaultRetryPolicy()
	}
	retryID := ""
	for retry := 0; ; retry++ {
		attempt, err := l.chat(ctx, request)
		if err == nil {
			return attempt.response, "", nil
		}
		failure := llm.Classify(err)
		if ctx.Err() != nil || attempt.chunks > 0 || !policy.Retryable(failure.Code) || policy.Exhausted(retry) {
			return llm.Response{}, attempt.text, err
		}
		delay, ok := policy.Delay(retry+1, failure.RetryAfter(), l.opts.Random)
		if !ok {
			return llm.Response{}, attempt.text, err
		}
		if retryID == "" {
			retryID = newRetryID()
		}
		data := retryEventData{
			RetryID:   retryID,
			Turn:      turn,
			Step:      step,
			Provider:  l.provider(),
			Mode:      policy.Mode,
			PolicyKey: policy.Key(),
			Retry:     retry + 1,
			DelayMs:   delay.Milliseconds(),
			Failure:   failure,
		}
		if policy.Mode != llm.RetryModeAlways {
			data.MaxRetries = policy.MaxRetries
		}
		if err := l.record("llm/retry", data); err != nil {
			return llm.Response{}, attempt.text, err
		}
		l.emit(EventAgentRetry, RetryEvent{
			SessionID:  l.opts.Session.ID,
			Turn:       turn,
			Step:       step,
			Retry:      retry + 1,
			MaxRetries: policy.MaxRetries,
			Mode:       policy.Mode,
			Code:       failure.Code,
			Delay:      delay,
		})
		if err := sleepContext(ctx, delay); err != nil {
			return llm.Response{}, attempt.text, err
		}
		if err := l.record("llm/retry-started", retryStartedData{
			RetryID: retryID,
			Turn:    turn,
			Step:    step,
			Retry:   retry + 1,
		}); err != nil {
			return llm.Response{}, attempt.text, err
		}
	}
}

// sleepContext waits for delay, or returns the context error when the turn is
// cancelled first.
func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// newRetryID builds the identity one retry chain carries.
func newRetryID() string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("retry-%d", time.Now().UnixNano())
	}
	return "retry-" + hex.EncodeToString(random)
}

// attempt is the outcome of one model call: the assembled response plus what
// already reached the user, so a failed attempt can be recorded honestly.
type attempt struct {
	response llm.Response
	text     string
	chunks   int
}

// chat calls the model, streaming deltas to the event bus when the client
// supports it and falling back to one blocking call otherwise. Both paths return
// the same fully assembled response; the chunk count tells the retry logic
// whether the attempt already reached the user, and the text is the prefix the
// user saw.
func (l *Loop) chat(ctx context.Context, request llm.Request) (attempt, error) {
	streaming, ok := l.opts.Client.(llm.StreamingClient)
	if !ok {
		response, err := l.opts.Client.Chat(ctx, request)
		return attempt{response: response}, err
	}
	var output strings.Builder
	outcome := attempt{}
	response, err := streaming.ChatStream(ctx, request, func(chunk llm.Chunk) error {
		if chunk.Content == "" && chunk.ToolCallID == "" && chunk.ToolName == "" &&
			chunk.ToolArgs == "" && chunk.FinishReason == "" {
			return nil
		}
		outcome.chunks++
		output.WriteString(chunk.Content)
		l.emit(EventAgentStream, StreamEvent{
			SessionID:    l.opts.Session.ID,
			Content:      chunk.Content,
			ToolCallID:   chunk.ToolCallID,
			ToolName:     chunk.ToolName,
			ToolArgs:     chunk.ToolArgs,
			FinishReason: chunk.FinishReason,
		})
		return nil
	})
	outcome.response = response
	outcome.text = output.String()
	return outcome, err
}

// recordPartialAnswer persists the prefix of an answer the user already saw
// before the turn was cancelled, so the log explains the text on screen and the
// next request does not pretend the model said nothing.
func (l *Loop) recordPartialAnswer(turn, step int, text string) error {
	message := llm.TextMessage(llm.RoleAssistant, text)
	data := session.AssistantMessageData{
		Turn:    turn,
		Step:    step,
		Message: session.NewAssistantMessage(message),
		Stream:  []any{},
	}
	return l.appendSurface("assistant/message", data, message)
}

// record persists one log-only event. Without a store the loop stays in memory.
func (l *Loop) record(eventType string, data any) error {
	if l.opts.Store == nil {
		return nil
	}
	if err := l.opts.Store.AppendEvent(l.opts.Session, eventType, data, nil, nil); err != nil {
		return fmt.Errorf("agent: persist %s: %w", eventType, err)
	}
	return nil
}

// appendSurface persists a surface event and mirrors it into the in-memory
// message list that feeds the next request.
func (l *Loop) appendSurface(eventType string, data any, message llm.Message) error {
	if l.opts.Store != nil {
		op := session.Append
		if err := l.opts.Store.AppendEvent(l.opts.Session, eventType, data, &op, nil); err != nil {
			return fmt.Errorf("agent: persist %s: %w", eventType, err)
		}
	}
	l.opts.Session.Messages = append(l.opts.Session.Messages, message)
	return nil
}

// closeStep ends one step and flushes it: a completed step must survive a crash,
// so the durability checkpoint sits at the batch boundary rather than on every
// event.
func (l *Loop) closeStep(turn, step int) error {
	if err := l.record("step/end", session.StepMarker{Turn: turn, Step: step}); err != nil {
		return err
	}
	return l.flush()
}

func (l *Loop) closeTurn(turn int, reason session.TurnEndReason) error {
	if err := l.record("turn/end", session.TurnEndData{Turn: turn, Reason: reason}); err != nil {
		return err
	}
	return l.flush()
}

// flush makes the session durable. Without a store the loop is memory-only and
// there is nothing to flush.
func (l *Loop) flush() error {
	if l.opts.Store == nil {
		return nil
	}
	if err := l.opts.Store.Flush(l.opts.Session); err != nil {
		return fmt.Errorf("agent: flush session: %w", err)
	}
	return nil
}

// turnEndReason maps a failed turn to its structured reason: a cancelled
// context means the user interrupted the turn, anything else is a real failure
// that records the provider-neutral code next to its message.
func turnEndReason(err error, cancelled bool) session.TurnEndReason {
	if cancelled {
		return session.TurnEndReason{
			Kind:   session.TurnEndAborted,
			Reason: &session.TurnEndCancelCause{Kind: session.CancelCauseUser},
		}
	}
	failure := llm.Classify(err)
	if failure == nil {
		failure = &llm.Failure{Message: "agent: turn failed", Code: llm.CodeUnknown}
	}
	code := failure.Code
	if code == "" {
		code = llm.CodeUnknown
	}
	return session.TurnEndReason{
		Kind: session.TurnEndError,
		Error: &session.TurnEndFailure{
			Message:              failure.Message,
			Code:                 code,
			Status:               failure.Status,
			ProviderRetryAfterMs: failure.ProviderRetryAfterMs,
			RequestID:            failure.RequestID,
		},
	}
}

func (l *Loop) messages() []llm.Message {
	if len(l.opts.Session.Messages) > 0 && l.opts.Session.Messages[0].Role == llm.RoleSystem {
		return append([]llm.Message(nil), l.opts.Session.Messages...)
	}
	messages := make([]llm.Message, 0, len(l.opts.Session.Messages)+1)
	messages = append(messages, llm.TextMessage(llm.RoleSystem, l.opts.SystemPrompt))
	messages = append(messages, l.opts.Session.Messages...)
	return messages
}

// hasSystemMessage reports whether the recorded history already carries the
// system prompt, which is what keeps a resumed session from appending it twice.
func (l *Loop) hasSystemMessage() bool {
	return len(l.opts.Session.Messages) > 0 && l.opts.Session.Messages[0].Role == llm.RoleSystem
}

func (l *Loop) provider() string {
	if strings.TrimSpace(l.opts.Provider) != "" {
		return l.opts.Provider
	}
	return "deepseek"
}

// recordEnvelope writes request/header when the request envelope changes, and
// request/context once per loop. The header is self-contained (call config plus
// exact tool schemas), so the log can rebuild the request without replaying
// adapter configuration; unchanged steps inherit the latest header.
func (l *Loop) recordEnvelope(turn, step int, request llm.Request) error {
	header := session.EpochHeaderData{
		Config: session.CallConfigData{Provider: l.provider(), Model: request.Model},
	}
	for _, definition := range request.Tools {
		header.Tools = append(header.Tools, session.ToolSchemaData{
			Name:        definition.Function.Name,
			Description: definition.Function.Description,
			Parameters:  definition.Function.Parameters,
		})
	}
	encoded, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("agent: encode request header: %w", err)
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if fingerprint == l.headerFingerprint {
		return nil
	}
	reason := session.RequestHeaderChange
	if !l.headerWritten {
		reason = session.RequestHeaderResume
		if turn == 1 {
			reason = session.RequestHeaderInitial
		}
	}
	if err := l.record("request/header", session.RequestHeaderData{Header: header, Reason: reason}); err != nil {
		return err
	}
	l.headerWritten = true
	l.headerFingerprint = fingerprint

	if !l.contextRecorded {
		context := session.RequestContextData{
			Provider:      header.Config.Provider,
			Model:         header.Config.Model,
			ContextWindow: l.opts.ContextWindow,
		}
		if err := l.record("request/context", context); err != nil {
			return err
		}
		l.contextRecorded = true
	}
	return nil
}

func (l *Loop) emit(name string, payload any) {
	if l.opts.Events != nil {
		l.opts.Events.Emit(name, payload)
	}
}

func toolDefinitions(definitions []tool.Definition) []llm.ToolDefinition {
	converted := make([]llm.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		converted = append(converted, llm.ToolDefinition{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		})
	}
	return converted
}

func normalizeArguments(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}
