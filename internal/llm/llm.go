// Package llm defines the model-facing contract of the harness: messages,
// tool-call aware requests and the minimal Client interface the agent loop
// depends on. The DeepSeek wire client lives in deepseek.go.
package llm

import (
	"context"
	"encoding/json"
)

// Role is the author of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// FunctionCall is one function invocation emitted by the model.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// Message is one conversation entry. Tool messages carry ToolCallID, assistant
// messages may carry ToolCalls.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// FunctionDefinition describes one callable tool to the model.
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolDefinition is the OpenAI-compatible tool envelope.
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// Request is one chat completion request.
type Request struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
}

// Usage reports token accounting for one response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is one normalized chat completion response.
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Client is the only model dependency of the agent loop.
type Client interface {
	// Model reports the default model name used for requests.
	Model() string
	// Chat performs one non-streaming chat completion.
	Chat(ctx context.Context, request Request) (Response, error)
}

// TextMessage builds a plain text message.
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Content: text}
}

// ToolResultMessage builds the message that carries one tool result back to the
// model.
func ToolResultMessage(callID, name, content string) Message {
	return Message{Role: RoleTool, ToolCallID: callID, Name: name, Content: content}
}

// TextResponse builds a final assistant response.
func TextResponse(text, finishReason string) Response {
	return Response{Message: Message{Role: RoleAssistant, Content: text}, FinishReason: finishReason}
}
