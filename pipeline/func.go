package pipeline

import (
	"context"
	"fmt"
)

// Func is a Step whose behaviour is supplied inline as a closure.
//
// Why it exists: defining a named type with four methods is the right shape for
// a step that will be reused, but it is heavy ceremony for the one-off, a
// project-specific notification, a temporary workaround, a check that exists
// for one deploy. Without an inline form, authors either write forty lines of
// boilerplate for a three-line action, or worse, wedge the logic into a
// neighbouring step where nobody will find it.
//
// It carries exactly the same obligations as any other Step. In particular
// Needs and Gives are NOT optional bookkeeping: Validate uses them to prove the
// pipeline is wired, so a closure that reads a key without declaring it defeats
// that check for the whole pipeline, not just for itself.
//
// The fields are named Label/Needs/Gives/Do rather than Name/Requires/Provides/
// Run because Go forbids a field and a method sharing an identifier on one
// type, and the interface methods must keep their names.
//
//	pipeline.Func{
//	    Label: "warm-cache",
//	    Needs: []pipeline.Key{steps.KeyContainers},
//	    Do: func(ctx context.Context, s *pipeline.State) error {
//	        return warmTheCache(ctx)
//	    },
//	}
type Func struct {
	// Label names the step in plan output and error messages. Required.
	Label string
	// Needs lists the state keys Do reads. MUST be complete.
	Needs []Key
	// Gives lists the state keys Do sets. MUST be complete.
	Gives []Key
	// Do is the work. Required.
	//
	// It receives the same ctx and State as any step, so it must honour
	// cancellation for blocking work and suppress side effects under
	// State.DryRun while still setting every key listed in Gives.
	Do func(ctx context.Context, s *State) error
	// Summary is what this step is FOR, one line, the same every run, exactly
	// like a CLI command's short help. Optional.
	Summary string
	// Detail is what this step will do THIS run, with the values filled in.
	// Optional.
	//
	// Neither may contain a credential: both are printed in a plan. See Docs.
	Detail string
	// Idempotent declares that running Do twice is indistinguishable from
	// running it once, which lets a debugger replay this step without asking.
	//
	// Named Idempotent rather than Replayable because Go forbids a field and a
	// method sharing a name, and the method is what satisfies the interface ,
	// the same reason Label/Needs/Gives/Do are not called
	// Name/Requires/Provides/Run.
	//
	// Defaults to false, which is the safe answer: an inline step that has not
	// thought about the question is assumed to have side effects.
	Idempotent bool
}

// Name reports the step's label.
func (f Func) Name() string {
	// A missing label would render as a blank line in plan output and produce
	// an unattributable failure. Validate rejects it before a run, so this
	// fallback only shows up if someone calls Name on an unvalidated Func.
	if f.Label == "" {
		return "(unnamed func step)"
	}
	return f.Label
}

// Requires reports the keys Do reads, as declared in Needs.
func (f Func) Requires() []Key { return f.Needs }

// Provides reports the keys Do sets, as declared in Gives.
func (f Func) Provides() []Key { return f.Gives }

// Replayable reports Idempotent, implementing the optional interface a
// debugger consults before replaying a step without confirmation.
func (f Func) Replayable() bool { return f.Idempotent }

// Validate implements Validator, so a Func missing its label or its closure is
// rejected when the pipeline is checked rather than panicking mid-run. A nil
// Do would otherwise be a nil-pointer dereference at the exact moment a deploy
// is halfway through, which is the worst available time to discover it.
func (f Func) Validate() error {
	if f.Label == "" {
		return fmt.Errorf("func step: Label is required")
	}
	if f.Do == nil {
		return fmt.Errorf("func step %q: Do is nil", f.Label)
	}
	return nil
}

// Run invokes the closure.
func (f Func) Run(ctx context.Context, s *State) error {
	if f.Do == nil {
		// Unreachable for a validated pipeline; kept so a Func executed
		// directly in a test fails with a message instead of a panic.
		return fmt.Errorf("func step %q: Do is nil", f.Name())
	}
	return f.Do(ctx, s)
}

// Docs reports Summary and Detail, so an inline step explains itself in a plan
// exactly as a named one does. See Documented.
func (f Func) Docs() Docs { return Docs{Summary: f.Summary, Detail: f.Detail} }
