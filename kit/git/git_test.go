package git_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/git"
	"github.com/ubgo/lath/kit/runner"
)

func TestShortCommit(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "a3f1c2d\n"}}}
	got, err := git.On(f, "").ShortCommit(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a3f1c2d" {
		t.Errorf("ShortCommit = %q", got)
	}
	// Zero means git's own default length, not a zero-length abbreviation.
	if !f.Ran("--short=7") {
		t.Errorf("did not request the default length: %v", f.Commands())
	}
}

func TestShortCommitCustomLength(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "a3f1c2d0f1\n"}}}
	if _, err := git.On(f, "").ShortCommit(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("--short=10") {
		t.Errorf("length not honoured: %v", f.Commands())
	}
}

// TestEmptyOutputIsAnError pins that a repository with no history reports so,
// rather than yielding an empty tag that would name an image ":".
func TestEmptyOutputIsAnError(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "\n"}}}
	_, err := git.On(f, "").ShortCommit(context.Background(), 0)
	if !errors.Is(err, git.ErrNoCommits) {
		t.Errorf("err = %v; want ErrNoCommits", err)
	}
}

// TestRequireCleanQuotesTheOffendingFiles pins that the error names what is
// dirty. "The tree is dirty" alone leaves the reader running the command
// themselves.
func TestRequireCleanQuotesTheOffendingFiles(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "status", Stdout: " M apps/api/main.go\n?? notes.txt\n"},
	}}
	err := git.On(f, "").RequireClean(context.Background())
	if !errors.Is(err, git.ErrDirty) {
		t.Fatalf("err = %v; want ErrDirty", err)
	}
	for _, want := range []string{"apps/api/main.go", "notes.txt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err does not name %s: %v", want, err)
		}
	}
}

func TestRequireCleanPassesOnACleanTree(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "status", Stdout: "\n"}}}
	if err := git.On(f, "").RequireClean(context.Background()); err != nil {
		t.Errorf("a clean tree was rejected: %v", err)
	}
}

func TestDirScopesTheCommands(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "abc1234\n"}}}
	if _, err := git.On(f, "/some/repo").ShortCommit(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	// The directory is a proc option rather than an argument, so it cannot be
	// asserted from the command line. This pins that the call still succeeds
	// and the fake saw a well-formed invocation.
	if !f.Ran("git rev-parse") {
		t.Errorf("commands = %v", f.Commands())
	}
}

func TestFailedCommandIsReported(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "rev-parse", Exit: 128, Stderr: "not a git repository"},
	}}
	_, err := git.On(f, "").ShortCommit(context.Background(), 0)
	if err == nil {
		t.Fatal("a failing git command reported success")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v; want git's own message", err)
	}
}

func TestBranch(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "main\n"}}}
	got, err := git.On(f, "").Branch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "main" {
		t.Errorf("Branch = %q, want main", got)
	}
	if !f.Ran("--abbrev-ref") {
		t.Errorf("did not ask for the symbolic name: %v", f.Commands())
	}
}

// TestBranchDetachedHead pins that CI's normal state is a value, not an error.
// Most checkout actions detach HEAD, so treating it as a failure would break
// every pipeline that stamps a branch.
func TestBranchDetachedHead(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "HEAD\n"}}}
	got, err := git.On(f, "").Branch(context.Background())
	if err != nil {
		t.Fatalf("a detached HEAD is not an error: %v", err)
	}
	if got != git.DetachedHead {
		t.Errorf("Branch = %q, want %q", got, git.DetachedHead)
	}
}

// TestBranchEmptyOutputIsAnError holds the never-empty invariant: a caller
// stamping the result must never write an empty label.
func TestBranchEmptyOutputIsAnError(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "\n"}}}
	if _, err := git.On(f, "").Branch(context.Background()); !errors.Is(err, git.ErrNoCommits) {
		t.Errorf("err = %v, want ErrNoCommits", err)
	}
}

// TestWithFlagsPrefixesEveryInvocation covers the escape hatch. git has
// hundreds of flags and modelling them would be a second git; a caller who
// needs one and cannot have it stops using this package.
func TestWithFlagsPrefixesEveryInvocation(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "rev-parse", Stdout: "a3f1c2d\n"}}}
	c := git.On(f, "").WithFlags("-c", "safe.directory=*")

	if _, err := c.ShortCommit(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("safe.directory=*") {
		t.Errorf("flags did not reach the command: %v", f.Commands())
	}

	// And they must persist across calls on the same client, not just the
	// first. A repository owned by another user needs them every time.
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range f.Commands() {
		if !strings.Contains(cmd, "safe.directory") {
			t.Errorf("a later invocation lost the flags: %v", cmd)
		}
	}
}

// TestStatusReportsPorcelain. The format git promises not to change. Parsing
// the human output is how a tool breaks on someone else's machine.
func TestStatusReportsPorcelain(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "status", Stdout: " M main.go\n?? new.txt\n"}}}
	got, err := git.On(f, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "new.txt") {
		t.Errorf("Status = %q", got)
	}
	if !f.Ran("--porcelain") {
		t.Errorf("did not ask for the stable format: %v", f.Commands())
	}
}

func TestStatusReportsAFailure(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "status", Stderr: "not a git repository", Exit: 128}}}
	if _, err := git.On(f, "").Status(context.Background()); err == nil {
		t.Error("a failing git status reported success")
	}
}

// TestAvailable reports whether the binary this package shells out to exists.
// Local only: a remote answer needs a round trip, which callers make
// deliberately.
func TestAvailable(t *testing.T) {
	t.Parallel()
	_, lookErr := exec.LookPath(git.Program)
	if got, want := git.Available(), lookErr == nil; got != want {
		t.Errorf("Available() = %v, want %v", got, want)
	}
}
