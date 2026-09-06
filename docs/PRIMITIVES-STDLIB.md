# Spec, lath's primitive libraries

**Status: built.** Everything below exists in `kit/`, is tested under `-race`, and has zero external dependencies. Sections 15 and 16 record what changed during implementation and why.

lath is a task runner whose tasks are Go. Deploying is one task among many. These packages are what a task needs in order to be as short to write as the shell script it replaces.

---

## 1 · Why these exist

Porting one 38-line shell script produced 148 lines of Go. Measured, the extra went almost entirely on re-implementing things a shell gives away:

| Shell | Go, by hand | What it costs |
|---|---:|---|
| `code=$?` | 18 lines | `$?` already encodes signal-vs-exit; Go makes you type-assert twice to learn the same thing |
| `cd $(dirname $0)/…` | 18 lines | `$0` is free in a script; a compiled binary lives elsewhere entirely |
| `CHILD=$!; wait` | 12 lines | one builtin each, versus Start + goroutine + channel + select |
| `tee -a "$LOG"` | 10 lines | open, defer a close that reports its error, MultiWriter |
| `sleep "$backoff"` | 6 lines | must be interruptible, so it is a select |

Only ~15 lines of that file were the thing it was actually for. These packages exist to invert that ratio.

**The counter-example matters equally.** The same measurement for a lint script, walk files, match a pattern, report, came out at 53 lines of Go against 47 of shell. Near parity, because `filepath.WalkDir` and `regexp` already *are* the primitive.

⚠️ That measurement was used to argue `scan` barely earned its place, and it was wrong. It counted only the exclusion loop. Rewritten against the finished package, the same lint is **17 lines**: 53 → 17, not 53 → 43. The lesson is about the method, not the package: a saving estimated from one part of a job under-counts the rest of it.

The admission test for everything below is unchanged.

---

## 2 · What earns a place

A function belongs here only if it **collapses three or more lines of ceremony, or hides a trap**. A shorter name for a stdlib call is a rename, not a primitive, and a package full of renames is the utils grab-bag.

Applied:

| Candidate | Verdict |
|---|---|
| `proc.Run` | **in**, hides the `$!`/pid trap and the exit-status archaeology |
| `fsx.EnsureDir` | **out**. That is `os.MkdirAll` with fewer letters |
| `fsx.WriteAtomic` | **in**, hides the write-temp-then-rename trap |
| `out.Tee` | **out**, that is `io.MultiWriter` |
| `out.LogFile` | **in**, create dirs, open append, and a close that reports its error |

### Rules every package follows

- **Zero external dependencies.** Standard library only, without exception. This is what keeps lath a single binary and a definition's dependency graph to one entry.
- **`context.Context` first** on anything that blocks, and it must actually be honoured. A `select` on `ctx.Done()`, not a comment promising to.
- **Errors wrap with `%w`**, and every condition a caller might branch on gets a sentinel.
- **No package-level state.** Nothing has an `Init`, a global logger, or a default client.
- **Functional options** where a call has more than three optional parameters; plain arguments otherwise.
- **Accept interfaces, return structs.**

---

## 3 · `lath/proc`, running programs

The core package. Everything else is smaller.

```go
// Result is what a finished process leaves behind.
type Result struct {
    ExitCode  int           // 0 on success; 128+signal when Signalled
    Signalled bool          // killed by a signal rather than exiting on its own
    Signal    syscall.Signal// the signal, when Signalled
    Duration  time.Duration
    Stdout    []byte        // captured only when Capture() was set
    Stderr    []byte
}

func (r Result) OK() bool  // ExitCode == 0 && !Signalled

// Run starts a program, waits, and reports how it ended.
// A non-zero exit is NOT an error — it is a Result. Only failure to start,
// or a cancelled context, returns one.
func Run(ctx context.Context, name string, args []string, opts ...Option) (Result, error)

// Output runs a program and returns its trimmed stdout.
// A non-zero exit IS an error here, because a caller wanting the output has
// no use for the failure case. This asymmetry with Run is deliberate.
func Output(ctx context.Context, name string, args []string, opts ...Option) (string, error)

// Start runs a program without waiting, for anything long-lived.
func Start(ctx context.Context, name string, args []string, opts ...Option) (*Process, error)

type Process struct{ /* … */ }
func (p *Process) Wait() (Result, error)
func (p *Process) Signal(sig syscall.Signal) error
func (p *Process) Stop(ctx context.Context, grace time.Duration) error // SIGTERM, then SIGKILL
func (p *Process) PID() int

// Look reports the absolute path of a program, or ErrNotFound.
func Look(name string) (string, error)

// Exists reports whether a program is on PATH. `command -v`.
func Exists(name string) bool
```

