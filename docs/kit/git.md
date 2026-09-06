# kit/git

Reading a working tree's commit and cleanliness — and writing to one when a caller asks.

```go
import "github.com/ubgo/lath/kit/git"
```

**Read-only for as long as a deploy was the only caller**, which has no business making commits. Publishing changed that: a release updating a package formula, a docs site or a manifest in another repository has to clone, commit and push, and the alternative to modelling it here was project code shelling out to git directly — the thing this package exists to prevent. The writes are narrow and explicit: nothing rewrites history, nothing forces, and nothing commits without being told which paths.

## API

```go
func On(r runner.Runner, dir string) Client     // nil Runner = this machine; empty dir = cwd
func Available() bool
func (c Client) WithFlags(extra ...string) Client
func (c Client) ShortCommit(ctx context.Context, length int) (string, error)
func (c Client) Branch(ctx context.Context) (string, error)
func (c Client) Status(ctx context.Context) (string, error)
func (c Client) RequireClean(ctx context.Context) error
```

The write half:

```go
func Clone(ctx context.Context, r runner.Runner, url, dir string, extra ...string) (Client, error)
func (c Client) Commit(ctx context.Context, message string, paths ...string) error
func (c Client) Push(ctx context.Context, remote string, extra ...string) error
func (c Client) Tag(ctx context.Context, name, message string) error
func (c Client) PushTag(ctx context.Context, remote, name string) error
func (c Client) WithIdentity(name, email string) Client
```

| Symbol | What |
|---|---|
| `Clone` | Into a directory that must not exist; returns a `Client` for it |
| `Commit` | Stages the named paths and records them; `ErrNothingToCommit` when nothing changed |
| `Push` · `PushTag` | To `remote`, or `DefaultRemote` when empty. Never forces |
| `Tag` | An **annotated** tag at HEAD; `ErrTagExists` when the name is taken |
| `WithIdentity` | Authors commits as a given name and email, for a machine with none configured |
| `ErrNothingToCommit` · `ErrTagExists` · `DefaultRemote` | The two ordinary outcomes of a retry, and `origin` |

⚠️ **`Commit` requires paths.** `git commit -a` in an automated publish commits whatever else is in the tree — nothing today, an editor's swap file tomorrow. Naming what is published keeps a surprise out of the commit.

⚠️ **`ErrNothingToCommit` is not a failure.** A publish re-run after a network drop renders identical bytes and reaches it; treating it as fatal makes a caller's own idempotency impossible. `ErrTagExists` is the same shape — a retry meets the tag its earlier run created.

⚠️ **`WithIdentity` is opt-in, never a default.** On a developer's machine the configured identity is the right one, and silently authoring as somebody else is a misattribution nobody notices until it is in history.

⚠️ **Credentials belong in the clone URL** (`https://x-access-token:TOKEN@host/owner/repo`) for an automated caller. This package takes no token: that would mean building the URL, which differs per host, and interpolating a secret into a string that lands in error messages.

```go
client := git.On(nil, "")

commit, err := client.ShortCommit(ctx, 0)     // 0 means DefaultShortLength (7)
branch, err := client.Branch(ctx)             // "main", or git.DetachedHead in CI
if err := client.RequireClean(ctx); err != nil {
    return err
}
```

## Branch

An image is usually stamped with both the commit and the branch it came from, and the branch is the half that answers *was this supposed to reach production*. A commit alone does not distinguish a release from someone's experiment that got deployed by mistake.

`Branch` never returns an empty string on success. A detached HEAD. The normal state in CI, since most checkout actions detach, yields `git.DetachedHead` (`"HEAD"`) rather than an error, so a caller stamping the result never writes an empty label and never has to special-case CI.

## Why RequireClean matters

The commit is what names the image. With a dirty tree the tag describes bytes that were **never committed**, so "what is running in production" becomes unanswerable and rollback becomes a guess.

The error quotes the offending files, because "the tree is dirty" without saying what leaves the reader running the command themselves:

```
git: the working tree has uncommitted changes:
   M apps/api/main.go
  ?? notes.txt
```

## WithFlags

git has hundreds of flags and modelling them would be a second git. `WithFlags` prefixes them to every invocation:

```go
// A repository owned by another user — common inside a container
client := git.On(where, "/srv/app").WithFlags("-c", "safe.directory=*")

// Do not fight for the index lock on a busy checkout
client = client.WithFlags("--no-optional-locks")
```

It returns a copy, so a configured client is safe to share.

## Errors

| | |
|---|---|
| `ErrDirty` | uncommitted changes when a clean tree was required |
| `ErrNoCommits` | the repository has no history to name |

A repository with no commits reports `ErrNoCommits` rather than yielding an empty string. An empty tag would name an image `repo:` and fail much later.
