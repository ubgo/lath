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

// SignalsSupported reports whether this platform has signals at all.
//
// Two things follow from it, which is why it is one constant rather than
// several. Stop's polite phase — SIGTERM, then SIGKILL after grace — is real
// only where this is true; elsewhere the first call already terminated the
// process and grace buys nothing. And Result.Signalled can only ever be true
// where this is, because a platform without signals reports an exit code
// instead.
//
// Exported so a caller can adjust rather than guess: a supervisor that gives a
// process thirty seconds to drain connections is doing something useful here
// and nothing at all where it is false.
const SignalsSupported = true
