package proc_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/proc"
)

// ── lifecycle edges ──────────────────────────────────────────────────────

// TestWaitTwice pins that a second Wait reports an error rather than blocking
// forever. A supervisor that retries a failed Wait would otherwise hang.
func TestWaitTwice(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	done := make(chan struct{})
	go func() { _, _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a second Wait blocked; it must return rather than hang")
	}
}

// TestSignalAfterExit pins that signalling a finished process is a no-op, not
// an error. During shutdown the process is often already gone, and reporting
// that would put noise in every clean exit.
func TestSignalAfterExit(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("signalling an exited process = %v; want nil", err)
	}
}

// TestStopZeroGrace pins that a zero grace period kills immediately instead of
// waiting forever for a SIGTERM the process ignores.
func TestStopZeroGrace(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "trap '' TERM; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Stop(context.Background(), 0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %s with a zero grace; it should kill at once", elapsed)
	}
}

// TestStopAlreadyExited pins that stopping a finished process succeeds.
func TestStopAlreadyExited(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := p.Stop(context.Background(), time.Second); err != nil {
		t.Errorf("Stop on an exited process = %v; want nil", err)
	}
}

// TestStopCancelledContext pins that Stop returns promptly when its own context
// ends, rather than waiting out the grace period.
func TestStopCancelledContext(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "trap '' TERM; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err = p.Stop(ctx, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Stop = %v; want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %s; a cancelled Stop must not wait out the grace", elapsed)
	}
}

// ── input and output edges ───────────────────────────────────────────────

func TestStdin(t *testing.T) {
	t.Parallel()
	requireShell(t)
	out, err := proc.Output(context.Background(), shell, []string{"-c", "cat"},
		proc.Stdin(strings.NewReader("piped input")))
	if err != nil {
		t.Fatal(err)
	}
	if out != "piped input" {
		t.Errorf("Output = %q; want the stdin contents", out)
	}
}

func TestSplitSeparatesStreams(t *testing.T) {
	t.Parallel()
	requireShell(t)
	var outBuf, errBuf strings.Builder
	if _, err := proc.Run(context.Background(), shell,
		[]string{"-c", "echo to-stdout; echo to-stderr >&2"},
		proc.Split(&outBuf, &errBuf)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outBuf.String(), "to-stdout") || strings.Contains(outBuf.String(), "to-stderr") {
		t.Errorf("stdout writer got %q", outBuf.String())
	}
	if !strings.Contains(errBuf.String(), "to-stderr") || strings.Contains(errBuf.String(), "to-stdout") {
		t.Errorf("stderr writer got %q", errBuf.String())
	}
}

// TestCaptureEmptyOutput pins that a process producing nothing yields an empty
// slice, not nil. With Capture set, "captured nothing" and "did not capture"
// must stay distinguishable.
func TestCaptureEmptyOutput(t *testing.T) {
	t.Parallel()
	requireShell(t)
	r, err := proc.Run(context.Background(), shell, []string{"-c", "true"}, proc.Capture())
	if err != nil {
		t.Fatal(err)
	}
	if r.Stdout == nil {
		t.Error("Stdout is nil with Capture set; want an empty slice")
	}
	if len(r.Stdout) != 0 {
		t.Errorf("Stdout = %q; want empty", r.Stdout)
	}
}

// TestLargeOutputDoesNotDeadlock pins that a process writing more than a pipe
// buffer completes. An implementation that waits before draining deadlocks at
// roughly 64KB, and only on outputs large enough that nobody tests them.
func TestLargeOutputDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	requireShell(t)
	const lines = 20000
	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, err = proc.Output(context.Background(), shell,
			[]string{"-c", fmt.Sprintf("i=0; while [ $i -lt %d ]; do echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; i=$((i+1)); done", lines)})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("large output deadlocked")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "\n") + 1; got != lines {
		t.Errorf("got %d lines; want %d", got, lines)
	}
}

// ── environment edges ────────────────────────────────────────────────────

