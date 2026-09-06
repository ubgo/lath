package runner_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/ssh"
)

func TestLocalRunsHere(t *testing.T) {
	t.Parallel()
	if !proc.Exists("echo") {
		t.Skip("echo not available")
	}
	r, err := runner.Local{}.Run(context.Background(), "echo", []string{"hello"}, proc.Capture())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(r.Stdout)); got != "hello" {
		t.Errorf("stdout = %q", got)
	}
	// Parenthesised: a composite literal directly in an if condition is
	// ambiguous with the block that follows it.
	if got := (runner.Local{}).Describe(); got != "locally" {
		t.Errorf("Describe = %q", got)
	}
}

// TestLocalReportsNonZeroExitAsData pins the contract shared with proc: a
// failing command is a Result, not an error. An error means it could not start.
func TestLocalReportsNonZeroExitAsData(t *testing.T) {
	t.Parallel()
	if !proc.Exists("false") {
		t.Skip("false not available")
	}
	r, err := runner.Local{}.Run(context.Background(), "false", nil)
	if err != nil {
		t.Fatalf("a non-zero exit was reported as an error: %v", err)
	}
	if r.OK() {
		t.Error("Result.OK() is true for a command that exited non-zero")
	}
}

func TestOrLocalDefaultsToThisMachine(t *testing.T) {
	t.Parallel()
	if got := runner.OrLocal(nil).Describe(); got != "locally" {
		t.Errorf("OrLocal(nil) = %q; want the local runner", got)
	}
	fake := &runner.Fake{}
	if runner.OrLocal(fake).Describe() != fake.Describe() {
		t.Error("OrLocal replaced a supplied runner")
	}
}

func TestRemoteDescribesTheHost(t *testing.T) {
	t.Parallel()
	r := runner.Remote{Host: ssh.Host{Addr: "box", User: "deploy", Port: 2222}}
	if got := r.Describe(); got != "on deploy@box:2222" {
		t.Errorf("Describe = %q", got)
	}
}

// TestCheckNamesWhereItRan pins that a failure says which machine it happened
// on. A message that omits it sends an operator to the wrong host.
func TestCheckNamesWhereItRan(t *testing.T) {
	t.Parallel()
	fake := &runner.Fake{}

	err := runner.Check("docker build", fake,
		proc.Result{ExitCode: 1, Stderr: []byte("no such file")}, nil)
	if err == nil {
		t.Fatal("a non-zero exit produced no error")
	}
	for _, want := range []string{"docker build", "fake runner", "exit 1", "no such file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to mention %q", err, want)
		}
	}

	// A start failure keeps the underlying error reachable.
	boom := errors.New("executable not found")
	err = runner.Check("docker build", fake, proc.Result{}, boom)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the cause to stay unwrappable", err)
	}

	if runner.Check("x", fake, proc.Result{}, nil) != nil {
		t.Error("a successful Result produced an error")
	}
}

// TestTailBoundsALongStderr pins that a runaway error message cannot flood a
// log, while still ending with the part that explains the failure.
func TestTailBoundsALongStderr(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000) + "THE REAL CAUSE"
	got := runner.Tail([]byte(long))

	if len(got) > 1000 {
		t.Errorf("Tail returned %d bytes; it must be bounded", len(got))
	}
	// The tail is kept, not the head: the cause is at the end.
	if !strings.HasSuffix(got, "THE REAL CAUSE") {
		t.Errorf("Tail dropped the end of the message: %q", got[max(0, len(got)-40):])
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("truncation is not marked: %q", got[:10])
	}
	// A short message is returned whole and trimmed.
	if got := runner.Tail([]byte("  short  \n")); got != "short" {
		t.Errorf("Tail = %q; want it trimmed", got)
	}
}

