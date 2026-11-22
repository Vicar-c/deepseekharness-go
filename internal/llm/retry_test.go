package llm_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/llm"
)

// TestDefaultRetryPolicy pins the built-in policy, because a silent change to it
// changes how long a user waits after a provider failure.
func TestDefaultRetryPolicy(t *testing.T) {
	policy := llm.DefaultRetryPolicy()
	if policy.Mode != llm.RetryModeNormal {
		t.Fatalf("mode = %q, want normal", policy.Mode)
	}
	if policy.MaxRetries != 5 {
		t.Fatalf("max retries = %d, want 5", policy.MaxRetries)
	}
	if policy.InitialDelay != 500*time.Millisecond {
		t.Fatalf("initial delay = %s, want 500ms", policy.InitialDelay)
	}
	if policy.MaxDelay != 10*time.Second {
		t.Fatalf("max delay = %s, want 10s", policy.MaxDelay)
	}
	if policy.JitterRatio != 0.1 {
		t.Fatalf("jitter = %v, want 0.1", policy.JitterRatio)
	}
	want := []string{"EMPTY_RESPONSE", "RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT"}
	if strings.Join(policy.RetryableCodes, ",") != strings.Join(want, ",") {
		t.Fatalf("retryable codes = %v, want %v", policy.RetryableCodes, want)
	}
	// The default slice must be a copy: mutating the policy must not change the
	// package defaults for the next loop.
	policy.RetryableCodes[0] = "MUTATED"
	if llm.DefaultRetryPolicy().RetryableCodes[0] != "EMPTY_RESPONSE" {
		t.Fatal("DefaultRetryPolicy shares its slice with the package default")
	}
}

// TestRetryable covers the codes a normal policy repeats, always mode, and the
// one code no policy repeats.
func TestRetryable(t *testing.T) {
	normal := llm.DefaultRetryPolicy()
	for _, code := range []string{llm.CodeRateLimit, llm.CodeServer, llm.CodeTimeout, llm.CodeTransport, llm.CodeEmptyResponse} {
		if !normal.Retryable(code) {
			t.Errorf("normal policy does not retry %s", code)
		}
	}
	for _, code := range []string{llm.CodeAuth, llm.CodeQuota, llm.CodeInvalidRequest, llm.CodeContextWindowExceeded, llm.CodeMissingCredential, "HTTP_418"} {
		if normal.Retryable(code) {
			t.Errorf("normal policy retries %s, but a repeat cannot fix it", code)
		}
	}
	always := llm.DefaultRetryPolicy()
	always.Mode = llm.RetryModeAlways
	if !always.Retryable(llm.CodeAuth) {
		t.Error("always mode must retry every failure")
	}
	if always.Retryable(llm.CodeAborted) {
		t.Error("no policy retries an abort")
	}
	var nilPolicy *llm.RetryPolicy
	if nilPolicy.Retryable(llm.CodeRateLimit) {
		t.Error("nil policy must not retry")
	}
}

// TestExhausted bounds retries in normal mode and leaves always mode unbounded.
func TestExhausted(t *testing.T) {
	policy := llm.DefaultRetryPolicy()
	policy.MaxRetries = 2
	for attempt, want := range []bool{false, false, true, true} {
		if got := policy.Exhausted(attempt); got != want {
			t.Errorf("Exhausted(%d) = %v, want %v", attempt, got, want)
		}
	}
	policy.Mode = llm.RetryModeAlways
	if policy.Exhausted(1_000_000) {
		t.Fatal("always mode must never be exhausted")
	}
	policy.MaxRetries = 0
	policy.Mode = llm.RetryModeNormal
	if !policy.Exhausted(0) {
		t.Fatal("a policy with no retries must be exhausted immediately")
	}
}

