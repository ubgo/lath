package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RootEnvVar carries the project root from the runner to the definition it
// executes: the directory that CONTAINS .lath, absolute.
//
// It exists because a definition cannot reliably work this out for itself. A
// definition is a nested Go module, so anything that finds a root by walking
// upward for a go.mod stops at .lath and answers with the definition's own
// directory. That is wrong in two places that matter: `go test` inside the
// definition, where the working directory is the definition, and any `lath
// run` issued from a subdirectory of a Go monorepo, where the walk stops at
// whichever module the operator happened to be standing in.
//
// The runner already knows the answer, having just discovered .lath, so it
// states it rather than leaving every definition to re-derive it and get it
// subtly wrong.
const RootEnvVar = "LATH_ROOT"

// ErrNoRoot means RootEnvVar was not set and no fallback found a root.
var ErrNoRoot = errors.New("pipeline: no project root")

// ProjectRoot reports the directory containing .lath.
//
// Use this, not a upward search for go.mod, wherever a definition needs to
// reach its own repository: the env file at the root, a credential under
// _keys, a Dockerfile.
//
// Under `lath run` the answer comes from RootEnvVar and is exact. Outside it,
// in a plain `go test` of the definition, there is no runner to ask, so the
// fallback walks upward for a directory that CONTAINS a .lath directory, which
// is the definition of the root rather than a proxy for it. A test that needs
// a different answer sets RootEnvVar.
func ProjectRoot() (string, error) {
	if root := os.Getenv(RootEnvVar); root != "" {
		return root, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("pipeline: project root: %w", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, DefinitionDir)); err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w above %s: set %s", ErrNoRoot, dir, RootEnvVar)
		}
		dir = parent
	}
}

// MustProjectRoot is ProjectRoot for a definition that cannot proceed without
// one, which is most of them: a deploy that cannot find its own repository has
// nothing to deploy.
func MustProjectRoot() string {
	root, err := ProjectRoot()
	if err != nil {
		panic(err)
	}
	return root
}

// DefinitionDir is the directory a project keeps its definition in. Named here
// because ProjectRoot's fallback searches for it, and the runner and the
// definition must agree on the name.
const DefinitionDir = ".lath"
