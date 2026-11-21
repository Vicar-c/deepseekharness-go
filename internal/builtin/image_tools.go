package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"strings"

	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

const (
	readImageMaxBytes  = 8 << 20
	readImageMaxInline = 64 << 10
)

// readImageTool reports image metadata and, for small images, an inline data
// URI. Big images skip the payload so the tool pipeline never truncates a
// base64 blob into an unusable one.
type readImageTool struct{ workspace *Workspace }

func (t *readImageTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "read_image",
		Description: "Read an image from the workspace: returns its dimensions and media type, plus an inline base64 data URI when the image is small enough.",
		Parameters: schema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Image path relative to the working directory."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		}),
	}
}

func (t *readImageTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
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
		return tool.Result{}, fmt.Errorf("%s is a directory", relative)
	}
	if info.Size() > readImageMaxBytes {
		return tool.Result{}, fmt.Errorf("%s is %d bytes: the image limit is %d bytes", relative, info.Size(), readImageMaxBytes)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read %s: %w", relative, err)
	}
	mediaType := http.DetectContentType(data)
	if !strings.HasPrefix(mediaType, "image/") {
		return tool.Result{}, fmt.Errorf("%s is not an image (detected %s)", relative, mediaType)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s (%d bytes, %s", relative, len(data), mediaType)
	if config, _, decodeErr := image.DecodeConfig(bytes.NewReader(data)); decodeErr == nil {
		fmt.Fprintf(&builder, ", %dx%d", config.Width, config.Height)
	}
	builder.WriteString(")")

	encoded := base64.StdEncoding.EncodeToString(data)
	if len(encoded) > readImageMaxInline {
		fmt.Fprintf(&builder, "\n[inline base64 skipped: %d encoded bytes exceed the %d byte inline limit]", len(encoded), readImageMaxInline)
		return tool.Result{Content: builder.String()}, nil
	}
	fmt.Fprintf(&builder, "\n\ndata:%s;base64,%s", mediaType, encoded)
	return tool.Result{Content: builder.String()}, nil
}
