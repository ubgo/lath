# kit/hashtree

Content digests for cache keys.

```go
import "github.com/ubgo/lath/kit/hashtree"
```

## API

```go
func Of(root string, f scan.Filter) (string, error)     // one tree, the common case
func OfFiles(paths []string) (string, error)
func New() *Hasher

func (h *Hasher) AddString(values ...string) *Hasher
func (h *Hasher) AddFile(path string) *Hasher            // path AND contents
func (h *Hasher) AddTree(root string, f scan.Filter) *Hasher
func (h *Hasher) AddFileContents(path string) *Hasher    // contents only
func (h *Hasher) Sum() (string, error)                   // hex SHA-256
```

## The bug it exists to prevent

lath's own compile cache once covered only the definition's source. Upgrading lath left every previously compiled binary in place and the new behaviour silently did not apply, worse than a failure, because the evidence said the new code was running.

**A cache key must cover everything the output depends on**, and what that is differs per caller. Hence a builder rather than a fixed function:

```go
key, err := hashtree.New().
    AddTree(defDir, scan.Filter{Ext: []string{".go"}}).
    AddString(lathVersion, runtime.Version()).
    AddFile("go.sum").
    Sum()
```

## Properties

**Order is significant.** A different assembly is a different question, so it hashes differently.

**Fields are length-prefixed and delimited.** Without that, `AddString("ab","c")` and `AddString("a","bc")` produce the same digest. Two different dependency sets sharing a cache entry.

**`AddTree` folds in relative paths as well as contents**, so renaming a file changes the digest even when no byte changed. Moving one into a subdirectory changes it too.

**A missing file is an error**, not an empty contribution. A key that silently ignores an absent dependency collides with one where the dependency exists. The exact failure this prevents.

**Errors are deferred to `Sum`**, so a chain needs no check at every step. The first error wins.

**An empty match set still hashes.** "Nothing matched" is a valid state to cache.
