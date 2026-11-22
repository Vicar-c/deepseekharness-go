package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Failure codes are the provider-neutral vocabulary the harness classifies model
// request failures into. They are copied from LLM_FAILURE_CODES in the upstream
// implementation; only the codes this rewrite can actually produce are declared,
// plus the HTTP_<status> fallback for a status no rule matched.
const (
	// CodeAborted reports a request abandoned because its context was cancelled.
	CodeAborted = "ABORTED"
	// CodeAuth reports a credential that the provider rejected.
	CodeAuth = "AUTH"
	// CodeContextWindowExceeded reports a request larger than the model context.
	CodeContextWindowExceeded = "CONTEXT_WINDOW_EXCEEDED"
	// CodeEmptyResponse reports a completion that carried no content and no tool
	// calls. Retrying it is safe: nothing durable was produced.
	CodeEmptyResponse = "EMPTY_RESPONSE"
	// CodeInvalidRequest reports a request the provider refused as malformed.
	CodeInvalidRequest = "INVALID_REQUEST"
	// CodeMissingCredential reports that no API key was configured at all.
	CodeMissingCredential = "MISSING_CREDENTIAL"
	// CodeNoAdapter reports that no model route can serve the request.
	CodeNoAdapter = "NO_ADAPTER"
	// CodeQuota reports an exhausted account balance rather than a transient rate
	// limit.
	CodeQuota = "QUOTA"
	// CodeRateLimit reports that the provider asked the caller to slow down.
	CodeRateLimit = "RATE_LIMIT"
	// CodeServer reports a provider-side failure that is worth repeating.
	CodeServer = "SERVER"
	// CodeTimeout reports a request that exceeded its deadline.
	CodeTimeout = "TIMEOUT"
	// CodeTransport reports that the request never reached the provider or its
	// answer never arrived.
	CodeTransport = "TRANSPORT"
	// CodeUnknown reports a failure outside this taxonomy.
	CodeUnknown = "UNKNOWN"
)

// HTTPStatusCode renders the fallback code of an unclassified HTTP status.
func HTTPStatusCode(status int) string { return "HTTP_" + strconv.Itoa(status) }

// Failure is one model request failure carrying the provider-neutral code the
// agent loop, the retry policy and the session log all reason about. Its JSON
// shape matches LlmFailure upstream, so a recorded turn/end or llm/retry event
// reads the same in both implementations.
type Failure struct {
	// Message is the human-readable failure, including provider wording when the
	// provider supplied any.
	Message string `json:"message"`
	// Code is one of the Code* constants or HTTP_<status>.
	Code string `json:"code"`
	// Status is the HTTP status, when the failure happened before streaming.
	Status int `json:"status,omitempty"`
	// ProviderRetryAfterMs is the delay the provider asked for, in milliseconds.
	ProviderRetryAfterMs int64 `json:"providerRetryAfterMs,omitempty"`
	// RequestID is the provider's request identity, when it reported one.
	RequestID string `json:"requestId,omitempty"`
}

// Error renders the failure with its code, so a bare log line stays diagnosable.
func (f *Failure) Error() string {
	if f == nil {
		return "llm: request failed"
	}
	if f.Code == "" {
		return f.Message
	}
	return fmt.Sprintf("%s [%s]", f.Message, f.Code)
}

// RetryAfter reports the provider-requested delay as a duration.
func (f *Failure) RetryAfter() time.Duration {
	if f == nil || f.ProviderRetryAfterMs <= 0 {
		return 0
	}
	return time.Duration(f.ProviderRetryAfterMs) * time.Millisecond
}

// FailureOf extracts the failure carried by err, if any.
func FailureOf(err error) (*Failure, bool) {
	var failure *Failure
	if errors.As(err, &failure) && failure != nil {
		return failure, true
	}
	return nil, false
}

// Classify maps any model-call error onto the failure taxonomy. A *Failure is
// returned unchanged; cancellation, deadline and transport errors are recognized
// directly; everything else becomes UNKNOWN.
func Classify(err error) *Failure {
	if err == nil {
		return nil
	}
	if failure, ok := FailureOf(err); ok {
		return failure
	}
	message := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		return &Failure{Message: message, Code: CodeAborted}
	case errors.Is(err, context.DeadlineExceeded):
		return &Failure{Message: message, Code: CodeTimeout}
	}
	var urlError interface{ Timeout() bool }
	if errors.As(err, &urlError) {
		return &Failure{Message: message, Code: CodeTransport}
	}
	return &Failure{Message: message, Code: CodeUnknown}
}

// CodeOf reports the failure code of err, or CodeUnknown when it carries none.
func CodeOf(err error) string {
	failure := Classify(err)
	if failure == nil {
		return ""
	}
	if failure.Code == "" {
		return CodeUnknown
	}
	return failure.Code
}

// providerEnvelope is the OpenAI-compatible error body.
type providerEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// providerDetail joins the provider code, type and message the way the upstream
// classifier does, so wording-based rules see every field.
func providerDetail(envelope providerEnvelope) string {
	return strings.TrimSpace(strings.Join([]string{envelope.Error.Type, envelope.Error.Code, envelope.Error.Message}, " "))
}

// ErrorFromHTTP classifies a non-2xx response. status is the HTTP status, header
// the response headers (retry delay and request identity) and body the raw
// response payload.
func ErrorFromHTTP(status int, header http.Header, body []byte) *Failure {
	var envelope providerEnvelope
	_ = json.Unmarshal(body, &envelope)
	// The raw body stays in the message: provider wording is what a user has to
	// read to fix an account or quota problem, and compact bounds its size.
	message := fmt.Sprintf("deepseek HTTP %d: %s", status, compact(body))
	failure := &Failure{Message: message, Code: classifyHTTPCode(status, providerDetail(envelope)), Status: status}
	if header != nil {
		failure.ProviderRetryAfterMs = retryAfterMillis(header.Get("Retry-After"))
		failure.RequestID = requestID(header)
	}
	return failure
}

