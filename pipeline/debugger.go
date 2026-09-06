package pipeline

import (
	"sync"
	"time"
)

// Action is a debugger's decision at a pause.
//
// A defined type with a fixed value set rather than a string, so a client
// sending an unrecognised command is rejected at the boundary instead of
// silently falling through a switch to whatever the default branch does, and
// the dangerous default here is "keep going".
type Action string

const (
	// ActNext runs the next step and pauses again.
	ActNext Action = "next"
	// ActRerun runs the step that just ran, again. See Replayable: this
	// replays a step, it does not undo one.
	ActRerun Action = "rerun"
	// ActContinue stops pausing and runs to completion.
	ActContinue Action = "continue"
	// ActQuit abandons the run. Steps already performed are NOT undone.
	ActQuit Action = "quit"
)

// ActionValues is the canonical iteration order for Action. Any client
// listing commands, any parser, and any test matrix reads from here so the set
// cannot drift between the place that parses an action and the place that
// acts on it.
var ActionValues = []Action{ActNext, ActRerun, ActContinue, ActQuit}

// Valid reports whether a is a declared action. Callers parsing input from a
// client MUST check this rather than casting.
func (a Action) Valid() bool {
	for _, v := range ActionValues {
		if a == v {
			return true
		}
	}
	return false
}

// Pause describes where a run has stopped and what the operator may do next.
//
// A struct rather than a parameter list because it is the message a debugger
// renders, and it will grow, adding a field must not break every
// implementation.
type Pause struct {
	// Position is the 1-based index of the step that just ran.
	Position int
	// Total is how many steps the pipeline has.
	Total int
	// Step is the step that just ran.
	Step Step
	// Next names the step that would run on ActNext, empty at the end.
	Next string
	// Err is the step's failure, or nil. A non-nil Err means the run is
	// paused ON a failure: ActNext and ActContinue will surface it, ActRerun
	// retries the step.
	Err error
	// State is the state bag rendered for display. Values pass through fmt,
	// so a secret.Value redacts itself, see State.Display for why this is a
	// rendered copy rather than the live map.
	State map[string]string
	// Replayable reports whether the step declared itself safe to run twice.
	// False for a step that did not say, which is the honest default: the
	// cost of wrongly assuming safe is a duplicate container, and the cost of
	// wrongly assuming unsafe is one keystroke.
	Replayable bool
}

// Debugger is consulted between steps. Implementations block.
//
// Why an interface rather than a concrete type: the in-process fake used
// throughout the tests and the socket-backed one used by lath share nothing
// but this contract, and the engine must be testable without a socket, a
// terminal, or a filesystem.
type Debugger interface {
	// Plan is called once, before the first step, with the whole sequence.
	Plan(pipeline string, steps []PlanEntry) error
	// Pause is called after each step and blocks until the operator decides.
	//
	// Invariant: an error returned here aborts the run. A debugger that has
	// lost its operator MUST return one rather than inventing ActContinue ,
	// losing supervision is not consent to proceed.
	Pause(p Pause) (Action, error)
	// Finish is called once when the run ends.
	Finish(f Finished) error
}

// Finished describes how a run ended.
//
// A struct for the same reason Pause is one: it is the last thing a client
// renders, and it will grow.
type Finished struct {
	// Err is the run's failure, or nil.
	Err error
	// State is the final state bag, rendered. Carried here because there is
	// no pause after the last step, see Pipeline.Run, so this is the only
	// place a client can learn what the run ended up producing.
	State map[string]string
}

// ReporterSource is an optional upgrade a Debugger may implement to also
// receive the run's human output. The step boundaries and the Detailf lines
// steps write.
//
// Why it exists: a definition builds its own State with its own Reporter, and
// the generated dispatcher cannot reach into that call to add a second one. So
// NewState does it, wrapping the caller's Reporter alongside this one whenever
// a debugger is attached. Without it a client could only learn about steps
// from pauses, which means the last step never reports finishing and no step
// detail is ever visible, and the alternative, making every definition wire
// the reporter itself, is the per-project boilerplate this design exists to
// avoid.
//
// Invariant: the caller's own Reporter is never replaced, only accompanied.
// The terminal a run was started in keeps scrolling exactly as before.
type ReporterSource interface {
	Reporter() Reporter
}

// DebugReporter marks a Reporter as belonging to a debugger.
//
// Implemented by the reporter a Debugger hands out, and checked by NewState so
// a caller who ALSO passes it explicitly does not get every line twice. A
// marker interface rather than an equality check because a Reporter's dynamic
// type need not be comparable. The debug transport's holds a func, and ==
// on two such values panics at run time.
type DebugReporter interface {
	Reporter
	// IsDebugReporter distinguishes this from an ordinary Reporter. It has no
	// behaviour; the method set is the whole signal.
	IsDebugReporter()
}

// bothReporters sends every call to two reporters: the caller's and the
// debugger's.
type bothReporters struct{ a, b Reporter }

func (m bothReporters) StepStart(index, total int, name string) {
	m.a.StepStart(index, total, name)
	m.b.StepStart(index, total, name)
}

func (m bothReporters) StepDone(index, total int, name string, took time.Duration) {
	m.a.StepDone(index, total, name, took)
	m.b.StepDone(index, total, name, took)
}

func (m bothReporters) Detailf(format string, args ...any) {
	m.a.Detailf(format, args...)
	m.b.Detailf(format, args...)
}

// Output forwards to whichever side accepts raw output, falling back to
// Detailf for one that does not. Without this the wrapper would hide the
// OutputReporter both sides may implement, and State.Output would silently
// drop back to Detailf for the whole run.
func (m bothReporters) Output(line string) {
	for _, r := range []Reporter{m.a, m.b} {
		if or, ok := r.(OutputReporter); ok {
			or.Output(line)
			continue
		}
		r.Detailf("%s", line)
	}
}

