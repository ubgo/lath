// Package steps adapts operations to the pipeline.
//
// Every type here is a thin wrapper: it reads what it needs from pipeline
// state, honours dry run, reports progress, and delegates the actual work to a
// package that knows how to do it, docker, git, or the kit.
//
// The split is deliberate and was arrived at the hard way. This package once
// held the docker command lines, the git invocations, the ssh file writing and
// an HTTP proxy client, which made it a package about four unrelated subjects
// and made none of that work usable without a pipeline. Now docker/ and git/
// are ordinary Go packages any program can import, and what remains here is
// only the wiring.
//
// The test for whether something belongs here: does it mention Requires,
// Provides, DryRun or Detailf? If not, it belongs in the package that owns the
// subject.
package common
