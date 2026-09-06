# kit/archive

tar.gz bundles, with the extraction guards a hand-rolled version forgets.

```go
import "github.com/ubgo/lath/kit/archive"
```

## API

```go
func TarGz(dst, root string, f scan.Filter, opts ...ArchiveOption) error
func Extract(ctx context.Context, src, dstDir string, opts ...ExtractOption) error

func CompressionLevel(n int) ArchiveOption   // gzip.NoCompression … BestCompression
func FollowSymlinks() ExtractOption
func MaxEntryBytes(n int64) ExtractOption    // default 2 GiB
```

```go
err := archive.TarGz("bundle.tar.gz", "./dist", scan.Filter{}, 
    archive.CompressionLevel(gzip.BestSpeed))   // already-compressed payload

err = archive.Extract(ctx, "bundle.tar.gz", "/srv/app/release")
```

## Why it exists rather than archive/tar

⚠️ **A tar entry named `../../etc/thing` is written wherever it says** unless something checks, and most hand-rolled extractors do not. `Extract` refuses any entry that would land outside the destination, with `ErrUnsafePath`.

Containment is checked with `filepath.Rel`, **not a string prefix**. A prefix test says `/tmp/dst-evil` is inside `/tmp/dst`, which it is not. The destination is resolved through symlinks once, so a symlinked target does not make every check compare the wrong path.

## Symlinks

**No mode writes a raw link.** A written symlink can point outside the destination no matter how its own path validates, so there are two behaviours and no unsafe third to reach for under deadline:

- default. A link entry is refused with `ErrUnsafePath`
- `FollowSymlinks()`. The target's **bytes** are written as a regular file, and the target must itself be inside the destination

`TarGz` does not archive symlinks at all: writing one means the archive can carry an escape, and `Extract` refuses them anyway.

## Other guards

**`MaxEntryBytes` caps a single file** at 2 GiB by default. A decompression bomb otherwise fills the disk before anything notices. Raise it deliberately for a genuinely large artifact; lower it for untrusted input.

**The header's `Size` is data, not a promise.** The copy is bounded by `io.LimitReader` independently of it, so an entry understating its payload is still capped.

**A failed `TarGz` removes the archive**, so a broken bundle never looks like a good one.

**Devices, FIFOs and sockets are skipped**. A deploy bundle has no business carrying them, and creating them is a privilege problem.

**Modes survive the round trip.** An executable that arrives non-executable is a deploy that fails at the far end, long after the transfer reported success.

## Errors

| | |
|---|---|
| `ErrUnsafePath` | an entry escapes the destination, or is a link |
| `ErrEntryTooLarge` | an entry exceeds the cap |
