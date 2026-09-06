module github.com/ubgo/lath/steps

go 1.24

require (
	github.com/ubgo/lath/kit v0.0.0
	github.com/ubgo/lath/pipeline v0.0.0
)

// Unpublished during development. A release replaces these with version
// requirements; the workspace makes them a no-op for local builds.
replace github.com/ubgo/lath/kit => ../kit

replace github.com/ubgo/lath/pipeline => ../pipeline
