package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

// TestProjectRootPrefersWhatTheRunnerSaid. Under `lath run` the answer is
// exact: the runner has just discovered .lath and states where it was. Nothing
// may second-guess that, because the fallback below is a heuristic and this is
// not.
func TestProjectRootPrefersWhatTheRunnerSaid(t *testing.T) {
	t.Setenv(pipeline.RootEnvVar, "/srv/somewhere")
	got, err := pipeline.ProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/srv/somewhere" {
		t.Errorf("ProjectRoot() = %q, want the runner's answer", got)
	}
}

// TestProjectRootFallsBackToTheDirectoryHoldingTheDefinition covers `go test`
// inside a definition, where there is no runner to ask. It searches for a
// directory CONTAINING .lath, which is what a project root is, rather than for
// a go.mod, which is what a Go module root is: the definition is itself a
// module, so that search would stop one directory too deep and answer with the
// definition instead of the repository.
func TestProjectRootFallsBackToTheDirectoryHoldingTheDefinition(t *testing.T) {
	root := t.TempDir()
	// The shape of a real project: a definition that is its own module, nested
	// inside a repository that is also one.
	if err := os.MkdirAll(filepath.Join(root, pipeline.DefinitionDir, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, mod := range []string{"go.mod", filepath.Join(pipeline.DefinitionDir, "go.mod")} {
		if err := os.WriteFile(filepath.Join(root, mod), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(pipeline.RootEnvVar, "")
	t.Chdir(filepath.Join(root, pipeline.DefinitionDir, "deploy"))

	got, err := pipeline.ProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	// Compared through EvalSymlinks: t.TempDir is under /var on macOS, which
	// is a symlink to /private/var, and the walk resolves differently.
	want, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Errorf("ProjectRoot() = %q, want the repository %q, not the definition", got, want)
	}
}

// TestProjectRootSaysWhatToSetWhenThereIsNoAnswer. "no project root" with
// nothing else is a dead end; the message names the variable that fixes it.
func TestProjectRootSaysWhatToSetWhenThereIsNoAnswer(t *testing.T) {
	t.Setenv(pipeline.RootEnvVar, "")
	t.Chdir(t.TempDir())

	_, err := pipeline.ProjectRoot()
	if !errors.Is(err, pipeline.ErrNoRoot) {
		t.Fatalf("err = %v, want ErrNoRoot", err)
	}
	if !strings.Contains(err.Error(), pipeline.RootEnvVar) {
		t.Errorf("err = %v, want it to name %s", err, pipeline.RootEnvVar)
	}
}

// TestMustProjectRootPanicsRatherThanReturningNothing. A definition that calls
// this has already decided it cannot proceed without a root: every path it
// builds would otherwise be relative to whatever directory the deploy happened
// to start in, which silently writes files to the wrong place instead of
// failing. The panic is the safer failure, and the message must carry the fix.
func TestMustProjectRootPanicsRatherThanReturningNothing(t *testing.T) {
	// Both sources removed: no runner-supplied root, and a working directory
	// with no .lath above it.
	t.Setenv(pipeline.RootEnvVar, "")
	t.Chdir(t.TempDir())

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustProjectRoot returned a root that does not exist")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panicked with %T, want the error ProjectRoot returned", r)
		}
		if !strings.Contains(err.Error(), pipeline.RootEnvVar) {
			t.Errorf("panic = %v; want it to name the variable that fixes this", err)
		}
	}()
	_ = pipeline.MustProjectRoot()
}

// TestMustProjectRootReturnsWhatTheRunnerSaid, the ordinary path: a definition
// launched by lath always has the variable set.
func TestMustProjectRootReturnsWhatTheRunnerSaid(t *testing.T) {
	root := t.TempDir()
	t.Setenv(pipeline.RootEnvVar, root)

	if got := pipeline.MustProjectRoot(); got != root {
		t.Errorf("MustProjectRoot = %q, want %q", got, root)
	}
}
