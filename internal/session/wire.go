package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

// ContentBlock is one block of a v4 message payload.
type ContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// MessageSource records who produced a surface message.
type MessageSource struct {
	Kind     string `json:"kind"`
	CallID   string `json:"callId,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// SurfaceMessage is the model-facing message carried by a surface event.
type SurfaceMessage struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	Content    []ContentBlock `json:"content"`
	Source     MessageSource  `json:"source"`
	IsError    bool           `json:"isError,omitempty"`
}

// AssistantMessageData is the assistant/message payload.
type AssistantMessageData struct {
	Turn    int            `json:"turn"`
	Step    int            `json:"step"`
	Message SurfaceMessage `json:"message"`
	Stream  []any          `json:"stream"`
}

// ToolResultData is the tool/result payload.
type ToolResultData struct {
	Turn    int            `json:"turn"`
	Step    int            `json:"step"`
	Message SurfaceMessage `json:"message"`
	// Code is a stable machine-readable error code, present only when the
	// result is an error (MIGRATION_MAP §6-8).
	Code string `json:"code,omitempty"`
}

// SystemMessageData is the system/message and developer/message payload.
type SystemMessageData struct {
	Turn    int            `json:"turn"`
	Step    int            `json:"step"`
	Message SurfaceMessage `json:"message"`
}

// ToolCallData is the log-only tool/call payload.
type ToolCallData struct {
	Turn      int    `json:"turn"`
	Step      int    `json:"step"`
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// TurnMarker is the turn/start payload.
type TurnMarker struct {
	Turn int `json:"turn"`
}

// Request header reasons, mirroring the upstream vocabulary: the log's first
// header, a loop's first header over existing history, a changed envelope and a
// new message series.
const (
	RequestHeaderInitial = "initial"
	RequestHeaderResume  = "resume"
	RequestHeaderChange  = "change"
	RequestHeaderSeries  = "series"
)

// CallConfigData is the call configuration recorded in a request header.
type CallConfigData struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ToolSchemaData is one tool declaration recorded in a request header.
type ToolSchemaData struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// EpochHeaderData is the self-contained request envelope: the call config plus
// the exact tool schemas, so a reader can rebuild the request without replaying
// adapter configuration.
type EpochHeaderData struct {
	Config CallConfigData   `json:"config"`
	Tools  []ToolSchemaData `json:"tools,omitempty"`
}

// RequestHeaderData is the request/header payload.
type RequestHeaderData struct {
	Header       EpochHeaderData `json:"header"`
	Reason       string          `json:"reason"`
	StartsSeries bool            `json:"startsSeries,omitempty"`
}

// RequestContextData is the request/context payload: the route metadata that is
// not part of the call config.
type RequestContextData struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// StepMarker is the step/start and step/end payload.
type StepMarker struct {
	Turn int `json:"turn"`
	Step int `json:"step"`
}

// TurnEndReason is the structured reason of a finished turn. Only the fields the
// kind uses are set: a failed turn carries the failure, a cancelled turn carries
// the cancellation cause.
type TurnEndReason struct {
	Kind   string              `json:"kind"`
	Error  *TurnEndFailure     `json:"error,omitempty"`
	Reason *TurnEndCancelCause `json:"reason,omitempty"`
}

// TurnEndFailure is the structured failure a failed turn records: the provider
// neutral code and the rendered message. The upstream type is the same
// LlmFailure object an llm/retry event carries.
type TurnEndFailure struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// TurnEndCancelCause names why a turn was cancelled.
type TurnEndCancelCause struct {
	Kind string `json:"kind"`
}

// TurnEndData is the turn/end payload.
type TurnEndData struct {
	Turn   int           `json:"turn"`
	Reason TurnEndReason `json:"reason"`
}

// NewUserMessage builds the surface message of a user prompt.
func NewUserMessage(text string) SurfaceMessage {
	return SurfaceMessage{
		ID:      NewMessageID("u"),
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: text}},
		Source:  MessageSource{Kind: "user"},
	}
}

// NewAssistantMessage builds the surface message of a model response, mapping
// text to a text block and every tool call to a tool-call block.
func NewAssistantMessage(message llm.Message) SurfaceMessage {
	blocks := make([]ContentBlock, 0, 1+len(message.ToolCalls))
	if message.Content != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: message.Content})
	}
	for _, call := range message.ToolCalls {
		blocks = append(blocks, ContentBlock{
			Type:      "tool-call",
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return SurfaceMessage{
		ID:      NewMessageID("a"),
		Role:    "assistant",
		Content: blocks,
		Source:  MessageSource{Kind: "model"},
	}
}

// NewToolResultMessage builds the surface message of one tool result.
func NewToolResultMessage(callID, content string, isError bool) SurfaceMessage {
	return SurfaceMessage{
		ID:         NewMessageID("t"),
		Role:       "tool",
		ToolCallID: callID,
		Content:    []ContentBlock{{Type: "text", Text: content}},
		Source:     MessageSource{Kind: "tool", CallID: callID},
		IsError:    isError,
	}
}

// NewSystemMessage builds the surface message of a system instruction.
func NewSystemMessage(text string) SurfaceMessage {
	return SurfaceMessage{
		ID:      NewMessageID("s"),
		Role:    "system",
		Content: []ContentBlock{{Type: "text", Text: text}},
		Source:  MessageSource{Kind: "user"},
	}
}

// NewMessageID returns a short unique identifier for a surface message.
func NewMessageID(prefix string) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(random))
}
