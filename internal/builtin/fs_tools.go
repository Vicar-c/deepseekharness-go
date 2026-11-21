package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

type readFileTool struct{ workspace *Workspace }

func (t *readFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "read",
		Description: "Read a UTF-8 text file from the workspace and return its content.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path relative to the working directory."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		}),
	}
}

func (t *readFileTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	absolute, relative, err := t.workspace.Resolve(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("stat %s: %w", relative, err)
	}
	if info.IsDir() {
		return tool.Result{}, fmt.Errorf("%s is a directory, use list_dir", relative)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read %s: %w", relative, err)
	}
	return tool.Result{Content: fmt.Sprintf("# %s (%d bytes)\n%s", relative, len(content), string(content))}, nil
}

type writeFileTool struct{ workspace *Workspace }

func (t *writeFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "write",
		Description: "Create or overwrite a UTF-8 text file inside the workspace.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path relative to the working directory."},
				"content": map[string]any{"type": "string", "description": "Full file content to write."},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		}),
	}
}

func (t *writeFileTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	absolute, relative, err := t.workspace.Resolve(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return tool.Result{}, fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(absolute, []byte(args.Content), 0o644); err != nil {
		return tool.Result{}, fmt.Errorf("write %s: %w", relative, err)
	}
	return tool.Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), relative)}, nil
}

type listDirTool struct{ workspace *Workspace }

func (t *listDirTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "list_dir",
		Description: "List the entries of a directory inside the workspace.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path relative to the working directory; defaults to ."},
			},
			"additionalProperties": false,
		}),
	}
}

func (t *listDirTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}
	absolute, relative, err := t.workspace.Resolve(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("list %s: %w", relative, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s (%d entries)\n", relative, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Fprintf(&builder, "d %s/\n", entry.Name())
			continue
		}
		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(&builder, "f %s\n", entry.Name())
			continue
		}
		fmt.Fprintf(&builder, "f %s (%d bytes)\n", entry.Name(), info.Size())
	}
	return tool.Result{Content: builder.String()}, nil
}

type editFileTool struct{ workspace *Workspace }

func (t *editFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "edit",
		Description: "Replace an exact string in a workspace file. The old string must occur exactly once unless replace_all is set, so an edit can never hit the wrong place by accident.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "File path relative to the working directory."},
				"old_string":  map[string]any{"type": "string", "description": "Exact text to replace."},
				"new_string":  map[string]any{"type": "string", "description": "Replacement text."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."},
			},
			"required":             []string{"path", "old_string", "new_string"},
			"additionalProperties": false,
		}),
	}
}

func (t *editFileTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	switch {
	case args.OldString == "":
		return tool.Result{}, errors.New("old_string must not be empty")
	case args.OldString == args.NewString:
		return tool.Result{}, errors.New("old_string and new_string are identical")
	}
	absolute, relative, err := t.workspace.Resolve(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("stat %s: %w", relative, err)
	}
	if info.IsDir() {
		return tool.Result{}, fmt.Errorf("%s is a directory", relative)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read %s: %w", relative, err)
	}
	occurrences := strings.Count(string(content), args.OldString)
	switch {
	case occurrences == 0:
		return tool.Result{}, fmt.Errorf("old_string was not found in %s", relative)
	case occurrences > 1 && !args.ReplaceAll:
		return tool.Result{}, fmt.Errorf("old_string occurs %d times in %s: add surrounding context or set replace_all", occurrences, relative)
	}
	replacements := 1
	if args.ReplaceAll {
		replacements = -1
	}
	updated := strings.Replace(string(content), args.OldString, args.NewString, replacements)
	if err := os.WriteFile(absolute, []byte(updated), info.Mode().Perm()); err != nil {
		return tool.Result{}, fmt.Errorf("write %s: %w", relative, err)
	}
	replaced := occurrences
	if !args.ReplaceAll {
		replaced = 1
	}
	return tool.Result{Content: fmt.Sprintf("edited %s: replaced %d occurrence(s), %d -> %d bytes", relative, replaced, len(content), len(updated))}, nil
}

type workingDirectoryTool struct{ workspace *Workspace }

func (t *workingDirectoryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "working_directory",
		Description: "Report or change the working directory shared by the builtin tools. Relative paths in later calls resolve against it, and it can never leave the workspace root.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "New working directory relative to the workspace; omit it to only report the current one."},
			},
			"additionalProperties": false,
		}),
	}
}

func (t *workingDirectoryTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		current := t.workspace.Cwd()
		if current == "" {
			current = "."
		}
		return tool.Result{Content: fmt.Sprintf("workspace: %s\nworking directory: %s", t.workspace.Root(), current)}, nil
	}
	relative, err := t.workspace.SetCwd(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: fmt.Sprintf("working directory set to %s", relative)}, nil
}
