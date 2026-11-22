package llm

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// Retry policy modes.
const (
	// RetryModeNormal retries only the failure codes the policy lists.
	RetryModeNormal = "normal"
	// RetryModeAlways retries every failure, without a retry bound.
	RetryModeAlways = "always"
)

// Default retry policy values, copied from the upstream provider defaults.
const (
	// DefaultMaxRetries is how many eligible retries follow the first attempt.
	DefaultMaxRetries = 5
	// DefaultInitialDelay is the first locally scheduled backoff delay.
	DefaultInitialDelay = 500 * time.Millisecond
	// DefaultMaxDelay bounds every local or accepted provider delay.
	DefaultMaxDelay = 10 * time.Second
	// DefaultJitterRatio is the symmetric random multiplier range around one.
	DefaultJitterRatio = 0.1
)

// DefaultRetryableCodes are the failure codes a normal policy retries when the
// caller does not choose its own set.
var DefaultRetryableCodes = []string{
	CodeEmptyResponse,
	CodeRateLimit,
	CodeServer,
	CodeTimeout,
	CodeTransport,
}

// RetryPolicy is one resolved model-request retry policy. The zero value is not
// usable; call DefaultRetryPolicy and adjust it.
type RetryPolicy struct {
	// Mode is RetryModeNormal or RetryModeAlways.
	Mode string
	// MaxRetries bounds retries in normal mode; it is ignored in always mode.
	MaxRetries int
	// RetryableCodes are the codes a normal policy retries.
	RetryableCodes []string
	// InitialDelay is the exponential backoff base.
	InitialDelay time.Duration
	// MaxDelay bounds a local delay and the provider delay the policy accepts.
	MaxDelay time.Duration
	// JitterRatio is the symmetric random spread around the local delay; 0
	// disables jitter.
	JitterRatio float64
}

// DefaultRetryPolicy returns the built-in normal policy.
func DefaultRetryPolicy() *RetryPolicy {
	return &RetryPolicy{
		Mode:           RetryModeNormal,
		MaxRetries:     DefaultMaxRetries,
		RetryableCodes: append([]string(nil), DefaultRetryableCodes...),
		InitialDelay:   DefaultInitialDelay,
		MaxDelay:       DefaultMaxDelay,
		JitterRatio:    DefaultJitterRatio,
	}
}

// Retryable reports whether code may be retried under this policy. Always mode
// retries everything except an abort: repeating a request the caller cancelled
// would ignore the cancellation.
func (p *RetryPolicy) Retryable(code string) bool {
	if p == nil || code == CodeAborted {
		return false
	}
	if p.Mode == RetryModeAlways {
		return true
	}
	for _, candidate := range p.RetryableCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

// Exhausted reports whether attempt retries already used up the policy budget.
// attempt counts retries performed so far, so the first failure sees attempt 0.
func (p *RetryPolicy) Exhausted(attempt int) bool {
	if p == nil {
		return true
	}
	if p.Mode == RetryModeAlways {
		return false
	}
	return attempt >= p.MaxRetries
}

// Delay reports how long to wait before retry number retry (1 for the first
// retry). The second result is false when the policy refuses to wait: a normal
// policy gives up instead of accepting a provider delay longer than MaxDelay.
//
// A provider-requested delay wins over the local backoff. Otherwise the delay is
// exponential from InitialDelay, capped at MaxDelay, and scaled by a symmetric
// jitter in [1-JitterRatio, 1+JitterRatio]. random samples [0,1); nil uses no
// jitter, which keeps tests deterministic.
func (p *RetryPolicy) Delay(retry int, providerRetryAfter time.Duration, random func() float64) (time.Duration, bool) {
	if p == nil {
		return 0, false
	}
	if providerRetryAfter > 0 {
		if providerRetryAfter > p.MaxDelay {
			if p.Mode != RetryModeAlways {
				return 0, false
			}
		} else {
			return providerRetryAfter, true
		}
	}
	if retry < 1 {
		retry = 1
	}
	initial := float64(p.InitialDelay) / float64(time.Millisecond)
	maximum := float64(p.MaxDelay) / float64(time.Millisecond)
	// The exponent bound only guards against an absurd retry count overflowing
	// the multiplication; the cap already limits the result.
	exponent := retry - 1
	if exponent > 1024 {
		exponent = 1024
	}
	exponential := math.Min(initial*math.Pow(2, float64(exponent)), maximum)
	jitter := 1.0
	if p.JitterRatio > 0 && random != nil {
		jitter = 1 - p.JitterRatio + 2*p.JitterRatio*random()
	}
	delay := math.Min(exponential*jitter, maximum)
	return time.Duration(delay * float64(time.Millisecond)), true
}

// Key identifies the resolved policy in the session log, so a reader can tell
// which policy produced a retry. The shape matches the upstream policy key: a
// JSON array, with the retryable codes sorted so an unordered policy still
// yields one key.
func (p *RetryPolicy) Key() string {
	if p == nil {
		return "null"
	}
	millis := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	var key any
	if p.Mode == RetryModeAlways {
		key = []any{p.Mode, millis(p.InitialDelay), millis(p.MaxDelay), p.JitterRatio}
	} else {
		codes := append([]string(nil), p.RetryableCodes...)
		sort.Strings(codes)
		key = []any{p.Mode, p.MaxRetries, codes, millis(p.InitialDelay), millis(p.MaxDelay), p.JitterRatio}
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return "null"
	}
	return string(encoded)
}