// TestEnvValueContainingEquals pins that a value with '=' survives. Naive
// splitting on the first '=' truncates connection strings and base64.
func TestEnvValueContainingEquals(t *testing.T) {
	t.Parallel()
	requireShell(t)
	out, err := proc.Output(context.Background(), shell, []string{"-c", "echo \"$WEIRD\""},
		proc.Env("WEIRD=a=b=c=="))
	if err != nil {
		t.Fatal(err)
	}
	if out != "a=b=c==" {
		t.Errorf("Output = %q; want the full value", out)
	}
}

// TestEnvLastWins pins the shell's own rule for a repeated variable.
func TestEnvLastWins(t *testing.T) {
	t.Parallel()
	requireShell(t)
	out, err := proc.Output(context.Background(), shell, []string{"-c", "echo \"$DUP\""},
		proc.Env("DUP=first", "DUP=second"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "second" {
		t.Errorf("Output = %q; want the last assignment to win", out)
	}
}

// TestEnvOnlyIsolationSurvivesLaterEnv pins that adding a variable does not
// silently undo the isolation EnvOnly was asked for.
//
// The regression this guards is quiet and security-relevant: EnvOnly is how a
// caller keeps its own secrets out of a child, and a later Env used to clear
// the flag, handing over the entire parent environment while the code still
// read as isolated.
func TestEnvOnlyIsolationSurvivesLaterEnv(t *testing.T) {
	// No t.Parallel: t.Setenv mutates process-wide state and the two are
	// mutually exclusive.
	requireShell(t)
	t.Setenv("LATH_TEST_SECRET", "must-not-leak")

	for _, order := range []struct {
		name string
		opts []proc.Option
	}{
		{"EnvOnly then Env", []proc.Option{
			proc.EnvOnly("PATH=" + mustPath(t)), proc.Env("EXTRA=1")}},
		{"Env then EnvOnly", []proc.Option{
			proc.Env("EXTRA=1"), proc.EnvOnly("PATH=" + mustPath(t))}},
	} {
		t.Run(order.name, func(t *testing.T) {
			out, err := proc.Output(context.Background(), shell,
				[]string{"-c", "echo \"${LATH_TEST_SECRET:-absent}/${EXTRA:-absent}\""},
				order.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := out, "absent/1"; got != want {
				t.Errorf("got %q; want %q: either the secret leaked or the added variable was dropped", got, want)
			}
		})
	}
}

// TestWaitThenStop pins the production shape that first exposed a panic: a
// supervisor waits for the process, then a deferred cleanup stops it. Stop
// calls Wait internally, and os/exec permits exactly one real Wait.
func TestWaitThenStop(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background(), time.Second); err != nil {
		t.Errorf("Stop after Wait = %v; want nil", err)
	}
	r2, err := p.Wait()
	if err != nil {
		t.Fatalf("third Wait: %v", err)
	}
	if r1.ExitCode != r2.ExitCode || r1.ExitCode != 3 {
		t.Errorf("Wait reported %d then %d; both must be the recorded exit 3", r1.ExitCode, r2.ExitCode)
	}
}

// TestConcurrentWait pins that goroutines racing to reap agree, rather than
// one of them getting an error because the other won.
func TestConcurrentWait(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	codes := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := p.Wait()
			codes[i], errs[i] = r.ExitCode, err
		}(i)
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Errorf("waiter %d: %v", i, errs[i])
		}
		if codes[i] != 7 {
			t.Errorf("waiter %d saw exit %d; want 7: waiters disagreed", i, codes[i])
		}
	}
}

// ── failure edges ────────────────────────────────────────────────────────

func TestDirDoesNotExist(t *testing.T) {
	t.Parallel()
	requireShell(t)
	_, err := proc.Run(context.Background(), shell, []string{"-c", "true"},
		proc.Dir("/no/such/directory/anywhere"))
	if err == nil {
		t.Fatal("a missing working directory produced no error")
	}
	// Not ErrNotFound: the PROGRAM was found, the directory was not. Conflating
	// them would send a caller looking for the wrong thing.
	if errors.Is(err, proc.ErrNotFound) {
		t.Error("a missing directory reported ErrNotFound, which is about the program")
	}
}

