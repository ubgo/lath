package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/session"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/pipeline/debug"
)

// TestChooseSessionPrompts covers the ambiguous case: several runs waiting, so
// attaching without asking could drive someone else's deploy.
func TestChooseSessionPrompts(t *testing.T) {
	sessions := []session.Session{
		{ID: "a-1", Meta: session.Meta{"project": "acme_api", "target": "deploy local"}},
		{ID: "b-2", Meta: session.Meta{"project": "billing-api", "target": "deploy run staging"}},
	}
	withStdin(t, "2\n")
	var got session.Session
	var err error
	out := captureOutput(t, func() { got, err = chooseSession(sessions) })
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b-2" {
		t.Errorf("chose %q, want the second", got.ID)
	}
	// The listing must name the project, or the operator cannot tell which
	// deploy they are about to drive.
	for _, want := range []string{"acme_api", "billing-api", "deploy local"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker omits %q:\n%s", want, out)
		}
	}
}

func TestChooseSessionRejectsABadChoice(t *testing.T) {
	sessions := []session.Session{{ID: "a-1"}, {ID: "b-2"}}
	for _, answer := range []string{"0\n", "3\n", "abc\n", "\n"} {
		withStdin(t, answer)
		var err error
		captureOutput(t, func() { _, err = chooseSession(sessions) })
		if err == nil {
			t.Errorf("answer %q was accepted", strings.TrimSpace(answer))
		}
	}
}

// shortSock returns a socket path short enough for the platform.
//
// A unix socket path is capped at ~104 bytes on macOS, and t.TempDir() plus a
// descriptive test name overruns it, producing a dial that never connects and
// a test that hangs until its timeout rather than failing with the reason.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// withStdin replaces os.Stdin for the duration of a test.
func withStdin(t *testing.T, input string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = orig; _ = f.Close() })
}

// TestOpenDebugSessionAdvertisesAndPassesTheSocket, lath advertises the
// session and hands the path to the definition in argv, because syscall.Exec
// keeps the PID: the advertisement taken here still describes the process that
// replaces this one.
func TestOpenDebugSessionAdvertisesAndPassesTheSocket(t *testing.T) {
	t.Setenv(session.DirEnv, t.TempDir())
	args, err := openDebugSession([]string{"deploy", "local"}, t.TempDir(), 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var sock, wait string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, socketFlag+"="); ok {
			sock = v
		}
		if v, ok := strings.CutPrefix(a, waitFlag+"="); ok {
			wait = v
		}
	}
	if sock == "" {
		t.Fatalf("no %s in %v", socketFlag, args)
	}
	if wait != (90 * time.Second).String() {
		t.Errorf("%s = %q, want the requested wait", waitFlag, wait)
	}
	// The original arguments must survive untouched, they belong to the
	// author's target.
	if args[0] != "deploy" || args[1] != "local" {
		t.Errorf("the target's own arguments were altered: %v", args)
	}

	live, err := session.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("advertised %d sessions, want 1", len(live))
	}
	if live[0].Meta["target"] != "deploy local" {
		t.Errorf("session describes itself as %q", live[0].Meta["target"])
	}
}

// TestDialWhenReadyGivesUpWhenTheRunEnds is the regression for a hang: a
// target that never builds a pipeline opens no session, so waiting on a clock
// alone meant staring at a blank terminal for the whole timeout.
func TestDialWhenReadyGivesUpWhenTheRunEnds(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	close(done)
	start := time.Now()
	_, err := dialWhenReady(filepath.Join(t.TempDir(), "never.sock"), done)
	if err == nil {
		t.Fatal("dialled a socket that was never created")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("waited %s after the run had already ended", elapsed)
	}
}

// TestDialWhenReadyAttachesOnceTheSocketAppears, the child compiles first, so
// the socket cannot be assumed present at the first attempt.
func TestDialWhenReadyAttachesOnceTheSocketAppears(t *testing.T) {
	t.Parallel()
	sock := shortSock(t)
	done := make(chan struct{})

	go func() {
		time.Sleep(300 * time.Millisecond)
		ln, err := debug.Listen(sock, nil)
		if err != nil {
			close(done)
			return
		}
		defer ln.Close()
		srv, err := ln.Accept(context.Background(), 5*time.Second)
		if err == nil {
			_ = srv.Plan("p", nil)
			time.Sleep(200 * time.Millisecond)
			_ = srv.Close()
		}
		close(done)
	}()

	cli, err := dialWhenReady(sock, make(chan struct{}))
	if err != nil {
		t.Fatalf("did not attach once the socket appeared: %v", err)
	}
	defer cli.Close()
	<-done
}

func TestTailFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "run.log")
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := tailFile(p, 5)
	if n := strings.Count(got, "line"); n != 5 {
		t.Errorf("tail returned %d lines, want 5", n)
	}
	if !strings.HasPrefix(got, "      ") {
		t.Errorf("tail is not indented for inline display: %q", got)
	}
	// A missing file is not an error here: the tail is a courtesy alongside a
	// message that already stands on its own.
	if tailFile(filepath.Join(t.TempDir(), "absent"), 5) != "" {
		t.Error("a missing log produced output")
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if tailFile(empty, 5) != "" {
		t.Error("an empty log produced output")
	}
}

