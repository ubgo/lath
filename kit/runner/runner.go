// Package runner decides where a command executes.
//
// It is the type that lets one pipeline run on a laptop and on a deploy target
// without changing anything else. Code never asks "am I local or remote", it
// asks its Runner to run something, and the Runner knows.
//
// Swapping Local for Remote is the whole difference between rehearsing a
// deploy against a scratch container and performing it against production.
package runner

import (
	"context"
	"fmt"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/ssh"
)

// Runner is somewhere commands can be executed.
//
// Invariant: Run reports a non-zero exit in the Result, NOT as an error,
// matching proc. An error means the command could not be started at all, the
// program is missing, or the host is unreachable.
type Runner interface {
	// Run executes name with args and reports how it ended.
	Run(ctx context.Context, name string, args []string, opts ...proc.Option) (proc.Result, error)
	// Describe names where commands go, for output and error messages.
	Describe() string
}

// Local runs commands on this machine.
type Local struct{}

// Run executes the command through proc.
func (Local) Run(ctx context.Context, name string, args []string, opts ...proc.Option) (proc.Result, error) {
	return proc.Run(ctx, name, args, opts...)
}

// Describe names this machine.
func (Local) Describe() string { return "locally" }

// Remote runs commands on another machine over ssh.
type Remote struct{ Host ssh.Host }

// Run sends the command to the host.
//
// Every argument is quoted, because ssh hands the command to a remote login
// shell: an unquoted path containing a space, or a value containing a
// semicolon, would otherwise be re-split or interpreted on the far side.
func (r Remote) Run(ctx context.Context, name string, args []string, opts ...proc.Option) (proc.Result, error) {
	command := ssh.Quote(name)
	for _, a := range args {
		command += " " + ssh.Quote(a)
	}
	return ssh.Run(ctx, r.Host, command, opts...)
}

// Describe names the host.
func (r Remote) Describe() string { return "on " + r.Host.String() }

// OrLocal returns r, or this machine when r is nil.
//
// The zero value of any struct holding a Runner therefore runs locally, which
// is the right default for the case these packages exist to enable: rehearsing
// something in full before it touches a server.
func OrLocal(r Runner) Runner {
	if r == nil {
		return Local{}
	}
	return r
}

// Check turns a Result into an error when the command failed, naming what was
// attempted, where, and what it said.
//
// Centralised so every caller reports a failure the same way: a message that
// omits where the command ran sends an operator to the wrong machine.
func Check(what string, where Runner, r proc.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s %s: %w", what, where.Describe(), err)
	}
	if !r.OK() {
		return fmt.Errorf("%s %s: %s: %s", what, where.Describe(), r, Tail(r.Stderr))
	}
	return nil
}

// stderrTailBytes caps how much of a failed command's output is quoted into an
// error: enough to see the cause, short enough to keep a log readable.
const stderrTailBytes = 800

// Tail returns the last stderrTailBytes of b, trimmed, for an error message.
func Tail(b []byte) string {
	s := trimSpace(string(b))
	if len(s) <= stderrTailBytes {
		return s
	}
	return "…" + s[len(s)-stderrTailBytes:]
}

// trimSpace avoids importing strings for one call.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
