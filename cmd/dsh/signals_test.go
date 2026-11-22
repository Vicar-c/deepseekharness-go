package main

import (
	"os"
	"syscall"
	"testing"
)

// TestExitCodeForSignal pins the shell convention: an interrupt is 130, a
// terminate request is a clean 0 (MIGRATION_MAP §6-18).
func TestExitCodeForSignal(t *testing.T) {
	cases := []struct {
		name string
		sig  os.Signal
		want int
	}{
		{"interrupt", os.Interrupt, 130},
		{"sigint", syscall.SIGINT, 130},
		{"sigterm", syscall.SIGTERM, 0},
		{"none", nil, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := exitCodeForSignal(testCase.sig); got != testCase.want {
				t.Fatalf("exitCodeForSignal(%v) = %d, want %d", testCase.sig, got, testCase.want)
			}
		})
	}
}

func TestFailureCodePrefersTheSignalCode(t *testing.T) {
	plain := &signalState{}
	if got := failureCode(plain); got != 1 {
		t.Fatalf("failureCode without a signal = %d, want 1", got)
	}
	if got := failureCode(nil); got != 1 {
		t.Fatalf("failureCode(nil) = %d, want 1", got)
	}

	interrupted := &signalState{}
	interrupted.note(os.Interrupt)
	if got := failureCode(interrupted); got != 130 {
		t.Fatalf("failureCode after SIGINT = %d, want 130", got)
	}
	if got := interrupted.exitCode(); got != 130 {
		t.Fatalf("exitCode after SIGINT = %d, want 130", got)
	}

	terminated := &signalState{}
	terminated.note(syscall.SIGTERM)
	if got := failureCode(terminated); got != 0 {
		t.Fatalf("failureCode after SIGTERM = %d, want 0", got)
	}
}

// TestSignalStateKeepsTheFirstSignal makes sure a second signal cannot rewrite the
// exit code of the shutdown that is already in progress.
func TestSignalStateKeepsTheFirstSignal(t *testing.T) {
	state := &signalState{}
	state.note(os.Interrupt)
	state.note(syscall.SIGTERM)
	if got := state.exitCode(); got != 130 {
		t.Fatalf("exitCode = %d, want the first signal's 130", got)
	}
}