### Options

```go
func Dir(path string) Option              // working directory
func Env(kv ...string) Option             // ADDS to the parent environment
func EnvOnly(kv ...string) Option         // REPLACES it entirely
func Stdin(r io.Reader) Option
func Out(w io.Writer) Option              // stdout and stderr together
func Split(stdout, stderr io.Writer) Option
func Capture() Option                     // fill Result.Stdout / Stderr
func Timeout(d time.Duration) Option
```

`Env` versus `EnvOnly` is spelled out because "does this add to or replace the environment?" is the question every such API leaves ambiguous, and guessing wrong produces a process missing `PATH`.

### Sentinels

```go
var (
    ErrNotFound = errors.New("proc: program not found")
    ErrTimeout  = errors.New("proc: timed out")
)
```

### Deliberately absent

- **Shell string parsing.** `proc.Run(ctx, "sh", []string{"-c", "…"})` is available and explicit. A `proc.Shell("a | b")` invites quoting bugs and is the seam where `lath/sh` will live if it ever exists.
- **Pipelines between processes.** Genuinely useful, genuinely fiddly, and not needed by anything on the current list. Add when a caller exists.

---

## 4 · `lath/proc`, finding and signalling

Same package, separate concern.

```go
// Info describes a running process.
type Info struct {
    PID     int
    Command string // the full command line, as `pgrep -fl` reports it
}

// Find returns processes whose full command line matches. `pgrep -f`.
func Find(ctx context.Context, pattern *regexp.Regexp) ([]Info, error)

// SignalMatching sends sig to every matching process and reports which were hit.
func SignalMatching(ctx context.Context, pattern *regexp.Regexp, sig syscall.Signal) ([]Info, error)
```

⚠️ **This is the least portable thing proposed.** There is no standard-library way to enumerate processes. The implementation reads `/proc` on Linux and calls `ps` on macOS, which means one of the two paths is a subprocess after all, and Windows is unsupported.

That is worth accepting only because a real script needs it. The worker-kill script is entirely this, but it should be the ONLY place platform-specific code lives, behind build tags, with an explicit "unsupported on this platform" error rather than a silent empty result.

---

## 5 · `lath/repo`, locating the repository

```go
// Root returns the repository root, found by walking up from the working
// directory for the first marker present.
//
// Markers, in order: go.work, go.mod, .git. The first two suit a Go repo; .git
// is the fallback for a definition living in a repository of another language,
// which lath explicitly supports.
func Root() (string, error)

// RootFrom is Root, starting somewhere specific. Exists so tests need no chdir.
func RootFrom(dir string) (string, error)

// Path joins parts onto the repository root.
func Path(parts ...string) (string, error)

// MustPath is Path, panicking on failure. For a definition's package-level
// vars, where there is no error to return and no work worth doing without it.
func MustPath(parts ...string) string
```

### Sentinels

```go
var ErrNoRepo = errors.New("repo: no repository root above the working directory")
```

**Why not the executable's path:** a definition is compiled into a cache directory far from its source, so the binary's location says nothing about where the repository is. Walking from the working directory is the only reliable answer, and it is what a shell script gets free from `$0`.

---

## 6 · `lath/supervise`, keeping something running

