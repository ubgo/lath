package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"time"
)

// Reporter receives human-facing progress output.
//
// Why this exists instead of printing directly: a package that writes to
// os.Stdout cannot be imported by a program that wants JSON, cannot be tested
// without capturing global state, and cannot be embedded in a larger tool. The
// interface keeps the engine importable, which is the whole point of it being
// a library rather than part of the command.
//
// It is deliberately NOT a logger. This is the command's product, the output
// a person reads while watching a deploy, not diagnostic logging, which
// belongs behind the importing application's own logging stack.
type Reporter interface {
	// StepStart is called immediately before a step's Run.
	StepStart(index, total int, name string)
	// StepDone is called after a step's Run returns without error.
	StepDone(index, total int, name string, took time.Duration)
	// Detailf is called BY a step to describe what it is doing. Steps use this
	// rather than printing, for the reasons above.
	Detailf(format string, args ...any)
}

// TextReporter writes plain progress lines to an io.Writer.
//
// Why an io.Writer field rather than a hardcoded os.Stdout: tests capture into
// a bytes.Buffer, and a caller embedding this engine can route output wherever
// it likes without a fork.
type TextReporter struct{ W io.Writer }

// NewTextReporter returns a TextReporter writing to w. Passing io.Discard is
// the supported way to run a pipeline silently.
//
// Its three methods implement Reporter and are not re-documented individually ,
// the contract for each is stated once on the interface above.
func NewTextReporter(w io.Writer) *TextReporter { return &TextReporter{W: w} }

func (r *TextReporter) StepStart(index, total int, name string) {
	fmt.Fprintf(r.W, "→ %d/%d %s\n", index, total, name)
}

func (r *TextReporter) StepDone(index, total int, name string, took time.Duration) {
	fmt.Fprintf(r.W, "  ok (%s)\n", took.Round(reportPrecision))
}

func (r *TextReporter) Detailf(format string, args ...any) {
	fmt.Fprintf(r.W, "    "+format+"\n", args...)
}

// Output writes a raw command line, indented under the step that produced it.
//
// Deeper than Detailf's indent so the two are distinguishable at a glance:
// what a step chose to say, and what the tool it ran actually printed.
func (r *TextReporter) Output(line string) {
	fmt.Fprintf(r.W, "      %s\n", line)
}

// reportPrecision is how far step timings are rounded before display.
// Millisecond because sub-millisecond noise is never actionable for work
// measured in seconds, and unrounded durations make output columns jump.
const reportPrecision = time.Millisecond

// discardReporter satisfies Reporter and drops everything. Used as the
// zero-value fallback so a State constructed without a Reporter cannot nil-
// panic mid-run. A crash in output plumbing must never abort a real deploy.
type discardReporter struct{}

func (discardReporter) StepStart(int, int, string)               {}
func (discardReporter) StepDone(int, int, string, time.Duration) {}
func (discardReporter) Detailf(string, ...any)                   {}
func (discardReporter) Output(string)                            {}

// OutputReporter is an optional upgrade a Reporter may implement to receive
// raw command output, what docker, git or a shell actually printed, as
// distinct from Detailf, which is a step describing itself.
//
// Why the distinction matters: the two have completely different volume and
// completely different value. A step emits two or three Detailf lines that are
// worth showing always; a single `docker build` emits hundreds of lines that
// are worth showing while you are debugging and worth collapsing otherwise. A
// renderer that cannot tell them apart has to treat all of it the same way,
// and the useful lines drown.
//
// Optional rather than required so every existing Reporter keeps compiling.
// One that does not implement it still sees the output, through Detailf, see
// State.Output. Nothing is ever silently dropped.
type OutputReporter interface {
	Reporter
	// Output receives one line, without its trailing newline.
	Output(line string)
}

// lineWriter splits writes into lines and hands each to fn.
//
// Why a splitter rather than passing the writes through: a process's output
// arrives in arbitrary chunks that have nothing to do with line boundaries ,
// half a line, three lines, a line split across two reads. A renderer that
// prefixed or indented raw chunks would produce garbage. Everything downstream
// wants lines, so the splitting happens once, here.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		// Carriage returns are stripped so a progress bar redrawing itself
		// does not leave stray control characters in a log file.
		w.fn(string(bytes.TrimRight(line, "\r")))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits whatever is left when a process ends without a final newline ,
// which is exactly how a prompt or a progress bar's last frame arrives.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.fn(string(bytes.TrimRight(w.buf, "\r")))
		w.buf = nil
	}
}
