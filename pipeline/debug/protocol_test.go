package debug_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/pipeline/debug"
)

// step records that it ran, and optionally fails.
type step struct {
	name string
	log  *[]string
	fail error
}

func (s *step) Name() string             { return s.name }
func (s *step) Requires() []pipeline.Key { return nil }
func (s *step) Provides() []pipeline.Key { return nil }
func (s *step) Replayable() bool         { return true }
func (s *step) Run(_ context.Context, st *pipeline.State) error {
	*s.log = append(*s.log, s.name)
	st.Detailf("ran %s", s.name)
	return s.fail
}

// connPair returns two connected endpoints over loopback TCP, no filesystem,
// no terminal, and no privileges.
//
// NOT net.Pipe, and the reason matters: net.Pipe is completely unbuffered, so
// a second write blocks until someone reads the first. That makes it unable to
// model the one case these tests care most about, an operator whose queued
// keystrokes are sitting in a socket buffer, and a test written over it
// deadlocks instead of failing. A real socket buffers; so does this.
func connPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type res struct {
		c   net.Conn
		err error
	}
	accepted := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		accepted <- res{c, err}
	}()

	dialed, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-accepted
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { _ = dialed.Close(); _ = r.c.Close() })
	return r.c, dialed
}

// session wires a Server and a Client over a connected pair. The whole
// protocol is exercised in-process, which is the reason the transport is a
// stream rather than a callback.
func session(t *testing.T) (*debug.Server, *debug.Client) {
	t.Helper()
	a, b := connPair(t)
	srv, cli := debug.NewServer(a), debug.NewClient(b)
	t.Cleanup(func() { _ = srv.Close(); _ = cli.Close() })
	return srv, cli
}

// drive runs p against srv while a goroutine answers each pause from script.
// Returns the run's error and every event the client saw.
func drive(t *testing.T, p pipeline.Pipeline, srv *debug.Server, cli *debug.Client,
	answer func(c *debug.Client, e debug.Event)) (error, []debug.Event) {
	t.Helper()

	var events []debug.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := cli.Next()
			if err != nil {
				return
			}
			events = append(events, e)
			if e.Kind == debug.KindPaused {
				answer(cli, e)
			}
			if e.Kind == debug.KindFinished {
				return
			}
		}
	}()

	st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter(), pipeline.Debug(srv))
	err := p.Run(context.Background(), st)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("client goroutine did not finish")
	}
	return err, events
}

func three(log *[]string) pipeline.Pipeline {
	return pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&step{name: "one", log: log}, &step{name: "two", log: log}, &step{name: "three", log: log},
	}}
}

func TestFullSessionOverAPipe(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)
	err, events := drive(t, three(&log), srv, cli, func(c *debug.Client, e debug.Event) {
		_ = c.Send(pipeline.ActNext, e.N)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,three" {
		t.Errorf("order = %q", got)
	}

	// The event stream must open with the plan and close with finished.
	if len(events) == 0 || events[0].Kind != debug.KindPlan {
		t.Fatalf("first event = %+v, want the plan", events[0])
	}
	if n := len(events[0].Steps); n != 3 {
		t.Errorf("plan carried %d steps, want 3", n)
	}
	last := events[len(events)-1]
	if last.Kind != debug.KindFinished || last.Err != "" {
		t.Errorf("last event = %+v, want a clean finish", last)
	}

	var starts, details, dones, pauses int
	for _, e := range events {
		switch e.Kind {
		case debug.KindStart:
			starts++
		case debug.KindDetail:
			details++
		case debug.KindDone:
			dones++
		case debug.KindPaused:
			pauses++
		}
	}
	// Two pauses for three steps: the final one has nothing left to decide.
	if starts != 3 || details != 3 || dones != 3 || pauses != 2 {
		t.Errorf("starts=%d details=%d dones=%d pauses=%d, want 3/3/3/2",
			starts, details, dones, pauses)
	}
}