```go
// Policy governs restarts.
type Policy struct {
    // Backoff is the first delay after a failure. Default 1s.
    Backoff time.Duration
    // Max caps it. Default 8s.
    Max time.Duration
    // HealthyAfter is how long a run must last to count as "worked, then
    // broke" rather than "never started". A run that long resets the backoff,
    // so a crash after an hour restarts immediately while a crash-on-boot
    // backs off. Default 15s.
    HealthyAfter time.Duration
    // MaxRestarts stops after N attempts. Zero means DefaultMaxRestarts (10);
    // use Unlimited for a supervisor that must never give up.
    //
    // Bounded by default because unlimited is right for a dev watchdog and
    // badly wrong for a CI step, where it turns a failing build into a job
    // that runs until the runner is killed.
    MaxRestarts int
    // Out receives progress lines. Nil discards them.
    Out io.Writer
}

// Loop runs fn until the context is cancelled, or MaxRestarts is reached.
//
// A returned error is a FAILURE TO RETRY, not a reason to stop. Only ctx
// cancellation ends the loop cleanly. That is the whole point of a supervisor
// and the single most important thing to get right — a supervisor that stops
// on the failure it exists to survive is worse than no supervisor.
func Loop(ctx context.Context, p Policy, fn func(context.Context) error) error

// Unlimited disables the restart limit. Negative rather than zero, so the zero
// value can mean "use the default" and an endless loop must be written out.
const Unlimited = -1

// Stats reports what happened, for a caller that wants to log or assert.
type Stats struct {
    Restarts   int
    LastError  error
    TotalUptime time.Duration
}
func LoopWithStats(ctx context.Context, p Policy, fn func(context.Context) error) (Stats, error)
```

---

## 7 · `lath/wait`, polling for a condition

```go
// Until polls cond until it returns true, ctx is cancelled, or timeout elapses.
func Until(ctx context.Context, cond func(context.Context) (bool, error), opts ...Option) error

func Interval(d time.Duration) Option  // default 250ms
func Timeout(d time.Duration) Option   // default 30s

// HTTPOK polls a URL until it answers with a 2xx.
//
// The common case by a wide margin — every health check and every smoke test —
// and the one people write a subtly wrong retry loop for: no timeout on the
// client, no context, no body drain.
func HTTPOK(ctx context.Context, url string, opts ...Option) error

var ErrTimeout = errors.New("wait: condition not met before timeout")
```

---

## 8 · `lath/scan`, walking and matching files

The lint-script shape. Included despite the near-parity measurement in §1, because the **exclusion** logic is what those scripts actually spend their lines on, and it is identical every time.

```go
// Filter selects files.
type Filter struct {
    Ext        []string // ".go"; empty means any
    Exclude    []string // path substrings — "vendor/", "/gen/"
    Include    []string // path substrings; empty means all. Exclude wins.
    SkipHidden bool     // drop dot-directories and dot-files
}

// Files walks root and returns paths matching the filter.
func Files(root string, f Filter) ([]string, error)

// Hit is one matching line.
type Hit struct {
    File string
    Line int    // 1-based
    Text string // the line, trimmed
}

// Match returns every line in files matching pattern.
func Match(files []string, pattern *regexp.Regexp) ([]Hit, error)

// Grep is Files followed by Match — the whole shape of a lint script.
func Grep(root string, f Filter, pattern *regexp.Regexp) ([]Hit, error)
```

⚠️ **The weakest case for inclusion.** `Grep` saves maybe 20 lines per lint script, and there are two of them. If it is cut, nothing else changes.

---

## 9 · `lath/out`, writing output

```go
// LogFile opens path for appending, creating parent directories.
// Close reports flush errors rather than discarding them.
func LogFile(path string) (io.WriteCloser, error)

// Prefix wraps a writer so every line is prefixed. "[guard] " and the like.
func Prefix(w io.Writer, prefix string) io.Writer
```

`Tee` is deliberately absent: it is `io.MultiWriter`.

---

## 10 · `lath/fsx`. The file operations with traps

