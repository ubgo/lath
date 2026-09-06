package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubgo/lath/pipeline"
)

// TestTextReporterRendersEachKind covers the default output: what an operator
// reads while watching a deploy.
func TestTextReporterRendersEachKind(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	r := pipeline.NewTextReporter(&buf)

	r.StepStart(3, 14, "build-image")
	r.Detailf("building %s", "app:1")
	r.Output("#5 DONE 0.0s")
	// It must also satisfy the optional interface, or State.Output would fall
	// back to Detailf and the two would render identically.
	var _ pipeline.OutputReporter = r
	r.StepDone(3, 14, "build-image", 1234*time.Millisecond)

	got := buf.String()
	for _, want := range []string{"3/14", "build-image", "building app:1", "#5 DONE", "1.234s"} {
		if !strings.Contains(got, want) {
			t.Errorf("output omits %q:\n%s", want, got)
		}
	}
	// Raw command output is indented deeper than a step's own words, so the
	// two are distinguishable at a glance.
	detailIndent := indentOf(got, "building app:1")
	outputIndent := indentOf(got, "#5 DONE")
	if outputIndent <= detailIndent {
		t.Errorf("command output (%d) is not indented deeper than detail (%d)", outputIndent, detailIndent)
	}
}

func indentOf(text, needle string) int {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	return -1
}

// TestDiscardReporterIsTheSilentDefault. A State built with no Reporter must
// not nil-panic mid-run: a crash in output plumbing must never abort a deploy.
func TestDiscardReporterIsTheSilentDefault(t *testing.T) {
	t.Parallel()
	s := pipeline.NewState(pipeline.ModeExecute, nil)
	s.Detailf("this goes nowhere")
	w := s.Output()
	if _, err := w.Write([]byte("nor this\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Mode() != pipeline.ModeExecute {
		t.Errorf("Mode() = %q", s.Mode())
	}
	if s.DryRun() {
		t.Error("ModeExecute reported as a dry run")
	}
}

// TestNewTextReporterToDiscard is the supported way to ask for silence.
func TestNewTextReporterToDiscard(t *testing.T) {
	t.Parallel()
	r := pipeline.NewTextReporter(io.Discard)
	r.StepStart(1, 1, "x")
	r.Detailf("y")
	r.Output("z")
	r.StepDone(1, 1, "x", time.Second)
}

// TestAttachDebuggerFuncOpensLazily is the rule that keeps a run from
// announcing "waiting for a debugger" before it has checked its own arguments.
func TestAttachDebuggerFuncOpensLazily(t *testing.T) {
	var opened int
	pipeline.AttachDebuggerFunc(func() (pipeline.Debugger, error) {
		opened++
		return &pipeline.FakeDebugger{Script: []pipeline.Action{pipeline.ActNext}}, nil
	})
	t.Cleanup(func() { pipeline.AttachDebugger(nil) })

	if opened != 0 {
		t.Fatal("the debugger was opened at arming time, before any pipeline existed")
	}
	_ = pipeline.NewState(pipeline.ModeExecute, nil)
	if opened != 1 {
		t.Fatalf("opened %d times on the first State, want 1", opened)
	}
	// Called at most once: a second attempt would restart the wait mid-run.
	_ = pipeline.NewState(pipeline.ModeExecute, nil)
	if opened != 1 {
		t.Errorf("opened %d times, want exactly 1", opened)
	}
	if err := pipeline.DebuggerError(); err != nil {
		t.Errorf("DebuggerError = %v, want nil after a successful open", err)
	}
}

// TestDebuggerErrorReportsAFailedAttach, NewState cannot fail, so a debugger
// that could not attach is reported here instead. A run nobody could supervise
// is a failed run, not a quiet one.
func TestDebuggerErrorReportsAFailedAttach(t *testing.T) {
	want := errors.New("nobody attached")
	pipeline.AttachDebuggerFunc(func() (pipeline.Debugger, error) { return nil, want })
	t.Cleanup(func() { pipeline.AttachDebugger(nil) })

	s := pipeline.NewState(pipeline.ModeExecute, nil)
	if s == nil {
		t.Fatal("NewState returned nil rather than a usable State")
	}
	if err := pipeline.DebuggerError(); !errors.Is(err, want) {
		t.Errorf("DebuggerError = %v, want %v", err, want)
	}
	// The run proceeds unsupervised rather than being aborted at construction.
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		pipeline.Func{Label: "one", Do: func(_ context.Context, _ *pipeline.State) error { return nil }},
	}}
	if err := p.Run(context.Background(), s); err != nil {
		t.Errorf("a failed attach aborted the run: %v", err)
	}
}

