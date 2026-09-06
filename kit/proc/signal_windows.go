//go:build windows

package proc

import (
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
// GracefulStopSupported, which a caller can read instead of guessing.
func deliver(p *os.Process, _ syscall.Signal) error { return p.Kill() }

// GracefulStopSupported is false: Windows terminates, it does not ask.
const GracefulStopSupported = false
