package proc

import "errors"

// Sentinels for the conditions a caller may reasonably branch on.
//
// A non-zero exit is deliberately not among them: that is a Result, not an
// error. These cover the cases where no process ever ran, or stopped running
// for a reason outside its own control.
var (
	// ErrNotFound means the program is not on PATH.
	ErrNotFound = errors.New("proc: program not found")
	// ErrTimeout means the process was killed because Timeout elapsed.
	ErrTimeout = errors.New("proc: timed out")
	// ErrUnsupported means the operation has no implementation on this
	// platform. Returned rather than silently reporting nothing, so a caller
	// is never told "no processes match" when the truth is "cannot look".
	ErrUnsupported = errors.New("proc: unsupported on this platform")
)