// TestAckDiscardsStaleControls is the key-mashing bug, and it is written so
// that the ack check is the ONLY thing that makes it pass.
//
// The obvious version of this test, mash `next` at every pause and assert the
// order, proves nothing: the surplus controls simply become the answers to
// the later pauses, and the pipeline runs the same steps in the same order
// either way. The difference only becomes visible when the operator STOPS
// answering: with the check, the surplus is discarded and the run stalls at
// the next pause; without it, the queued keystrokes drive the pipeline onward
// unsupervised, which on a real deploy is how step 6 starts containers nobody
// asked for.
func TestAckDiscardsStaleControls(t *testing.T) {
	t.Parallel()
	var log []string
	a, b := connPair(t)
	srv, cli := debug.NewServer(a), debug.NewClient(b)

	const surplus = 4
	go func() {
		for {
			e, err := cli.Next()
			if err != nil {
				return
			}
			if e.Kind != debug.KindPaused {
				continue
			}
			if e.N == 1 {
				// An impatient operator on the first pause, and silence after.
				for i := 0; i < surplus; i++ {
					_ = cli.Send(pipeline.ActNext, 1)
				}
				continue
			}
			// Deliberately answers nothing here. Everything still in the
			// socket buffer names pause 1 and must be refused.
			time.AfterFunc(200*time.Millisecond, func() { _ = cli.Close() })
			return
		}
	}()

	st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter(), pipeline.Debug(srv))
	err := three(&log).Run(context.Background(), st)

	if !errors.Is(err, pipeline.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted: stale controls advanced the run", err)
	}
	if got := strings.Join(log, ","); got != "one,two" {
		t.Fatalf("order = %q, want \"one,two\": %d stale controls ran %d extra step(s)",
			got, surplus-1, len(log)-2)
	}
}

// TestPauseCarriesEverythingTheClientRenders, a wrong Total or Next is
// invisible here and obvious in a panel.
func TestPauseCarriesEverythingTheClientRenders(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)
	_, events := drive(t, three(&log), srv, cli, func(c *debug.Client, e debug.Event) {
		_ = c.Send(pipeline.ActNext, e.N)
	})
	var paused []debug.Event
	for _, e := range events {
		if e.Kind == debug.KindPaused {
			paused = append(paused, e)
		}
	}
	if len(paused) != 2 {
		t.Fatalf("got %d pauses", len(paused))
	}
	if paused[0].N != 1 || paused[0].Total != 3 || paused[0].Name != "one" || paused[0].Next != "two" {
		t.Errorf("pause 1 = %+v", paused[0])
	}
	if !paused[0].Replayable {
		t.Error("a replayable step arrived as not replayable")
	}
	if paused[1].Next != "three" {
		t.Errorf("Next = %q, want the step that would run", paused[1].Next)
	}
}

func TestRerunOverTheWire(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)
	replayed := false
	err, _ := drive(t, three(&log), srv, cli, func(c *debug.Client, e debug.Event) {
		if e.N == 2 && !replayed {
			replayed = true
			_ = c.Send(pipeline.ActRerun, e.N)
			return
		}
		_ = c.Send(pipeline.ActNext, e.N)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,two,three" {
		t.Errorf("order = %q", got)
	}
}

func TestQuitOverTheWire(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)
	err, events := drive(t, three(&log), srv, cli, func(c *debug.Client, e debug.Event) {
		_ = c.Send(pipeline.ActQuit, e.N)
	})
	if !errors.Is(err, pipeline.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if got := strings.Join(log, ","); got != "one" {
		t.Errorf("order = %q", got)
	}
	last := events[len(events)-1]
	if last.Kind != debug.KindFinished || last.Err == "" {
		t.Errorf("finish did not report the abort: %+v", last)
	}
}

// TestDisconnectAbortsTheRun holds the decision from the spec: losing the
// supervisor is not consent to proceed.
func TestDisconnectAbortsTheRun(t *testing.T) {
	t.Parallel()
	var log []string
	a, b := connPair(t)
	srv, cli := debug.NewServer(a), debug.NewClient(b)

	go func() {
		for {
			e, err := cli.Next()
			if err != nil {
				return
			}
			if e.Kind == debug.KindPaused {
				// The operator's terminal dies mid-run.
				_ = cli.Close()
				return
			}
		}
	}()

	st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter(), pipeline.Debug(srv))
	err := three(&log).Run(context.Background(), st)
	if !errors.Is(err, pipeline.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if got := strings.Join(log, ","); got != "one" {
		t.Errorf("order = %q, want the run to stop at the disconnect", got)
	}
}

// TestFailurePausesOverTheWire. The step error must reach the client, or the
// operator cannot see why it stopped.
func TestFailurePausesOverTheWire(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&step{name: "one", log: &log},
		&step{name: "two", log: &log, fail: errors.New("i/o timeout")},
		&step{name: "three", log: &log},
	}}
	_, events := drive(t, p, srv, cli, func(c *debug.Client, e debug.Event) {
		_ = c.Send(pipeline.ActNext, e.N)
	})
	var sawErr bool
	for _, e := range events {
		if e.Kind == debug.KindPaused && strings.Contains(e.Err, "i/o timeout") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("the failure never reached the client")
	}
}