// TestLaunchedStatusReportsTheChildsExitCode, `lath tui` on a target with no
// pipeline behaves like `lath run`, which includes passing the status through.
func TestLaunchedStatusReportsTheChildsExitCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		script string
		want   int
	}{
		{"exit 0", exitOK},
		{"exit 7", 7},
	}
	for _, tc := range cases {
		cmd := exec.Command("sh", "-c", tc.script)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		l := &launched{cmd: cmd, done: make(chan struct{})}
		go l.reap()
		if got := l.status(); got != tc.want {
			t.Errorf("status for %q = %d, want %d", tc.script, got, tc.want)
		}
		// status blocks on a closed channel; calling it twice must not hang.
		if got := l.status(); got != tc.want {
			t.Errorf("second status call = %d", got)
		}
	}
}

// TestChooseTargetUsesArgumentsVerbatim, given arguments, they ARE the
// command, with no prompt.
func TestChooseTargetUsesArgumentsVerbatim(t *testing.T) {
	t.Parallel()
	got, err := chooseTarget(nil, []string{"deploy", "local", "--apply"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "deploy local --apply" {
		t.Errorf("chooseTarget = %v", got)
	}
}

// TestChooseTargetPrompts covers the picker: a number, then the arguments.
func TestChooseTargetPrompts(t *testing.T) {
	targets := []Target{{Command: "deploy"}, {Command: "secrets push"}}
	withStdin(t, "2\nstaging --apply\n")
	var got []string
	var err error
	out := captureOutput(t, func() { got, err = chooseTarget(targets, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "secrets push staging --apply" {
		t.Errorf("chooseTarget = %v", got)
	}
	if !strings.Contains(out, "deploy") || !strings.Contains(out, "secrets push") {
		t.Errorf("picker did not list the targets:\n%s", out)
	}
}

func TestChooseTargetRejectsABadChoice(t *testing.T) {
	targets := []Target{{Command: "deploy"}}
	for _, answer := range []string{"9\n", "x\n"} {
		withStdin(t, answer)
		var err error
		captureOutput(t, func() { _, err = chooseTarget(targets, nil) })
		if err == nil {
			t.Errorf("answer %q accepted", strings.TrimSpace(answer))
		}
	}
}

// TestRunTUIWithNoSessionsAndNoDefinition, nothing waiting means START
// something, and with nothing to start the message must say so.
func TestRunTUIWithNoSessionsAndNoDefinition(t *testing.T) {
	t.Setenv(session.DirEnv, t.TempDir())
	t.Chdir(t.TempDir())
	var code int
	out := captureOutput(t, func() { code = runTUI(nil) })
	if code == exitOK {
		t.Error("reported success with no definition and no session")
	}
	if !strings.Contains(out, definitionDir) {
		t.Errorf("message does not name the definition directory:\n%s", out)
	}
}

// TestDrivePanelRendersAndFinishes drives the panel over a real socket, with
// the run finishing on its own.
func TestDrivePanelRendersAndFinishes(t *testing.T) {
	sock := shortSock(t)
	ln, err := debug.Listen(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		srv, err := ln.Accept(context.Background(), 5*time.Second)
		if err != nil {
			return
		}
		defer srv.Close()
		_ = srv.Plan("ship", []pipeline.PlanEntry{
			{Position: 1, Total: 2, Name: "one"},
			{Position: 2, Total: 2, Name: "two"},
		})
		rep := srv.Reporter()
		rep.StepStart(1, 2, "one")
		rep.Detailf("did one")
		rep.StepDone(1, 2, "one", time.Millisecond)
		_, _ = srv.Pause(pipeline.Pause{Position: 1, Total: 2, Step: fakeStep{"one"}, Next: "two"})
		rep.StepStart(2, 2, "two")
		rep.StepDone(2, 2, "two", time.Millisecond)
		_ = srv.Finish(pipeline.Finished{State: map[string]string{"commit": "abc1234"}})
	}()

	cli, err := debug.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	withStdin(t, "n\n")
	var code int
	out := captureOutput(t, func() {
		code = drivePanel(cli, debug.Event{}, false, session.Session{
			Meta: session.Meta{"project": "proj", "target": "ship"},
		})
	})
	if code != exitOK {
		t.Errorf("exit = %d, want %d", code, exitOK)
	}
	for _, want := range []string{"one", "two", "did one", "commit", "abc1234", "finished"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel output omits %q:\n%s", want, out)
		}
	}
}

// fakeStep is the minimum a Pause needs to name its step.
type fakeStep struct{ name string }

func (f fakeStep) Name() string                             { return f.name }
func (fakeStep) Requires() []pipeline.Key                   { return nil }
func (fakeStep) Provides() []pipeline.Key                   { return nil }
func (fakeStep) Run(context.Context, *pipeline.State) error { return nil }
