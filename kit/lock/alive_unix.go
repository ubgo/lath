//go:build !windows

package lock

import (
	"errors"
	"syscall"
)

// processExists reports whether a PID is live.
//
// Signal 0 delivers nothing and only performs the permission and existence
// checks. EPERM means the process exists but belongs to another user, still
// alive, and still holding the lock.
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