// TestUnknownActionIsDiscarded. The alternative to discarding is treating it
// as "keep going", the one outcome nobody asked for.
func TestUnknownActionIsDiscarded(t *testing.T) {
	t.Parallel()
	var log []string
	a, b := connPair(t)
	srv := debug.NewServer(a)
	enc, dec := json.NewEncoder(b), json.NewDecoder(b)

	go func() {
		for {
			var e debug.Event
			if err := dec.Decode(&e); err != nil {
				return
			}
			if e.Kind == debug.KindPaused {
				// A hand-written control with a bogus command, then a real one.
				_ = enc.Encode(map[string]any{"cmd": "proceed", "ack": e.N})
				_ = enc.Encode(map[string]any{"cmd": "next", "ack": e.N})
			}
		}
	}()

	st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter(), pipeline.Debug(srv))
	if err := three(&log).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,three" {
		t.Errorf("order = %q", got)
	}
}

// TestTranscriptIsHandReadable pins the property the format was chosen for:
// one JSON object per line, decodable by anything, including a person with nc.
func TestTranscriptIsHandReadable(t *testing.T) {
	t.Parallel()
	var buf syncBuffer
	srv := debug.NewServer(&buf)
	_ = srv.Plan("deploy-prod", []pipeline.PlanEntry{{Position: 1, Total: 1, Name: "resolve-env"}})
	_ = srv.Finish(pipeline.Finished{})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one object per message:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var e debug.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d is not standalone JSON: %v", i+1, err)
		}
	}
}

// syncBuffer is a ReadWriteCloser over an in-memory buffer, for tests that
// only inspect what was written.
type syncBuffer struct{ b strings.Builder }

func (s *syncBuffer) Write(p []byte) (int, error) { return s.b.Write(p) }
func (s *syncBuffer) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *syncBuffer) Close() error                { return nil }
func (s *syncBuffer) String() string              { return s.b.String() }

func TestEventKindValid(t *testing.T) {
	t.Parallel()
	for _, k := range debug.EventKindValues {
		if !k.Valid() {
			t.Errorf("%q is in EventKindValues but not Valid", k)
		}
	}
	if debug.EventKind("progress").Valid() {
		t.Error("an undeclared kind must not validate")
	}
}

func TestClientRejectsAnUnknownAction(t *testing.T) {
	t.Parallel()
	_, cli := session(t)
	if err := cli.Send(pipeline.Action("proceed"), 1); err == nil {
		t.Error("Send accepted an undeclared action")
	}
}

// TestOutputReachesTheClientWithoutDefinitionChanges is the property the whole
// design rests on: a definition that builds its own State with its own
// Reporter, every definition that exists, still sends its step boundaries
// and detail lines to an attached client, with not one line changed.
func TestOutputReachesTheClientWithoutDefinitionChanges(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)

	var events []debug.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := cli.Next()
			if err != nil {
				return
			}
			events = append(events, e)
			if e.Kind == debug.KindPaused {
				_ = cli.Send(pipeline.ActNext, e.N)
			}
			if e.Kind == debug.KindFinished {
				return
			}
		}
	}()

	// EXACTLY what a definition writes today: its own reporter, nothing else.
	// The debugger is armed process-wide, as the generated dispatcher does it.
	pipeline.AttachDebugger(srv)
	defer pipeline.AttachDebugger(nil)

	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(io.Discard))
	if err := three(&log).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not finish")
	}

	var starts, details, dones int
	for _, e := range events {
		switch e.Kind {
		case debug.KindStart:
			starts++
		case debug.KindDetail:
			details++
		case debug.KindDone:
			dones++
		}
	}
	if starts != 3 || details != 3 || dones != 3 {
		t.Errorf("starts=%d details=%d dones=%d, want 3 each: a definition had to opt in",
			starts, details, dones)
	}
}

// TestPassingTheDebugReporterDoesNotDoubleOutput, a caller who wires it
// explicitly must not see every line twice.
func TestPassingTheDebugReporterDoesNotDoubleOutput(t *testing.T) {
	t.Parallel()
	var log []string
	srv, cli := session(t)

	var details int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := cli.Next()
			if err != nil {
				return
			}
			if e.Kind == debug.KindDetail {
				details++
			}
			if e.Kind == debug.KindPaused {
				_ = cli.Send(pipeline.ActNext, e.N)
			}
			if e.Kind == debug.KindFinished {
				return
			}
		}
	}()

	pipeline.AttachDebugger(srv)
	defer pipeline.AttachDebugger(nil)

	st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter())
	if err := three(&log).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	<-done
	if details != 3 {
		t.Errorf("details = %d, want 3: the reporter was applied twice", details)
	}
}

