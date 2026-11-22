//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package session

import "fmt"

// fileLock is a placeholder for platforms without an advisory file lock in the
// standard library. Opening a session for writing fails loudly instead of
// silently allowing two writers to interleave one log.
type fileLock struct{}

func acquireFileLock(path string) (*fileLock, error) {
	return nil, fmt.Errorf("session: file locking is not supported on this platform")
}

func (*fileLock) release() error { return nil }
