package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrMissingAPIKey is returned by the DeepSeek client when no key is configured.
var ErrMissingAPIKey = errors.New("llm: missing API key (set DEEPSEEK_API_KEY)")

// DeepSeekOptions configures the OpenAI-compatible DeepSeek client.
type DeepSeekOptions struct {
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// DeepSeek is an OpenAI-compatible client for the DeepSeek chat completions API.
type DeepSeek struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewDeepSeek builds a client. An empty API key is allowed so the harness can
// boot without credentials; Chat then fails with ErrMissingAPIKey.
func NewDeepSeek(opts DeepSeekOptions) (*DeepSeek, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("llm: base URL is empty")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("llm: model is empty")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &DeepSeek{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(opts.APIKey),
		model:   opts.Model,
		http:    httpClient,
	}, nil
}

// Model reports the default model of this client.
func (c *DeepSeek) Model() string { return c.model }

type deepSeekRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream"`
}

type deepSeekResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat performs one non-streaming chat completion.
func (c *DeepSeek) Chat(ctx context.Context, request Request) (Response, error) {
	httpRequest, err := c.newHTTPRequest(ctx, request, false)
	if err != nil {
		return Response{}, err
	}
	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("llm: call deepseek: %w", err)
	}
	defer httpResponse.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, 16<<20))
	if err != nil {
		return Response{}, fmt.Errorf("llm: read response: %w", err)
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return Response{}, fmt.Errorf("llm: deepseek HTTP %d: %s", httpResponse.StatusCode, compact(raw))
	}
	var decoded deepSeekResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Response{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return Response{}, fmt.Errorf("llm: deepseek error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return Response{}, errors.New("llm: deepseek returned no choices")
	}
	choice := decoded.Choices[0]
	return Response{
		Message:      choice.Message,
		FinishReason: choice.FinishReason,
		Usage:        decoded.Usage,
	}, nil
}

// ChatStream performs one streamed chat completion. Every incremental delta is
// handed to onChunk as soon as it arrives; the returned Response carries the
// fully assembled message. An error from onChunk aborts the stream.
func (c *DeepSeek) ChatStream(ctx context.Context, request Request, onChunk func(Chunk) error) (Response, error) {
	httpRequest, err := c.newHTTPRequest(ctx, request, true)
	if err != nil {
		return Response{}, err
	}
	httpRequest.Header.Set("Accept", "text/event-stream")

	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("llm: call deepseek: %w", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
		return Response{}, fmt.Errorf("llm: deepseek HTTP %d: %s", httpResponse.StatusCode, compact(raw))
	}

	assembler := newStreamAssembler()
	scanner := bufio.NewScanner(httpResponse.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // SSE comment or keep-alive
		}
		if !strings.HasPrefix(line, "data:") {
			continue // event: / id: / retry: frames are not used by this API
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var frame deepSeekStreamFrame
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			return Response{}, fmt.Errorf("llm: decode stream frame: %w", err)
		}
		if frame.Error != nil && frame.Error.Message != "" {
			return Response{}, fmt.Errorf("llm: deepseek error: %s", frame.Error.Message)
		}
		for _, choice := range frame.Choices {
			if choice.Delta.Content != "" {
				assembler.addContent(choice.Delta.Content)
				if err := emit(onChunk, Chunk{Content: choice.Delta.Content}); err != nil {
					return Response{}, err
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				assembler.addToolCall(delta)
				if delta.ID == "" && delta.Function.Name == "" && delta.Function.Arguments == "" {
					continue
				}
				chunk := Chunk{ToolCallID: delta.ID, ToolName: delta.Function.Name, ToolArgs: delta.Function.Arguments}
				if err := emit(onChunk, chunk); err != nil {
					return Response{}, err
				}
			}
			if choice.FinishReason != "" {
				assembler.finish = choice.FinishReason
				if err := emit(onChunk, Chunk{FinishReason: choice.FinishReason}); err != nil {
					return Response{}, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("llm: read stream: %w", err)
	}
	return assembler.response(), nil
}

func emit(onChunk func(Chunk) error, chunk Chunk) error {
	if onChunk == nil {
		return nil
	}
	return onChunk(chunk)
}

func (c *DeepSeek) newHTTPRequest(ctx context.Context, request Request, stream bool) (*http.Request, error) {
	if c.apiKey == "" {
		return nil, ErrMissingAPIKey
	}
	if request.Model == "" {
		request.Model = c.model
	}
	payload := deepSeekRequest{
		Model:       request.Model,
		Messages:    request.Messages,
		Tools:       request.Tools,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
		Stream:      stream,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llm: encode request: %w", err)
	}
	url := c.baseURL + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	return httpRequest, nil
}

// deepSeekToolCallDelta is one incremental tool-call fragment in a stream.
type deepSeekToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// deepSeekStreamFrame is one SSE data frame of a streamed completion.
type deepSeekStreamFrame struct {
	Choices []struct {
		Delta struct {
			Content   string                  `json:"content"`
			ToolCalls []deepSeekToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamAssembler struct {
	content strings.Builder
	calls   []*streamCall
	byIndex map[int]*streamCall
	finish  string
}

type streamCall struct {
	id   string
	name string
	args strings.Builder
}

func newStreamAssembler() *streamAssembler {
	return &streamAssembler{byIndex: make(map[int]*streamCall)}
}

func (a *streamAssembler) addContent(text string) { a.content.WriteString(text) }

func (a *streamAssembler) addToolCall(delta deepSeekToolCallDelta) {
	call, ok := a.byIndex[delta.Index]
	if !ok {
		call = &streamCall{}
		a.byIndex[delta.Index] = call
		for len(a.calls) <= delta.Index {
			a.calls = append(a.calls, nil)
		}
		a.calls[delta.Index] = call
	}
	if delta.ID != "" {
		call.id = delta.ID
	}
	if delta.Function.Name != "" {
		call.name = delta.Function.Name
	}
	call.args.WriteString(delta.Function.Arguments)
}

func (a *streamAssembler) response() Response {
	message := Message{Role: RoleAssistant, Content: a.content.String()}
	for _, call := range a.calls {
		if call == nil {
			continue
		}
		message.ToolCalls = append(message.ToolCalls, ToolCall{
			ID:       call.id,
			Type:     "function",
			Function: FunctionCall{Name: call.name, Arguments: call.args.String()},
		})
	}
	return Response{Message: message, FinishReason: a.finish}
}

func compact(raw []byte) string {
	const max = 2 << 10
	text := strings.TrimSpace(string(raw))
	if len(text) > max {
		text = text[:max] + "..."
	}
	if text == "" {
		return "(empty body)"
	}
	return text
}
