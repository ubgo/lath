package proc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/proc"
)

// shell is the program used to script child behaviour in tests. Present on
// every platform this package supports; if it is missing, the tests are not
// meaningful and should say so rather than fail obscurely.
const shell = "sh"

func requireShell(t *testing.T) {
	t.Helper()
	if !proc.Exists(shell) {
		t.Skipf("%s not available", shell)
	}
}

// TestRunReportsExitCodeAsData pins the central decision: a non-zero exit is a
// Result, NOT an error. A caller supervising a process needs the code as data,
// and forcing it through the error path makes every such caller unwrap it back.
func TestRunReportsExitCodeAsData(t *testing.T) {
	t.Parallel()
	requireShell(t)
	for _, code := range []int{0, 1, 3, 42} {
		r, err := proc.Run(context.Background(), shell,
			[]string{"-c", "exit " + itoa(code)})
		if err != nil {
			t.Fatalf("exit %d returned an error: %v", code, err)
		}
		if r.ExitCode != code {
			t.Errorf("ExitCode = %d; want %d", r.ExitCode, code)
		}
		if r.Signalled {
			t.Errorf("exit %d reported Signalled", code)
		}
		if got := r.OK(); got != (code == 0) {
			t.Errorf("OK() = %v for exit %d", got, code)
		}
	}
}

// TestRunDistinguishesSignalFromExit is the distinction this package exists to
// preserve. Conflating them turns a supervisor into an infinite loop, because
// a stop request becomes indistinguishable from a crash.
func TestRunDistinguishesSignalFromExit(t *testing.T) {
	t.Parallel()
	requireShell(t)

	// A child that kills itself with SIGTERM.
	killed, err := proc.Run(context.Background(), shell, []string{"-c", "kill -TERM $$"})
	if err != nil {
		t.Fatal(err)
	}
	if !killed.Signalled {
		t.Fatal("a SIGTERM'd process did not report Signalled")
	}
	if killed.Signal != syscall.SIGTERM {
		t.Errorf("Signal = %v; want SIGTERM", killed.Signal)
	}
	if killed.ExitCode != 143 { // 128 + 15, as a shell reports it
		t.Errorf("ExitCode = %d; want 143", killed.ExitCode)
	}

	// A child that EXITS with the same number must be distinguishable.
	exited, err := proc.Run(context.Background(), shell, []string{"-c", "exit 143"})
	if err != nil {
		t.Fatal(err)
	}
	if exited.Signalled {
		t.Fatal("an ordinary exit 143 reported Signalled: indistinguishable from a kill")
	}
	if exited.ExitCode != killed.ExitCode {
		t.Fatalf("fixture is wrong: codes should match (%d vs %d)", exited.ExitCode, killed.ExitCode)
	}
}

func TestOutputTreatsFailureAsError(t *testing.T) {
	t.Parallel()
	requireShell(t)

	out, err := proc.Output(context.Background(), shell, []string{"-c", "echo  hello  "})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Errorf("Output = %q; want %q (trimmed)", out, "hello")
	}

	// The opposite of Run, deliberately: a caller wanting output has no use
	// for the failure case.
	_, err = proc.Output(context.Background(), shell, []string{"-c", "echo boom >&2; exit 7"})
	if err == nil {
		t.Fatal("Output returned no error for a non-zero exit")
	}
	// The message must carry stderr, or "exit 7" tells the caller nothing.
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not include stderr", err)
	}
}

