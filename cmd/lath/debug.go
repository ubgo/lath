package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/session"
	"github.com/ubgo/lath/pipeline/debug"
)

// debugFlag turns an ordinary run into a step-through session.
//
// Recognised by lath and never seen by the author's target: it is stripped
// from the arguments before hand-off, and replaced with socketFlag so the
// definition knows where to listen.
const debugFlag = "--debug"

// socketFlag carries the session's socket path into the definition.
//
// Why lath opens the session and the definition merely listens: advertising
// needs kit/session, which needs kit/lock's process-start-time liveness check,
// and the definition side must stay stdlib-only so no project's go.mod grows a
// dependency to support a debugger. lath already links kit; the definition
// must not have to.
//
// This survives the hand-off because syscall.Exec REPLACES the process rather
// than spawning one: the PID and the process start time are unchanged, so the
// advertisement lath took a moment earlier still describes the running
// definition exactly.
const socketFlag = "--lath-debug-socket"

// ttyFlag tells the definition that it owns the terminal, so a tool may draw
// on it directly even though a debugger is attached. Passed only by `lath tui`
// when it launched the run itself into this terminal, never when attaching to
// a run started elsewhere, whose output goes to its own terminal.
const ttyFlag = "--lath-debug-tty"

// waitFlag carries how long the definition should wait to be attached to.
// Separate from socketFlag so each argument carries one value and a malformed
// one names itself.
const waitFlag = "--lath-debug-wait"

// takeDebugFlag removes debugFlag from args, reporting whether it was present
// and how long the run should wait to be attached to.
//
// Removed rather than passed through because an author's target parses its own
// arguments, and an unrecognised flag arriving there is at best ignored and at
// worst rejected, neither of which the operator asked for by typing --debug.
//
// Accepts a duration, "--debug=10m", as the one escape hatch, for stepping
// something whose first step takes long enough that you would rather start it
// and walk away. An unparseable value is reported rather than silently
// falling back to the default, since the caller clearly meant something.
func takeDebugFlag(args []string) ([]string, bool, time.Duration, error) {
	out := make([]string, 0, len(args))
	found := false
	wait := debug.DefaultAttachTimeout
	for _, a := range args {
		switch {
		case a == debugFlag:
			found = true
		case strings.HasPrefix(a, debugFlag+"="):
			found = true
			d, err := time.ParseDuration(strings.TrimPrefix(a, debugFlag+"="))
			if err != nil {
				return nil, false, 0, fmt.Errorf("%s: %w", a, err)
			}
			if d <= 0 {
				return nil, false, 0, fmt.Errorf("%s: the wait must be positive", a)
			}
			wait = d
		default:
			out = append(out, a)
			continue
		}
	}
	return out, found, wait, nil
}

// openDebugSession advertises a session for this run and returns the arguments
// to hand off, with socketFlag appended.
//
// The advertisement is deliberately NOT closed here: the exec'd definition
// inherits it by inheriting the PID, and closing it would unadvertise the
// session lath just created. It is swept when the process ends, see
// session.List, which removes any session whose holder is gone.
func openDebugSession(args []string, projectDir string, wait time.Duration) ([]string, error) {
	key := sessionKey(projectDir, args)
	adv, err := session.Advertise(context.Background(), key, session.Meta{
		"project": filepath.Base(projectDir),
		"dir":     projectDir,
		"target":  strings.Join(args, " "),
	})
	if err != nil {
		return nil, err
	}
	return append(args, socketFlag+"="+adv.Socket(), waitFlag+"="+wait.String()), nil
}

// sessionKey identifies the work being debugged.
//
// Both halves matter. Without the project, two repositories running the same
// target would collide; without the target, one project stepped through a
// deploy and a secrets push at once would. The PID that session.Advertise adds
// separates two runs of the same target in the same project.
func sessionKey(projectDir string, args []string) string {
	return projectDir + "|" + strings.Join(args, " ")
}

// debugPreflight refuses a session that cannot be driven.
//
// Checked before anything is built or advertised, because the failure an
// operator must never see is a deploy that reached step 6 and then blocked
// forever on a keystroke nobody could send. A CI runner has no terminal to
// attach FROM, and the run would sit out its timeout and abort, after doing
// real work.
func debugPreflight() error {
	if isTerminal(os.Stdin) || isTerminal(os.Stdout) {
		return nil
	}
	return fmt.Errorf("%s needs a terminal: nothing here could attach to the session", debugFlag)
}
