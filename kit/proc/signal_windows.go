//go:build windows

package proc

import (
	"errors"
	"os"
	"syscall"
)

// deliver terminates the process, because Windows has no signals.
//
// Go's os.Process.Signal accepts only os.Kill there; anything else fails with
// "not supported by windows". That failure used to propagate, which meant
// Stop, Timeout and context cancellation ALL did nothing on Windows: a test
// that expected a cancelled command to die sat for the full thirty seconds and
// then reported exit 0. A process nobody can stop is worse than no Stop at
// all, so the signal a caller asked for becomes a kill here.
//
// The cost is stated rather than hidden: there is no graceful phase on
// Windows. A process gets TerminateProcess with no chance to flush, close or
// release a port, and no amount of grace changes that — see
// SignalsSupported, which a caller can read instead of guessing.
func deliver(p *os.Process, _ syscall.Signal) error {
	err := p.Kill()
	if err == nil {
		return nil
	}
	// Windows says "already gone" in two ways that look like real failures.
	// EINVAL comes from a handle whose process has been reaped, and
	// ERROR_ACCESS_DENIED from TerminateProcess on one that has exited. Unix
	// says ESRCH for the same situation and Signal already treats that as
	// nothing to do; without this, the ordinary shape — Wait, then a deferred
	// Stop — reported an error for a process that had simply finished.
	//
	// Safe to fold here because this only ever runs against a child this
	// package started. Signalling a stranger's process goes through
	// SignalMatching, which reports ErrUnsupported on Windows and never
	// reaches this function.
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return os.ErrProcessDone
	}
	return err
}

// SignalsSupported is false: Windows terminates, it does not ask. Stop's
// grace period buys nothing here, and Result.Signalled is never true — a
// terminated process reports an exit code like any other.
const SignalsSupported = false
