package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/session"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/pipeline/debug"
)

// TestTakeDebugFlag covers the flag lath consumes on the operator's behalf.
// It must never reach the author's target, which parses its own arguments.
func TestTakeDebugFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    []string
		out   []string
		found bool
		wait  time.Duration
		bad   bool
	}{
		{"absent", []string{"deploy", "prod"}, []string{"deploy", "prod"}, false, debug.DefaultAttachTimeout, false},
		{"bare", []string{"deploy", "--debug", "prod"}, []string{"deploy", "prod"}, true, debug.DefaultAttachTimeout, false},
		{"with a wait", []string{"deploy", "--debug=10m"}, []string{"deploy"}, true, 10 * time.Minute, false},
		{"unparseable", []string{"--debug=soon"}, nil, false, 0, true},
		{"zero", []string{"--debug=0s"}, nil, false, 0, true},
		{"negative", []string{"--debug=-1m"}, nil, false, 0, true},
		// A target's own flags must survive untouched.
		{"other flags", []string{"deploy", "--apply", "--allow-prod"},
			[]string{"deploy", "--apply", "--allow-prod"}, false, debug.DefaultAttachTimeout, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, found, wait, err := takeDebugFlag(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if found != tc.found || wait != tc.wait {
				t.Errorf("found=%v wait=%v, want %v/%v", found, wait, tc.found, tc.wait)
			}
			if len(out) != len(tc.out) {
				t.Fatalf("args = %v, want %v", out, tc.out)
			}
			for i := range out {
				if out[i] != tc.out[i] {
					t.Errorf("args = %v, want %v", out, tc.out)
				}
			}
		})
	}
}

// TestAwaitFirstEventWhenTheRunHasNoPipeline is the regression for the bug a
// live session found: picking a target that does its work without building a
// Pipeline, generating a file, printing a plan, must be reported as "there
// was nothing to step", not as a failure to attach.
//
// The subtlety is that the socket EXISTS by then: the generated dispatcher
// listens as soon as it sees the flag, and only accepts lazily. So dialling
// succeeds and no event ever arrives, which is indistinguishable from a slow
// pipeline until the child exits.
func TestAwaitFirstEventWhenTheRunHasNoPipeline(t *testing.T) {
	t.Parallel()
	_, cli := connectedPair(t)

	done := make(chan struct{})
	close(done) // the run has already finished

	_, err := awaitFirstEvent(cli, done)
	if !errors.Is(err, errNoPipeline) {
		t.Fatalf("err = %v, want errNoPipeline", err)
	}
}

// TestAwaitFirstEventPrefersALateEvent. A pipeline short enough to finish
// before the wait is even entered still deserves its panel.
func TestAwaitFirstEventPrefersALateEvent(t *testing.T) {
	t.Parallel()
	srv, cli := connectedPair(t)

	if err := srv.Plan("ship", []pipeline.PlanEntry{{Position: 1, Total: 1, Name: "one"}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)

	e, err := awaitFirstEvent(cli, done)
	if err != nil {
		t.Fatalf("a queued event was discarded: %v", err)
	}
	if e.Kind != debug.KindPlan || e.Pipeline != "ship" {
		t.Errorf("event = %+v", e)
	}
}

// TestLiveWriterStopsAtQuit holds the rule that keeps a launched run's own
// output from scribbling over the panel once it takes the screen, while
// still showing it beforehand, which is what makes `lath tui` usable for a
// target with no pipeline in it.
func TestLiveWriterStopsAtQuit(t *testing.T) {
	t.Parallel()
	var log, live capture
	w := newLiveWriter(&log)
	w.live = &live

	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	w.Quiet()
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}

	if got := live.String(); got != "before\n" {
		t.Errorf("terminal saw %q, want only the pre-panel line", got)
	}
	if got := log.String(); got != "before\nafter\n" {
		t.Errorf("log saw %q, want everything", got)
	}
}

type capture struct{ b []byte }

func (c *capture) Write(p []byte) (int, error) { c.b = append(c.b, p...); return len(p), nil }
func (c *capture) String() string              { return string(c.b) }

// TestArgumentsPreferATargetOverASessionID is the regression for a bug that
// made `lath tui deploy local --apply` fail whenever ANY session happened to
// be waiting: the argument was read as a session id, not found, and reported
// as an error instead of being run.
//
// The rule is that a match decides, not the mere existence of sessions. A
// session id is an opaque hash nobody types from memory; a target is what
// people actually pass.
func TestArgumentsPreferATargetOverASessionID(t *testing.T) {
	t.Parallel()
	sessions := []session.Session{
		{ID: "a91f4c2e-1234", Meta: session.Meta{"project": "acme_api"}},
	}

	if _, ok := findSession(sessions, "deploy"); ok {
		t.Error("a target name must not resolve to a session")
	}
	got, ok := findSession(sessions, "a91f4c2e-1234")
	if !ok || got.ID != "a91f4c2e-1234" {
		t.Error("an exact session id must resolve to its session")
	}
}

// TestWrapFoldsRatherThanTruncates. The original panel cut every line at a
// fixed width, which hid the half of a docker error that names the socket it
// could not reach. A wrapped line is untidy; a cut one loses the answer.
func TestWrapFoldsRatherThanTruncates(t *testing.T) {
	t.Parallel()
	const line = "ERROR: Cannot connect to the Docker daemon at unix:///var/run/docker.sock"

	out := wrap(line, "    ", 60)
	if strings.Contains(out, "…") {
		t.Errorf("output was truncated:\n%s", out)
	}
	// Every character of the original must survive somewhere in the result.
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "docker.sock") {
		t.Errorf("the end of the line was lost:\n%s", out)
	}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(l) > 60 {
			t.Errorf("line exceeds the width: %q", l)
		}
	}
}

// TestWrapHandlesAWidthTooSmallToIndent. A very narrow window must not spin
// or produce one character per line forever.
func TestWrapHandlesAWidthTooSmallToIndent(t *testing.T) {
	t.Parallel()
	out := wrap("abcdefghij", "        ", 4)
	if out == "" {
		t.Fatal("produced nothing")
	}
	if n := strings.Count(out, "\n"); n > 12 {
		t.Errorf("degenerate wrapping produced %d lines", n)
	}
}