```go
// WriteAtomic writes to a temporary file in the same directory, then renames.
// A reader never sees a partial file, and an interrupted write leaves the old
// one intact. The same-directory detail matters: rename is only atomic within
// a filesystem.
func WriteAtomic(path string, data []byte, perm os.FileMode) error

// CopyFile copies src to dst, preserving mode.
func CopyFile(src, dst string) error

// Exists reports whether a path exists, treating any error other than
// not-exist as an error rather than as absence.
func Exists(path string) (bool, error)
```

`EnsureDir` is absent, it is `os.MkdirAll`.

---

## 11 · `lath/env`, reading the environment

```go
// Require returns an environment variable, or an error naming what is missing.
// The point is the error: "DATABASE_URL is not set" beats an empty string
// surfacing three layers away as a connection failure.
func Require(name string) (string, error)

// RequireAll checks several at once and reports EVERY missing name, not the
// first — so one run tells you everything to fix.
func RequireAll(names ...string) error

// Get returns a variable or a fallback.
func Get(name, fallback string) string
```

---

## 12 · What `Guard` becomes

The measure of whether this is worth building.

```go
func Guard(ctx context.Context) error {
    root, err := repo.Root()
    if err != nil {
        return err
    }
    log, err := out.LogFile(filepath.Join(root, "tmp/air.log"))
    if err != nil {
        return err
    }
    defer log.Close()
    w := io.MultiWriter(os.Stdout, log)

    return supervise.Loop(ctx, supervise.Policy{Out: w}, func(ctx context.Context) error {
        r, err := proc.Run(ctx, "portless", []string{"--force", "acme-api", "air"},
            proc.Dir(filepath.Join(root, "apps/api")), proc.Out(w))
        if err != nil {
            return err
        }
        if !r.OK() {
            return fmt.Errorf("dev exited: %+v", r)
        }
        return nil
    })
}
```

**~20 lines against 148**, and the shell is 38.

---

## 13 · Testing

Every package is testable without a network, a container, or a fixture repository:

| Package | How |
|---|---|
| `proc` | run `go` itself, or `/bin/sh -c 'exit 3'`. Signals via a child that kills itself. Timeout via `sleep`. |
| `repo` | `t.TempDir()` with a marker file; `RootFrom` exists so no test needs to chdir |
| `supervise` | a `fn` that fails N times then succeeds; assert restart count and elapsed backoff |
| `wait` | a `cond` closing over a counter; `httptest.Server` for `HTTPOK` |
| `scan` | `t.TempDir()` with a known file tree |
| `fsx` | assert the temp file is gone and content is complete after `WriteAtomic` |
| `env` | `t.Setenv` |

Specific cases that must be pinned, because each is a bug someone will otherwise ship:

- `proc.Run` returns a **Result, not an error**, for a non-zero exit, and `Output` does the opposite.
- A signalled child reports `Signalled: true` and `128+signal`, and is distinguishable from an ordinary exit with the same number.
- `supervise.Loop` **keeps going** when `fn` returns an error, and stops only on ctx cancellation.
- `wait.Until` honours cancellation mid-interval rather than sleeping out the full period.
- `WriteAtomic` leaves no temporary file on failure.

---

## 14 · Not building

| | Why |
|---|---|
| a shell parser or interpreter | `lath/sh` later, if ever. `proc.Run(ctx, "sh", …)` covers it now. |
| process pipelines (`a \| b`) | fiddly, and nothing on the list needs it |
| an HTTP client wrapper | stdlib plus `wait.HTTPOK` is enough; a client wrapper is where opinions accumulate |
| a logging package | `pipeline.Reporter` exists for progress; anything else is the caller's choice |
| retries with jitter, circuit breakers | no caller |
| **any external dependency** | the constraint that makes lath a single binary |

---

## 15 · Review, answered

