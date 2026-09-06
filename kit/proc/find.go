package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// pgrepProgram is the tool used to enumerate processes.
//
// Shelling out rather than reading /proc or calling sysctl, deliberately.
//
// Go has no standard way to list processes, so the alternatives are
// platform-specific: /proc on Linux, a sysctl call through unsafe pointers on
// macOS, a Win32 API on Windows. That would make this the only non-portable
// code in the kit, for one capability, and on macOS the "portable" version
// would end up shelling out to ps anyway.
//
// One implementation that fails honestly where pgrep is absent is a better
// trade than three that drift.
const pgrepProgram = "pgrep"

// pgrepNoMatchExitCode is what pgrep returns when nothing matched. Not an
// error: "no processes match" is a valid, common answer.
const pgrepNoMatchExitCode = 1

// pidAndCommandFields is how many parts `pgrep -fl` output splits into before
// the command line itself: just the pid.
const pidAndCommandFields = 2

// Info describes a running process.
type Info struct {
	// PID is the process id.
	PID int
	// Command is the full command line, as pgrep -f matches against.
	Command string
}

// Look reports the absolute path of a program, or ErrNotFound.
func Look(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("proc: %s: %w", name, ErrNotFound)
	}
	return path, nil
}

// Exists reports whether a program is on PATH. The `command -v` test.
func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ⚠️ Find and SignalMatching DO NOT WORK ON WINDOWS.
//
// They shell out to pgrep, which Windows does not have, so both return
// ErrUnsupported there. Every other function in this package is portable.
//
// The alternative was platform-specific code, /proc on Linux, sysctl through
// unsafe pointers on macOS, a Win32 API on Windows, which would make this the
// only non-portable implementation in the kit, for one capability, while the
// macOS path ended up shelling out to ps anyway.

// Find returns processes whose full command line matches pattern.
//
// The pattern is passed to pgrep, which uses POSIX extended regular
// expressions, close to Go's syntax but not identical. A *regexp.Regexp is
// taken rather than a string so the caller's pattern is at least valid
// somewhere, and so the type says what it is.
//
// An empty result is not an error: nothing matching is the common case.
// Returns ErrUnsupported when pgrep is absent, rather than reporting no
// matches. A caller must never be told "nothing is running" when the truth is
// "could not look".
func Find(ctx context.Context, pattern *regexp.Regexp, extra ...string) ([]Info, error) {
	if !Exists(pgrepProgram) {
		return nil, fmt.Errorf("proc: %s is required to find processes: %w",
			pgrepProgram, ErrUnsupported)
	}

	// -f matches the full command line, -l prints it alongside the pid. extra
	// reaches everything else pgrep offers, "-u", "someone" to scope to a
	// user, "-P", "1" to a parent, because modelling pgrep's flags here would
	// be a second pgrep, and a caller who needs one and cannot have it stops
	// using this package.
	args := append([]string{"-fl"}, extra...)
	r, err := Run(ctx, pgrepProgram, append(args, pattern.String()), Capture())
	if err != nil {
		return nil, err
	}
	if r.ExitCode == pgrepNoMatchExitCode {
		return nil, nil
	}
	if !r.OK() {
		return nil, fmt.Errorf("proc: %s: %s: %s",
			pgrepProgram, r, strings.TrimSpace(string(r.Stderr)))
	}

	var out []Info
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", pidAndCommandFields)
		pid, convErr := strconv.Atoi(parts[0])
		if convErr != nil {
			continue
		}
		cmdline := ""
		if len(parts) == pidAndCommandFields {
			cmdline = parts[1]
		}
		// pgrep's own process matches any pattern it was given. Excluded here
		// rather than left to every caller, who would each discover it once.
		if pid == selfPID() {
			continue
		}
		out = append(out, Info{PID: pid, Command: cmdline})
	}
	return out, nil
}

// SignalMatching sends sig to every process matching pattern and reports which
// were signalled.
//
// Returns the processes it found even when signalling some of them failed, so
// a caller can report what happened rather than only that something did.
func SignalMatching(ctx context.Context, pattern *regexp.Regexp, sig syscall.Signal, extra ...string) ([]Info, error) {
	found, err := Find(ctx, pattern, extra...)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, p := range found {
		if err := killPID(p.PID, sig); err != nil {
			// Already gone between the scan and the signal is a race, not a
			// failure. A process exiting on its own is the outcome wanted.
			if errors.Is(err, errProcessGone) {
				continue
			}
			failures = append(failures, fmt.Errorf("pid %d: %w", p.PID, err))
		}
	}
	return found, errors.Join(failures...)
}