// TestAlreadyCancelledContext pins that Run with a dead context does not hang,
// and reports something a caller can act on.
func TestAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	requireShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = proc.Run(ctx, shell, []string{"-c", "sleep 5"})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run with an already-cancelled context hung")
	}
}

// TestTimeoutThatDoesNotFire pins that a run finishing inside its timeout
// reports no error and leaves no lingering goroutine work.
func TestTimeoutThatDoesNotFire(t *testing.T) {
	t.Parallel()
	requireShell(t)
	r, err := proc.Run(context.Background(), shell, []string{"-c", "true"},
		proc.Timeout(30*time.Second))
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if !r.OK() {
		t.Errorf("Result = %v; want success", r)
	}
}

// TestNoArgs pins that a program taking no arguments works with both nil and
// an empty slice. A caller should not have to know which.
func TestNoArgs(t *testing.T) {
	t.Parallel()
	if !proc.Exists("true") {
		t.Skip("true not available")
	}
	for _, args := range [][]string{nil, {}} {
		r, err := proc.Run(context.Background(), "true", args)
		if err != nil || !r.OK() {
			t.Errorf("Run(true, %v) = %v, %v", args, r, err)
		}
	}
}

// ── Result semantics ─────────────────────────────────────────────────────

func TestResultOKMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		r    proc.Result
		want bool
	}{
		{"clean exit", proc.Result{ExitCode: 0}, true},
		{"non-zero exit", proc.Result{ExitCode: 1}, false},
		// The case that matters: exit code zero but killed. A supervisor
		// treating this as success would ignore a process that was destroyed.
		{"signalled with a zero code", proc.Result{ExitCode: 0, Signalled: true}, false},
		{"signalled", proc.Result{ExitCode: 143, Signalled: true, Signal: syscall.SIGTERM}, false},
	} {
		if got := tc.r.OK(); got != tc.want {
			t.Errorf("%s: OK() = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestResultString(t *testing.T) {
	t.Parallel()
	plain := proc.Result{ExitCode: 2, Duration: 1500 * time.Millisecond}.String()
	if !strings.Contains(plain, "exit 2") || strings.Contains(plain, "killed") {
		t.Errorf("plain = %q", plain)
	}
	// A signalled Result must NAME the signal, "143" alone is the clue that
	// costs an hour to interpret.
	killed := proc.Result{
		ExitCode: 143, Signalled: true, Signal: syscall.SIGTERM,
		Duration: time.Second,
	}.String()
	if !strings.Contains(killed, "killed") || !strings.Contains(killed, "terminated") {
		t.Errorf("signalled = %q; want it to name the signal", killed)
	}
}

// ── concurrency ──────────────────────────────────────────────────────────

// TestConcurrentRuns pins that simultaneous runs do not interfere, no shared
// buffers, no package state.
func TestConcurrentRuns(t *testing.T) {
	t.Parallel()
	requireShell(t)
	const n = 12
	var wg sync.WaitGroup
	results := make([]string, n)
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = proc.Output(context.Background(), shell,
				[]string{"-c", fmt.Sprintf("echo run-%d", i)})
		}(i)
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("run %d: %v", i, errs[i])
			continue
		}
		if want := fmt.Sprintf("run-%d", i); results[i] != want {
			t.Errorf("run %d produced %q; want %q: output crossed between runs", i, results[i], want)
		}
	}
}

// ── process discovery ────────────────────────────────────────────────────

func TestSignalMatchingNoMatches(t *testing.T) {
	t.Parallel()
	if !proc.Exists("pgrep") {
		t.Skip("pgrep not available")
	}
	found, err := proc.SignalMatching(context.Background(),
		regexp.MustCompile("lath-nothing-matches-4b91c2"), syscall.SIGTERM)
	if err != nil {
		t.Fatalf("err = %v; matching nothing is not an error", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v", found)
	}
}

// TestFindExcludesSelf pins that Find never reports the calling process.
//
// It matters because the natural way to use Find is "is my service already
// running?", and a caller that finds itself concludes yes, then refuses to
// start, or signals itself. The test binary's own command line matches the
// pattern below, so the only reason it is absent is the exclusion.
func TestFindExcludesSelf(t *testing.T) {
	t.Parallel()
	if !proc.Exists("pgrep") {
		t.Skip("pgrep not available")
	}
	found, err := proc.Find(context.Background(), regexp.MustCompile(`proc\.test`))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.PID == os.Getpid() {
			t.Errorf("Find returned the calling process: %+v", f)
		}
	}
}

