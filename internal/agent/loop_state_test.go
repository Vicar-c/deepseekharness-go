package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// probeState is the state payload of the probe tool. The loop, not the tool,
// knows the turn and step, so it fills them in before persisting.
type probeState struct {
	Turn  int    `json:"turn"`
	Step  int    `json:"step"`
	Value string `json:"value"`
}

func (p *probeState) SetTurnStep(turn, step int) {
	p.Turn = turn
	p.Step = step
}

// stateProbeTool reports one state change plus its result.
type stateProbeTool struct{}

func (*stateProbeTool) Definition() tool.Definition {
	return tool.Definition{Name: "state_probe", Description: "reports session state", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (*stateProbeTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{
		Content: "probe ran",
		State:   []tool.StateEvent{{Type: "todo/write", Data: &probeState{Value: "changed"}}},
	}, nil
}

// TestLoopPersistsToolState is B-31 at the loop level: a tool's state event is
// durable, lands before its tool/result, and carries the turn and step that
// produced it.
func TestLoopPersistsToolState(t *testing.T) {
	store := session.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create("state")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(&stateProbeTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	client := &scriptedClient{responses: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: llm.FunctionCall{Name: "state_probe", Arguments: `{}`},
			}}},
			FinishReason: "tool_calls",
		},
		llm.TextResponse("done", "stop"),
	}}
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{Logger: testLogger()})
	loop, err := agent.New(agent.Options{
		Client:   client,
		Pipeline: pipeline,
		Session:  sess,
		Store:    store,
		Events:   kernel.NewEvents(),
		Logger:   testLogger(),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if _, err := loop.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recording, err := session.ReadRecording(filepath.Join(store.Dir(), sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}
	stateIndex, resultIndex, callIndex := -1, -1, -1
	var state probeState
	for index, event := range recording.Events {
		switch event.Type {
		case "tool/call":
			callIndex = index
		case "todo/write":
			stateIndex = index
			if err := json.Unmarshal(event.Data, &state); err != nil {
				t.Fatalf("decode todo/write: %v", err)
			}
		case "tool/result":
			resultIndex = index
		}
	}
	if stateIndex < 0 {
		t.Fatal("no todo/write event was recorded")
	}
	if state.Turn != 1 || state.Step != 1 {
		t.Fatalf("state turn/step = %d/%d, want 1/1", state.Turn, state.Step)
	}
	if state.Value != "changed" {
		t.Fatalf("state value = %q", state.Value)
	}
	if callIndex < 0 || resultIndex < 0 {
		t.Fatalf("tool events missing: call %d, result %d", callIndex, resultIndex)
	}
	if !(callIndex < stateIndex && stateIndex < resultIndex) {
		t.Fatalf("event order call=%d state=%d result=%d; want the state between the call and its result",
			callIndex, stateIndex, resultIndex)
	}
}
