//go:build unix

package proc

import (
	"os"
	"syscall"
)

// deliver sends sig to the process.
//
// On unix this is the signal the caller asked for, which is the whole point of
// Stop's two-phase shutdown: SIGTERM lets a process flush, close and release
// its port; SIGKILL is the fallback for one that ignores it.
func deliver(p *os.Process, sig syscall.Signal) error { return p.Signal(sig) }

// GracefulStopSupported reports whether asking a process to exit — as opposed
// to killing it — means anything on this platform.
//
// Exported so a CALLER can adjust rather than guess. A supervisor that gives a
// process thirty seconds to drain connections is doing something useful here
// and nothing at all where the first signal already terminated it.
const GracefulStopSupported = true
