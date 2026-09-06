// Package git reads the state of a git working tree, and writes to one when a
// caller asks it to.
//
// Plain functions over a runner.Runner, with no pipeline and nothing from lath
// beyond the kit.
//
// This package was read-only for as long as its only caller was a deploy,
// which has no business making commits. Publishing changed that: a release
// that updates a package formula, a docs site, or a manifest in another
// repository has to clone, commit and push, and the alternative to modelling
// it here was project code shelling out to git directly — which is the thing
// this package exists to prevent. The writes are therefore explicit and
// narrow: Clone, Commit, Push, Tag, PushTag. Nothing rewrites history, nothing
// forces, and nothing commits without being told which paths.
package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
)

// Program is the CLI these functions drive.
const Program = "git"

// DefaultShortLength is how many hex characters identify a commit. Seven is
// git's own default for --short and is unambiguous in any repository this will
// meet.
const DefaultShortLength = 7

// ErrDirty reports uncommitted changes when a clean tree was required.
var ErrDirty = errors.New("git: the working tree has uncommitted changes")

// ErrNoCommits reports a repository with no history to name.
var ErrNoCommits = errors.New("git: no commit found")

// Client is git in one working tree, on one machine.
type Client struct {
	where runner.Runner
	dir   string
	extra []string
}

// On returns a Client for dir on r. A nil Runner means this machine; an empty
// dir means the working directory.
func On(r runner.Runner, dir string) Client {
	return Client{where: runner.OrLocal(r), dir: dir}
}

// WithFlags returns a copy that passes extra flags to every git invocation,
// inserted before the subcommand, "-c", "safe.directory=*" for a repository
// owned by another user, or "--no-optional-locks" on a busy checkout.
//
// The escape hatch this package needs: git has hundreds of flags and modelling
// them would be a second git. Without it a caller hits the first unmodelled
// one and stops using the package.
func (c Client) WithFlags(extra ...string) Client {
	c.extra = append(append([]string{}, c.extra...), extra...)
	return c
}

// args prefixes the global flags to a subcommand.
func (c Client) args(sub ...string) []string {
	return append(append([]string{}, c.extra...), sub...)
}

// Available reports whether the git CLI can be found locally.
func Available() bool { return proc.Exists(Program) }

func (c Client) opts() []proc.Option {
	opts := []proc.Option{proc.Capture()}
	if c.dir != "" {
		opts = append(opts, proc.Dir(c.dir))
	}
	return opts
}

// ShortCommit returns the abbreviated SHA of HEAD.
func (c Client) ShortCommit(ctx context.Context, length int) (string, error) {
	if length <= 0 {
		length = DefaultShortLength
	}
	r, err := c.where.Run(ctx, Program,
		c.args("rev-parse", "--short="+strconv.Itoa(length), "HEAD"), c.opts()...)
	if err := runner.Check("git rev-parse", c.where, r, err); err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(r.Stdout))
	if commit == "" {
		return "", fmt.Errorf("git rev-parse: %w", ErrNoCommits)
	}
	return commit, nil
}

// Status returns the porcelain status, empty when the tree is clean.
// DetachedHead is what Branch reports when HEAD points at a commit rather than
// a branch. This is the normal state in CI, most checkout actions detach, so
// it is a documented value, not an error.
const DetachedHead = "HEAD"

// Branch returns the branch HEAD is on, or DetachedHead when it is detached.
//
// Why it exists: an image is usually stamped with both the commit and the
// branch it came from, and the branch is the half that answers "was this
// supposed to reach production". A commit alone does not distinguish a
// release from someone's experiment that was deployed by mistake.
//
// Invariant: never returns an empty string on success. A detached HEAD yields
// DetachedHead, so a caller stamping the result never writes an empty label.
func (c Client) Branch(ctx context.Context) (string, error) {
	r, err := c.where.Run(ctx, Program, c.args("rev-parse", "--abbrev-ref", "HEAD"), c.opts()...)
	if err := runner.Check("git rev-parse", c.where, r, err); err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(r.Stdout))
	if branch == "" {
		return "", fmt.Errorf("git rev-parse: %w", ErrNoCommits)
	}
	return branch, nil
}

