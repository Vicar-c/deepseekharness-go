package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

// TestErrorFromHTTPClassifiesStatus pins the provider failure rules: the same
// HTTP answer must always produce the same provider-neutral code, because retry
// and the session log both route on it.
func TestErrorFromHTTPClassifiesStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		headers http.Header
		want    string
	}{
		{name: "unauthorized", status: 401, body: `{"error":{"message":"Authentication Fails"}}`, want: llm.CodeAuth},
		{name: "forbidden", status: 403, body: `{"error":{"message":"no access"}}`, want: llm.CodeAuth},
		{name: "authentication type", status: 500, body: `{"error":{"type":"authentication_error"}}`, want: llm.CodeAuth},
		{name: "payment required", status: 402, body: `{"error":{"message":"top up"}}`, want: llm.CodeQuota},
		{name: "insufficient balance", status: 400, body: `{"error":{"message":"Insufficient Balance"}}`, want: llm.CodeQuota},
		{name: "rate limit status", status: 429, body: `{"error":{"message":"rate limited"}}`, want: llm.CodeRateLimit},
		{name: "rate limit type", status: 400, body: `{"error":{"type":"rate_limit_error"}}`, want: llm.CodeRateLimit},
		{name: "context length", status: 400, body: `{"error":{"code":"context_length_exceeded"}}`, want: llm.CodeContextWindowExceeded},
		{name: "context wording", status: 400, body: `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`, want: llm.CodeContextWindowExceeded},
		{name: "invalid request", status: 400, body: `{"error":{"message":"bad field"}}`, want: llm.CodeInvalidRequest},
		{name: "too large", status: 413, body: `{"error":{"message":"body too large"}}`, want: llm.CodeInvalidRequest},
		{name: "invalid request type", status: 422, body: `{"error":{"type":"invalid_request_error"}}`, want: llm.CodeInvalidRequest},
		{name: "server error", status: 500, body: `{"error":{"message":"boom"}}`, want: llm.CodeServer},
		{name: "overloaded", status: 529, body: `{"error":{"message":"busy"}}`, want: llm.CodeServer},
		{name: "unclassified", status: 418, body: `{}, "teapot"`, want: "HTTP_418"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failure := llm.ErrorFromHTTP(testCase.status, testCase.headers, []byte(testCase.body))
			if failure.Code != testCase.want {
				t.Fatalf("code = %q, want %q (message %q)", failure.Code, testCase.want, failure.Message)
			}
			if failure.Status != testCase.status {
				t.Fatalf("status = %d, want %d", failure.Status, testCase.status)
			}
			if !strings.Contains(failure.Message, "HTTP") {
				t.Fatalf("message %q does not name the status", failure.Message)
			}
		})
	}
}

// TestErrorFromHTTPKeepsProviderDetails proves retry and request identity survive
// classification: a provider asking for a delay must reach the retry policy.
func TestErrorFromHTTPKeepsProviderDetails(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "3")
	headers.Set("x-request-id", "req-42")
	failure := llm.ErrorFromHTTP(429, headers, []byte(`{"error":{"message":"slow down"}}`))
	if failure.ProviderRetryAfterMs != 3000 {
		t.Fatalf("provider retry after = %dms, want 3000", failure.ProviderRetryAfterMs)
	}
	if failure.RetryAfter() != 3*time.Second {
		t.Fatalf("RetryAfter() = %s, want 3s", failure.RetryAfter())
	}
	if failure.RequestID != "req-42" {
		t.Fatalf("request id = %q, want req-42", failure.RequestID)
	}
}

