package pipeline

import (
	"fmt"
	"io"
	"os"
)

// Mode selects how far a run goes. It is a defined type with a fixed value set
// rather than a bool, because "dry run" is not the only non-executing mode we
// expect (a future "explain" or "diff" mode is the obvious next one) and a bool
// parameter at a call site reads as `NewState(true)`, which says nothing.
type Mode string

const (
	// ModeExecute performs real side effects.
	ModeExecute Mode = "execute"
	// ModeDryRun runs every step's logic but suppresses side effects. Steps
	// MUST honour it, see State.DryRun for the contract.
	ModeDryRun Mode = "dry-run"
)

// ModeValues is the canonical iteration order for Mode. Any UI listing modes,
// any flag-validation loop, and any test matrix reads from here so the set
// cannot drift between the place that parses a mode and the place that uses it.
var ModeValues = []Mode{ModeExecute, ModeDryRun}

// Valid reports whether m is a declared mode. Callers parsing user input MUST
// check this rather than casting, otherwise an unrecognised string becomes a
// mode that every switch silently treats as "not dry run", the dangerous
// default.
func (m Mode) Valid() bool {
	for _, v := range ModeValues {
		if m == v {
			return true
		}
	}
	return false
}

// State carries values between steps and the output sink they report through.
//
// Why a bag rather than typed function chaining: Go cannot express a
// heterogeneous typed sequence (Step[A,B] then Step[B,C] then …) without either
// variadic generics, which the language lacks, or nesting so deep the
// definition becomes unreadable. The chosen trade is a runtime-typed bag whose
// wiring is proven at construction time by Pipeline.Validate, so the guarantee
// arrives before execution even though it is not the compiler's.
//
// Invariant: not safe for concurrent use. Steps run sequentially by design; if
// a step fans out internally it must not share State across goroutines.
type State struct {
	values map[Key]any
	// planWriter receives a forced plan listing; nil means stderr. See
	// PlanWriter and ModeEnvVar.
	planWriter io.Writer
	mode       Mode
	reporter   Reporter
	debugger   Debugger
}

// StateOption adjusts a State at construction.
//
// Variadic rather than extra parameters so every existing NewState call site
// compiles unchanged, and so a future option costs no signature change.
type StateOption func(*State)

// NewState returns a State for the given mode, reporting through r.
//
// A nil Reporter is replaced with a discarding one rather than rejected: output
// plumbing must never be the reason a deploy aborts.
func NewState(mode Mode, r Reporter, opts ...StateOption) *State {
	if !mode.Valid() {
		mode = ModeExecute
	}
	// The runner can force a rehearsal, so `lath plan` and `lath dry-run`
	// work on a definition whose author never offered the option. It can only
	// ever make a run SAFER; see applyForcedMode.
	mode = applyForcedMode(mode)
	if r == nil {
		r = discardReporter{}
	}
	// The process-wide debugger is the default so a definition needs no
	// changes to be steppable; an explicit Debug option overrides it, which is
	// what tests and embedders use. See AttachDebugger.
	s := &State{values: make(map[Key]any), mode: mode, reporter: r, debugger: attachedDebugger()}
	for _, opt := range opts {
		opt(s)
	}
	// An attached debugger that can also receive output is given it, without
	// displacing the caller's. See ReporterSource for why this happens here
	// rather than in the definition.
	if src, ok := s.debugger.(ReporterSource); ok {
		// Skipped when the caller already passed the debugger's reporter,
		// which would otherwise send every line twice.
		if _, already := s.reporter.(DebugReporter); !already {
			if dr := src.Reporter(); dr != nil {
				s.reporter = bothReporters{a: s.reporter, b: dr}
			}
		}
	}
	return s
}

// Display renders the state bag for a human or a debugger.
//
// A rendered copy rather than the live map, and rendered through fmt
// specifically: a value that redacts itself, secret.Value closes String,
// GoString, Format, MarshalJSON, MarshalText and LogValue, cannot leak its
// plaintext through this path. Handing out the raw map would let any consumer
// type-assert its way past that containment.
func (s *State) Display() map[string]string {
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[string(k)] = fmt.Sprintf("%v", v)
	}
	return out
}

