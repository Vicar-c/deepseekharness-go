// Package agent runs the model-driven loop: call the LLM, execute the tools it
// requests, feed the results back, and repeat until the model answers.
package agent

import (
	"context"
	"crypto/sha256"
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
	// MaxParallelToolCalls bounds the overlap of concurrency-safe tool calls in
	// one model step. Zero uses the tool pipeline default (10); 1 forces serial.
	MaxParallelToolCalls int
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
	// The rendered system prompt is recorded once per session, before the first
	// user message, so a resumed conversation replays the same request prefix.
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

	openStep := 0
	var lastErr error
	cancelled := false
	for iteration := 1; iteration <= l.opts.MaxIterations; iteration++ {
		if err := l.record("step/start", session.StepMarker{Turn: turn, Step: iteration}); err != nil {
			return "", err
		}
		openStep = iteration
		request := llm.Request{
			Model:    l.opts.Model,
			Messages: l.messages(),
			Tools:    l.definitions,
		}
		if err := l.recordEnvelope(turn, iteration, request); err != nil {
			return "", err
		}
		response, err := l.chat(ctx, request)
		if err != nil {
			cancelled = errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
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
			if err := l.closeTurn(turn, session.TurnEndCompleted); err != nil {
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
	if err := l.closeTurn(turn, turnEndReason(cancelled)); err != nil {
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

// chat calls the model, streaming deltas to the event bus when the client
// supports it and falling back to one blocking call otherwise. Both paths
// return the same fully assembled response.
func (l *Loop) chat(ctx context.Context, request llm.Request) (llm.Response, error) {
	streaming, ok := l.opts.Client.(llm.StreamingClient)
	if !ok {
		return l.opts.Client.Chat(ctx, request)
	}
	return streaming.ChatStream(ctx, request, func(chunk llm.Chunk) error {
		if chunk.Content == "" && chunk.ToolCallID == "" && chunk.ToolName == "" &&
			chunk.ToolArgs == "" && chunk.FinishReason == "" {
			return nil
		}
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

func (l *Loop) closeTurn(turn int, kind string) error {
	if err := l.record("turn/end", session.TurnEndData{Turn: turn, Reason: session.TurnEndReason{Kind: kind}}); err != nil {
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

// turnEndReason maps a failed turn to its reason: a cancelled context means the
// user interrupted the turn, anything else is a real error.
func turnEndReason(cancelled bool) string {
	if cancelled {
		return session.TurnEndCancelled
	}
	return session.TurnEndError
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
