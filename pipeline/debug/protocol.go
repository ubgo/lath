// Package debug carries a pipeline's progress to an out-of-process debugger
// and its decisions back.
//
// Why out of process: lath replaces itself with the compiled definition via
// syscall.Exec, so by the time a pipeline runs there is no runner left to draw
// a panel. And whatever the definition imports, every definition imports
// forever. A terminal-UI library placed here would land in every project's
// go.mod whether or not it is ever used. Splitting the renderer into a
// separate process keeps this side to net and encoding/json, both stdlib, so
// no definition's dependencies change at all.
//
// The transport is newline-delimited JSON, deliberately: when the client
// itself is what is broken, a session can be driven by hand with
// `nc -U <socket>` and the transcript read by eye.
package debug

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ubgo/lath/pipeline"
)

// EventKind names a message travelling from the pipeline to the client.
type EventKind string

const (
	// KindPlan carries the whole step sequence, once, on connect.
	KindPlan EventKind = "plan"
	// KindStart precedes a step.
	KindStart EventKind = "start"
	// KindDetail is a step describing what it is doing.
	KindDetail EventKind = "detail"
	// KindOutput is one line a command printed. Separate from KindDetail
	// because the volumes differ by orders of magnitude and a client wants to
	// render, collapse or scroll them differently.
	KindOutput EventKind = "output"
	// KindDone follows a step that succeeded.
	KindDone EventKind = "done"
	// KindPaused means the run is waiting for a decision.
	KindPaused EventKind = "paused"
	// KindFinished is the last message of a session.
	KindFinished EventKind = "finished"
)

// EventKindValues is the canonical iteration order, so a client switching on
// a kind and this package cannot drift apart.
var EventKindValues = []EventKind{
	KindPlan, KindStart, KindDetail, KindOutput, KindDone, KindPaused, KindFinished,
}

// Valid reports whether k is a declared kind.
func (k EventKind) Valid() bool {
	for _, v := range EventKindValues {
		if k == v {
			return true
		}
	}
	return false
}

// Event is one message from the pipeline. One struct rather than a type per
// kind, because the wire format is line-delimited JSON and a client decodes
// before it knows which kind arrived.
type Event struct {
	Kind EventKind `json:"t"`

	// Pipeline and Steps are set on KindPlan.
	Pipeline string               `json:"pipeline,omitempty"`
	Steps    []pipeline.PlanEntry `json:"steps,omitempty"`

	// N, Total and Name are set on KindStart, KindDone and KindPaused.
	N     int    `json:"n,omitempty"`
	Total int    `json:"total,omitempty"`
	Name  string `json:"name,omitempty"`

	// Msg carries a KindDetail message.
	Msg string `json:"msg,omitempty"`

	// Took is a step's duration on KindDone.
	Took string `json:"took,omitempty"`

	// Next, State and Replayable are set on KindPaused.
	Next       string            `json:"next,omitempty"`
	State      map[string]string `json:"state,omitempty"`
	Replayable bool              `json:"replayable,omitempty"`

	// Err carries a failure on KindPaused and KindFinished. A string rather
	// than an error because errors do not survive JSON, and the client shows
	// it rather than branching on it.
	Err string `json:"err,omitempty"`
}

// Control is one decision from the client.
type Control struct {
	// Cmd is the action to take.
	Cmd pipeline.Action `json:"cmd"`
	// Ack is the pause number this answers.
	//
	// Without it, key-mashing silently deploys: four `next` presses at one
	// pause queue in the buffer, and the loop reads them one after another,
	// running the next three steps unpaused. The pipeline discards any
	// Control whose Ack is not the pause it is currently sitting at, which
	// also handles a client that reconnects and replays, and a `nc` session
	// where the operator typed ahead.
	Ack int `json:"ack"`
}

// reporter turns Reporter calls into events.
//
// This is the whole "server" half of the events channel: three methods, no new
// concepts. See pipeline.Reporter for why steps never print.
type reporter struct {
	send func(Event) error
}

func (r reporter) StepStart(index, total int, name string) {
	// Errors are dropped deliberately: output plumbing must never abort a
	// deploy. A client that has gone away is detected at the next Pause,
	// which is the point where it matters.
	_ = r.send(Event{Kind: KindStart, N: index, Total: total, Name: name})
}

func (r reporter) StepDone(index, total int, name string, took time.Duration) {
	_ = r.send(Event{Kind: KindDone, N: index, Total: total, Name: name,
		Took: took.Round(time.Millisecond).String()})
}

// IsDebugReporter marks this as a debugger's reporter, so pipeline.NewState
// does not wrap it a second time when a caller passes it explicitly.
func (r reporter) IsDebugReporter() {}

func (r reporter) Detailf(format string, args ...any) {
	_ = r.send(Event{Kind: KindDetail, Msg: fmt.Sprintf(format, args...)})
}

// Output implements pipeline.OutputReporter, so command output reaches an
// attached client as it happens rather than only the terminal the run started
// in.
func (r reporter) Output(line string) {
	_ = r.send(Event{Kind: KindOutput, Msg: line})
}

// NewReporter returns a pipeline.Reporter that emits events through send.
//
// Exported so a caller can mirror output to a terminal and a client at once by
// wrapping both, which is what `--debug` does: the operator watching the
// pipeline's own terminal still sees it scroll.
func NewReporter(send func(Event) error) pipeline.Reporter { return reporter{send: send} }

// MultiReporter fans every call out to each reporter in turn.
//
// Why it exists: with a debugger attached the output has two audiences, the
// terminal the pipeline was started in, and the attached client, and a run
// whose own terminal went silent the moment a debugger connected would look
// like it had hung.
type MultiReporter []pipeline.Reporter

func (m MultiReporter) StepStart(index, total int, name string) {
	for _, r := range m {
		r.StepStart(index, total, name)
	}
}

func (m MultiReporter) StepDone(index, total int, name string, took time.Duration) {
	for _, r := range m {
		r.StepDone(index, total, name, took)
	}
}

func (m MultiReporter) Detailf(format string, args ...any) {
	for _, r := range m {
		r.Detailf(format, args...)
	}
}

// Output forwards command output to any member that accepts it, and falls
// back to Detailf for those that do not, so wrapping a plain Reporter never
// silently discards a run's output.
func (m MultiReporter) Output(line string) {
	for _, r := range m {
		if or, ok := r.(pipeline.OutputReporter); ok {
			or.Output(line)
			continue
		}
		r.Detailf("%s", line)
	}
}

// errString renders err for the wire, empty for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// encodeLine writes one protocol message as a single newline-terminated line.
//
// Generic over the two message types rather than taking `any`: the wire format
// has exactly two shapes, and accepting anything would let an unrelated struct
// be written into a stream whose reader can only decode those two. The type
// parameter makes that a compile error.
//
// json.Encoder already appends the newline; naming it here states the framing
// once, so a reader of client.go or server.go does not have to know that.
func encodeLine[T Event | Control](enc *json.Encoder, v T) error { return enc.Encode(v) }
