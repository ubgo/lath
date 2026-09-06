# kit/repo

Locating the repository root.

```go
import "github.com/ubgo/lath/kit/repo"
```

## API

```go
func Root(opts ...Option) (string, error)
func RootFrom(dir string, opts ...Option) (string, error)
func Path(parts ...string) (string, error)
func MustPath(parts ...string) string
func WithMarkers(markers ...string) Option

var Markers = []string{"go.work", "go.mod", ".git"}   // the default list
```

```go
root, err := repo.Root()
envFile, err := repo.Path(".env.prod")          // <root>/.env.prod
keys := repo.MustPath("_keys")                   // panics if there is no repository
```

## Behaviour

**Each marker is recognised on its own.** A repository checked out without a `.git`. A release tarball, a CI export, still has `go.mod`, and a task that could not find its root there would fail for no good reason.

**`.git` may be a FILE.** Git worktrees and submodules write a file there, not a directory, so an implementation checking for a directory fails in exactly the setups where a task runner is most useful.

**The nearest marker wins.** A nested module resolves to itself, not the outer workspace, getting that backwards sends every derived path into the wrong tree.

**`Path` joins and cleans**, so an empty part cannot produce a doubled separator and `..` is resolved lexically.

**`MustPath` panics with an error value**, so a recovering caller can inspect it. The `Must` prefix is Go's established signal, `regexp.MustCompile`, `template.Must`.

## WithMarkers

⚠️ For a repository that marks its root differently. A `.hg`, a `WORKSPACE`, a `lerna.json`:

```go
root, err := repo.RootFrom(dir, repo.WithMarkers("WORKSPACE"))
```

A **per-call** option, not mutation of the package's `Markers` slice. That slice is shared, so changing it races with every other caller and silently redefines "the repository" for code that never asked. An empty option falls back to the default rather than matching nothing.

## Errors

| | |
|---|---|
| `ErrNoRepo` | no marker found above the starting directory; the message names what was sought |
