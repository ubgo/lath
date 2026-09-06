package pipeline

import "errors"

// Sentinel errors for the conditions a caller may reasonably branch on.
//
// Why sentinels rather than error strings: the runner distinguishes "your
// pipeline is mis-wired" (a definition bug, fix the file) from "a step failed"
// (an operational failure, retry may help) and exits differently. Matching on
// message text would break the moment a message is reworded.
//
// Every error returned from this package wraps exactly one of these, so
// errors.Is is always the correct check.
var (
	// ErrKeyMissing means a step read a State key nothing had provided.
	// Pipeline.Validate makes this unreachable for a validated pipeline, so
	// encountering it at run time indicates a Step whose Requires() under-
	// reports what Run() actually reads.
	ErrKeyMissing = errors.New("pipeline: state key not provided")

	// ErrKeyType means a State key held a different type than the reader
	// expected. Indicates two steps disagreeing about a key's contract.
	ErrKeyType = errors.New("pipeline: state key holds unexpected type")

	// ErrNotWired means a step requires a key no earlier step provides. This
	// is the check that turns a whole class of 2am failures into a message
	// printed before any step executes.
	ErrNotWired = errors.New("pipeline: step requires a key no earlier step provides")

	// ErrDuplicateProvider means two steps both claim to provide one key.
	// Banned because it makes the value a step reads depend on ordering that
	// nothing enforces. The second write silently wins.
	ErrDuplicateProvider = errors.New("pipeline: key provided more than once")

	// ErrStepFailed wraps any error returned by a Step's Run method, so a
	// caller can tell step failure apart from a wiring or state error without
	// inspecting message text.
	ErrStepFailed = errors.New("pipeline: step failed")

	// ErrStepConfig means a step's own configuration is invalid, a missing
	// required field, a value outside a closed set, a nonsensical duration.
	// Distinct from ErrNotWired (the pipeline's shape is wrong) and from
	// ErrStepFailed (the work itself failed): this one means the definition
	// file is wrong, and no amount of retrying will help.
	ErrStepConfig = errors.New("pipeline: step configuration is invalid")

	// ErrAborted means a debugger's operator abandoned the run, or the
	// debugger lost its client. Distinct from ErrStepFailed: nothing failed,
	// a person decided to stop. Steps already performed are NOT undone, which
	// is why the message names the step it stopped at.
	ErrAborted = errors.New("pipeline: run abandoned")

	// ErrNoSteps means a Pipeline was constructed with an empty step list.
	// Treated as an error rather than a no-op success because "ran fine,
	// changed nothing" is indistinguishable from a real deploy in a log.
	ErrNoSteps = errors.New("pipeline: no steps")
)