| # | Question | Answer |
|---|---|---|
| 1 | Is `scan` worth it? | **Yes.** The estimate that doubted it was wrong: measured against the finished package, the lint is 17 lines, not 43. Kept. |
| 2 | `proc.Find` cannot support Windows, accept? | **Accepted.** It shells out to `pgrep` and returns `ErrUnsupported` elsewhere. Stated at the top of `find.go`, where a caller reads it. Every other function in the kit is portable. |
| 3 | Seven packages, or fewer and bigger? | **More, not fewer.** At the scale this is heading for, a `lath/x` grab-bag is unfindable and every import pulls in everything. Small packages named for what they own are what Go's own stdlib does: `os`, `io`, `net/http`, not one `util`. |
| 4 | Should `MaxRestarts` default to unlimited? | **No, bounded, at 10.** Unlimited is right for a watchdog and badly wrong for a CI step, where it turns a failing build into a job that runs until the runner is killed. `Unlimited` is `-1`, so the zero value can mean "use the default" and an endless loop has to be written out. |
| 5 | Should `MustPath` panic? | **Yes, and it keeps the name.** `Must` is Go's established signal, `regexp.MustCompile`, `template.Must`. A reader knows from the name. |

---

## 16 · What changed while building

Drift from the spec above, and two defects the work uncovered.

### API drift

| Spec | Built | Why |
|---|---|---|
| `proc.Info{PID, PPID, Command, Args}` | `proc.Info{PID, Command}` | `pgrep -fl` reports a pid and the full command line, nothing else. `PPID` would need a second call per process, and `Args` was the same string as `Command`. Two fields that could not be filled honestly were removed rather than left empty. |
|, | `supervise.Unlimited`, `supervise.DefaultMaxRestarts` | Fell out of question 4: a bounded default needs a way to say "no bound". |
|, | `scan.Filter.SkipHidden` | Dot-directories are excluded by every lint script by hand; a field costs less than the substring entry repeated everywhere. |

### A data race in `proc`

`Process.timedOut` was written by the watchdog goroutine and read by `Wait`. Caught by `-race` on first run. It is now `atomic.Bool`.

The general shape is worth remembering: any field written by a goroutine started inside a constructor and read by a method is a race, however obviously ordered it looks.

### The dispatcher handed every target a context that never cancels

⚠️ The larger one, and it was in **lath itself**, not the kit.

The generated dispatcher declared `ctx := context.Background()`. That context is never cancelled, so a target taking one could not be interrupted. Every target wanting Ctrl-C to work had to install `signal.NotifyContext` itself.

