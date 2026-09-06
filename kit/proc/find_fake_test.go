package proc_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/ubgo/lath/kit/proc"
)

// fakePgrep puts a stub pgrep at the front of PATH and returns nothing.
//
// Why a fake rather than fault injection: Find shells out, so its failure
// modes, pgrep absent, pgrep failing, pgrep printing something unparseable ,
// are reachable by controlling what pgrep IS. That needs no seam in the
// package and exercises the real exec path, which an injected error would not.
//
// body is the script after the shebang. An empty body means "no pgrep at all":
// PATH is pointed at an empty directory instead.
func fakePgrep(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Find is unsupported on Windows")
	}
	dir := t.TempDir()
	if body != "" {
		script := filepath.Join(dir, "pgrep")
		if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Note: no t.Parallel in any test using this, t.Setenv forbids it.
	t.Setenv("PATH", dir)
}

// TestFindWithoutPgrepReportsUnsupported pins the distinction the doc comment
// promises: "could not look" must never be reported as "nothing is running".
// A caller asking "is the server already up?" would otherwise start a second
// one on a machine without pgrep.
func TestFindWithoutPgrepReportsUnsupported(t *testing.T) {
	fakePgrep(t, "") // an empty PATH directory
	_, err := proc.Find(context.Background(), regexp.MustCompile("anything"))
	if !errors.Is(err, proc.ErrUnsupported) {
		t.Fatalf("err = %v; want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "pgrep") {
		t.Errorf("err = %v; want it to name the missing program", err)
	}
}

// TestFindNoMatchIsNotAnError pins pgrep's exit 1, which means "nothing
// matched" and not "something broke".
func TestFindNoMatchIsNotAnError(t *testing.T) {
	fakePgrep(t, "exit 1")
	found, err := proc.Find(context.Background(), regexp.MustCompile("x"))
	if err != nil {
		t.Fatalf("err = %v; exit 1 from pgrep means no matches", err)
	}
	if len(found) != 0 {
		t.Errorf("found %v; want nothing", found)
	}
}

// TestFindUnexpectedExitIsReported pins that any OTHER non-zero exit is a
// failure and carries pgrep's stderr. The difference between a usage error
// and an empty system.
func TestFindUnexpectedExitIsReported(t *testing.T) {
	fakePgrep(t, "echo 'pgrep: invalid option' >&2; exit 2")
	_, err := proc.Find(context.Background(), regexp.MustCompile("x"))
	if err == nil {
		t.Fatal("pgrep exiting 2 produced no error")
	}
	if errors.Is(err, proc.ErrUnsupported) {
		t.Error("a failing pgrep was reported as an unsupported platform")
	}
	if !strings.Contains(err.Error(), "invalid option") {
		t.Errorf("err = %v; want it to carry pgrep's stderr", err)
	}
}

// TestFindParsesOutput covers the shapes pgrep's output can take, including
// the ones that must be skipped rather than crashing the scan.
func TestFindParsesOutput(t *testing.T) {
	self := os.Getpid()
	for _, tc := range []struct {
		name   string
		stdout string
		want   []proc.Info
	}{
		{
			"a pid and a command line",
			"4242 /usr/bin/node server.js --port 3000",
			[]proc.Info{{PID: 4242, Command: "/usr/bin/node server.js --port 3000"}},
		},
		{
			// pgrep without -l, or a process whose argv is unreadable.
			"a pid with no command",
			"4242",
			[]proc.Info{{PID: 4242, Command: ""}},
		},
		{
			// Skipped, not fatal: one unparseable line must not discard the
			// rest of a scan.
			"a malformed line is skipped",
			"not-a-pid some command\n4242 real",
			[]proc.Info{{PID: 4242, Command: "real"}},
		},
		{
			"blank lines are ignored",
			"\n\n4242 real\n\n",
			[]proc.Info{{PID: 4242, Command: "real"}},
		},
		{
			"leading and trailing spaces are trimmed",
			"   4242 real   ",
			[]proc.Info{{PID: 4242, Command: "real"}},
		},
		{
			// The exclusion, driven directly: pgrep reports OUR pid and it
			// must not come back.
			"the calling process is excluded",
			fmt.Sprintf("%d self\n4242 other", self),
			[]proc.Info{{PID: 4242, Command: "other"}},
		},
		{
			"only the calling process matched",
			fmt.Sprint(self),
			nil,
		},
		{
			"several processes keep their order",
			"1 one\n2 two\n3 three",
			[]proc.Info{{PID: 1, Command: "one"}, {PID: 2, Command: "two"}, {PID: 3, Command: "three"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Single-quoted for the shell; no fixture contains a quote.
			fakePgrep(t, "printf '%s\\n' '"+tc.stdout+"'")
			got, err := proc.Find(context.Background(), regexp.MustCompile("x"))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

// TestSignalMatchingPropagatesFindFailure pins that a broken lookup is
// reported rather than quietly signalling nothing.
func TestSignalMatchingPropagatesFindFailure(t *testing.T) {
	fakePgrep(t, "")
	_, err := proc.SignalMatching(context.Background(),
		regexp.MustCompile("x"), syscall.SIGTERM)
	if !errors.Is(err, proc.ErrUnsupported) {
		t.Errorf("err = %v; want ErrUnsupported", err)
	}
}

// TestSignalMatchingIgnoresVanishedProcesses pins that a process which exited
// between the scan and the signal is not an error, it is the outcome wanted,
// and the race is unavoidable.
func TestSignalMatchingIgnoresVanishedProcesses(t *testing.T) {
	// A pid above the system maximum: guaranteed absent, so Kill gives ESRCH.
	const absentPID = 4194304
	fakePgrep(t, fmt.Sprintf("echo '%d long-gone'", absentPID))

	found, err := proc.SignalMatching(context.Background(),
		regexp.MustCompile("x"), syscall.SIGTERM)
	if err != nil {
		t.Errorf("err = %v; a process that already exited is not a failure", err)
	}
	// It is still REPORTED: the caller asked what matched, and it did match.
	if len(found) != 1 || found[0].PID != absentPID {
		t.Errorf("found = %v; want the matched process to be reported", found)
	}
}