// Status returns the working tree's porcelain status, empty when clean.
//
// Porcelain rather than the human format because it is the one git promises
// not to change between versions, parsing the readable output is how a tool
// breaks on someone else's machine.
//
// Returned as raw text rather than parsed entries: the callers here only ask
// "is it clean" and "show me what is dirty", and a parser for the full format
// would be code with no reader. RequireClean is the answer to the first.
func (c Client) Status(ctx context.Context) (string, error) {
	r, err := c.where.Run(ctx, Program, c.args("status", "--porcelain"), c.opts()...)
	if err := runner.Check("git status", c.where, r, err); err != nil {
		return "", err
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// RequireClean fails when the working tree has uncommitted changes.
//
// Why a deploy should care: the commit is what names the image, so with a dirty
// tree the tag describes bytes that were never committed. "What is running in
// production" becomes unanswerable and rollback becomes a guess.
//
// The error quotes the offending files, because "the tree is dirty" without
// saying what leaves the reader running the command themselves.
func (c Client) RequireClean(ctx context.Context) error {
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w:\n%s", ErrDirty, indent(status))
	}
	return nil
}

// ErrNothingToCommit reports a commit with no staged change.
//
// Not a failure: re-running a publish that already committed reaches this,
// and so does a formula whose rendered bytes are identical to the ones already
// in the tap. A caller treating it as fatal would make its own idempotency
// impossible.
var ErrNothingToCommit = errors.New("git: nothing to commit")

// ErrTagExists reports a tag name already in the repository.
//
// Worth its own error because a release retry hits it on the tag it created
// last time, which is a success from an earlier run rather than a conflict.
var ErrTagExists = errors.New("git: the tag already exists")

// DefaultRemote is the remote push writes to when none is named.
const DefaultRemote = "origin"

// Clone copies a repository into dir and returns a Client for it.
//
// dir must not exist: git refuses to clone into a non-empty directory, and the
// refusal is worth keeping rather than clearing the path on a caller's behalf
// — a publish that wipes a directory it did not create is one typo away from
// deleting work.
//
// The credential is expected to be IN url for an automated caller
// (https://x-access-token:TOKEN@host/owner/repo). This package does not take a
// token: doing so would mean building the URL, which differs per host, and
// interpolating a credential into a string that lands in error messages. A
// caller that already has a secret.Value knows how to keep it out of logs.
func Clone(ctx context.Context, r runner.Runner, url, dir string, extra ...string) (Client, error) {
	where := runner.OrLocal(r)
	args := append([]string{"clone", url, dir}, extra...)
	res, err := where.Run(ctx, Program, args, proc.Capture())
	if err := runner.Check("git clone", where, res, err); err != nil {
		return Client{}, err
	}
	return On(r, dir), nil
}

// Commit stages the named paths and records them.
//
// Paths are required, and that is the design: `git commit -a` in an automated
// publish commits whatever else happens to be in the tree, which in a cloned
// tap is nothing today and an editor's swap file tomorrow. Naming what is
// being published keeps a surprise out of the commit.
//
// Returns ErrNothingToCommit when the staged content matches HEAD, which is
// the ordinary result of re-running a publish that already succeeded.
func (c Client) Commit(ctx context.Context, message string, paths ...string) error {
	if len(paths) == 0 {
		return fmt.Errorf("git commit: at least one path is required")
	}
	res, err := c.where.Run(ctx, Program, c.args(append([]string{"add", "--"}, paths...)...), c.opts()...)
	if err := runner.Check("git add", c.where, res, err); err != nil {
		return err
	}

	// Asked BEFORE committing rather than by interpreting the commit's
	// failure: "nothing to commit" and a real refusal both exit non-zero, and
	// telling them apart from the message is a guess about git's wording.
	staged, err := c.where.Run(ctx, Program, c.args("diff", "--cached", "--quiet"), c.opts()...)
	if err == nil && staged.OK() {
		return ErrNothingToCommit
	}

	res, err = c.where.Run(ctx, Program, c.args("commit", "-m", message), c.opts()...)
	return runner.Check("git commit", c.where, res, err)
}

// Push sends the current branch to a remote. An empty remote means
// DefaultRemote.
//
// Never forces. A publish that force-pushes a shared repository can erase a
// change someone else made between the clone and the push; --force stays
// reachable through extra, where the caller owns it.
func (c Client) Push(ctx context.Context, remote string, extra ...string) error {
	if remote == "" {
		remote = DefaultRemote
	}
	res, err := c.where.Run(ctx, Program, c.args(append([]string{"push", remote}, extra...)...), c.opts()...)
	return runner.Check("git push", c.where, res, err)
}

// Tag creates an annotated tag at HEAD.
//
// Annotated rather than lightweight: an annotated tag carries who made it and
// when, which is the difference between a release record and a moveable
// label. Returns ErrTagExists if the name is taken, so a retry can tell its
// own earlier success from a collision.
func (c Client) Tag(ctx context.Context, name, message string) error {
	if name == "" {
		return fmt.Errorf("git tag: a name is required")
	}
	if message == "" {
		message = name
	}
	res, err := c.where.Run(ctx, Program, c.args("tag", "-a", name, "-m", message), c.opts()...)
	if err := runner.Check("git tag", c.where, res, err); err != nil {
		if strings.Contains(strings.ToLower(string(res.Stderr)), tagExistsMarker) {
			return fmt.Errorf("%s: %w", name, ErrTagExists)
		}
		return err
	}
	return nil
}

// tagExistsMarker is git's own wording for a taken tag name. WIRE FORMAT: it
// is text git prints, pinned here with a test rather than inlined at the
// comparison.
const tagExistsMarker = "already exists"

// PushTag sends one tag to a remote. An empty remote means DefaultRemote.
//
// Separate from Push because they fail for different reasons and, in a
// release, at different moments: the tag is the permanent record and is
// pushed first, deliberately, so that everything after it can be retried
// against a version that is already fixed.
func (c Client) PushTag(ctx context.Context, remote, name string) error {
	if remote == "" {
		remote = DefaultRemote
	}
	res, err := c.where.Run(ctx, Program, c.args("push", remote, "tag", name), c.opts()...)
	return runner.Check("git push tag", c.where, res, err)
}

// WithIdentity returns a copy that authors commits as name and email.
//
// For an automated caller on a machine with no git identity configured, where
// a commit otherwise fails with "please tell me who you are" partway through a
// publish. Not a default: on a developer's machine the configured identity is
// the right one, and silently authoring commits as somebody else is a
// misattribution nobody notices until it is in history.
func (c Client) WithIdentity(name, email string) Client {
	return c.WithFlags("-c", "user.name="+name, "-c", "user.email="+email)
}

// indent prefixes every line, so multi-line command output stays visually
// distinct from the error describing it.
func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
