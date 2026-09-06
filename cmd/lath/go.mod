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
	github.com/ubgo/lath/kit v0.1.0
	github.com/ubgo/lath/pipeline v0.1.0
)

// No replace directives, deliberately, and this module is the reason the rule
// matters most: Go refuses to `go install` a module whose go.mod carries one,
// so a replace here made `go install github.com/ubgo/lath/cmd/lath@latest`
// fail outright — the tool was public and uninstallable. go.work points these
// at the local checkout for development, which is what a workspace is for.
