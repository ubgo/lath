package pipeline

import (
	"context"
	"fmt"
	"time"
)

// Pipeline is an ordered sequence of steps.
//
// The order is the author's; Validate only confirms it is POSSIBLE, that
// every consumer follows its producer, never that it is optimal. Deciding
// that migrations run before processes start is a judgement the tool does not
// second-guess.
type Pipeline struct {
	// Name identifies the pipeline in errors and output.
	Name string
	// Steps run in slice order.
	Steps []Step
}

// Validate reports the first wiring fault: a step requiring a key no earlier
// step provides, or two steps providing the same key.
//
// Why this is a separate method rather than a check inside Run: `plan` calls it
// without executing anything, and a test calls it over every pipeline in a repo
// which is the guarantee that stands in for the compile-time step chaining Go
// cannot express. Keeping it side-effect-free is what makes both possible.
//
// It also invokes each step's own Validate when the step implements Validator,
// so a configuration fault is reported here rather than mid-run.
//
// Invariant: returns an error wrapping ErrStepConfig, ErrNotWired,
// ErrDuplicateProvider, or ErrNoSteps, never a bare error. Callers branch with
// errors.Is.
func (p Pipeline) Validate() error {
	if len(p.Steps) == 0 {
		return fmt.Errorf("pipeline %q: %w", p.Name, ErrNoSteps)
	}
	// providerOf maps an already-provided key to the step that provides it, so
	// a duplicate can name BOTH steps, "provided by X" is what makes the
	// message actionable rather than merely correct.
	providerOf := make(map[Key]string, len(p.Steps))
	for i, st := range p.Steps {
		position := i + 1 // 1-based: the message is read by a human counting steps

		// Configuration is checked before wiring, on purpose: a step with a nil
		// closure or an empty required field is a more fundamental fault than a
		// missing input, and reporting the input first would send the author
		// looking in the wrong place.
		if v, ok := st.(Validator); ok {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("pipeline %q: step %d (%s): %w: %w",
					p.Name, position, st.Name(), ErrStepConfig, err)
			}
		}

		for _, need := range st.Requires() {
			if _, ok := providerOf[need]; !ok {
				return fmt.Errorf("pipeline %q: step %d (%s) requires %q: %w",
					p.Name, position, st.Name(), need, ErrNotWired)
			}
		}
		for _, out := range st.Provides() {
			if prev, dup := providerOf[out]; dup {
				return fmt.Errorf("pipeline %q: step %d (%s) provides %q, already provided by %s: %w",
					p.Name, position, st.Name(), out, prev, ErrDuplicateProvider)
			}
			providerOf[out] = st.Name()
		}
	}
	return nil
}

// PlanEntry is one step as it appears in a plan: what it is and what data it
// moves. Returned as structured values rather than pre-formatted lines so a
// caller can render text, JSON, or a table without parsing strings back apart.
type PlanEntry struct {
	Position int    `json:"position"` // 1-based
	Total    int    `json:"total"`
	Name     string `json:"name"`
	Requires []Key  `json:"requires,omitempty"`
	Provides []Key  `json:"provides,omitempty"`
	// Summary is what this KIND of step is for, one line, the same on every
	// run. Empty when the step documents nothing.
	Summary string `json:"summary,omitempty"`
	// Detail is what this step will do THIS run, with the values filled in.
	Detail string `json:"detail,omitempty"`
}

// Docs is what a step says about itself.
//
// Two fields because they answer different questions, and a plan wants both:
//
//   - Summary is the step's purpose, fixed and identical on every run, exactly
//     like a CLI command's one-line help. "Points this environment's hostname
//     at the deploy target."
//   - Detail is what will happen THIS time, with the values resolved.
//     "cloudflare: A api.example.com -> 203.0.113.7".
//
// A name alone gives neither: a reader seeing "ensure-dns" cannot tell what it
// is for, nor whether it means Cloudflare, Route 53 or a hosts file, which is
// precisely what they opened a plan to find out.
type Docs struct {
	Summary string
	Detail  string
}

// Documented is the optional interface a step implements to explain itself.
//
// Optional because most steps are fully explained by their name and the keys
// they read and write. Implement it where the name leaves a real question.
//
// Both fields are rendered in a plan, so NEITHER may contain a credential.
type Documented interface {
	Docs() Docs
}

// docsFor returns a step's own documentation, or the zero Docs.
func docsFor(st Step) Docs {
	d, ok := st.(Documented)
	if !ok {
		return Docs{}
	}
	return d.Docs()
}

// Plan describes the pipeline without executing anything.
//
// This is the capability that justifies steps being data instead of statements
// in a function body: a function cannot list what it is about to do.
func (p Pipeline) Plan() []PlanEntry {
	out := make([]PlanEntry, 0, len(p.Steps))
	for i, st := range p.Steps {
		out = append(out, PlanEntry{
			Position: i + 1,
			Total:    len(p.Steps),
			Name:     st.Name(),
			Requires: st.Requires(),
			Provides: st.Provides(),
			Summary:  docsFor(st).Summary,
			Detail:   docsFor(st).Detail,
		})
	}
	return out
}

