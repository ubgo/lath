package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ubgo/lath/pipeline"
)

// definitionEnv is the environment the compiled definition runs with: this
// process's, plus the project root.
//
// The root is passed because a definition cannot work it out for itself and
// the runner already knows it. A definition is a nested Go module, so any
// upward search for a go.mod stops at .lath and answers with the definition's
// own directory rather than the repository containing it. Every definition
// that reads a file from its repository, an env file, a credential, a
// Dockerfile, would otherwise reimplement this and get it wrong in the two
// cases that are hard to notice: `go test` inside the definition, and `lath
// run` issued from a subdirectory of a monorepo.
//
// See pipeline.ProjectRoot, which is what a definition calls to read it.
func definitionEnv() ([]string, error) {
	root, err := filepath.Abs(definitionDir)
	if err != nil {
		return nil, fmt.Errorf("resolving the project root: %w", err)
	}
	// definitionDir is the .lath directory; the root is what contains it.
	return append(os.Environ(),
		pipeline.RootEnvVar+"="+filepath.Dir(root)), nil
}
