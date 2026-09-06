package pipeline_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/pipeline"
)

// collectingReporter implements OutputReporter, keeping the two streams apart
// so a test can prove which path a line took.
type collectingReporter struct {
	details []string
	output  []string
}

func (c *collectingReporter) StepStart(int, int, string)               {}
func (c *collectingReporter) StepDone(int, int, string, time.Duration) {}
func (c *collectingReporter) Detailf(f string, a ...any) {
	c.details = append(c.details, sprintf(f, a...))
}
func (c *collectingReporter) Output(line string) { c.output = append(c.output, line) }

// plainReporter does NOT implement OutputReporter, every Reporter written
// before this feature existed is one of these.
type plainReporter struct{ details []string }

func (p *plainReporter) StepStart(int, int, string)               {}
func (p *plainReporter) StepDone(int, int, string, time.Duration) {}
func (p *plainReporter) Detailf(f string, a ...any)               { p.details = append(p.details, sprintf(f, a...)) }

// sprintf is fmt.Sprintf, named so the reporters below read as recorders.
func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func TestOutputSplitsIntoLines(t *testing.T) {
	t.Parallel()
	r := &collectingReporter{}
	s := pipeline.NewState(pipeline.ModeExecute, r)
	w := s.Output()

	// Chunks that have nothing to do with line boundaries, which is exactly
	// how a process's output actually arrives.
	for _, chunk := range []string{"#1 load", " definition\n#2 tra", "nsferring\n#3 DO", "NE"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	want := []string{"#1 load definition", "#2 transferring", "#3 DONE"}
	if got := strings.Join(r.output, "|"); got != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", r.output, want)
	}
}

// TestOutputFlushesAPartialLine. A progress bar's last frame and an
// unterminated prompt both arrive without a newline. Losing them would lose
// exactly the line that says why something stopped.
func TestOutputFlushesAPartialLine(t *testing.T) {
	t.Parallel()
	r := &collectingReporter{}
	w := pipeline.NewState(pipeline.ModeExecute, r).Output()
	if _, err := w.Write([]byte("no trailing newline")); err != nil {
		t.Fatal(err)
	}
	if len(r.output) != 0 {
		t.Fatalf("emitted %q before Close", r.output)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(r.output) != 1 || r.output[0] != "no trailing newline" {
		t.Errorf("after Close: %q", r.output)
	}
	// Close is deferred as well as called in places; it must be idempotent.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(r.output) != 1 {
		t.Errorf("second Close emitted again: %q", r.output)
	}
}

