//go:build unix

package proc_test

// The signalling tests live in their own file because they are UNIX by
// nature: they need real signal semantics, a pid this user may not signal,
// and EPERM. syscall.Kill does not exist on Windows, so keeping them beside
// the portable tests made the package's test binary uncompilable there —
// invisible to `crosscheck`, which built the package and not its tests, and
// caught only when `go vet` was added to it for every platform.

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/ubgo/lath/kit/proc"
)

// needsAnUnsignallableProcess skips unless pid 1 is genuinely someone else's.
//
// Both tests below need a process this user may NOT signal, and use pid 1
// because on a normal machine it is init and root's. That premise is false in
// a container, where pid 1 is the very shell running the test and signalling
// it is permitted — so the tests failed there while passing everywhere else,
// reporting a product bug that did not exist.
//
// Probing with signal 0 asks the kernel the question directly: it performs the
// permission check and delivers nothing.
func needsAnUnsignallableProcess(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root may signal any process, so nothing is refused")
	}
	if err := syscall.Kill(1, syscall.Signal(0)); err == nil {
		t.Skip("this process may signal pid 1 (containerised init), so nothing is refused here")
	}
}

// TestSignalMatchingReportsRefusedSignals pins the other half: a signal that
// failed for a real reason is surfaced, while the scan result still comes back
// so the caller can say what happened.
func TestSignalMatchingReportsRefusedSignals(t *testing.T) {
	needsAnUnsignallableProcess(t)
	// pid 1 exists and is not ours: signalling it is refused with EPERM.
	// Signal 0 performs the permission check without delivering anything.
	fakePgrep(t, "echo '1 init'")

	found, err := proc.SignalMatching(context.Background(),
		regexp.MustCompile("x"), syscall.Signal(0))
	if err == nil {
		t.Fatal("a refused signal produced no error")
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Errorf("err = %v; want it to wrap EPERM", err)
	}
	if !strings.Contains(err.Error(), "pid 1") {
		t.Errorf("err = %v; want it to name the pid that refused", err)
	}
	// The contract that makes partial failure usable.
	if len(found) != 1 {
		t.Errorf("found = %v; the matches must be returned alongside the error", found)
	}
}

// TestSignalMatchingJoinsMultipleFailures pins that every failure is reported,
// not just the first. The same reasoning as env.RequireAll.
func TestSignalMatchingJoinsMultipleFailures(t *testing.T) {
	needsAnUnsignallableProcess(t)
	fakePgrep(t, "printf '1 init\\n1 init-again\\n'")
	_, err := proc.SignalMatching(context.Background(),
		regexp.MustCompile("x"), syscall.Signal(0))
	if err == nil {
		t.Fatal("no error")
	}
	if got := strings.Count(err.Error(), "pid 1"); got != 2 {
		t.Errorf("the error names %d failures; want 2: %v", got, err)
	}
}