// TestFakeRecordsAndReplies pins the shared test double, since three packages
// depend on it behaving predictably.
func TestFakeRecordsAndReplies(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "inspect", Stdout: "running"},
		{Match: "ps", Exit: 1, Stderr: "boom"},
	}}

	r, err := f.Run(context.Background(), "docker", []string{"inspect", "c"})
	if err != nil || string(r.Stdout) != "running" {
		t.Errorf("inspect = %q %v", r.Stdout, err)
	}
	r, _ = f.Run(context.Background(), "docker", []string{"ps"})
	if r.ExitCode != 1 || string(r.Stderr) != "boom" {
		t.Errorf("ps = %+v", r)
	}
	// An unmatched command succeeds silently, so a test only scripts what it
	// cares about.
	r, _ = f.Run(context.Background(), "docker", []string{"pull", "x"})
	if !r.OK() {
		t.Error("an unscripted command failed")
	}

	if len(f.Commands()) != 3 {
		t.Errorf("recorded %d commands; want 3", len(f.Commands()))
	}
	if !f.Ran("docker inspect c") || f.Ran("docker rm") {
		t.Errorf("Ran misreported: %v", f.Commands())
	}

	// Fail overrides everything, for testing the unstartable case.
	boom := errors.New("not installed")
	failing := &runner.Fake{Fail: boom}
	if _, err := failing.Run(context.Background(), "docker", nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// TestRemoteQuotesEveryArgument is the assertion Remote.Run exists for.
//
// ssh hands the command to a remote LOGIN SHELL, so the arguments are parsed a
// second time on the far side. Unquoted, a path containing a space becomes two
// paths and a value containing a semicolon becomes a second command. The
// second of those is a remote code execution reachable from any value a
// pipeline interpolates, which is why this is pinned rather than trusted.
//
// A stub ssh at the front of PATH records the argv: the flags and the command
// string this package builds are the whole of its behaviour, and no network is
// involved.
func TestRemoteQuotesEveryArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvLog + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	r := runner.Remote{Host: ssh.Host{Addr: "192.0.2.1", User: "deploy"}}
	if _, err := r.Run(context.Background(), "docker", []string{
		"run", "--name", "a name with spaces", "sh", "-c", "echo hi; rm -rf /",
	}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the stub ssh was never invoked: %v", err)
	}
	// The command is the LAST argument: everything before it is ssh's own
	// flags and the destination.
	args := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	command := args[len(args)-1]

	for _, want := range []string{"'a name with spaces'", "'echo hi; rm -rf /'"} {
		if !strings.Contains(command, want) {
			t.Errorf("command = %s\nwant it to contain %s", command, want)
		}
	}
	// The dangerous shapes must not survive as shell syntax. A bare semicolon
	// outside quotes would be a second command on the far side.
	if strings.Contains(command, "; rm -rf /'") && !strings.Contains(command, "'echo hi; rm -rf /'") {
		t.Error("the semicolon escaped its quotes")
	}
}

// TestFakeStreamsStderrAsWellAsStdout. A double that returns scripted stderr
// without WRITING it looks exactly like a streaming bug in the code under
// test, and the code most likely to be under test here is the code that
// reports failures.
func TestFakeStreamsStderrAsWellAsStdout(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "docker", Stdout: "to stdout", Stderr: "to stderr", Exit: 1},
	}}

	res, err := f.Run(context.Background(), "docker", []string{"ps"}, proc.Split(&out, &errBuf))
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "to stdout" {
		t.Errorf("streamed stdout = %q, want it written as a real runner would", got)
	}
	if got := errBuf.String(); got != "to stderr" {
		t.Errorf("streamed stderr = %q, want it written as a real runner would", got)
	}
	// Still returned as well as streamed: callers read one or the other.
	if string(res.Stderr) != "to stderr" || res.ExitCode != 1 {
		t.Errorf("result = %+v; want the scripted exit and streams", res)
	}
}

// TestFakeIsSilentWhenNothingMatches. An unmatched command must not emit the
// previous script's output, which would attribute one command's result to
// another.
func TestFakeIsSilentWhenNothingMatches(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "docker", Stdout: "containers"}}}

	res, err := f.Run(context.Background(), "git", []string{"status"}, proc.Out(&out))
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || len(res.Stdout) != 0 {
		t.Errorf("an unmatched command produced %q / %q", out.String(), res.Stdout)
	}
}
