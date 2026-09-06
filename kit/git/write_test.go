package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/git"
)

// The write half is tested against REAL git, not a fake runner.
//
// A fake proves this package builds the argument list it meant to; only git
// proves the argument list does what the doc comments claim. These operations
// are the ones that touch other people's repositories, so "I passed the flags
// I intended" is not the property worth pinning. No network is involved: a
// bare repository in a temp directory is a perfectly real remote.

// remote creates a bare repository and returns its path.
func remote(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	dir := filepath.Join(t.TempDir(), "tap.git")
	run(t, "", "git", "init", "--bare", "--initial-branch=main", dir)
	return dir
}

// seed gives a bare repository one commit, so it has a branch to clone.
func seed(t *testing.T, bare string) {
	t.Helper()
	work := t.TempDir()
	run(t, "", "git", "clone", bare, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# tap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", ".")
	run(t, work, "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-m", "init")
	run(t, work, "git", "push", "origin", "HEAD:main")
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestRepublishingIdenticalContentIsNotAFailure. A publish re-run after a
// network drop renders the same bytes; treating that as fatal would make a
// caller's own idempotency impossible.
func TestRepublishingIdenticalContentIsNotAFailure(t *testing.T) {
	bare := remote(t)
	seed(t, bare)
	ctx := context.Background()

	work := filepath.Join(t.TempDir(), "tap")
	repo, err := git.Clone(ctx, nil, bare, work)
	if err != nil {
		t.Fatal(err)
	}
	repo = repo.WithIdentity("lath", "lath@example.com")

	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# tap\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(ctx, "first", "README.md"); err != nil {
		t.Fatal(err)
	}
	// Same content again, nothing staged.
	err = repo.Commit(ctx, "second", "README.md")
	if !errors.Is(err, git.ErrNothingToCommit) {
		t.Fatalf("err = %v; want ErrNothingToCommit so a retry can continue", err)
	}
}

// TestCommitRequiresPaths. `git commit -a` in an automated publish commits
// whatever else is in the tree — nothing today, an editor swap file tomorrow.
func TestCommitRequiresPaths(t *testing.T) {
	bare := remote(t)
	seed(t, bare)
	work := filepath.Join(t.TempDir(), "tap")
	repo, err := git.Clone(context.Background(), nil, bare, work)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Commit(context.Background(), "everything"); err == nil {
		t.Fatal("a commit with no paths was accepted")
	}
}

// TestTagIsAnnotatedAndRefusesADuplicate. Annotated because a release record
// should carry who made it and when; the duplicate check is what lets a retry
// tell its own earlier success from a collision.
func TestTagIsAnnotatedAndRefusesADuplicate(t *testing.T) {
	bare := remote(t)
	seed(t, bare)
	ctx := context.Background()

	work := filepath.Join(t.TempDir(), "tap")
	repo, err := git.Clone(ctx, nil, bare, work)
	if err != nil {
		t.Fatal(err)
	}
	repo = repo.WithIdentity("lath", "lath@example.com")

	if err := repo.Tag(ctx, "v1.0.0", "release 1.0.0"); err != nil {
		t.Fatal(err)
	}
	// Annotated tags are objects; lightweight ones are not.
	if kind := strings.TrimSpace(run(t, work, "git", "cat-file", "-t", "v1.0.0")); kind != "tag" {
		t.Errorf("tag object type = %q, want an annotated tag", kind)
	}

	err = repo.Tag(ctx, "v1.0.0", "again")
	if !errors.Is(err, git.ErrTagExists) {
		t.Fatalf("err = %v; want ErrTagExists", err)
	}

	if err := repo.PushTag(ctx, "", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run(t, "", "git", "--git-dir", bare, "tag", "--list"), "v1.0.0") {
		t.Error("the tag never reached the remote")
	}
}

// TestCloneRefusesAnExistingDirectory. A publish that clears a path it did not
// create is one typo away from deleting work.
func TestCloneRefusesAnExistingDirectory(t *testing.T) {
	bare := remote(t)
	seed(t, bare)
	occupied := t.TempDir()
	if err := os.WriteFile(filepath.Join(occupied, "mine.txt"), []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := git.Clone(context.Background(), nil, bare, occupied); err == nil {
		t.Fatal("cloning over a non-empty directory succeeded")
	}
	if _, err := os.Stat(filepath.Join(occupied, "mine.txt")); err != nil {
		t.Errorf("the existing file was disturbed: %v", err)
	}
}

// TestWithIdentityAuthorsAsAsked. On a machine with no git identity, a commit
// fails partway through a publish; the opt-in is how an automated caller
// avoids that without misattributing commits on a developer's machine.
func TestWithIdentityAuthorsAsAsked(t *testing.T) {
	bare := remote(t)
	seed(t, bare)
	ctx := context.Background()

	work := filepath.Join(t.TempDir(), "tap")
	repo, err := git.Clone(ctx, nil, bare, work)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# tap\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.WithIdentity("release bot", "bot@example.com").
		Commit(ctx, "edit", "README.md"); err != nil {
		t.Fatal(err)
	}

	author := strings.TrimSpace(run(t, work, "git", "log", "-1", "--format=%an <%ae>"))
	if author != "release bot <bot@example.com>" {
		t.Errorf("author = %q, want the identity that was asked for", author)
	}
}
