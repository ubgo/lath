module github.com/ubgo/lath/steps

go 1.24

require (
	github.com/ubgo/lath/kit v0.1.0
	github.com/ubgo/lath/pipeline v0.1.0
)

// No replace directives, deliberately. Go refuses to `go install` a module
// whose go.mod carries one, so a replace here would make this module — and
// anything depending on it — uninstallable from the proxy. Local development
// does not need them: go.work already points every one of these at the
// checkout, which is what a workspace is for.
