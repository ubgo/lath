// Package steps is a directory, not a package, see the subdirectories.
//
// # These step packages are not privileged
//
// lath ships common, docker and git the same way anyone else would ship
// theirs: a Go module that imports lath/pipeline and implements Step. There is
// no registry to join, no interface only these can satisfy, and no build-time
// knowledge of them anywhere in the runner.
//
// They sit beside kit and pipeline like every other package in this module,
// because that is what they are. An earlier version moved them out to signal
// "not core". Which was the wrong mechanism: a directory cannot carry that
// meaning, and inventing a placement rule nobody can infer is how the previous
// layout became unexplainable.
//
// What actually keeps them unprivileged is structural, not positional: the
// runner has no build-time knowledge of them, there is no registry, and
// nothing here implements an interface a third party could not. A third
// party's steps live in their own repository regardless of how this one is
// arranged, so the arrangement was never communicating anything to them.
//
// A third-party set is structurally identical:
//
//	module github.com/someone/lath-steps-slack
//
//	require github.com/ubgo/lath/pipeline v1.2.3
//
//	type Notify struct{ Channel string }
//
//	func (Notify) Name() string             { return "notify-slack" }
//	func (Notify) Requires() []pipeline.Key { return []pipeline.Key{common.KeyImage} }
//	func (Notify) Provides() []pipeline.Key { return nil }
//	func (n Notify) Run(ctx context.Context, s *pipeline.State) error { … }
//
// A definition imports it and uses it beside these with nothing to configure,
// because a definition is an ordinary Go module and Go already knows how to
// resolve a dependency.
//
// # What is here
//
//	common  vendor-neutral steps, plus the shared state keys
//	docker  steps over kit/docker
//	git     steps over kit/git
//
// # The rule for adding one
//
// If it imports pipeline it is a step and belongs in a step package. If it
// does not, it is a library and belongs in kit, where any Go program can use
// it without lath. The docker and git packages here are thin: every one of
// them delegates to the kit package of the same name, which is usable on its
// own.
package steps