// TestOutputStripsCarriageReturns. A progress bar redraws itself with \r, and
// leaving those in puts stray control characters into every log file.
func TestOutputStripsCarriageReturns(t *testing.T) {
	t.Parallel()
	r := &collectingReporter{}
	w := pipeline.NewState(pipeline.ModeExecute, r).Output()
	if _, err := w.Write([]byte("downloading 50%\r\ndownloading 100%\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	for _, line := range r.output {
		if strings.ContainsRune(line, '\r') {
			t.Errorf("carriage return survived in %q", line)
		}
	}
}

// TestOutputSurvivesPercentSigns is the trap in the fallback path: passing a
// line as Detailf's FORMAT renders "100%" as "%!(NOVERB)" and loses the
// content. Docker output is full of percent signs.
func TestOutputSurvivesPercentSigns(t *testing.T) {
	t.Parallel()
	const line = "transferring context: 100% done (50%% cached)"

	for _, tc := range []struct {
		name string
		make func() (pipeline.Reporter, func() []string)
	}{
		{"OutputReporter", func() (pipeline.Reporter, func() []string) {
			r := &collectingReporter{}
			return r, func() []string { return r.output }
		}},
		{"Detailf fallback", func() (pipeline.Reporter, func() []string) {
			r := &plainReporter{}
			return r, func() []string { return r.details }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep, read := tc.make()
			w := pipeline.NewState(pipeline.ModeExecute, rep).Output()
			if _, err := w.Write([]byte(line + "\n")); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			got := read()
			if len(got) != 1 || got[0] != line {
				t.Errorf("got %q, want %q", got, line)
			}
		})
	}
}

// TestOutputFallsBackWhenTheReporterIsPlain holds the promise that output is
// never silently dropped, only rendered less precisely.
func TestOutputFallsBackWhenTheReporterIsPlain(t *testing.T) {
	t.Parallel()
	r := &plainReporter{}
	w := pipeline.NewState(pipeline.ModeExecute, r).Output()
	if _, err := w.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if got := strings.Join(r.details, "|"); got != "one|two" {
		t.Errorf("details = %q; a plain Reporter must still see the output", r.details)
	}
}

// TestOutputReachesADebuggerAndTheTerminal, the wrapper NewState installs
// must not hide the OutputReporter either side implements.
func TestOutputReachesADebuggerAndTheTerminal(t *testing.T) {
	t.Parallel()
	terminal := &collectingReporter{}
	dbg := &reporterDebugger{FakeDebugger: pipeline.FakeDebugger{}, rep: &collectingReporter{}}

	s := pipeline.NewState(pipeline.ModeExecute, terminal, pipeline.Debug(dbg))
	w := s.Output()
	if _, err := w.Write([]byte("built\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	if len(terminal.output) != 1 {
		t.Errorf("terminal saw %q", terminal.output)
	}
	if got := dbg.rep.(*collectingReporter).output; len(got) != 1 {
		t.Errorf("debugger saw %q", got)
	}
}

type reporterDebugger struct {
	pipeline.FakeDebugger
	rep pipeline.Reporter
}

func (r *reporterDebugger) Reporter() pipeline.Reporter { return r.rep }

// TestStepsCanStreamThroughState is the end-to-end shape a step uses.
func TestStepsCanStreamThroughState(t *testing.T) {
	t.Parallel()
	r := &collectingReporter{}
	step := pipeline.Func{Label: "run-something", Do: func(_ context.Context, s *pipeline.State) error {
		w := s.Output()
		defer w.Close()
		_, err := w.Write([]byte("line one\nline two\n"))
		return err
	}}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{step}}
	if err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, r)); err != nil {
		t.Fatal(err)
	}
	if len(r.output) != 2 {
		t.Errorf("output = %q, want two lines", r.output)
	}
}

// TestPassthroughSeesThroughTheDebuggerWrapper is the regression for a bug
// that made yielding the terminal impossible.
//
// Arming a debugger replaces the caller's *TextReporter with a wrapper that
// fans out to both. The type assertion that finds the terminal then failed,
// so a run explicitly told it owned the screen still could not use it, and
// docker kept rendering its plain fallback under `lath tui`.
func TestPassthroughSeesThroughTheDebuggerWrapper(t *testing.T) {
	// NOT parallel: YieldTerminal is process-wide, matching how the generated
	// dispatcher sets it once at startup.
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	pipeline.YieldTerminal()
	t.Cleanup(pipeline.ResetTerminalYield)

	// A debugger that supplies a Reporter, which the real one does, is what
	// makes NewState install the wrapper. A FakeDebugger alone does not, and a
	// version of this test using one passed even with the lookup deleted.
	dbg := &reporterDebugger{rep: &collectingReporter{}}
	s := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(f), pipeline.Debug(dbg))

	// /dev/null IS a character device, so the only thing under test here is
	// whether the wrapper hid the file.
	if s.Passthrough() == nil {
		t.Error("the debugger's reporter wrapper hid the terminal")
	}
}

// TestPassthroughRefusesWhileADebuggerOwnsTheScreen holds the default: a panel
// and a tool redrawing itself cannot share a terminal.
func TestPassthroughRefusesWhileADebuggerOwnsTheScreen(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	pipeline.ResetTerminalYield()
	s := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(f),
		pipeline.Debug(&pipeline.FakeDebugger{}))
	if s.Passthrough() != nil {
		t.Error("handed the terminal to a step while a panel was drawing on it")
	}
}

// TestPassthroughRefusesAPipe. A pipe is what CI, a redirect and a recording
// all look like, and none of them can render a progress table.
func TestPassthroughRefusesAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	pipeline.ResetTerminalYield()
	s := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(w))
	if s.Passthrough() != nil {
		t.Error("a pipe was treated as a terminal")
	}
}