It surfaced only because rewriting the dev watchdog on the kit removed its hand-rolled signal handling, and the watchdog stopped responding to Ctrl-C. Fixed in the template, so it is absorbed once:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
```

A task runner whose Ctrl-C does nothing is worse than useless, because it looks like it works. The test now pins the presence of `signal.NotifyContext`, the **absence** of a bare `context.Background()`, and that `context`/`os/signal`/`syscall` are imported exactly when a target needs them. An unused import would not compile.

### Six defects found by the edge-case pass

⚠️ Written after the kit was already "done", green, and in use. Every one of these was in code that passed its own tests.

| Where | Defect | Why the first round of tests missed it |
|---|---|---|
| `proc.Wait` | A second call **panicked**, `close of closed channel`. `Stop` calls `Wait` internally, so the ordinary shape of waiting on a process and then stopping it in a deferred cleanup crashed. | The first tests called `Wait` exactly once, which the doc comment said was the contract. The contract was wrong, not the code, `Stop` was already violating it. `Wait` is now idempotent: the first call reaps, later ones return the same result from memory. |
| `proc.Wait` | A failure that was not an `*exec.ExitError` (a pipe error, a reap that could not run) was reported as `ExitCode: -1` with a **nil error**. A genuine failure looked like a process that merely ended oddly. | Nothing produced a non-`ExitError` failure, because producing one takes deliberate effort. |
| `proc.EnvOnly` | A later `Env` cleared the isolation flag, so `EnvOnly("PATH=…")` followed by `Env("EXTRA=1")` handed the child **the entire parent environment**. Silent, order-dependent, and `EnvOnly` is what a caller reaches for to keep secrets out of a subprocess. | Each option was tested alone. The bug lived only in the combination. |
| `fsx.CopyFile` | `CopyFile(x, x)` **destroyed the file**: the truncating open emptied it before the copy read a byte. | Nobody writes that call on purpose. It happens when two paths that were meant to differ resolve to the same file. |
| `wait.Interval` | A non-positive interval **panicked** inside `time.NewTicker`. Reachable from `Interval(budget/attempts)` whenever the division floors to zero. | Zero is not a value you think to pass. |
| `supervise.MaxRestarts` | Only `-1` meant unlimited. Any other negative, from a computed limit, meant **give up after one attempt**, the exact inverse of what a negative bound reads as. | The constant was always passed by name. |

The pattern is worth naming, because it decides where the next tests go: **five of the six were in combinations and edge values, not in the main path.** Every function worked when called the way its author imagined. What broke was calling it twice, calling it with its own arguments, combining two options, or passing a value that arithmetic produced rather than a human typed.

### What is still uncovered, and why

`env`, `out`, `scan`, `supervise`, `wait` are at 100%. The rest:

| Package | Coverage | What remains |
|---|---|---|
| `proc` | 95.0% | 8 lines |
| `repo` | 91.3% | 2 lines |
| `fsx` | 90.5% | 4 lines |

Each remaining line falls into one of three categories, and the distinction matters more than the number.

**1 · Unreachable by construction, `proc`, 8 lines.** `Process.cmd.Process == nil` cannot happen: `Start` returns `(nil, err)` when the process fails to start, so no `*Process` with a nil inner process is ever handed out. Likewise the error paths of `Signal`, `Stop`'s two signal calls, and the non-`ExitError` branches of `wait`/`resultFrom`. The first require signalling a process the test does not own, the second a `cmd.Wait` failure that the idempotency fix now prevents. `Find`'s `Run` error sits behind an `Exists` check that has already passed. These are defensive branches guarding against a future refactor. They are not dead code to delete, but no test can reach them through the public API, and adding one would mean testing a constructor that does not exist.

**2 · Platform-dependent, `repo`, 2 lines.** `os.Getwd` failing is exercised on Linux and unreachable on macOS, where `Getwd` returns the kernel's cached path and succeeds even after the directory is removed, verified directly rather than assumed. The test is written and skips with a stated reason, and the skip is checked against `ErrNoRepo` so it cannot pass for the wrong cause. That check was added after the first version of the test passed vacuously: a temp directory has no marker above it, so the climb failed and the assertion was satisfied without `os.Getwd` ever erroring.

**3 · Would need a seam, `fsx`, 4 lines.** Failures of `Write`, `Chmod`, and `Close` on a temp file the function just created, plus the deferred `Close` in `CopyFile`. Reaching them means a full filesystem, a revoked descriptor, or an `RLIMIT_FSIZE` that raises a process-wide signal, none portable, and the last incompatible with parallel tests. The alternative is an interface seam through `WriteAtomic`, which is real API complexity in exchange for four `if err != nil { return fmt.Errorf(…) }` wraps. Declined deliberately. The consequence that would actually hurt. A failed write leaving a `.tmp-*` file behind, is covered from the outside, through a rename that fails against a directory.

⚠️ The first version of this section called all of it "fault-injection-only". That was wrong, and worth recording as a habit rather than a slip: it was a label that sounded like a reason, and it hid the fact that most of those lines were plainly reachable. Writing the fake `pgrep` took one helper and moved `proc` from 78% to 95%; `out` and `scan` reached 100% on three tests each. **Reach for the cheap external lever, PATH, permissions, a directory where a file is expected, before concluding a branch needs machinery.**

---

## 17 · The result

```
dev-guard.sh           38 lines
guard.go BEFORE kit   148 lines
guard.go WITH kit      42 lines
```

Near-parity with the shell script it replaces, while being typed, testable, and interruptible, and the same primitives now serve every other task.