// TestFindReportsAStartedProcess is the inverse: a process we started, with a
// marker nothing else can match, must be found with the right pid.
func TestFindReportsAStartedProcess(t *testing.T) {
	t.Parallel()
	requireShell(t)
	if !proc.Exists("pgrep") {
		t.Skip("pgrep not available")
	}
	const marker = "lath-find-fixture-7f21ab"
	// A compound command, so sh stays alive and keeps the marker in its argv.
	// `sh -c "sleep 30 # marker"` would exec sleep directly and lose it.
	p, err := proc.Start(context.Background(), shell, []string{"-c", ": " + marker + "; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(context.Background(), time.Second) }()
	time.Sleep(300 * time.Millisecond)

	found, err := proc.Find(context.Background(), regexp.MustCompile(marker))
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, f := range found {
		if f.PID == p.PID() {
			seen = true
		}
	}
	if !seen {
		t.Errorf("Find did not report pid %d; got %+v", p.PID(), found)
	}
}

// ── Output's error contract ──────────────────────────────────────────────

// TestOutputTreatsNonZeroAsError pins the deliberate split from Run: Output is
// for "give me the value", so a failed command has no value to give and must
// not return an empty string with a nil error.
func TestOutputTreatsNonZeroAsError(t *testing.T) {
	t.Parallel()
	requireShell(t)
	out, err := proc.Output(context.Background(), shell,
		[]string{"-c", "echo the-reason >&2; exit 4"})
	if err == nil {
		t.Fatal("a command exiting 4 produced no error")
	}
	if out != "" {
		t.Errorf("Output = %q; want empty on failure", out)
	}
	// stderr must reach the error, or the caller is left with an exit code and
	// no explanation of a failure they cannot re-run.
	if !strings.Contains(err.Error(), "the-reason") {
		t.Errorf("err = %v; want it to carry stderr", err)
	}
}

func TestOutputTrimsTrailingNewline(t *testing.T) {
	t.Parallel()
	requireShell(t)
	// A command's trailing newline is line-formatting, not part of the value;
	// leaving it turns every use into a path or URL with a newline inside.
	out, err := proc.Output(context.Background(), shell, []string{"-c", "echo value"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "value" {
		t.Errorf("Output = %q; want %q", out, "value")
	}
}

func TestOutputMissingProgram(t *testing.T) {
	t.Parallel()
	_, err := proc.Output(context.Background(), "lath-no-such-program-3f9c", nil)
	if !errors.Is(err, proc.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

// TestPIDIsStableAcrossWait pins that the id remains readable after the
// process is reaped. A supervisor logs it when reporting the exit.
func TestPIDIsStableAcrossWait(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	before := p.PID()
	if before <= 0 {
		t.Fatalf("PID = %d before Wait", before)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if after := p.PID(); after != before {
		t.Errorf("PID changed across Wait: %d then %d", before, after)
	}
}

// TestOutputWithNoStderrStillExplains pins that a command failing silently
// still produces a usable error. Without the placeholder the message ends in a
// bare colon, and a reader cannot tell whether stderr was empty or the field
// was dropped.
func TestOutputWithNoStderrStillExplains(t *testing.T) {
	t.Parallel()
	requireShell(t)
	_, err := proc.Output(context.Background(), shell, []string{"-c", "exit 5"})
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "no stderr") {
		t.Errorf("err = %v; want it to say stderr was empty", err)
	}
	if !strings.Contains(err.Error(), "exit 5") {
		t.Errorf("err = %v; want it to carry the exit code", err)
	}
}
