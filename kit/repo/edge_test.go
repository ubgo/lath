package repo_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/repo"
)

// TestRootFromFindsEveryMarker pins that each marker is recognised on its own.
// A repo checked out without a .git (a release tarball, a CI export) still has
// go.mod, and a task that cannot find its root there fails for no good reason.
func TestRootFromFindsEveryMarker(t *testing.T) {
	t.Parallel()
	for _, marker := range repo.Markers {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, marker), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			deep := filepath.Join(root, "a", "b", "c")
			if err := os.MkdirAll(deep, 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := repo.RootFrom(deep)
			if err != nil {
				t.Fatal(err)
			}
			assertSamePath(t, got, root)
		})
	}
}

// TestRootFromAcceptsAGitFile pins that .git as a FILE works. Git worktrees
// and submodules write a file there, not a directory, so an implementation
// that checked for a directory would fail in exactly the setups where a task
// runner is most useful.
func TestRootFromAcceptsAGitFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"),
		[]byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RootFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, got, root)
}

// TestRootFromStopsAtTheNearestMarker pins that a nested module resolves to
// itself, not to the outer workspace. Getting this backwards would send every
// path a nested definition builds into the wrong tree.
func TestRootFromStopsAtTheNearestMarker(t *testing.T) {
	t.Parallel()
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "go.work"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "modules", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(inner, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RootFrom(sub)
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, got, inner)
}

// TestRootFromDirectoryIsItsOwnRoot pins that a directory holding a marker
// resolves to itself rather than climbing past it.
func TestRootFromDirectoryIsItsOwnRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RootFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, got, root)
}

// TestRootFromNoMarkerAnywhere pins that the climb terminates at the
// filesystem root instead of looping, and that the error says what was sought.
func TestRootFromNoMarkerAnywhere(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		// /dev has no marker above it and is guaranteed to exist.
		_, err := repo.RootFrom("/dev")
		if !errors.Is(err, repo.ErrNoRepo) {
			t.Fatalf("err = %v; want ErrNoRepo", err)
		}
		for _, marker := range repo.Markers {
			if !strings.Contains(err.Error(), marker) {
				t.Errorf("the error does not name %s: %v", marker, err)
			}
		}
	}
}

// TestRootFromRelativePath pins that a relative argument is resolved against
// the working directory rather than treated as if it were absolute.
func TestRootFromRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "cmd", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	for _, arg := range []string{".", "..", "./", "../.."} {
		got, err := repo.RootFrom(arg)
		if err != nil {
			t.Fatalf("RootFrom(%q) = %v", arg, err)
		}
		assertSamePath(t, got, root)
	}
}

// TestRootFromMissingDirectory pins that a path that does not exist still
// resolves by climbing, filepath.Abs is lexical, and the parent chain of a
// missing directory is a legitimate place to look.
func TestRootFromMissingDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RootFrom(filepath.Join(root, "does", "not", "exist"))
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, got, root)
}

// TestPathJoinsUnderTheRoot covers the shapes a caller actually passes,
// including none at all.
func TestPathJoinsUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	for _, tc := range []struct {
		name  string
		parts []string
		want  string
	}{
		{"no parts is the root itself", nil, root},
		{"one part", []string{"tmp"}, filepath.Join(root, "tmp")},
		{"several parts", []string{"a", "b", "c.txt"}, filepath.Join(root, "a", "b", "c.txt")},
		{"a pre-joined part", []string{"a/b"}, filepath.Join(root, "a", "b")},
		// Join cleans the result, so an empty part cannot produce a doubled
		// separator.
		{"an empty part", []string{"", "x"}, filepath.Join(root, "x")},
		{"a traversal is cleaned, not resolved away", []string{"a", "..", "b"}, filepath.Join(root, "b")},
	} {
		got, err := repo.Path(tc.parts...)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		assertSamePath(t, got, tc.want)
	}
}

