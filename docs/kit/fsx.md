# kit/fsx

Filesystem operations that cannot leave a half-written file behind.

```go
import "github.com/ubgo/lath/kit/fsx"
```

## API

```go
func WriteAtomic(path string, data []byte, perm os.FileMode) error
func CopyFile(src, dst string) error
func CopyFileMode(src, dst string, perm os.FileMode) error
func Exists(path string) (bool, error)
```

```go
err := fsx.WriteAtomic("/srv/app/config.json", data, 0o600)

// A config copied out of a repository is 0644 there and must be 0600 where it lands
err = fsx.CopyFileMode("./config.yaml", "/srv/app/config.yaml", 0o600)
```

## Guarantees

**`WriteAtomic` never exposes a partial file.** It writes to a temporary file in the same directory, sets the mode, and renames, so a concurrent reader sees the old content or the new, never a prefix. A failed write removes the temporary rather than leaving a `.tmp-*` file that poisons every later directory listing.

**`CopyFile` refuses to copy a file onto itself**, returning `ErrSameFile`. Without that check the truncating open **destroys the source** before the copy reads a byte. It compares inodes with `os.SameFile`, not path strings, so a hardlink or a symlink to the same file is caught too.

Reaching that error means two paths meant to differ resolved to one, and the caller needs to know, so it is reported rather than quietly skipped.

**`Exists` distinguishes missing from unreadable.** A permission error returns `(false, err)`, not `(false, nil)`. Collapsing them makes a permission problem look like a clean slate, and the caller then creates or overwrites something it never actually checked. A dangling symlink reports `false, nil`, `os.Stat` follows links, and the target is what a caller means.

## Errors

| | |
|---|---|
| `ErrSameFile` | source and destination are the same file |