// recordingReporter captures everything a run says, so a test can assert that
// both destinations received it.
type recordingReporter struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingReporter) StepStart(i, total int, name string) {
	r.add(fmt.Sprintf("start %d/%d %s", i, total, name))
}

func (r *recordingReporter) StepDone(i, total int, name string, _ time.Duration) {
	r.add(fmt.Sprintf("done %d/%d %s", i, total, name))
}

func (r *recordingReporter) Detailf(format string, args ...any) {
	r.add("detail " + fmt.Sprintf(format, args...))
}

func (r *recordingReporter) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, s)
}

func (r *recordingReporter) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// debuggerWithReporter is a Debugger that also wants the run's human output,
// the shape the debug transport has: see pipeline.ReporterSource.
type debuggerWithReporter struct {
	pipeline.FakeDebugger
	rep *debugSideReporter
}

func (d *debuggerWithReporter) Reporter() pipeline.Reporter { return d.rep }

// debugSideReporter is what a Debugger hands out. The marker method is what
// stops NewState wrapping it twice.
type debugSideReporter struct{ recordingReporter }

func (*debugSideReporter) IsDebugReporter() {}

// TestBothReportersReachEveryDestination.
//
// A definition builds its own State with its own Reporter, and a debugger
// attached afterwards must not steal that output or be starved of it. If the
// wrapper dropped either side, the symptom is subtle and awful: the terminal
// looks fine while the attached debugger shows a run whose last step never
// finishes, or vice versa.
func TestBothReportersReachEveryDestination(t *testing.T) {
	caller := &recordingReporter{}
	debug := &debuggerWithReporter{rep: &debugSideReporter{}}
	// Script: one answer per pause, and this pipeline pauses once.
	debug.Script = []pipeline.Action{pipeline.ActContinue}
	pipeline.AttachDebugger(debug)
	t.Cleanup(func() { pipeline.AttachDebugger(nil) })

	p := pipeline.Pipeline{Name: "both", Steps: []pipeline.Step{
		pipeline.Func{
			Label: "speak",
			Do: func(_ context.Context, s *pipeline.State) error {
				s.Detailf("a step describing itself")
				return nil
			},
		},
	}}
	if err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, caller)); err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]string{
		"the caller's reporter": caller.all(),
		"the debugger's":        debug.rep.all(),
	} {
		for _, want := range []string{"start 1/1 speak", "detail a step describing itself", "done 1/1 speak"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s never saw %q:\n%s", name, want, got)
			}
		}
	}
}

// TestADebugReporterIsNotWrappedTwice. A caller who passes the debugger's own
// reporter explicitly would otherwise see every line duplicated, which reads
// as a bug in the step rather than in the plumbing.
func TestADebugReporterIsNotWrappedTwice(t *testing.T) {
	shared := &debugSideReporter{}
	debug := &debuggerWithReporter{rep: shared}
	debug.Script = []pipeline.Action{pipeline.ActContinue}
	pipeline.AttachDebugger(debug)
	t.Cleanup(func() { pipeline.AttachDebugger(nil) })

	s := pipeline.NewState(pipeline.ModeExecute, shared)
	s.Detailf("said once")

	if got := strings.Count(shared.all(), "said once"); got != 1 {
		t.Errorf("the line appeared %d times, want 1:\n%s", got, shared.all())
	}
}