// Replayable is an optional upgrade a Step may implement to declare that
// running it twice is indistinguishable from running it once.
//
// Why an optional interface rather than a method on Step: every existing step
// keeps compiling, and a step that has never considered the question is
// treated as unsafe. Read-only and compute steps implement it; steps that
// start containers or write files do not, and a debugger confirms before
// replaying them.
type Replayable interface {
	// Replayable reports whether re-running this step is idempotent.
	Replayable() bool
}

// replayable reports whether st declared itself safe to replay.
func replayable(st Step) bool {
	r, ok := st.(Replayable)
	return ok && r.Replayable()
}

// attached holds the process-wide debugger armed by AttachDebugger, or the
// function that produces one on first use.
//
// Guarded by a mutex rather than left bare because a definition may build
// pipelines from more than one goroutine, and a data race here would be
// reported by -race in code the author never wrote.
var attached struct {
	sync.Mutex
	d       Debugger
	open    func() (Debugger, error)
	opened  bool
	openErr error
}

// AttachDebugger arms every State created afterwards in this process.
//
// Why a package-level rather than an argument: NewState is called inside the
// author's own definition, which the generated dispatcher runs before and
// cannot reach into. The alternative, an opt-in argument in every definition,
// fails silently when forgotten, and `--debug` quietly doing nothing is the
// worst failure shape a debugging tool can have.
//
// Invariant: set once at startup, from an explicit flag, by generated code the
// author can read. Never persisted, never read from the environment, cleared
// by process exit. That is deliberately narrower than the hidden state this
// codebase otherwise bans, which is state living on a machine and making the
// same command behave differently without saying so.
//
// Passing nil disarms, which is what tests use to undo themselves.
func AttachDebugger(d Debugger) {
	attached.Lock()
	defer attached.Unlock()
	attached.d, attached.open, attached.opened, attached.openErr = d, nil, d != nil, nil
}

// AttachDebuggerFunc arms a debugger that is not created until the first
// pipeline is built.
//
// Why lazily: producing the debugger means waiting for an operator to attach,
// and doing that at process start makes a run announce "waiting for a
// debugger" before it has checked its own arguments. The first version did
// exactly that. You attached, and were then told the environment name was
// wrong. Deferring it to the first NewState means a target that fails
// validation fails instantly, and the wait happens only when there is
// genuinely a pipeline about to run.
//
// Called at most once. A failure is remembered and returned to every later
// caller rather than retried, because the failure is "nobody attached", and
// asking again would restart the wait in the middle of a run.
func AttachDebuggerFunc(open func() (Debugger, error)) {
	attached.Lock()
	defer attached.Unlock()
	attached.d, attached.open, attached.opened, attached.openErr = nil, open, false, nil
}

// attachedDebugger returns the armed debugger, opening it if needed.
//
// An open failure yields nil rather than an error: NewState cannot fail, and
// the alternative, aborting a run because a debugger could not attach, would
// turn an observation tool into a way to break deploys. The failure is
// reported through DebuggerError instead.
func attachedDebugger() Debugger {
	attached.Lock()
	defer attached.Unlock()
	if attached.opened || attached.open == nil {
		return attached.d
	}
	attached.opened = true
	attached.d, attached.openErr = attached.open()
	return attached.d
}

// DebuggerError reports why an armed debugger could not be opened, or nil.
//
// The generated dispatcher checks it after a run so a session nobody attached
// to exits non-zero and says so, rather than looking like an ordinary run.
func DebuggerError() error {
	attached.Lock()
	defer attached.Unlock()
	return attached.openErr
}

// yielded records that the attached debugger has handed the terminal to the
// run, so a tool may draw on it directly.
var yielded struct {
	sync.Mutex
	on bool
}

// YieldTerminal declares that an attached debugger will not draw while a step
// is running, so the step may own the terminal.
//
// Why this exists: a debugger's panel and a tool's own progress display cannot
// share a screen. docker's BuildKit renders its live table by moving the
// cursor and repainting, twenty thousand escape sequences in one build, and
// those would shred a framed panel drawn around it. So by default an attached
// debugger takes the output as lines and renders it itself.
//
// That costs the operator the display docker was going to give them, which is
// better than anything a panel reproduces. The alternative is to take turns:
// the panel draws only when the run is PAUSED, and while a step runs it stays
// silent and the tool draws instead. Nothing overlaps, because at any moment
// exactly one of them is executing.
//
// Set by the runner when it launched the run into a terminal it owns. It is
// never set when attaching to a run started elsewhere: that run's output goes
// to ITS terminal, not this one, and there is nothing here to yield.
func YieldTerminal() {
	yielded.Lock()
	defer yielded.Unlock()
	yielded.on = true
}

// ResetTerminalYield undoes YieldTerminal.
//
// Exists for tests, which cannot otherwise undo a process-wide setting between
// cases. Exported rather than hidden in a _test file because the tests that
// need it live in other packages. The same reason FakeDebugger is exported.
func ResetTerminalYield() {
	yielded.Lock()
	defer yielded.Unlock()
	yielded.on = false
}

// terminalYielded reports whether the terminal has been handed to the run.
func terminalYielded() bool {
	yielded.Lock()
	defer yielded.Unlock()
	return yielded.on
}

// Debug returns a StateOption attaching d to one State explicitly.
//
// Preferred over AttachDebugger wherever the caller constructs the State
// itself. An embedder, and every test in this package, since a process-wide
// value cannot be used by parallel tests. An explicit debugger overrides the
// attached one.
func Debug(d Debugger) StateOption {
	return func(s *State) { s.debugger = d }
}