// TestDelayExponentialBackoff pins the doubling and the cap, with jitter off.
func TestDelayExponentialBackoff(t *testing.T) {
	policy := llm.DefaultRetryPolicy()
	policy.JitterRatio = 0
	want := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}
	for index, expected := range want {
		delay, ok := policy.Delay(index+1, 0, nil)
		if !ok {
			t.Fatalf("retry %d refused a local delay", index+1)
		}
		if delay != expected {
			t.Errorf("retry %d delay = %s, want %s", index+1, delay, expected)
		}
	}
}

// TestDelayJitterBounds keeps a jittered delay inside the documented range.
func TestDelayJitterBounds(t *testing.T) {
	policy := llm.DefaultRetryPolicy()
	if delay, _ := policy.Delay(1, 0, func() float64 { return 0 }); delay != 450*time.Millisecond {
		t.Fatalf("low sample delay = %s, want 450ms", delay)
	}
	if delay, _ := policy.Delay(1, 0, func() float64 { return 1 }); delay != 550*time.Millisecond {
		t.Fatalf("high sample delay = %s, want 550ms", delay)
	}
	// The jitter must never push a delay past the cap.
	policy.InitialDelay = 9 * time.Second
	if delay, _ := policy.Delay(4, 0, func() float64 { return 1 }); delay > policy.MaxDelay {
		t.Fatalf("jittered delay %s exceeds the cap %s", delay, policy.MaxDelay)
	}
}

// TestDelayProviderRetryAfter proves a provider-requested delay wins, and that a
// normal policy gives up rather than sleep longer than its cap.
func TestDelayProviderRetryAfter(t *testing.T) {
	policy := llm.DefaultRetryPolicy()
	policy.JitterRatio = 0
	delay, ok := policy.Delay(1, 2500*time.Millisecond, nil)
	if !ok || delay != 2500*time.Millisecond {
		t.Fatalf("delay = %s, ok = %v, want 2.5s", delay, ok)
	}
	if _, ok := policy.Delay(1, 11*time.Second, nil); ok {
		t.Fatal("normal policy must refuse a provider delay beyond its cap")
	}
	always := llm.DefaultRetryPolicy()
	always.Mode = llm.RetryModeAlways
	always.JitterRatio = 0
	delay, ok = always.Delay(1, 11*time.Second, nil)
	if !ok || delay > always.MaxDelay {
		t.Fatalf("always policy delay = %s, ok = %v, want a local delay", delay, ok)
	}
}

// TestPolicyKey identifies the policy in the log and ignores the code order.
func TestPolicyKey(t *testing.T) {
	first := llm.DefaultRetryPolicy()
	second := llm.DefaultRetryPolicy()
	second.RetryableCodes = []string{"TRANSPORT", "TIMEOUT", "SERVER", "RATE_LIMIT", "EMPTY_RESPONSE"}
	if first.Key() != second.Key() {
		t.Fatalf("policy keys differ for the same policy:\n%s\n%s", first.Key(), second.Key())
	}
	var decoded []any
	if err := json.Unmarshal([]byte(first.Key()), &decoded); err != nil {
		t.Fatalf("policy key is not a JSON array: %v", err)
	}
	if len(decoded) != 6 || decoded[0] != "normal" {
		t.Fatalf("policy key = %v, want the normal-mode shape", decoded)
	}
	second.MaxRetries = 7
	if first.Key() == second.Key() {
		t.Fatal("policy key ignores maxRetries")
	}
	always := llm.DefaultRetryPolicy()
	always.Mode = llm.RetryModeAlways
	if err := json.Unmarshal([]byte(always.Key()), &decoded); err != nil {
		t.Fatalf("always policy key is not JSON: %v", err)
	}
	if len(decoded) != 4 || decoded[0] != "always" {
		t.Fatalf("always policy key = %v, want four fields", decoded)
	}
	var nilPolicy *llm.RetryPolicy
	if nilPolicy.Key() != "null" {
		t.Fatalf("nil policy key = %q", nilPolicy.Key())
	}
}
