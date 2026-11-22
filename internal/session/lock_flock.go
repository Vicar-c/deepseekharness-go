//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package session

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// fileLock is an exclusive advisory lock on a lock file, held for as long as the
// writer owns the session. flock releases it automatically when the process
// dies, so a crash never leaves a session permanently locked.
type fileLock struct {
	file *os.File
}

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %v", ErrSessionLocked, err)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) release() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return errors.Join(unlockErr, l.file.Close())
}