// TestReporterEmitsEveryKind covers the events half of the protocol: three
// Reporter methods turned into wire messages. A missing one is invisible in
// the pipeline and shows up as a panel that never fills in.
func TestReporterEmitsEveryKind(t *testing.T) {
	t.Parallel()
	var got []debug.Event
	rep := debug.NewReporter(func(e debug.Event) error {
		got = append(got, e)
		return nil
	})

	rep.StepStart(2, 9, "build-image")
	rep.Detailf("building %s", "app:1")
	rep.StepDone(2, 9, "build-image", 1500*time.Millisecond)
	if or, ok := rep.(pipeline.OutputReporter); ok {
		or.Output("#5 DONE 0.0s")
	} else {
		t.Fatal("the debug reporter does not accept raw command output")
	}

	if len(got) != 4 {
		t.Fatalf("emitted %d events, want 4: %+v", len(got), got)
	}
	if got[0].Kind != debug.KindStart || got[0].N != 2 || got[0].Total != 9 || got[0].Name != "build-image" {
		t.Errorf("start = %+v", got[0])
	}
	if got[1].Kind != debug.KindDetail || got[1].Msg != "building app:1" {
		t.Errorf("detail = %+v: the format was not applied", got[1])
	}
	if got[2].Kind != debug.KindDone || got[2].Took == "" {
		t.Errorf("done = %+v: a step's duration is missing", got[2])
	}
	if got[3].Kind != debug.KindOutput || got[3].Msg != "#5 DONE 0.0s" {
		t.Errorf("output = %+v", got[3])
	}
}

// TestReporterSurvivesASendFailure, output plumbing must never abort a
// deploy. A client that has gone away is detected at the next Pause, which is
// where it matters.
func TestReporterSurvivesASendFailure(t *testing.T) {
	t.Parallel()
	rep := debug.NewReporter(func(debug.Event) error { return errors.New("client gone") })
	rep.StepStart(1, 1, "one")
	rep.Detailf("x")
	rep.StepDone(1, 1, "one", time.Second)
	if or, ok := rep.(pipeline.OutputReporter); ok {
		or.Output("line")
	}
	// Reaching here without a panic is the assertion.
}

// TestMultiReporterFansOutIncludingRawOutput, wrapping a plain Reporter must
// not silently discard a run's command output.
func TestMultiReporterFansOutIncludingRawOutput(t *testing.T) {
	t.Parallel()
	var withOutput, plainOnly []string
	rich := debug.NewReporter(func(e debug.Event) error {
		withOutput = append(withOutput, string(e.Kind))
		return nil
	})
	plain := &plainRecorder{lines: &plainOnly}

	m := debug.MultiReporter{rich, plain}
	m.StepStart(1, 1, "one")
	m.StepDone(1, 1, "one", time.Second)
	m.Detailf("detail")
	m.Output("raw line")

	if len(withOutput) != 4 {
		t.Errorf("the output-aware member saw %v, want all four", withOutput)
	}
	// The plain member has no Output method, so the line must arrive through
	// Detailf rather than being dropped.
	var sawRaw bool
	for _, l := range plainOnly {
		if l == "raw line" {
			sawRaw = true
		}
	}
	if !sawRaw {
		t.Errorf("a plain Reporter lost the command output: %v", plainOnly)
	}
}

// plainRecorder implements pipeline.Reporter but NOT OutputReporter, the
// shape of every Reporter written before raw output existed.
type plainRecorder struct{ lines *[]string }

func (plainRecorder) StepStart(int, int, string)               {}
func (plainRecorder) StepDone(int, int, string, time.Duration) {}
func (p plainRecorder) Detailf(f string, a ...any)             { *p.lines = append(*p.lines, fmt.Sprintf(f, a...)) }

// TestClientNextRejectsAnUnknownKind. A message this client cannot render is
// a protocol mismatch, and guessing at it is worse than saying so.
func TestClientNextRejectsAnUnknownKind(t *testing.T) {
	t.Parallel()
	a, b := connPair(t)
	cli := debug.NewClient(b)
	if err := json.NewEncoder(a).Encode(map[string]any{"t": "invented"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Next(); err == nil {
		t.Error("an unknown event kind was accepted")
	}
}