// TestRetryAfterHeaderForms covers both accepted header forms and the rejection
// of anything else.
func TestRetryAfterHeaderForms(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		wantMs int64
		wantOK bool
	}{
		{name: "seconds", value: "7", wantMs: 7000, wantOK: true},
		{name: "fractional seconds", value: "0.5", wantMs: 500, wantOK: true},
		{name: "zero", value: "0", wantMs: 0, wantOK: false},
		{name: "negative", value: "-2", wantMs: 0, wantOK: false},
		{name: "garbage", value: "soon", wantMs: 0, wantOK: false},
		{name: "empty", value: "", wantMs: 0, wantOK: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set("Retry-After", testCase.value)
			failure := llm.ErrorFromHTTP(429, headers, nil)
			if failure.ProviderRetryAfterMs != testCase.wantMs {
				t.Fatalf("provider retry after = %d, want %d", failure.ProviderRetryAfterMs, testCase.wantMs)
			}
			if got := failure.RetryAfter() > 0; got != testCase.wantOK {
				t.Fatalf("RetryAfter() > 0 = %v, want %v", got, testCase.wantOK)
			}
		})
	}
}

// TestRetryAfterHTTPDate accepts the absolute-date form of Retry-After.
func TestRetryAfterHTTPDate(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", time.Now().Add(90*time.Second).UTC().Format(http.TimeFormat))
	failure := llm.ErrorFromHTTP(429, headers, nil)
	if failure.ProviderRetryAfterMs < 80_000 || failure.ProviderRetryAfterMs > 90_000 {
		t.Fatalf("provider retry after = %dms, want about 90s", failure.ProviderRetryAfterMs)
	}
}

// TestErrorFromPayloadClassifiesInBandFailures covers errors delivered inside a
// stream, which carry no HTTP status.
func TestErrorFromPayloadClassifiesInBandFailures(t *testing.T) {
	rate := llm.ErrorFromPayload("rate limited", "rate_limit_error", "rate_limit_error")
	if rate.Code != llm.CodeRateLimit {
		t.Fatalf("code = %q, want RATE_LIMIT", rate.Code)
	}
	unknown := llm.ErrorFromPayload("something broke", "", "")
	if unknown.Code != llm.CodeServer {
		t.Fatalf("code = %q, want SERVER for an in-band failure with no status", unknown.Code)
	}
	if !strings.Contains(unknown.Message, "something broke") {
		t.Fatalf("message %q lost the provider wording", unknown.Message)
	}
}

// TestContextOverflowWording pins the wording rules: only text that explicitly
// names the model context counts as an overflow, so an unrelated "too long" does
// not get recorded as one.
func TestContextOverflowWording(t *testing.T) {
	overflow := []string{
		"context_length_exceeded",
		"context window exceeded",
		"This model's maximum context length is 128000 tokens",
		"the request is too large for this model's context window",
		"prompt is too long for this model",
		"input exceeds the model's context window",
		"messages exceed the context length",
	}
	for _, detail := range overflow {
		if !llm.IsContextWindowExceededError(detail) {
			t.Errorf("IsContextWindowExceededError(%q) = false, want true", detail)
		}
	}
	notOverflow := []string{
		"maximum tokens exceeded",
		"output too long",
		"the response was cut off",
		"insufficient balance",
		"",
	}
	for _, detail := range notOverflow {
		if llm.IsContextWindowExceededError(detail) {
			t.Errorf("IsContextWindowExceededError(%q) = true, want false", detail)
		}
	}
}

// TestQuotaWording separates a terminal quota from a transient rate limit.
func TestQuotaWording(t *testing.T) {
	quota := []string{
		"insufficient balance",
		"insufficient_quota",
		"you exceeded your current quota",
		"usage limit reached",
		"credit balance exhausted",
		"out of credits",
	}
	for _, detail := range quota {
		if !llm.IsQuotaExceededError(detail) {
			t.Errorf("IsQuotaExceededError(%q) = false, want true", detail)
		}
	}
	notQuota := []string{"rate limit reached", "too many requests", ""}
	for _, detail := range notQuota {
		if llm.IsQuotaExceededError(detail) {
			t.Errorf("IsQuotaExceededError(%q) = true, want false", detail)
		}
	}
}

