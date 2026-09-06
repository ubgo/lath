//go:build windows

package proc

import (
	"errors"
	"fmt"
	"syscall"
)

// errProcessGone has no Windows equivalent that killPID can return, since
// killPID never gets far enough to discover it. Declared so the shared code
// compiles and the comparison is simply never true.
var errProcessGone = errors.New("proc: no such process")

// killPID is unsupported on Windows for the same reason Find is: this package
// signals processes it did not start via POSIX semantics that Windows does not
// share. Reported rather than silently doing nothing, so a caller is never
// told a process was signalled when it was not.
func killPID(pid int, _ syscall.Signal) error {
	return fmt.Errorf("proc: signalling pid %d: %w", pid, ErrUnsupported)
}
