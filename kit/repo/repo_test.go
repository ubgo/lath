package repo_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ubgo/lath/kit/repo"
)

func TestRootFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		marker  string
		startAt string // relative to the created root
		wantErr error
	}{
		{name: "go.work at the root", marker: "go.work", startAt: "."},
		{name: "go.mod at the root", marker: "go.mod", startAt: "."},
		{name: ".git for a non-Go repository", marker: ".git", startAt: "."},
		{name: "found from a nested directory", marker: "go.work", startAt: "a/b/c"},
		{name: "no marker anywhere", marker: "", startAt: ".", wantErr: repo.ErrNoRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.marker != "" {
				if err := os.WriteFile(filepath.Join(root, tc.marker), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			start := filepath.Join(root, tc.startAt)
			if err := os.MkdirAll(start, 0o755); err != nil {
				t.Fatal(err)
			}

			got, err := repo.RootFrom(start)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("RootFrom = %q, %v; want %v", got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// t.TempDir gives /var/... on macOS, which resolves to /private/var.
			wantResolved, _ := filepath.EvalSymlinks(root)
			gotResolved, _ := filepath.EvalSymlinks(got)
			if gotResolved != wantResolved {
				t.Errorf("RootFrom = %q; want %q", got, root)
			}
		})
	}
}

// TestMarkerPriority pins that go.work wins over go.mod. In a workspace both
// exist, and the workspace root is the repository, returning the module
// directory instead would silently scope every path to one module.
func TestMarkerPriority(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inner := filepath.Join(root, "modules", "one")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, name := range map[string]string{root: "go.work", inner: "go.mod"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// From inside the module, the nearest marker is its own go.mod, which is
	// correct: the search is nearest-first, and the module IS a repository
	// boundary for a caller standing in it.
	got, err := repo.RootFrom(inner)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "one" {
		t.Errorf("RootFrom(module) = %q; want the nearest marker, the module itself", got)
	}
}

func TestMustPathPanicsWithoutRepo(t *testing.T) {
	// Not parallel: it changes the working directory, which is process-global.
	dir := t.TempDir()
	t.Chdir(dir)

	defer func() {
		if recover() == nil {
			t.Error("MustPath did not panic outside a repository")
		}
	}()
	_ = repo.MustPath("x")
}