// ErrorFromPayload classifies an in-band error frame, which carries provider
// wording but no HTTP status.
func ErrorFromPayload(message, code, failureType string) *Failure {
	envelope := providerEnvelope{}
	envelope.Error.Message = message
	envelope.Error.Code = code
	envelope.Error.Type = failureType
	detail := providerDetail(envelope)
	if strings.TrimSpace(message) == "" {
		message = "deepseek stream error"
	}
	return &Failure{Message: "deepseek error: " + message, Code: classifyHTTPCode(0, detail)}
}

// classifyHTTPCode applies the provider failure rules in the order the upstream
// transport uses. status 0 means the failure arrived in-band, after streaming
// started.
func classifyHTTPCode(status int, detail string) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden ||
		strings.Contains(detail, "authentication_error") || strings.Contains(detail, "permission_error"):
		return CodeAuth
	case IsQuotaExceededError(detail) || status == http.StatusPaymentRequired:
		return CodeQuota
	case status == http.StatusTooManyRequests || strings.Contains(detail, "rate_limit_error"):
		return CodeRateLimit
	case IsContextWindowExceededError(detail):
		return CodeContextWindowExceeded
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge ||
		strings.Contains(detail, "invalid_request_error"):
		return CodeInvalidRequest
	case status >= 500 || strings.Contains(detail, "api_error") || strings.Contains(detail, "overloaded_error"):
		return CodeServer
	case status == 0:
		return CodeServer
	default:
		return HTTPStatusCode(status)
	}
}

// retryAfterMillis parses a Retry-After header into milliseconds. Both the
// delay-seconds and the HTTP-date forms are accepted; anything else is 0.
func retryAfterMillis(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return int64(seconds * 1000)
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at).Milliseconds()
		if delay <= 0 {
			return 0
		}
		return delay
	}
	return 0
}

// requestID returns the provider request identity from the headers it may be
// reported in.
func requestID(header http.Header) string {
	for _, name := range []string{"request-id", "x-request-id", "x-deepseek-request-id"} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// Wording-based providers report a context overflow in the error message rather
// than in a status. These expressions are ports of the upstream classifiers: a
// rule only matches when the wording explicitly names the model context, so a
// generic "too long" is not mistaken for an overflow.
var (
	structuredContextOverflow = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])context[\s_-](?:length|window)[\s_-](?:exceed(?:ed|s)?|overflow(?:ed)?|limit[\s_-]exceeded)(?:$|[^a-z0-9])`)
	maxContextCapacity        = regexp.MustCompile(`(?i)\b(?:maximum|max)(?:\s+(?:allowed|supported))?\s+context\s+(?:length|window)\b`)
	tooLargeForContext        = regexp.MustCompile(`(?i)\b(?:request|prompt|input|messages?)\s+(?:is\s+|are\s+)?too\s+(?:large|long)\s+for\s+(?:(?:this|the)\s+)?(?:model(?:'s)?\s+)?context(?:\s+window)?\b`)
	tooLongForModel           = regexp.MustCompile(`(?i)\b(?:input|prompt|request)\s+(?:is\s+)?too\s+(?:long|large)\s+for\s+(?:this|the)\s+model\b`)
	exceedsModelContext       = regexp.MustCompile(`(?i)\b(?:input|prompt|request|messages?)\b.{0,40}\b(?:exceed(?:s|ed)?|overflows?|is\s+larger\s+than)\b.{0,40}\b(?:the\s+)?(?:model(?:'s)?\s+)?context(?:\s+(?:length|window))?\b`)
)

// IsContextWindowExceededError reports whether detail names a request that
// exceeds the model context window.
func IsContextWindowExceededError(detail string) bool {
	return structuredContextOverflow.MatchString(detail) ||
		maxContextCapacity.MatchString(detail) ||
		tooLargeForContext.MatchString(detail) ||
		tooLongForModel.MatchString(detail) ||
		exceedsModelContext.MatchString(detail)
}

// quotaWording are the phrases that identify a terminal account quota rather
// than a transient request-rate limit.
var (
	insufficientQuota = regexp.MustCompile(`(?i)\binsufficient[\s_-]+(?:quota|balance|credits?)\b`)
	quotaExceeded     = regexp.MustCompile(`(?i)\b(?:quota|usage[\s_-]+limit)[\s_-]+(?:exceeded|exhausted|reached)\b`)
	exceedsQuota      = regexp.MustCompile(`(?i)\bexceed(?:ed|s)?[\s_-]+(?:(?:your|the)[\s_-]+)?(?:current[\s_-]+)?quota\b`)
	balanceExhausted  = regexp.MustCompile(`(?i)\b(?:balance|credits?)[\s_-]+(?:exhausted|depleted)\b`)
	outOfCredits      = regexp.MustCompile(`(?i)\bout[\s_-]+of[\s_-]+(?:credits?|budget)\b`)
)

// IsQuotaExceededError reports whether detail names an exhausted account quota,
// balance or budget.
func IsQuotaExceededError(detail string) bool {
	return insufficientQuota.MatchString(detail) ||
		quotaExceeded.MatchString(detail) ||
		exceedsQuota.MatchString(detail) ||
		balanceExhausted.MatchString(detail) ||
		outOfCredits.MatchString(detail)
}
