package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// Todo statuses accepted by todo_write.
const (
	todoPending    = "pending"
	todoInProgress = "in_progress"
	todoCompleted  = "completed"
)

// TodoItem is one entry of the todo list the model maintains.
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// TodoWriteData is the payload of the todo/write session event. The tool returns
// it as state; the loop fills turn and step before persisting it.
type TodoWriteData struct {
	Turn  int        `json:"turn"`
	Step  int        `json:"step"`
	Todos []TodoItem `json:"todos"`
}

// SetTurnStep implements tool.TurnStepSetter.
func (d *TodoWriteData) SetTurnStep(turn, step int) {
	d.Turn = turn
	d.Step = step
}

// TodoStore keeps the current list for the lifetime of the process. The list is
// restored from the session log when a session is resumed.
type TodoStore struct {
	mu    sync.Mutex
	items []TodoItem
}

// NewTodoStore creates an empty store.
func NewTodoStore() *TodoStore { return &TodoStore{} }

// Items returns a copy of the current list.
func (s *TodoStore) Items() []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TodoItem(nil), s.items...)
}

// Replace swaps the whole list and returns the new one.
func (s *TodoStore) Replace(items []TodoItem) []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append([]TodoItem(nil), items...)
	return append([]TodoItem(nil), s.items...)
}

// TodoPlugin registers the todo list tool.
type TodoPlugin struct{ store *TodoStore }

// NewTodoPlugin creates the todo plugin with its own store.
func NewTodoPlugin() *TodoPlugin { return NewTodoPluginWithStore(NewTodoStore()) }

// NewTodoPluginWithStore creates the todo plugin around a shared store, which
// lets the host restore the list from a session log before the tools run.
func NewTodoPluginWithStore(store *TodoStore) *TodoPlugin {
	if store == nil {
		store = NewTodoStore()
	}
	return &TodoPlugin{store: store}
}

// Name implements kernel.Plugin.
func (*TodoPlugin) Name() string { return "builtin/todo" }

// Apply implements kernel.Plugin.
func (p *TodoPlugin) Apply(ctx *kernel.Context) error {
	registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
	if !ok {
		return errors.New("builtin/todo: tool registry service is missing")
	}
	return register(registry, &todoWriteTool{store: p.store})
}

type todoWriteTool struct{ store *TodoStore }

func (t *todoWriteTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "todo_write",
		Description: "Replace the session todo list and return the rendered list. Use it to plan multi-step work; an empty list clears it.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string", "description": "What needs to be done."},
							"status": map[string]any{
								"type": "string",
								"enum": []string{todoPending, todoInProgress, todoCompleted},
							},
						},
						"required":             []string{"content"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		}),
	}
}

func (t *todoWriteTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	for index, item := range args.Todos {
		item.Content = strings.TrimSpace(item.Content)
		if item.Content == "" {
			return tool.Result{}, fmt.Errorf("todos[%d].content must not be empty", index)
		}
		if item.Status == "" {
			item.Status = todoPending
		}
		switch item.Status {
		case todoPending, todoInProgress, todoCompleted:
		default:
			return tool.Result{}, fmt.Errorf("todos[%d].status = %q: want pending, in_progress or completed", index, item.Status)
		}
		args.Todos[index] = item
	}
	items := t.store.Replace(args.Todos)
	state := []tool.StateEvent{{Type: "todo/write", Data: &TodoWriteData{Todos: items}}}
	if len(items) == 0 {
		return tool.Result{Content: "todo list cleared", State: state}, nil
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Status]++
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# todo (%d item(s): %d pending, %d in progress, %d completed)\n",
		len(items), counts[todoPending], counts[todoInProgress], counts[todoCompleted])
	for _, item := range items {
		switch item.Status {
		case todoCompleted:
			builder.WriteString("[x] ")
		case todoInProgress:
			builder.WriteString("[~] ")
		default:
			builder.WriteString("[ ] ")
		}
		builder.WriteString(item.Content)
		builder.WriteString("\n")
	}
	return tool.Result{Content: builder.String(), State: state}, nil
}
