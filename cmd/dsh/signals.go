package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// shutdownGrace is how long the process may take to shut down after the first
// termination signal before it exits anyway.
const shutdownGrace = 5 * time.Second

// signalState remembers which termination signal arrived so the process can exit
// with the shell-conventional code (130 for an interrupt, 0 for a terminate)
// instead of collapsing every stop into "error".
type signalState struct {
	mu       sync.Mutex
	received os.Signal
}

func (s *signalState) note(received os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.received == nil {
		s.received = received
	}
}

// signaled reports whether a termination signal was received.
func (s *signalState) signaled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received != nil
}

// exitCode is the code the process should exit with for the received signal.
func (s *signalState) exitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return exitCodeForSignal(s.received)
}

// exitCodeForSignal maps a stop signal to the conventional process exit code.
func exitCodeForSignal(received os.Signal) int {
	if received == os.Interrupt || received == syscall.SIGINT {
		return 130
	}
	return 0
}

// failureCode is the exit code for a command that failed: the signal code when
// the run was stopped by a signal, 1 otherwise.
func failureCode(state *signalState) int {
	if state != nil && state.signaled() {
		return state.exitCode()
	}
	return 1
}

// newSignalContext cancels the context on the first SIGINT/SIGTERM, forces an
// exit after shutdownGrace seconds, and exits immediately on a second signal.
// The returned stop function releases the handler without exiting.
func newSignalContext() (context.Context, *signalState, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	state := &signalState{}
	notifications := make(chan os.Signal, 2)
	stopped := make(chan struct{})
	signal.Notify(notifications, os.Interrupt, syscall.SIGTERM)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(notifications)
			close(stopped)
			cancel()
		})
	}

	go func() {
		select {
		case first := <-notifications:
			state.note(first)
			cancel()
			grace := time.NewTimer(shutdownGrace)
			defer grace.Stop()
			select {
			case second := <-notifications:
				state.note(second)
				os.Exit(state.exitCode())
			case <-grace.C:
				os.Exit(state.exitCode())
			case <-stopped:
			}
		case <-stopped:
		}
	}()

	return ctx, state, stop
}
