package session

import (
	"encoding/json"
	"fmt"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// Turn end reason kinds. The loop itself only emits completed and error; a turn
// closed by crash recovery is interrupted, and a turn the user cancelled is
// cancelled (MIGRATION_MAP §6-5, T-0018).
const (
	TurnEndCompleted   = "completed"
	TurnEndError       = "error"
	TurnEndCancelled   = "cancelled"
	TurnEndInterrupted = "interrupted"
)

// Closer is one event recovery appends to close a crashed turn.
type Closer struct {
	Type      string
	Data      any
	SurfaceOp *SurfaceOp
}

// InterruptedTurnClosers inspects a log that a crash left inside a turn and
// returns the events needed to close it, as a normal batch:
//
//  1. a tool/result for every tool call the model asked for that never got one
//     (TOOL_OUTCOME_UNKNOWN when the call had been recorded, TOOL_NOT_STARTED
//     when it had not);
//  2. a step/end for every step still open;
//  3. a synthetic turn/end with reason interrupted.
//
// A log whose last turn already ended yields no closers, so the repair is
// idempotent: resuming twice appends nothing the second time.
func InterruptedTurnClosers(recording *Recording) []Closer {
	if recording == nil {
		return nil
	}
	openIndex := openTurnIndex(recording)
	if openIndex < 0 {
		return nil
	}

	var marker TurnMarker
	_ = json.Unmarshal(recording.Events[openIndex].Data, &marker)

	type pendingCall struct {
		CallID string
		Name   string
		Turn   int
		Step   int
	}
	var requested []pendingCall
	answered := make(map[string]bool)
	recorded := make(map[string]bool)
	openSteps := make(map[int]bool)

	for _, event := range recording.Events[openIndex+1:] {
		switch event.Type {
		case "assistant/message":
			var data AssistantMessageData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				continue
			}
			for _, block := range data.Message.Content {
				if block.Type != "tool-call" || block.ID == "" {
					continue
				}
				requested = append(requested, pendingCall{
					CallID: block.ID,
					Name:   block.Name,
					Turn:   data.Turn,
					Step:   data.Step,
				})
			}
		case "tool/call":
			var data ToolCallData
			if err := json.Unmarshal(event.Data, &data); err == nil && data.CallID != "" {
				recorded[data.CallID] = true
			}
		case "tool/result":
			var data ToolResultData
			if err := json.Unmarshal(event.Data, &data); err == nil && data.Message.ToolCallID != "" {
				answered[data.Message.ToolCallID] = true
			}
		case "step/start":
			var data StepMarker
			if err := json.Unmarshal(event.Data, &data); err == nil {
				openSteps[data.Step] = true
			}
		case "step/end":
			var data StepMarker
			if err := json.Unmarshal(event.Data, &data); err == nil {
				delete(openSteps, data.Step)
			}
		}
	}

	var closers []Closer
	reported := make(map[string]bool, len(requested))
	for _, call := range requested {
		if answered[call.CallID] || reported[call.CallID] {
			continue
		}
		reported[call.CallID] = true
		code := tool.CodeNotStarted
		reason := "was never dispatched before the session crashed"
		if recorded[call.CallID] {
			code = tool.CodeOutcomeUnknown
			reason = "started but its result never reached the log"
		}
		content := fmt.Sprintf("tool %q %s", call.Name, reason)
		appendOp := Append
		closers = append(closers, Closer{
			Type: "tool/result",
			Data: ToolResultData{
				Turn:    call.Turn,
				Step:    call.Step,
				Code:    code,
				Message: NewToolResultMessage(call.CallID, content, true),
			},
			SurfaceOp: &appendOp,
		})
	}

	for step := 1; step <= maxStep(openSteps)+1; step++ {
		if !openSteps[step] {
			continue
		}
		closers = append(closers, Closer{
			Type: "step/end",
			Data: StepMarker{Turn: marker.Turn, Step: step},
		})
	}

	closers = append(closers, Closer{
		Type: "turn/end",
		Data: TurnEndData{Turn: marker.Turn, Reason: TurnEndReason{Kind: TurnEndInterrupted}},
	})
	return closers
}

// openTurnIndex returns the index of the last turn/start when that turn has no
// turn/end after it, and -1 otherwise.
func openTurnIndex(recording *Recording) int {
	for index := len(recording.Events) - 1; index >= 0; index-- {
		switch recording.Events[index].Type {
		case "turn/end":
			return -1
		case "turn/start":
			return index
		}
	}
	return -1
}

func maxStep(steps map[int]bool) int {
	highest := 0
	for step := range steps {
		if step > highest {
			highest = step
		}
	}
	return highest
}
