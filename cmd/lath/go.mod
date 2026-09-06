module github.com/ubgo/lath/cmd/lath

go 1.24

// The runner links none of the deploy libraries — it compiles a definition and
// execs it. These two are the exception, and only for the step debugger:
// kit/session advertises a run so `lath tui` can find it, and pipeline/debug
// is the wire contract both ends must agree on.
//
// The pipeline side stays stdlib-only precisely so a definition's own go.mod
// never grows a dependency to be debuggable; the cost lands here, on the
// binary the user installs, instead.
require (
	github.com/ubgo/lath/kit v0.0.0
	github.com/ubgo/lath/pipeline v0.0.0
)

// Unpublished during development. A release replaces these with version
// requirements; the workspace makes them a no-op for local builds.
replace github.com/ubgo/lath/kit => ../../kit

replace github.com/ubgo/lath/pipeline => ../../pipeline