// Run validates, then executes every step in order.
//
// Fail-fast is deliberate and load-bearing: a failed health check must never be
// followed by a traffic switch. There is no continue-on-error option for that
// reason. A partial deploy that keeps going is worse than one that stops.
//
// Invariant: returns an error wrapping ErrStepFailed for a step failure, or one
// of the wiring sentinels for a definition fault, or ctx.Err() on cancellation.
func (p Pipeline) Run(ctx context.Context, s *State) error {
	if err := p.Validate(); err != nil {
		// Returned unwrapped on purpose: Validate's error already names the
		// pipeline, the step and its position. Adding another "pipeline %q:"
		// prefix here would duplicate it in the one message an operator reads.
		return err
	}

	// The runner asked for a listing rather than a run. Validate still ran
	// above, so a plan reports a mis-wired pipeline instead of describing one
	// that could never work. See ModeEnvVar.
	if planning() {
		printPlan(p, s.planWriter)
		return nil
	}

	// Captured once: ActContinue disarms stepping for the rest of the run by
	// clearing this local, which must not affect the State a caller may reuse.
	dbg := s.debugger
	if dbg != nil {
		if err := dbg.Plan(p.Name, p.Plan()); err != nil {
			return fmt.Errorf("pipeline %q: debugger: %w", p.Name, err)
		}
	}

	err := p.run(ctx, s, dbg)
	if dbg != nil {
		// Reported even when the run failed, so a client always learns how it
		// ended rather than inferring it from a closed connection. Its own
		// error never replaces the run's: a debugger that cannot say goodbye
		// has not changed what happened.
		_ = dbg.Finish(Finished{Err: err, State: s.Display()})
	}
	return err
}

// run walks the steps, consulting dbg between them when one is attached.
//
// Split from Run so the Finish call above has exactly one return to wrap,
// rather than being repeated at every exit.
func (p Pipeline) run(ctx context.Context, s *State, dbg Debugger) error {
	total := len(p.Steps)
	// Whether a SUCCESSFUL step pauses. ActContinue clears it; a failure
	// ignores it entirely.
	pauseOnSuccess := true
	for i := 0; i < total; i++ {
		st := p.Steps[i]
		// Checked between steps rather than only at the top: a cancelled
		// context must stop the pipeline at the next boundary, not run to
		// completion because the signal arrived after the loop started.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("pipeline %q: cancelled before step %d (%s): %w",
				p.Name, i+1, st.Name(), err)
		}
		position := i + 1

		// Taken before the step runs, so ActRerun replays it against the
		// inputs it actually saw rather than the ones it left behind.
		var before map[Key]any
		if dbg != nil {
			before = s.snapshot()
		}

		s.reporter.StepStart(position, total, st.Name())
		started := time.Now()
		runErr := st.Run(ctx, s)

		failed := func() error {
			return fmt.Errorf("pipeline %q: step %d (%s): %w: %w",
				p.Name, position, st.Name(), ErrStepFailed, runErr)
		}
		if runErr == nil {
			s.reporter.StepDone(position, total, st.Name(), time.Since(started))
		}
		last := position == total
		// No pause after the last step SUCCEEDS: there is nothing left to
		// decide, and offering the choice would let an operator turn a
		// completed run into an abandoned one by closing a window. A failure
		// still pauses, wherever it happens, because retrying it is the whole
		// reason for pausing on failures at all.
		//
		// ActContinue stops the pauses between SUCCESSFUL steps only. It used
		// to discard the debugger outright, which meant "carry on" silently
		// also meant "and do not stop if this breaks" — surrendering the retry
		// for the rest of the run at the moment it becomes most valuable.
		if dbg == nil || (last && runErr == nil) || (!pauseOnSuccess && runErr == nil) {
			// Unchanged behaviour: without a debugger a failure ends the run
			// exactly as it always has.
			if runErr != nil {
				return failed()
			}
			continue
		}

		// BLOCKS until the operator decides. Nothing to read means nothing to
		// run: the loop physically cannot advance past this call.
		act, err := dbg.Pause(Pause{
			Position:   position,
			Total:      total,
			Step:       st,
			Next:       p.nameAt(i + 1),
			Err:        runErr,
			State:      s.Display(),
			Replayable: replayable(st),
		})
		if err != nil {
			return fmt.Errorf("pipeline %q: debugger: %w: %w", p.Name, ErrAborted, err)
		}
		switch act {
		case ActNext:
			if runErr != nil {
				// Unreachable through a well-behaved client, which must not
				// offer "next" when the step it would advance past failed.
				// Treated as giving up rather than as an error, since that is
				// the only thing it could mean.
				return failed()
			}
		case ActRerun:
			s.restore(before)
			i-- // the same index next iteration
		case ActContinue:
			pauseOnSuccess = false // stop asking between steps that work
			if runErr != nil {
				return failed()
			}
		case ActQuit:
			if runErr != nil {
				// The run failed because the STEP failed; the operator only
				// declined to retry it. Reporting this as "abandoned" would
				// hide the actual cause behind the choice not to fix it.
				return failed()
			}
			return fmt.Errorf("pipeline %q: %w at step %d (%s)",
				p.Name, ErrAborted, position, st.Name())
		default:
			// Unreachable for a validated Action, and a returned error rather
			// than a fallthrough because the fallthrough would be "keep
			// going". The one outcome an operator did not ask for.
			return fmt.Errorf("pipeline %q: debugger returned %q: %w",
				p.Name, act, ErrAborted)
		}
	}
	return nil
}

// nameAt returns the name of the step at i, or empty past the end.
func (p Pipeline) nameAt(i int) string {
	if i < 0 || i >= len(p.Steps) {
		return ""
	}
	return p.Steps[i].Name()
}
