package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// exitCodeSignalBase is what shells add to a signal number when reporting a
// signalled exit, SIGTERM (15) becomes 143. Matched so a code logged here is
// the same number seen anywhere else on the system.
const exitCodeSignalBase = 128

// unknownExitCode is reported when a process failed in a way that produced no
// exit status at all. Negative so it can never be mistaken for a real code.
const unknownExitCode = -1

// Result describes a finished process.
//
// A non-zero ExitCode is NOT an error. It is information. Run returns an error
// only when the program could not be started or the context ended. That split
// is deliberate: a caller supervising a process needs the exit code as data,
// and forcing it through Go's error path means every such caller writes the
// same errors.As dance to get it back.
type Result struct {
	// ExitCode is the process's exit status, or exitCodeSignalBase+Signal when
	// Signalled. unknownExitCode when neither could be determined.
	ExitCode int
	// Signalled reports that the process was killed rather than exiting on its
	// own.
	//
	// This is the field that matters most. A supervisor that treats a
	// signalled exit as a crash restarts something that was asked to stop, and
	// loops forever if the signal keeps arriving.
	Signalled bool
	// Signal is the signal that killed it. Meaningless unless Signalled.
	Signal syscall.Signal
	// Duration is how long the process ran.
	Duration time.Duration
	// Stdout and Stderr are populated only when Capture was set. Nil otherwise,
	// so a caller cannot mistake "not captured" for "produced no output".
	Stdout []byte
	Stderr []byte
}

// OK reports whether the process finished successfully, exit zero, not killed.
func (r Result) OK() bool { return r.ExitCode == 0 && !r.Signalled }

// String renders a Result for a log line, naming the signal when there was one.
func (r Result) String() string {
	if r.Signalled {
		return fmt.Sprintf("exit %d (killed by %v) after %s",
			r.ExitCode, r.Signal, r.Duration.Round(time.Millisecond))
	}
	return fmt.Sprintf("exit %d after %s", r.ExitCode, r.Duration.Round(time.Millisecond))
}

// resultFrom converts what exec.Cmd.Wait reports into a Result.
//
// The archaeology here is the reason this package exists: Go surfaces a
// process's fate as an error that must be unwrapped to *exec.ExitError and then
// type-asserted to syscall.WaitStatus before it will admit whether a signal was
// involved.
func resultFrom(waitErr error, ran time.Duration) Result {
	r := Result{Duration: ran}
	if waitErr == nil {
		return r
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		r.ExitCode = unknownExitCode
		return r
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.Signalled = true
		r.Signal = ws.Signal()
		r.ExitCode = exitCodeSignalBase + int(r.Signal)
		return r
	}
	r.ExitCode = ee.ExitCode()
	return r
}