// TestClassifyNonHTTPFailures maps cancellation, deadlines and plain errors onto
// the taxonomy the retry policy reads.
func TestClassifyNonHTTPFailures(t *testing.T) {
	cancelled := llm.Classify(context.Canceled)
	if cancelled.Code != llm.CodeAborted {
		t.Fatalf("cancelled code = %q, want ABORTED", cancelled.Code)
	}
	deadline := llm.Classify(context.DeadlineExceeded)
	if deadline.Code != llm.CodeTimeout {
		t.Fatalf("deadline code = %q, want TIMEOUT", deadline.Code)
	}
	unknown := llm.Classify(errors.New("kaboom"))
	if unknown.Code != llm.CodeUnknown {
		t.Fatalf("plain error code = %q, want UNKNOWN", unknown.Code)
	}
	if unknown.Message != "kaboom" {
		t.Fatalf("message = %q, want kaboom", unknown.Message)
	}
	if llm.Classify(nil) != nil {
		t.Fatal("Classify(nil) must be nil")
	}
}

// TestClassifyPreservesFailure keeps retry delays and codes intact when the loop
// wraps a failure with context about the failed call.
func TestClassifyPreservesFailure(t *testing.T) {
	original := &llm.Failure{
		Message:              "llm: deepseek HTTP 429: slow down",
		Code:                 llm.CodeRateLimit,
		Status:               429,
		ProviderRetryAfterMs: 2500,
		RequestID:            "req-1",
	}
	wrapped := fmt.Errorf("agent: model call: %w", original)
	if got := llm.Classify(wrapped); got != original {
		t.Fatalf("Classify returned %+v, want the original failure", got)
	}
	if !strings.Contains(wrapped.Error(), "RATE_LIMIT") {
		t.Fatalf("rendered error %q does not name the code", wrapped)
	}
	failure, ok := llm.FailureOf(wrapped)
	if !ok || failure.ProviderRetryAfterMs != 2500 {
		t.Fatalf("FailureOf = %+v, %v", failure, ok)
	}
	if llm.CodeOf(wrapped) != llm.CodeRateLimit {
		t.Fatalf("CodeOf = %q, want RATE_LIMIT", llm.CodeOf(wrapped))
	}
	if llm.CodeOf(errors.New("plain")) != llm.CodeUnknown {
		t.Fatalf("CodeOf(plain) = %q, want UNKNOWN", llm.CodeOf(errors.New("plain")))
	}
	if llm.CodeOf(nil) != "" {
		t.Fatalf("CodeOf(nil) = %q, want empty", llm.CodeOf(nil))
	}
}

// TestFailureJSONMatchesTheLogSchema keeps the recorded llm/retry and turn/end
// payloads readable by the upstream implementation.
func TestFailureJSONMatchesTheLogSchema(t *testing.T) {
	failure := &llm.Failure{
		Message:              "deepseek HTTP 429: slow down",
		Code:                 llm.CodeRateLimit,
		Status:               429,
		ProviderRetryAfterMs: 1500,
		RequestID:            "req-9",
	}
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"message", "code", "status", "providerRetryAfterMs", "requestId"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("encoded failure %s is missing %q", encoded, key)
		}
	}
	bare, err := json.Marshal(&llm.Failure{Message: "x", Code: llm.CodeAuth})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(bare), "status") || strings.Contains(string(bare), "requestId") {
		t.Fatalf("empty optional fields must be absent, got %s", bare)
	}
}

// TestMissingAPIKeyIsAFailure keeps the sentinel matchable while giving callers
// the same taxonomy as every other model-call failure.
func TestMissingAPIKeyIsAFailure(t *testing.T) {
	if !errors.Is(llm.ErrMissingAPIKey, llm.ErrMissingAPIKey) {
		t.Fatal("ErrMissingAPIKey must match itself")
	}
	failure, ok := llm.FailureOf(llm.ErrMissingAPIKey)
	if !ok {
		t.Fatal("ErrMissingAPIKey carries no failure")
	}
	if failure.Code != llm.CodeMissingCredential {
		t.Fatalf("code = %q, want MISSING_CREDENTIAL", failure.Code)
	}
}

// TestHTTPStatusCode renders the fallback code.
func TestHTTPStatusCode(t *testing.T) {
	if got := llm.HTTPStatusCode(418); got != "HTTP_418" {
		t.Fatalf("HTTPStatusCode(418) = %q", got)
	}
}
