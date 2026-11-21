package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

const (
	globDefaultLimit = 500
	globMaxLimit     = 2000
	grepDefaultLimit = 100
	grepMaxLimit     = 1000
	grepMaxFileBytes = 2 << 20
	grepMaxLineRunes = 400
)

// skippedDirs are the directories the search tools never descend into.
var skippedDirs = map[string]bool{
	".git":         true,
	".idea":        true,
	".venv":        true,
	"bin":          true,
	"dist":         true,
	"node_modules": true,
	"vendor":       true,
}

// globTool finds workspace files by pattern. "**" matches across directories,
// "*" and "?" stay inside one path segment.
type globTool struct{ workspace *Workspace }

func (t *globTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "glob",
		Description: "Find files whose path matches a glob pattern. Use ** to cross directories (e.g. **/*.go); results are sorted and capped.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern such as *.go or internal/**/*.go."},
				"path":    map[string]any{"type": "string", "description": "Directory to search under; defaults to the working directory."},
				"limit":   map[string]any{"type": "integer", "description": "Maximum matches to return; defaults to 500."},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		}),
	}
}

func (t *globTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		return tool.Result{}, fmt.Errorf("pattern is required")
	}
	matcher, err := compileGlob(pattern)
	if err != nil {
		return tool.Result{}, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	base := strings.TrimSpace(args.Path)
	if base == "" {
		base = "."
	}
	baseAbsolute, baseRelative, err := t.workspace.Resolve(base)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := os.Stat(baseAbsolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("stat %s: %w", baseRelative, err)
	}
	if !info.IsDir() {
		return tool.Result{}, fmt.Errorf("%s is not a directory", baseRelative)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = globDefaultLimit
	}
	if limit > globMaxLimit {
		limit = globMaxLimit
	}
	baseNameOnly := !strings.Contains(pattern, "/")

	var matches []string
	walkErr := filepath.WalkDir(baseAbsolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if skippedDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		relative, relErr := filepath.Rel(baseAbsolute, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		target := relative
		if baseNameOnly {
			target = entry.Name()
		}
		if !matcher.MatchString(target) {
			return nil
		}
		matches = append(matches, relative)
		if len(matches) >= limit {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return tool.Result{}, fmt.Errorf("walk %s: %w", baseRelative, walkErr)
	}
	sort.Strings(matches)
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %d match(es) under %s for %q\n", len(matches), baseRelative, pattern)
	for _, match := range matches {
		builder.WriteString(match)
		builder.WriteString("\n")
	}
	if len(matches) >= limit {
		fmt.Fprintf(&builder, "...[stopped at the %d match limit]\n", limit)
	}
	return tool.Result{Content: builder.String()}, nil
}

// compileGlob turns a glob pattern into an anchored regular expression.
func compileGlob(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch char := pattern[index]; char {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				if index+2 < len(pattern) && pattern[index+2] == '/' {
					builder.WriteString("(?:.*/)?")
					index += 2
					continue
				}
				builder.WriteString(".*")
				index++
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			builder.WriteString("\\")
			builder.WriteByte(char)
		default:
			builder.WriteByte(char)
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

// grepTool searches the workspace with a Go regular expression.
type grepTool struct{ workspace *Workspace }

func (t *grepTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "grep",
		Description: "Search workspace files with a Go regular expression and return matching lines as path:line:text.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string", "description": "Go regular expression to search for."},
				"path":        map[string]any{"type": "string", "description": "Directory to search under; defaults to the working directory."},
				"glob":        map[string]any{"type": "string", "description": "Only search files whose name matches this glob, e.g. *.go."},
				"ignore_case": map[string]any{"type": "boolean", "description": "Match case-insensitively."},
				"max_matches": map[string]any{"type": "integer", "description": "Maximum matching lines to return; defaults to 100."},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		}),
	}
}

func (t *grepTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var args struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignore_case"`
		MaxMatches int    `json:"max_matches"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return tool.Result{}, fmt.Errorf("decode arguments: %w", err)
	}
	expression := strings.TrimSpace(args.Pattern)
	if expression == "" {
		return tool.Result{}, fmt.Errorf("pattern is required")
	}
	if args.IgnoreCase {
		expression = "(?i)" + expression
	}
	matcher, err := regexp.Compile(expression)
	if err != nil {
		return tool.Result{}, fmt.Errorf("invalid pattern %q: %w", args.Pattern, err)
	}
	base := strings.TrimSpace(args.Path)
	if base == "" {
		base = "."
	}
	baseAbsolute, baseRelative, err := t.workspace.Resolve(base)
	if err != nil {
		return tool.Result{}, err
	}
	limit := args.MaxMatches
	if limit <= 0 {
		limit = grepDefaultLimit
	}
	if limit > grepMaxLimit {
		limit = grepMaxLimit
	}
	root := t.workspace.Root()

	var matches []string
	walkErr := filepath.WalkDir(baseAbsolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if skippedDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if args.Glob != "" {
			if ok, matchErr := filepath.Match(args.Glob, entry.Name()); matchErr != nil || !ok {
				return nil
			}
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > grepMaxFileBytes {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 0, 64*1024), grepMaxFileBytes)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := strings.TrimRight(scanner.Text(), "\r")
			if !matcher.MatchString(line) {
				continue
			}
			matches = append(matches, fmt.Sprintf("%s:%d: %s", relative, lineNumber, truncateRunes(line, grepMaxLineRunes)))
			if len(matches) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return tool.Result{}, fmt.Errorf("walk %s: %w", baseRelative, walkErr)
	}
	if len(matches) == 0 {
		return tool.Result{Content: fmt.Sprintf("(no matches for %q under %s)", args.Pattern, baseRelative)}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %d matching line(s) under %s for %q\n", len(matches), baseRelative, args.Pattern)
	for _, match := range matches {
		builder.WriteString(match)
		builder.WriteString("\n")
	}
	if len(matches) >= limit {
		fmt.Fprintf(&builder, "...[stopped at the %d match limit]\n", limit)
	}
	return tool.Result{Content: builder.String()}, nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
