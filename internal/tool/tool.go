// Package tool defines the tool contract, the registry the agent loop exposes
// to the model, and the execution pipeline that wraps every call.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
)

// Definition describes one tool to the model.
type Definition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Call is one tool invocation requested by the model.
type Call struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Result is the outcome of a tool call, rendered back to the model.
type Result struct {
	Content string
	IsError bool
}

// Tool is one executable capability. All fields are safe for concurrent use.
type Tool interface {
	// Definition returns the static description exposed to the model.
	Definition() Definition
	// Execute runs the tool with raw JSON arguments.
	Execute(ctx context.Context, arguments json.RawMessage) (Result, error)
}

// Registry is an ordered, concurrency-safe tool registry.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool. Names must be unique and non-empty.
func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return errors.New("tool: tool is nil")
	}
	definition := tool.Definition()
	if definition.Name == "" {
		return errors.New("tool: tool name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[definition.Name]; exists {
		return fmt.Errorf("tool: duplicate tool %q", definition.Name)
	}
	r.tools[definition.Name] = tool
	r.order = append(r.order, definition.Name)
	return nil
}

// Get looks one tool up by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns tools in registration order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if tool, ok := r.tools[name]; ok {
			list = append(list, tool)
		}
	}
	return list
}

// Definitions returns the static descriptions in registration order.
func (r *Registry) Definitions() []Definition {
	tools := r.List()
	definitions := make([]Definition, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, tool.Definition())
	}
	return definitions
}

// Len reports how many tools are registered.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Guard can veto a tool call before it runs (approval, sandbox policy, budget).
type Guard func(ctx context.Context, call Call) error

// ExecutionEvent is the payload published as "tool/executed".
type ExecutionEvent struct {
	Call     Call
	Result   Result
	Duration int64 // milliseconds
}

// ensure kernel stays referenced when event emission is compiled out in tests.
var _ = kernel.Event{}
