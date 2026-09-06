package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ModeEnvVar lets the RUNNER override what a definition asked for.
//
// It exists because "what would this do?" is a question about someone else's
// code. A definition decides its own mode, normally from its own flags, and a
// reader who wants the steps listed should not have to find that flag, or
// depend on the author having written a plan target at all.
//
// The runner sets this; a definition never reads it.
const ModeEnvVar = "LATH_MODE"

// PlanFormatEnvVar selects how a forced plan is rendered.
//
// Separate from ModeEnvVar because it answers a different question, how to
// print rather than whether to run, and because a caller that wants machine
// output should not have to restate the mode to get it.
const PlanFormatEnvVar = "LATH_PLAN_FORMAT"

// Values PlanFormatEnvVar accepts. Anything else, including empty, is text.
const (
	// PlanFormatText is the default: two lines a person can read.
	PlanFormatText = "text"
	// PlanFormatJSON emits the same PlanEntry values a client would get from
	// Plan(), so a script sees exactly what the listing showed.
	PlanFormatJSON = "json"
)

// Values ModeEnvVar accepts.
const (
	// ModeEnvPlan lists the steps and executes none of them.
	ModeEnvPlan = "plan"
	// ModeEnvDryRun forces every pipeline into ModeDryRun, whatever the
	// definition asked for.
	ModeEnvDryRun = "dry-run"
)

// PlanWriter sends a forced plan somewhere other than stderr.
//
// For a caller embedding the engine, and for tests, which need to read what
// was listed.
func PlanWriter(w io.Writer) StateOption {
	return func(s *State) { s.planWriter = w }
}

// forcedMode reports the mode the runner is imposing, or "".
func forcedMode() string { return strings.TrimSpace(os.Getenv(ModeEnvVar)) }

// planning reports whether steps must be listed rather than run.
func planning() bool { return forcedMode() == ModeEnvPlan }

// Planning reports that the caller only wants the pipeline DESCRIBED.
//
// A definition may consult this to skip work that assembling a pipeline would
// otherwise demand: credentials that must be present to deploy, a network
// lookup, an expensive checksum. Nothing that decides the SHAPE of the
// pipeline should be skipped, or the plan describes something other than what
// would run.
//
// Without it, `lath plan` is defeated by any definition that refuses to
// assemble until it is fully configured, which is exactly the definition
// someone reaches for a plan to understand.
//
// The mode itself is set by the runner; this is the only part of it a
// definition has any business reading.
func Planning() bool { return planning() }

// applyForcedMode downgrades a caller's mode when the runner demands it.
//
// One direction only: this can turn an execute into a dry run, never the
// reverse. A runner flag that could turn someone's rehearsal into a real
// deploy would be a footgun with the safety filed off.
func applyForcedMode(mode Mode) Mode {
	switch forcedMode() {
	case ModeEnvPlan, ModeEnvDryRun:
		return ModeDryRun
	}
	return mode
}

// printPlan lists what would run, without running it.
//
// Reports the same information Plan returns, in the same order, because they
// are the same answer: this is the runner asking a pipeline to describe itself
// when the definition offers no way to ask.
func printPlan(p Pipeline, planWriter io.Writer) {
	if strings.TrimSpace(os.Getenv(PlanFormatEnvVar)) == PlanFormatJSON {
		// Machine output goes to STDOUT, where a pipe expects it, while the
		// human listing stays on stderr so it does not mix with whatever the
		// definition itself prints. A caller that named a writer gets it
		// either way; that is what tests use.
		w := planWriter
		if w == nil {
			w = os.Stdout
		}
		printPlanJSON(p, w)
		return
	}
	if planWriter == nil {
		// Resolved here rather than captured in a package variable: os.Stderr
		// is a variable itself, and a test, or an embedder, that swaps it
		// expects the swap to take effect. Stderr rather than stdout so the
		// listing never mixes with what the definition prints.
		planWriter = os.Stderr
	}
	name := p.Name
	if name == "" {
		name = "pipeline"
	}
	fmt.Fprintf(planWriter, "\n%s: %d step(s), nothing executed\n\n", name, len(p.Steps))
	for _, e := range p.Plan() {
		// Two lines at most, laid out like a CLI's own help: what the step is
		// called and what it is for, then what it will actually do this time.
		// The keys ride on the second line rather than the first, where they
		// would push the summary off the edge of a terminal.
		fmt.Fprintf(planWriter, "  %2d/%d  %-20s %s\n", e.Position, e.Total, e.Name, e.Summary)

		var trailer []string
		if e.Detail != "" {
			trailer = append(trailer, e.Detail)
		}
		if len(e.Requires) > 0 {
			trailer = append(trailer, fmt.Sprintf("reads=%v", e.Requires))
		}
		if len(e.Provides) > 0 {
			trailer = append(trailer, fmt.Sprintf("writes=%v", e.Provides))
		}
		if len(trailer) > 0 {
			fmt.Fprintf(planWriter, "         %s\n", strings.Join(trailer, "  ·  "))
		}
	}

	fmt.Fprintf(planWriter, "\nlisted because %s=%s. Run it without that to perform it.\n",
		ModeEnvVar, ModeEnvPlan)
}

// planJSON is the machine rendering of a plan.
//
// The steps are the same PlanEntry values Plan() returns, so a script and a
// reader are looking at one answer rather than two renderings that can
// disagree.
type planJSON struct {
	Pipeline string      `json:"pipeline"`
	Steps    []PlanEntry `json:"steps"`
}

// printPlanJSON writes the plan as JSON.
//
// Errors are reported rather than swallowed: a script parsing this needs to
// know it got nothing, and stdout that is silently empty looks like a pipeline
// with no steps.
func printPlanJSON(p Pipeline, w io.Writer) {
	name := p.Name
	if name == "" {
		name = "pipeline"
	}
	out, err := json.MarshalIndent(planJSON{Pipeline: name, Steps: p.Plan()}, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintln(w, string(out))
}
