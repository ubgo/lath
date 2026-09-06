# kit/out

Log files and line-prefixed output.

```go
import "github.com/ubgo/lath/kit/out"
```

## API

```go
func LogFile(path string) (io.WriteCloser, error)                          // 0644 file, 0755 dirs
func LogFileMode(path string, filePerm, dirPerm os.FileMode) (io.WriteCloser, error)
func Prefix(w io.Writer, prefix string) io.Writer
```

```go
log, err := out.LogFile("tmp/logs/dev.log")     // parent directories created
defer log.Close()

w := io.MultiWriter(os.Stdout, log)
proc.Run(ctx, "task", []string{"dev"}, proc.Out(out.Prefix(w, "[dev] ")))
```

For a log that carries request bodies or tokens:

```go
log, err := out.LogFileMode("tmp/logs/audit.log", 0o600, 0o700)
```

## Details

**`LogFile` appends.** Truncating would destroy the history of a crash loop at the moment it is most wanted.

**Parent directories are created**, so the first run on a fresh checkout needs no `mkdir`.

**`Close` names the file.** A bare "file already closed" in a deploy log is unactionable.

**Concurrent writes do not interleave within a line.** `O_APPEND` makes each write atomic, which is what keeps a supervisor and its child from corrupting each other's lines.

## Prefix

⚠️ **A line arriving in several `Write` calls gets ONE prefix.** That is the whole reason this exists: subprocess output arrives in pipe-sized chunks with no relationship to line boundaries, so a naive implementation looks right in tests that write whole lines and produces `[dev] par[dev] tial` against a real process.

```go
w := out.Prefix(os.Stdout, "> ")
io.WriteString(w, "hel")
io.WriteString(w, "lo wor")
io.WriteString(w, "ld\n")     // "> hello world\n"
```

A blank line is a line and gets a prefix, dropping it would silently reflow a subprocess's output. A partial line with no trailing newline is prefixed without inventing one. Nothing written means nothing emitted, so there is no lone prefix.

`Write` reports the **caller's** byte count on success, not the larger number sent downstream, or `io.Copy` would treat every write as short.
