package brew_test

// The publish composition kit/brew's doc comment promises, run for real
// against a local bare repository.
//
// It lives HERE rather than in kit/git because the claim is this package's:
// its doc comment shows eight lines that render a formula and put it in a tap,
// and an example nobody executes is the failure mode documentation has and
// code does not. Testing it from kit/git would also point that package's tests
// at a higher-level one, which is the wrong direction for a dependency.
//
// No network: a bare repository in a temp directory is a perfectly real
// remote.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/git"
)

func TestPublishAFormulaEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "tap.git")
	run(t, "", "git", "init", "--bare", "--initial-branch=main", bare)

	// One commit, so the tap has a branch to clone.
	seedDir := t.TempDir()
	run(t, "", "git", "clone", bare, seedDir)
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("# tap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seedDir, "git", "add", ".")
	run(t, seedDir, "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-m", "init")
	run(t, seedDir, "git", "push", "origin", "HEAD:main")

	formula := full()
	text, err := formula.Render()
	if err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(t.TempDir(), "tap")
	repo, err := git.Clone(ctx, nil, bare, work)
	if err != nil {
		t.Fatal(err)
	}
	repo = repo.WithIdentity("lath", "lath@example.com")

	path := filepath.Join(work, formula.Path())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(ctx, "volt "+formula.Version, formula.Path()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(ctx, ""); err != nil {
		t.Fatal(err)
	}

	// Read back from the REMOTE: a push that silently went nowhere leaves the
	// local tree looking perfect.
	got := run(t, "", "git", "--git-dir", bare, "show", "HEAD:"+formula.Path())
	if !strings.Contains(got, "class Volt < Formula") {
		t.Errorf("the formula did not reach the tap:\n%s", got)
	}
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