func TestNotFound(t *testing.T) {
	t.Parallel()
	_, err := proc.Run(context.Background(), "definitely-not-a-real-program", nil)
	if !errors.Is(err, proc.ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}

func TestOutAndCaptureCompose(t *testing.T) {
	t.Parallel()
	requireShell(t)
	var live bytes.Buffer
	r, err := proc.Run(context.Background(), shell, []string{"-c", "echo streamed"},
		proc.Out(&live), proc.Capture())
	if err != nil {
		t.Fatal(err)
	}
	// A caller may want output live AND kept; one must not disable the other.
	if !strings.Contains(live.String(), "streamed") {
		t.Errorf("Out writer got %q", live.String())
	}
	if !strings.Contains(string(r.Stdout), "streamed") {
		t.Errorf("Capture got %q", r.Stdout)
	}
}

// TestStdoutNilWhenNotCaptured pins that "not captured" is distinguishable
// from "produced no output".
func TestStdoutNilWhenNotCaptured(t *testing.T) {
	t.Parallel()
	requireShell(t)
	r, err := proc.Run(context.Background(), shell, []string{"-c", "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Stdout != nil {
		t.Errorf("Stdout = %q without Capture; want nil", r.Stdout)
	}
}

func TestEnvAddsAndEnvOnlyReplaces(t *testing.T) {
	t.Parallel()
	requireShell(t)

	// Env ADDS: PATH must survive, or the child cannot find anything.
	out, err := proc.Output(context.Background(), shell,
		[]string{"-c", "echo \"$MY_VAR|${PATH:+has-path}\""}, proc.Env("MY_VAR=set"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "set|has-path" {
		t.Errorf("Env: got %q; want the variable added AND PATH preserved", out)
	}

	// EnvOnly REPLACES: nothing but what was given.
	out, err = proc.Output(context.Background(), shell,
		[]string{"-c", "echo \"$MY_VAR|${OTHER:-unset}\""},
		proc.EnvOnly("MY_VAR=only", "PATH="+mustPath(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "only|unset") {
		t.Errorf("EnvOnly: got %q; want the environment replaced", out)
	}
}

func TestDir(t *testing.T) {
	t.Parallel()
	requireShell(t)
	dir := t.TempDir()
	out, err := proc.Output(context.Background(), shell, []string{"-c", "pwd"}, proc.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	// macOS reports /private/var for /var, so compare the tail.
	if !strings.HasSuffix(out, strings.TrimPrefix(dir, "/private")) {
		t.Errorf("pwd = %q; want it inside %q", out, dir)
	}
}

func TestTimeoutKillsAndReports(t *testing.T) {
	t.Parallel()
	requireShell(t)
	start := time.Now()
	r, err := proc.Run(context.Background(), shell, []string{"-c", "sleep 30"},
		proc.Timeout(300*time.Millisecond))
	if !errors.Is(err, proc.ErrTimeout) {
		t.Fatalf("err = %v; want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; the timeout did not fire", elapsed)
	}
	if !r.Signalled {
		t.Error("a timed-out process should report Signalled")
	}
}

// TestContextCancellationStops pins that cancelling ends the process rather
// than leaking it, and that SIGTERM is used so it can shut down cleanly.
func TestContextCancellationStops(t *testing.T) {
	t.Parallel()
	requireShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()

	start := time.Now()
	r, err := proc.Run(ctx, shell, []string{"-c", "sleep 30"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; cancellation did not stop it", elapsed)
	}
	if !r.Signalled || r.Signal != syscall.SIGTERM {
		t.Errorf("got %+v; want a SIGTERM'd process", r)
	}
}

func TestStartWaitAndStop(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell, []string{"-c", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if p.PID() <= 0 {
		t.Fatalf("PID = %d", p.PID())
	}
	if err := p.Stop(context.Background(), time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestStopEscalatesToKill pins that a process ignoring SIGTERM is still
// stopped. Without it, Stop hangs forever on exactly the process that most
// needs stopping.
func TestStopEscalatesToKill(t *testing.T) {
	t.Parallel()
	requireShell(t)
	p, err := proc.Start(context.Background(), shell,
		[]string{"-c", "trap '' TERM; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Stop(context.Background(), 300*time.Millisecond); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; escalation to SIGKILL did not happen", elapsed)
	}
}

func TestLookAndExists(t *testing.T) {
	t.Parallel()
	requireShell(t)
	if path, err := proc.Look(shell); err != nil || path == "" {
		t.Errorf("Look(%q) = %q, %v", shell, path, err)
	}
	if _, err := proc.Look("definitely-not-a-real-program"); !errors.Is(err, proc.ErrNotFound) {
		t.Errorf("Look of a missing program = %v; want ErrNotFound", err)
	}
	if !proc.Exists(shell) || proc.Exists("definitely-not-a-real-program") {
		t.Error("Exists disagrees with Look")
	}
}

func TestFind(t *testing.T) {
	t.Parallel()
	if !proc.Exists("pgrep") {
		t.Skip("pgrep not available")
	}
	requireShell(t)

	marker := "lath-proc-test-marker-" + itoa(int(time.Now().UnixNano()%100000))
	// `sh -c 'sleep 30 # marker'` does NOT work: for a simple command sh execs
	// it directly, replacing itself, and the marker disappears from argv along
	// with the shell. A leading no-op makes it a compound command, so sh stays
	// and its argv, marker included, is what pgrep matches.
	p, err := proc.Start(context.Background(), shell,
		[]string{"-c", ": " + marker + "; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(context.Background(), time.Second) }()
	time.Sleep(200 * time.Millisecond)

	found, err := proc.Find(context.Background(), regexp.MustCompile(marker))
	if err != nil {
		t.Fatal(err)
	}
	var sawIt bool
	for _, f := range found {
		if f.PID == p.PID() {
			sawIt = true
		}
	}
	if !sawIt {
		t.Errorf("Find did not return the started process (pid %d); got %+v", p.PID(), found)
	}

	// No match is an empty result, NOT an error.
	none, err := proc.Find(context.Background(),
		regexp.MustCompile("lath-nothing-matches-this-9f3a2b"))
	if err != nil {
		t.Fatalf("no-match returned an error: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("no-match returned %+v", none)
	}
}

func itoa(i int) string { return strings.TrimSpace(strings.Join([]string{intToStr(i)}, "")) }

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func mustPath(t *testing.T) string {
	t.Helper()
	p, err := proc.Look(shell)
	if err != nil {
		t.Fatal(err)
	}
	return p[:strings.LastIndex(p, "/")]
}

// TestCaptureComposesWithOut is the invariant the whole streaming design rests
// on: a caller can have the output live AND keep it, so a long command is
// watchable while it runs and still quotable when it fails.
//
// Tested here against a real process rather than through a fake, because a
// fake that returns scripted output regardless of the options cannot tell the
// two apart. Which is exactly how a broken version of this passed once.
func TestCaptureComposesWithOut(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	var streamed strings.Builder

	r, err := proc.Run(context.Background(), "sh",
		[]string{"-c", "echo to-stdout; echo to-stderr >&2"},
		proc.Out(&streamed), proc.Capture())
	if err != nil {
		t.Fatal(err)
	}

	// Live.
	live := streamed.String()
	for _, want := range []string{"to-stdout", "to-stderr"} {
		if !strings.Contains(live, want) {
			t.Errorf("streamed = %q, missing %q", live, want)
		}
	}
	// And kept.
	if !strings.Contains(string(r.Stdout), "to-stdout") {
		t.Errorf("Result.Stdout = %q; capture did not survive streaming", r.Stdout)
	}
	if !strings.Contains(string(r.Stderr), "to-stderr") {
		t.Errorf("Result.Stderr = %q; capture did not survive streaming", r.Stderr)
	}
}

// TestWritersResolvesOptions covers the accessor an alternate Runner needs in
// order to honour Out at all.
func TestWritersResolvesOptions(t *testing.T) {
	t.Parallel()
	var a, b strings.Builder

	if out, errW := proc.Writers(); out != nil || errW != nil {
		t.Errorf("no options should mean discard, got %v/%v", out, errW)
	}
	out, errW := proc.Writers(proc.Out(&a))
	if out != io.Writer(&a) || errW != io.Writer(&a) {
		t.Error("Out must send both streams to one writer")
	}
	out, errW = proc.Writers(proc.Split(&a, &b))
	if out != io.Writer(&a) || errW != io.Writer(&b) {
		t.Error("Split must keep the streams apart")
	}
}

// TestOutIsSafeForConcurrentStreams pins the fix for a race that only shows up
// when both streams share a writer. Which is what Out does, and what every
// caller streaming a build gets.
//
// os/exec copies stdout and stderr in separate goroutines. Without
// serialisation they write to the shared writer at the same time: a
// strings.Builder detects it and panics under -race, a bytes.Buffer silently
// corrupts. This produces heavy interleaved output from both streams to make
// the collision likely rather than theoretical.
func TestOutIsSafeForConcurrentStreams(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	var shared strings.Builder

	script := "for i in $(seq 1 200); do echo out-$i; echo err-$i >&2; done"
	if _, err := proc.Run(context.Background(), "sh", []string{"-c", script},
		proc.Out(&shared), proc.Capture()); err != nil {
		t.Fatal(err)
	}

	got := shared.String()
	for _, want := range []string{"out-1\n", "out-200\n", "err-1\n", "err-200\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from the combined stream", strings.TrimSpace(want))
		}
	}
	if n := strings.Count(got, "\n"); n != 400 {
		t.Errorf("got %d lines, want 400: writes were lost or interleaved", n)
	}
}

// TestOutToAFileKeepsTheDescriptor is why docker renders its live progress
// table instead of a flat log.
//
// os/exec connects an *os.File to the child by duplicating its DESCRIPTOR, but
// wraps any other io.Writer in a pipe. A tool then sees a pipe, decides it is
// not talking to a terminal, and drops to plain output. So anything this
// package does to a writer on the way through, capturing, serialising, must
// leave an *os.File alone.
//
// This caught a real regression: the fix for the concurrent-write race wrapped
// every shared writer, including files, and silently cost docker its table.
func TestOutToAFileKeepsTheDescriptor(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// The child asks the kernel what kind of thing its stdout is. A regular
	// file answers yes to -f; a pipe does not.
	if _, err := proc.Run(context.Background(), "sh",
		[]string{"-c", "test -f /dev/stdout && echo file || echo pipe"},
		proc.Out(f)); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(blob)); got != "file" {
		t.Errorf("child saw a %s, not the file it was given: the descriptor was wrapped", got)
	}
}
