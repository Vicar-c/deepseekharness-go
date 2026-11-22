//go:build windows

package session

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// Windows exclusivity uses LockFileEx on a sidecar lock file. The handle is held
// for the life of the writer and the kernel releases the range when the process
// exits, so a crashed harness does not leave a session locked.
const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

var (
	dshKernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = dshKernel32.NewProc("LockFileEx")
	procUnlockFileEx = dshKernel32.NewProc("UnlockFileEx")
)

type fileLock struct {
	file       *os.File
	overlapped *syscall.Overlapped
}

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	overlapped := &syscall.Overlapped{}
	locked, _, callErr := procLockFileEx.Call(
		file.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, // reserved
		1, // low 32 bits of the byte count
		0, // high 32 bits of the byte count
		uintptr(unsafe.Pointer(overlapped)),
	)
	// The OVERLAPPED must outlive the call; KeepAlive keeps it reachable until
	// the lock is released.
	runtime.KeepAlive(overlapped)
	if locked == 0 {
		_ = file.Close()
		if callErr == nil {
			callErr = syscall.ERROR_ACCESS_DENIED
		}
		return nil, fmt.Errorf("%w: %v", ErrSessionLocked, callErr)
	}
	return &fileLock{file: file, overlapped: overlapped}, nil
}

func (l *fileLock) release() error {
	unlocked, _, unlockErr := procUnlockFileEx.Call(
		l.file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(l.overlapped)),
	)
	runtime.KeepAlive(l.overlapped)
	if unlocked == 0 && unlockErr != nil {
		return errors.Join(fmt.Errorf("session: unlock %s: %v", l.file.Name(), unlockErr), l.file.Close())
	}
	return l.file.Close()
}