// snapshot copies the value map, so a rerun can restore the inputs a step saw.
//
// Shallow, and that limit is real: a step that MUTATES a value it read ,
// appending to a slice already in state, is not undone by a restore. Steps
// are expected to Set new values rather than edit ones in place, which every
// step in this repository does, but a debugger cannot enforce it.
func (s *State) snapshot() map[Key]any {
	out := make(map[Key]any, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

// restore replaces the value map with a snapshot.
func (s *State) restore(snap map[Key]any) {
	s.values = snap
}

// Mode reports the run mode.
func (s *State) Mode() Mode { return s.mode }

// DryRun reports whether side effects must be suppressed.
//
// Contract for step authors: a step MUST still compute and Set every key it
// declares in Provides, even under dry run. Skipping the Set would make every
// downstream step fail with ErrKeyMissing, so a dry run would exercise a
// different code path than the real one, which defeats the purpose.
func (s *State) DryRun() bool { return s.mode == ModeDryRun }

// Detailf emits a description of what the current step is doing.
func (s *State) Detailf(format string, args ...any) { s.reporter.Detailf(format, args...) }

// Set stores a typed value under k. Later writes to the same key overwrite;
// Pipeline.Validate rejects pipelines where two steps declare the same
// Provides, so an overwrite indicates a step writing a key it never declared.
func Set[T any](s *State, k Key, v T) { s.values[k] = v }

// Get reads a typed value.
//
// Why it errors on absence instead of returning the zero value: a zero value
// here is an empty image tag or an empty host, which does not fail at the read
// it fails several steps later as an inexplicable "docker: invalid
// reference", far from the cause. Failing at the read names the missing key.
func Get[T any](s *State, k Key) (T, error) {
	var zero T
	raw, ok := s.values[k]
	if !ok {
		return zero, fmt.Errorf("get %s: %w", k, ErrKeyMissing)
	}
	typed, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("get %s: holds %T, want %T: %w", k, raw, zero, ErrKeyType)
	}
	return typed, nil
}

// Has reports whether k has been provided. For steps with genuinely optional
// inputs, which, note, must NOT appear in Requires, or Validate will demand a
// provider for them.
func Has(s *State, k Key) bool {
	_, ok := s.values[k]
	return ok
}

// Output returns a writer that turns a command's output into reporter lines.
//
// The seam that makes a long step debuggable. A step hands this to whatever it
// runs, and every line the tool prints reaches the terminal and any attached
// debugger as it happens, instead of being swallowed and summarised. Before
// this existed a two-minute docker build printed nothing at all, which is
// indistinguishable from a hang and impossible to debug from.
//
// Falls back to Detailf when the Reporter does not implement OutputReporter,
// so output is never silently dropped, only rendered less precisely.
//
// Invariant: the caller MUST call Close (or defer it) so a final line without
// a trailing newline is not lost. That is not a rare case: it is exactly how a
// progress bar's last frame and an unterminated prompt arrive.
func (s *State) Output() *OutputWriter {
	emit := func(line string) { s.reporter.Detailf("%s", line) }
	if or, ok := s.reporter.(OutputReporter); ok {
		emit = or.Output
	}
	// Note the Detailf fallback passes the line as an ARGUMENT, never as the
	// format: a docker line containing a percent sign would otherwise render
	// as "%!d(MISSING)" and the real content would be lost.
	return &OutputWriter{w: &lineWriter{fn: emit}}
}

// OutputWriter carries a command's output into a run's report.
type OutputWriter struct{ w *lineWriter }

func (o *OutputWriter) Write(p []byte) (int, error) { return o.w.Write(p) }

// Close flushes a trailing partial line. Safe to call more than once.
func (o *OutputWriter) Close() error {
	o.w.Flush()
	return nil
}

// Passthrough reports the terminal this run's output goes to, or nil.
//
// Why it exists: a tool renders far better output when it can see a terminal.
// docker's BuildKit draws a live, self-updating progress table on a TTY and
// falls back to a flat line-per-event log on a pipe, and lath was always
// giving it a pipe, so its own build output looked strictly worse than running
// the same command by hand.
//
// The cause is subtle and worth stating: os/exec connects a child's stdout
// DIRECTLY to the terminal's file descriptor when Stdout is an *os.File, but
// wraps it in a pipe for any other io.Writer. Capturing output means wrapping
// it in a MultiWriter, which is another io.Writer, so the mere act of keeping
// a copy of the output is what downgrades it.
//
// Returns nil, meaning "capture and stream instead", whenever anything needs
// to see the output as lines rather than as bytes on a terminal:
//
//   - a debugger is attached and has NOT yielded the terminal, because a
//     panel would be corrupted by a tool redrawing itself with
//     cursor-movement escapes, see YieldTerminal for when it does yield;
//   - the reporter is not a plain TextReporter, so something is transforming
//     the output;
//   - the destination is not a character device, a pipe, a file, CI.
func (s *State) Passthrough() *os.File {
	if s.debugger != nil && !terminalYielded() {
		return nil
	}
	f, ok := terminalOf(s.reporter)
	if !ok {
		return nil
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return f
}

// terminalOf finds the file a Reporter ultimately writes to.
//
// It has to see THROUGH the wrapper NewState installs for an attached
// debugger, which is the reason this is a function rather than a type
// assertion: arming a debugger replaces the caller's *TextReporter with a
// bothReporters that fans out to it, so the assertion that used to find the
// terminal silently stopped finding it, and the effect was that a run under
// the panel could never hand a tool the screen, however explicitly it had been
// told to.
func terminalOf(r Reporter) (*os.File, bool) {
	switch t := r.(type) {
	case *TextReporter:
		f, ok := t.W.(*os.File)
		return f, ok
	case bothReporters:
		// The caller's own reporter is the one aimed at the terminal; the
		// debugger's is aimed at a socket.
		return terminalOf(t.a)
	}
	return nil, false
}
