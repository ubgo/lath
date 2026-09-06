package pipeline

// Validator is an OPTIONAL interface a Step may implement to check its own
// configuration.
//
// Why optional rather than part of Step: not every step has configuration to
// check, and forcing an empty method on those would be noise. Steps that do
// have configuration gain a real benefit, Pipeline.Validate calls this, so a
// bad Dockerfile path, a zero timeout, or an undeclared transfer method is
// reported by `plan`, before anything is built or stopped.
//
// The division of labour, which matters:
//
//   - Validate checks what the AUTHOR wrote, fields on the step. It runs with
//     no State and no context, so it can be called any number of times, in any
//     order, without side effects.
//   - Run checks what the PIPELINE produced, values read from State. Those
//     cannot be known before execution.
//
// A check that belongs in Validate but sits in Run still works; it just fires
// later than it needed to, which for a deploy can mean after the previous
// version was already stopped.
type Validator interface {
	// Validate reports a configuration fault, or nil.
	Validate() error
}
