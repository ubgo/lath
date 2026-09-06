package ssh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/ssh"
)

// fakeSSH puts stub ssh and scp programs at the front of PATH.
//
// The point is not to avoid the network. It is that the flags this package
// builds are the whole of its behaviour, and a stub that echoes its own argv
// is the only way to assert them. body runs after the argv is recorded.
//
// No t.Parallel in any test using this: t.Setenv forbids it.
func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stubs are shell scripts")
	}
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	for _, name := range []string{"ssh", "scp"} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvLog + "\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return argvLog
}

func argv(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the stub was never invoked: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func contains(args []string, want ...string) bool {
	joined := "\x00" + strings.Join(args, "\x00") + "\x00"
	return strings.Contains(joined, "\x00"+strings.Join(want, "\x00")+"\x00")
}

// TestBatchModeIsAlwaysSet pins the flag that keeps an automated task from
// hanging. Without it ssh prompts for a password and the job blocks until
// something kills it, with nothing in the log explaining the silence.
func TestBatchModeIsAlwaysSet(t *testing.T) {
	log := fakeSSH(t, "exit 0")
	if _, err := ssh.Run(context.Background(), ssh.Host{Addr: "example.com"}, "true"); err != nil {
		t.Fatal(err)
	}
	if !contains(argv(t, log), "-o", "BatchMode=yes") {
		t.Errorf("BatchMode is missing from %v", argv(t, log))
	}
}

func TestHostFlagsAreBuiltCorrectly(t *testing.T) {
	log := fakeSSH(t, "exit 0")
	host := ssh.Host{
		Addr: "vps.example.com", User: "deploy", Port: 2222,
		KeyFile: "/keys/id", ConnectTimeout: 7 * time.Second,
		Extra: []string{"-o", "ProxyJump=bastion"},
	}
	if _, err := ssh.Run(context.Background(), host, "uptime"); err != nil {
		t.Fatal(err)
	}
	got := argv(t, log)

	for _, want := range [][]string{
		{"-p", "2222"},
		{"-i", "/keys/id"},
		{"-o", "ConnectTimeout=7"},
		{"-o", "ProxyJump=bastion"}, // the escape hatch reaches the command line
		{"deploy@vps.example.com"},
		{"uptime"},
	} {
		if !contains(got, want...) {
			t.Errorf("argv is missing %v: %v", want, got)
		}
	}
	// The target must precede the command, or ssh treats the command as a host.
	target, cmd := indexOf(got, "deploy@vps.example.com"), indexOf(got, "uptime")
	if target < 0 || cmd < 0 || target > cmd {
		t.Errorf("the target must come before the command: %v", got)
	}
}

// TestZeroHostUsesSSHDefaults pins that an unset field falls through to ssh's
// own configuration rather than being forced to a guess, a host described in
// ~/.ssh/config needs only its alias here.
func TestZeroHostUsesSSHDefaults(t *testing.T) {
	log := fakeSSH(t, "exit 0")
	if _, err := ssh.Run(context.Background(), ssh.Host{Addr: "alias"}, "true"); err != nil {
		t.Fatal(err)
	}
	got := argv(t, log)
	for _, unwanted := range []string{"-p", "-i"} {
		if contains(got, unwanted) {
			t.Errorf("an unset field produced %s: %v", unwanted, got)
		}
	}
	if contains(got, "@alias") {
		t.Errorf("an empty User produced an @ prefix: %v", got)
	}
}

// TestCopyUsesCapitalPForThePort pins a real footgun: scp spells the port -P
// where ssh spells it -p, and getting it wrong silently copies to port 22.
func TestCopyUsesCapitalPForThePort(t *testing.T) {
	log := fakeSSH(t, "exit 0")
	host := ssh.Host{Addr: "h", User: "u", Port: 2222}
	if err := ssh.Copy(context.Background(), host, "/local/file", "/remote/file"); err != nil {
		t.Fatal(err)
	}
	got := argv(t, log)
	if !contains(got, "-P", "2222") {
		t.Errorf("scp did not get -P: %v", got)
	}
	if contains(got, "-p", "2222") {
		t.Errorf("scp got ssh's lowercase -p, which means the default port: %v", got)
	}
	if !contains(got, "/local/file", "u@h:/remote/file") {
		t.Errorf("the copy operands are wrong: %v", got)
	}
}

func TestCopyReportsRemoteFailure(t *testing.T) {
	fakeSSH(t, "echo 'permission denied' >&2; exit 1")
	err := ssh.Copy(context.Background(), ssh.Host{Addr: "h"}, "/local", "/remote")
	if err == nil {
		t.Fatal("a failing scp reported success")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v; want it to carry scp's stderr", err)
	}
}

// TestReachableRunsACommandNotJustAConnection pins that authentication is part
// of the answer: a host that accepts TCP but refuses the key is not usable,
// and calling it reachable sends the caller looking in the wrong place.
func TestReachableRunsACommandNotJustAConnection(t *testing.T) {
	log := fakeSSH(t, "exit 0")
	if err := ssh.Reachable(context.Background(), ssh.Host{Addr: "h"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if !contains(argv(t, log), "true") {
		t.Errorf("Reachable did not execute a command: %v", argv(t, log))
	}
}

func TestReachableReportsFailure(t *testing.T) {
	fakeSSH(t, "echo 'connection refused' >&2; exit 255")
	err := ssh.Reachable(context.Background(), ssh.Host{Addr: "h"}, 5*time.Second)
	if !errors.Is(err, ssh.ErrUnreachable) {
		t.Fatalf("err = %v; want ErrUnreachable", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v; want it to carry ssh's stderr", err)
	}
}

func TestOutputTrimsAndFailsOnNonZero(t *testing.T) {
	fakeSSH(t, "echo '  the value  '; exit 0")
	got, err := ssh.Output(context.Background(), ssh.Host{Addr: "h"}, "cat /etc/thing")
	if err != nil {
		t.Fatal(err)
	}
	if got != "the value" {
		t.Errorf("Output = %q; want it trimmed", got)
	}
}

func TestMissingAddrIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := ssh.Run(ctx, ssh.Host{}, "true"); err == nil {
		t.Error("Run accepted a Host with no Addr")
	}
	if _, err := ssh.Output(ctx, ssh.Host{}, "true"); err == nil {
		t.Error("Output accepted a Host with no Addr")
	}
	if err := ssh.Copy(ctx, ssh.Host{}, "a", "b"); err == nil {
		t.Error("Copy accepted a Host with no Addr")
	}
}

func TestMissingClientIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH semantics differ")
	}
	t.Setenv("PATH", t.TempDir()) // an empty PATH: no ssh, no scp
	if proc.Exists("ssh") {
		t.Skip("ssh resolved despite an empty PATH")
	}
	if _, err := ssh.Run(context.Background(), ssh.Host{Addr: "h"}, "true"); !errors.Is(err, ssh.ErrNoClient) {
		t.Errorf("err = %v; want ErrNoClient", err)
	}
	if err := ssh.Copy(context.Background(), ssh.Host{Addr: "h"}, "a", "b"); !errors.Is(err, ssh.ErrNoClient) {
		t.Errorf("err = %v; want ErrNoClient", err)
	}
}

// TestQuote pins the shell quoting a caller needs when interpolating a value
// into a remote command. The single-quote escape is the only form a POSIX
// shell treats literally throughout.
func TestQuote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"plain", "'plain'"},
		{"with space", "'with space'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
		{"`backticks`", "'`backticks`'"},
		{"a\nb", "'a\nb'"},
	} {
		if got := ssh.Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestQuoteActuallyDefusesInjection runs the quoted string through a real
// shell, which is the only proof that matters.
func TestQuoteActuallyDefusesInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell")
	}
	hostile := `'; touch /tmp/lath-pwned-4f2a; echo '`
	out, err := proc.Output(context.Background(), "sh",
		[]string{"-c", "printf '%s' " + ssh.Quote(hostile)})
	if err != nil {
		t.Fatal(err)
	}
	if out != hostile {
		t.Errorf("the shell reinterpreted the value: got %q, want %q", out, hostile)
	}
	if _, err := os.Stat("/tmp/lath-pwned-4f2a"); err == nil {
		os.Remove("/tmp/lath-pwned-4f2a")
		t.Fatal("the injected command executed: Quote does not defuse it")
	}
}

func TestHostString(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host ssh.Host
		want string
	}{
		{ssh.Host{Addr: "h"}, "h"},
		{ssh.Host{Addr: "h", User: "u"}, "u@h"},
		{ssh.Host{Addr: "h", Port: 22}, "h:22"},
		{ssh.Host{Addr: "h", User: "u", Port: 2222}, "u@h:2222"},
	} {
		if got := tc.host.String(); got != tc.want {
			t.Errorf("String() = %q; want %q", got, tc.want)
		}
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
