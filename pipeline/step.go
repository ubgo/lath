package pipeline

import "context"

// Step is one unit of work in a pipeline.
//
// Why Requires and Provides are part of the interface rather than left to each
// step to sort out at run time: they let Validate prove the ordering is
// possible BEFORE anything executes. Without them, a step reading a value
// nothing produced fails when it runs. Which for a deploy can mean after the
// old containers have already been stopped. Declaring the data dependencies is
// what moves that entire class of failure to before step one.
//
// Contract for implementers, all four load-bearing:
//
//  1. Requires MUST list every key Run reads. Under-reporting defeats Validate
//     and reintroduces the failure mode above.
//  2. Provides MUST list every key Run sets, and no two steps in one pipeline
//     may provide the same key.
//  3. Run MUST honour State.DryRun by suppressing side effects while still
//     setting every declared Provides key (see State.DryRun).
//  4. Run MUST respect ctx cancellation for any blocking work. The engine
//     checks ctx between steps, but it cannot interrupt one that is already
//     running.
//
// Steps are values, not functions, so their configuration is typed struct
// fields the compiler checks. A misspelled field or a string where a Duration
// belongs fails the build rather than the deploy.
type Step interface {
	// Name identifies the step in output and errors. Stable across runs;
	// used by operators to talk about which step failed.
	Name() string
	// Requires lists the state keys Run reads.
	Requires() []Key
	// Provides lists the state keys Run sets.
	Provides() []Key
	// Run performs the work.
	Run(ctx context.Context, s *State) error
}
