package llm

import (
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
	if c.apiKey == "" {
		return Response{}, ErrMissingAPIKey
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
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("llm: encode request: %w", err)
	}
	url := c.baseURL + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("llm: build request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)

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
