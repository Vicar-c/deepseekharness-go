// Package agent runs the model-driven loop: call the LLM, execute the tools it
// requests, feed the results back, and repeat until the model answers.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// Events published by the loop.
const (
	EventAgentStarted  = "agent/started"
	EventAgentStep     = "agent/step"
	EventAgentFinished = "agent/finished"
)

// DefaultSystemPrompt is used when Options.SystemPrompt is empty.
const DefaultSystemPrompt = `You are DeepSeek Harness (dsh), a Go-rewritten agent harness.
Work inside the configured workspace. Prefer tools over guessing: read files before editing,
run commands to verify your work, and keep answers concise. Tool results arrive as user-visible
tool messages; when you are done, reply with the final answer only.`

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
}

// StepEvent is the "agent/step" payload.
type StepEvent struct {
	Iteration int
	ToolCalls int
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
	started := time.Now()
	turn := l.opts.Session.Turn + 1
	l.opts.Session.Turn = turn
	l.emit(EventAgentStarted, map[string]any{"sessionId": l.opts.Session.ID, "input": input})

	if err := l.record("turn/start", session.TurnMarker{Turn: turn}); err != nil {
		return "", err
	}
	if err := l.appendSurface("user/message", session.NewUserMessage(input), llm.TextMessage(llm.RoleUser, input)); err != nil {
		return "", err
	}

	openStep := 0
	var lastErr error
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
		response, err := l.opts.Client.Chat(ctx, request)
		if err != nil {
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
			if err := l.closeTurn(turn, "completed"); err != nil {
				return "", err
			}
			l.emit(EventAgentFinished, FinishedEvent{
				SessionID:  l.opts.Session.ID,
				Iterations: iteration,
				Duration:   time.Since(started).Milliseconds(),
			})
			return response.Message.Content, nil
		}
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
			result := l.opts.Pipeline.Execute(ctx, tool.Call{ID: call.ID, Name: call.Function.Name, Arguments: arguments})
			message := llm.ToolResultMessage(call.ID, call.Function.Name, result.Content)
			data := session.ToolResultData{
				Turn:    turn,
				Step:    iteration,
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
	if err := l.closeTurn(turn, "error"); err != nil {
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

func (l *Loop) closeStep(turn, step int) error {
	return l.record("step/end", session.StepMarker{Turn: turn, Step: step})
}

func (l *Loop) closeTurn(turn int, kind string) error {
	return l.record("turn/end", session.TurnEndData{Turn: turn, Reason: session.TurnEndReason{Kind: kind}})
}

func (l *Loop) messages() []llm.Message {
	messages := make([]llm.Message, 0, len(l.opts.Session.Messages)+1)
	messages = append(messages, llm.TextMessage(llm.RoleSystem, l.opts.SystemPrompt))
	messages = append(messages, l.opts.Session.Messages...)
	return messages
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
