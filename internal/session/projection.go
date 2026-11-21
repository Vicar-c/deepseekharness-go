package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

// messageBlock is one content block of an upstream message. Only text and
// tool-call blocks are mapped to llm.Message; reasoning, image and plugin
// blocks are ignored by the P1 projection.
type messageBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// upstreamMessage is the message payload carried by the surface events.
type upstreamMessage struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	ToolCallID string         `json:"toolCallId"`
	Content    []messageBlock `json:"content"`
}

// projectedMessage remembers which event seq produced a message, so a
// surfaceOp replace can address surface nodes by sequence number.
type projectedMessage struct {
	seq     int64
	message llm.Message
}

// Project replays the surface events and returns the model-visible messages.
//
// Log-only events (turn/step markers, tool/call, session/title, …) contribute no
// message: tool/call only supplies the tool name that the matching tool/result
// message would otherwise lack. Events carrying surfaceOp "append" extend the
// surface; a replace operation substitutes the nodes in its seq range.
func (r *Recording) Project() ([]llm.Message, error) {
	toolNames := r.toolNames()
	surface := make([]projectedMessage, 0, len(r.Events))
	for _, event := range r.Events {
		if !isSurfaceEventType(event.Type) {
			continue
		}
		message, err := projectEvent(event, toolNames)
		if err != nil {
			return nil, fmt.Errorf("session: project event seq %d (%s): %w", event.Seq, event.Type, err)
		}
		op := SurfaceOp{}
		if event.SurfaceOp != nil {
			op = *event.SurfaceOp
		}
		if !op.Replace {
			surface = append(surface, projectedMessage{seq: event.Seq, message: message})
			continue
		}
		start, end := -1, -1
		for index, item := range surface {
			if item.seq == op.StartSeq {
				start = index
			}
			if item.seq == op.EndSeq {
				end = index
			}
		}
		if start < 0 || end < start {
			return nil, fmt.Errorf("session: surfaceOp replace %d..%d does not match existing surface nodes", op.StartSeq, op.EndSeq)
		}
		replaced := make([]projectedMessage, 0, len(surface)-(end-start))
		replaced = append(replaced, surface[:start]...)
		replaced = append(replaced, projectedMessage{seq: event.Seq, message: message})
		replaced = append(replaced, surface[end+1:]...)
		surface = replaced
	}
	messages := make([]llm.Message, len(surface))
	for index, item := range surface {
		messages[index] = item.message
	}
	return messages, nil
}

// toolNames maps a tool call id to the tool name recorded by tool/call.
func (r *Recording) toolNames() map[string]string {
	names := make(map[string]string)
	for _, event := range r.Events {
		if event.Type != "tool/call" {
			continue
		}
		var data struct {
			CallID string `json:"callId"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil || data.CallID == "" {
			continue
		}
		names[data.CallID] = data.Name
	}
	return names
}

// isSurfaceEventType reports whether a v4 event type may appear on the ordered
// surface and therefore may carry a surfaceOp.
func isSurfaceEventType(name string) bool {
	switch name {
	case "system/message", "developer/message", "user/message", "assistant/message", "tool/result":
		return true
	default:
		return false
	}
}

func projectEvent(event Event, toolNames map[string]string) (llm.Message, error) {
	switch event.Type {
	case "user/message":
		// The user event payload is the message itself.
		var message upstreamMessage
		if err := json.Unmarshal(event.Data, &message); err != nil {
			return llm.Message{}, fmt.Errorf("decode user message: %w", err)
		}
		return llm.Message{Role: llm.RoleUser, Content: blocksText(message.Content)}, nil

	case "assistant/message":
		message, err := nestedMessage(event.Data)
		if err != nil {
			return llm.Message{}, err
		}
		result := llm.Message{Role: llm.RoleAssistant, Content: blocksText(message.Content)}
		for _, block := range message.Content {
			if block.Type != "tool-call" {
				continue
			}
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: llm.FunctionCall{
					Name:      block.Name,
					Arguments: block.Arguments,
				},
			})
		}
		return result, nil

	case "tool/result":
		message, err := nestedMessage(event.Data)
		if err != nil {
			return llm.Message{}, err
		}
		return llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: message.ToolCallID,
			Name:       toolNames[message.ToolCallID],
			Content:    blocksText(message.Content),
		}, nil

	case "system/message", "developer/message":
		message, err := nestedMessage(event.Data)
		if err != nil {
			return llm.Message{}, err
		}
		// llm has no developer role; a developer message is a system-level
		// instruction, so it is projected as one.
		return llm.Message{Role: llm.RoleSystem, Content: blocksText(message.Content)}, nil

	default:
		return llm.Message{}, fmt.Errorf("unsupported surface event type %q", event.Type)
	}
}

func nestedMessage(data json.RawMessage) (upstreamMessage, error) {
	var payload struct {
		Message upstreamMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return upstreamMessage{}, fmt.Errorf("decode message payload: %w", err)
	}
	return payload.Message, nil
}

// blocksText concatenates the text blocks of one message. Reasoning and image
// blocks have no llm.Message equivalent in P1 and are skipped.
func blocksText(blocks []messageBlock) string {
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		builder.WriteString(block.Text)
	}
	return builder.String()
}
