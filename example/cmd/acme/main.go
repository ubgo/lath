// Command acme stands in for the application a definition deploys.
//
// The deploy builds it on the host with golang.Build, which stamps these
// variables through the linker. A real service would import
// github.com/ubgo/buildinfo instead and bind golangkit.BuildinfoCommit and
// friends; this example declares its own so it has no dependencies.
package main

var (
	commit = "unknown"
	branch = "unknown"
	built  = "unknown"
)

func main() { println("the actual app", commit, branch, built) }
