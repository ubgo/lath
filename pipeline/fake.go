package pipeline

import (
	"fmt"
	"sync"
)

// FakeDebugger is a scripted Debugger for tests.
//
// Deliberately NOT in a _test.go file, for the same reason runner.Fake is not:
// every package that builds on the debugger. The socket transport, the step
// adapters, a user's own definition, needs to drive a pipeline through pauses
// without a socket, a terminal or a filesystem, and a test-only type cannot be
// imported.
//
// Give it a script; it answers each pause in turn. Running out of script is a
// deliberate error rather than an implicit ActContinue, because a test whose
// pipeline quietly ran to completion past the end of its script is a test that
// asserts nothing.
type FakeDebugger struct {
	// Script is the sequence of answers, one per pause.
	Script []Action

	mu         sync.Mutex
	pauses     []Pause
	plan       []PlanEntry
	name       string
	finished   bool
	finalErr   error
	finalState map[string]string
	planned    int
}

// Plan records the pipeline's shape.
func (f *FakeDebugger) Plan(pipeline string, steps []PlanEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name, f.plan, f.planned = pipeline, steps, f.planned+1
	return nil
}

// Pause records the pause and returns the next scripted action.
func (f *FakeDebugger) Pause(p Pause) (Action, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, p)
	if len(f.pauses) > len(f.Script) {
		return "", fmt.Errorf("fake debugger: script exhausted after %d pause(s), at step %d (%s)",
			len(f.Script), p.Position, p.Step.Name())
	}
	return f.Script[len(f.pauses)-1], nil
}

// Finish records how the run ended.
func (f *FakeDebugger) Finish(fin Finished) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished, f.finalErr, f.finalState = true, fin.Err, fin.State
	return nil
}

// FinalState returns the state bag as it was when the run ended.
func (f *FakeDebugger) FinalState() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finalState
}

// Pauses returns every pause seen, in order.
func (f *FakeDebugger) Pauses() []Pause {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Pause(nil), f.pauses...)
}

// Steps returns the name of the step that had just run at each pause. The
// value most tests assert on: it is the execution order, replays included.
func (f *FakeDebugger) Steps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.pauses))
	for _, p := range f.pauses {
		out = append(out, p.Step.Name())
	}
	return out
}

// PlannedTimes reports how often Plan was called. Exactly one for any run.
func (f *FakeDebugger) PlannedTimes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.planned
}

// PlanEntries returns the plan handed to Plan.
func (f *FakeDebugger) PlanEntries() []PlanEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PlanEntry(nil), f.plan...)
}

// Finished reports whether Finish was called, and with what.
func (f *FakeDebugger) Finished() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finished, f.finalErr
}
