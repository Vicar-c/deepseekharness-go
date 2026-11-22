package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Workspace is the sandbox root shared by the file and shell tools. It also
// holds the current working directory, so working_directory can move every
// tool at once while staying inside the root.
type Workspace struct {
	root string
	mu   sync.RWMutex
	cwd  string
}

// NewWorkspace anchors a workspace at root.
func NewWorkspace(root string) *Workspace {
	absolute, err := filepath.Abs(root)
	if err != nil || absolute == "" {
		absolute = root
	}
	return &Workspace{root: absolute}
}

// Root reports the absolute sandbox root.
func (w *Workspace) Root() string { return w.root }

// Cwd reports the current working directory relative to the root. The empty
// string means the root itself.
func (w *Workspace) Cwd() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cwd
}

// Dir reports the absolute current working directory.
func (w *Workspace) Dir() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.cwd == "" {
		return w.root
	}
	return filepath.Join(w.root, w.cwd)
}

// Resolve maps requested, which is relative to the current working directory,
// onto an absolute path. Paths that escape the workspace root are rejected.
func (w *Workspace) Resolve(requested string) (absolute string, relative string, err error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", "", errors.New("path is required")
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(w.Dir(), target)
	}
	absolute, err = filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	relative, err = filepath.Rel(w.root, absolute)
	if err != nil {
		return "", "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("path %q escapes the workspace", requested)
	}
	return absolute, filepath.ToSlash(relative), nil
}

// ResetCwd returns the working directory to the workspace root. A session that
// is starting fresh must not inherit the directory another session moved to.
func (w *Workspace) ResetCwd() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cwd = ""
}

// RestoreCwd applies a root-relative working directory recorded in a session
// log. Unlike SetCwd the path is resolved against the root rather than the
// current directory, because a recorded value is already root-relative. A
// directory that no longer exists is reported with os.ErrNotExist so a caller can
// fall back to the root instead of refusing to resume the conversation.
func (w *Workspace) RestoreCwd(relative string) error {
	relative = strings.TrimSpace(relative)
	if relative == "" || relative == "." {
		w.ResetCwd()
		return nil
	}
	if filepath.IsAbs(relative) {
		return fmt.Errorf("restored path %q must be relative to the workspace", relative)
	}
	target := filepath.Join(w.root, relative)
	cleaned, err := filepath.Rel(w.root, target)
	if err != nil {
		return fmt.Errorf("resolve relative path: %w", err)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("restored path %q escapes the workspace", relative)
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("restored path %q is not a directory", relative)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cwd = filepath.ToSlash(cleaned)
	return nil
}

// SetCwd moves the working directory used by later tool calls.
func (w *Workspace) SetCwd(requested string) (string, error) {
	absolute, relative, err := w.Resolve(requested)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", relative, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", relative)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if relative == "." {
		w.cwd = ""
		return ".", nil
	}
	w.cwd = relative
	return relative, nil
}
