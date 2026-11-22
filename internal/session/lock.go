package session

import "errors"

// ErrSessionLocked reports a second process that tried to write a session whose
// log is already owned. The OS releases the lock when the owner exits, so a crash
// never leaves a session permanently locked.
var ErrSessionLocked = errors.New("session: log is locked by another process")
