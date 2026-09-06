// Package kit is lath's library collection.
//
// # Where does a package go?
//
// One question decides it: does it import pipeline?
//
//   - No  → it is a library. It belongs here, and any Go program can use it
//     whether or not that program uses lath.
//   - Yes → it is a pipeline adapter. It belongs in steps.
//
// That is the whole rule, and it replaced one that did not survive contact.
// The earlier rule was "nothing in kit may name a vendor", which put docker
// and git in separate modules, while kit itself was already shelling out to
// pgrep, ps, ssh, scp and sh. The distinction it claimed to draw did not
// exist, and the layout it produced could not be explained.
//
// # What a kit package may depend on
//
// Anything in the standard library, other kit packages, and external PROGRAMS
// it documents at the point of use. Nothing else: no third-party Go modules,
// and never pipeline.
//
// Depending on an external program is normal here and always has been, proc
// needs pgrep, lock needs ps, ssh needs ssh. What matters is that the package
// says so, reports a clear error when the program is absent, and does not
// pretend to work without it.
package kit
