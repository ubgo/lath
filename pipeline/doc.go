// Package pipeline is the execution engine for ordered, introspectable step
// sequences.
//
// Why it exists as its own package: plain Go function composition already
// sequences work correctly, but a function body cannot be listed before it
// runs. Two capabilities require the steps to be data rather than statements ,
// previewing what will happen (`plan`) and reporting progress by position
// ("step 4 of 12"). Everything else here exists to make that indirection cost
// as little type safety as possible.
//
// The package is deliberately domain-free: no Docker, no SSH, no proxy, no
// notion of what a "deploy" is. Concrete work lives in step implementations
// (see the sibling steps package), which means this engine is reusable for any
// staged process, release, migration, teardown, without modification.
//
// Invariant: nothing in this package writes to stdout, stderr, or any global.
// All human-facing output goes through the Reporter carried on State, so an
// importing program controls its own output and tests can assert on it.
package pipeline