func TestMustPathPanicsWithoutARepo(t *testing.T) {
	t.Chdir(t.TempDir()) // a bare temp dir: no marker above it on any tested OS
	if _, err := repo.Root(); err == nil {
		t.Skip("the temp directory sits inside a repository on this machine")
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustPath returned normally with no repository")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panicked with %T; want an error so a recovering caller can inspect it", r)
		}
		if !errors.Is(err, repo.ErrNoRepo) {
			t.Errorf("panicked with %v; want ErrNoRepo", err)
		}
	}()
	_ = repo.MustPath("x")
}

func TestMustPathReturnsWhatPathDoes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	want, err := repo.Path("a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got := repo.MustPath("a", "b"); got != want {
		t.Errorf("MustPath = %q; Path = %q", got, want)
	}
}

// assertSamePath compares after EvalSymlinks: macOS resolves t.TempDir() under
// /var, which is a symlink to /private/var, so a raw string compare fails for
// reasons that have nothing to do with the code under test.
func assertSamePath(t *testing.T, got, want string) {
	t.Helper()
	g, err := filepath.EvalSymlinks(got)
	if err != nil {
		g = got
	}
	w, err := filepath.EvalSymlinks(want)
	if err != nil {
		w = want
	}
	if g != w {
		t.Errorf("got %q; want %q", got, want)
	}
}

// TestRootWithADeletedWorkingDirectory pins that a vanished working directory
// is reported as such, rather than panicking or resolving somewhere else.
//
// It happens in practice: a task deletes a build directory it is running in,
// or a watch process outlives a `git clean`. Every path derived afterwards
// would be wrong, so the failure has to surface.
//
// Platform note: on macOS os.Getwd returns the kernel's cached path and
// succeeds even after the directory is gone, so this exercises the branch on
// Linux and skips here. The skip is checked against ErrNoRepo rather than
// merely "an error occurred". A temp directory has no marker above it, so a
// bare error assertion would pass without ever reaching os.Getwd's failure.
func TestRootWithADeletedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows locks the working directory against removal")
	}
	parent := t.TempDir()
	doomed := filepath.Join(parent, "doomed")
	if err := os.Mkdir(doomed, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(doomed)
	if err := os.Remove(doomed); err != nil {
		t.Fatal(err)
	}

	_, err := repo.Root()
	switch {
	case err == nil:
		t.Skip("this platform still resolves a removed working directory")
	case errors.Is(err, repo.ErrNoRepo):
		// os.Getwd succeeded and the climb simply found no marker. That is a
		// different code path, so there is nothing here to assert.
		t.Skip("os.Getwd resolved the removed directory; the climb failed instead")
	}

	if !strings.Contains(err.Error(), "repo:") {
		t.Errorf("err = %v; want it attributed to the package", err)
	}
	// Path and MustPath must fail the same way rather than returning a
	// half-formed path built on an empty root.
	if _, perr := repo.Path("x"); !errors.Is(perr, err) && perr == nil {
		t.Error("Path succeeded with no working directory")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("MustPath returned normally with no working directory")
			}
		}()
		_ = repo.MustPath("x")
	}()
}

// TestWithMarkersIsPerCallNotGlobal pins the fix for a real design flaw: the
// only way to change what counts as a root used to be mutating the package's
// Markers slice, which races with every other caller and silently redefines
// "the repository" for code that never asked.
func TestWithMarkersIsPerCallNotGlobal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "WORKSPACE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// The default markers do not know about WORKSPACE.
	if _, err := repo.RootFrom(deep); err == nil {
		t.Error("the default markers matched a WORKSPACE file")
	}
	got, err := repo.RootFrom(deep, repo.WithMarkers("WORKSPACE"))
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, got, root)

	// And the package default is untouched, so a concurrent caller is
	// unaffected. The whole point of the option.
	if _, err := repo.RootFrom(deep); err == nil {
		t.Error("WithMarkers leaked into the package default")
	}
}

// TestWithMarkersEmptyFallsBackToTheDefault pins that an empty option cannot
// produce a search that matches nothing at all.
func TestWithMarkersEmptyFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RootFrom(root, repo.WithMarkers())
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, got, root)
}
