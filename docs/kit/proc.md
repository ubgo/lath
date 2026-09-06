# kit/proc

Running processes: start them, wait for them, signal them, find them.

```go
import "github.com/ubgo/lath/kit/proc"
```

## Rendering and inspecting a command

```go
proc.CommandLine("docker", args...)
// docker build -t ghcr.io/acme/app:a3f1 -f .docker/Dockerfile.prod .
```

`CommandLine` renders a command as a line a person can **paste into a shell**. Printing argv with `%v` gives `[build -t x .]`, which reads as the slice it is and cannot be run, and the whole value of showing an operator what a step would do is that they can then do it themselves. Quoting is conservative: anything outside `[A-Za-z0-9@%+=:,./_-]` is single-quoted, so a `$VAR` or backtick renders as itself.

```go
stdout, stderr := proc.Writers(opts...)
```

`Writers` resolves options to the streams a command's output should reach. Exported because `Run` is not the only implementation of "run a command". A `runner.Runner` may execute over ssh or in a container, and any of those must honour `Out` and `Split` or a caller that streams gets silence from every runner but the local one.

⚠️ **An `*os.File` passed to `Out` keeps its descriptor.** `os/exec` connects a file to the child directly and wraps every other writer in a pipe, which is what makes docker decide it is not talking to a terminal. Anything that wraps a writer on the way through (capturing, serialising) must leave files alone.

## The one thing to understand first

**A non-zero exit is data, not an error.**

```go
r, err := proc.Run(ctx, "docker", []string{"build", "."}, proc.Capture())
// err   != nil  →  the command could not be STARTED (docker is not installed)
// r.OK() == false → it ran and failed
```

That split exists because whether exit 1 is a failure depends entirely on the command: for `grep` it means "no match", for `test -e` it means "absent", for `docker build` it means the build broke. Forcing every caller through Go's error path would mean every one of them writes the same `errors.As` dance to get the code back.

`Output` makes the opposite choice, because it is for "give me the value" and a failed command has no value:

```go
commit, err := proc.Output(ctx, "git", []string{"rev-parse", "HEAD"})
// non-zero exit IS an error here, and it carries stderr
```

## Running

```go
func Run(ctx context.Context, name string, args []string, opts ...Option) (Result, error)
func Output(ctx context.Context, name string, args []string, opts ...Option) (string, error)
func Start(ctx context.Context, name string, args []string, opts ...Option) (*Process, error)
func Look(name string) (string, error)
func Exists(name string) bool
```

### Options

```go
proc.Dir("/srv/app")                   // working directory
proc.Env("KEY=value", "OTHER=x")       // ADDS to the parent environment
proc.EnvOnly("PATH=/usr/bin")          // REPLACES it entirely
proc.Stdin(reader)                     // feed it input
proc.Out(w)                            // stream stdout+stderr to w
proc.Split(outW, errW)                 // stream them separately
proc.Capture()                         // collect into Result.Stdout / .Stderr
proc.Timeout(30 * time.Second)         // SIGKILL after this
```

⚠️ **`Env` and `EnvOnly` compose in any order and neither discards the other.** `EnvOnly` decides whether the parent environment is inherited; `Env` only adds. An earlier version cleared that flag, so `EnvOnly` followed by `Env` silently handed the child the **entire parent environment**. An isolation that looked applied and was not.

```go
// Isolated, plus one variable. Secrets in the parent env do NOT reach the child.
proc.Run(ctx, "sh", []string{"-c", "env"},
    proc.EnvOnly("PATH="+os.Getenv("PATH")),
    proc.Env("BUILD_ID=42"))
```

## Result

```go
type Result struct {
    ExitCode  int             // 128+signal when Signalled; -1 when unknowable
    Signalled bool            // killed rather than exited on its own
    Signal    syscall.Signal
    Duration  time.Duration
    Stdout    []byte          // only with Capture; nil otherwise
    Stderr    []byte
}
func (r Result) OK() bool     // exit zero AND not signalled
func (r Result) String() string
```

⚠️ **`Signalled` is the field that matters.** A supervisor treating a signalled exit as a crash restarts something that was asked to stop, and loops forever if the signal keeps arriving. Note `OK()` is false for exit code 0 **with** `Signalled`. A process destroyed mid-work did not succeed.

`String()` names the signal, because `143` alone is the clue that costs an hour:

```
exit 143 (killed by terminated) after 1.2s
```

`Stdout`/`Stderr` are nil without `Capture` and an **empty slice** with it, so "captured nothing" and "did not capture" stay distinguishable.

## Long-running processes

```go
p, err := proc.Start(ctx, "myserver", []string{"--port", "8080"}, proc.Out(os.Stdout))
defer p.Stop(context.Background(), 5*time.Second)   // SIGTERM, then SIGKILL

r, err := p.Wait()
fmt.Println(p.PID())
p.Signal(syscall.SIGHUP)
```

**`Wait` is safe to call any number of times, from any goroutine.** The first call reaps; later ones return the same result from memory. That matters because `Stop` calls `Wait` internally, so *wait, then stop in a deferred cleanup*, the ordinary shape, would otherwise panic. Signalling an already-exited process is a no-op, not an error, since during shutdown it is usually already gone.

Cancelling the context sends **SIGTERM**, not SIGKILL: a server should get the chance to shut down cleanly. `proc.Timeout` uses SIGKILL and reports `ErrTimeout`.

## Finding processes

```go
func Find(ctx context.Context, pattern *regexp.Regexp, extra ...string) ([]Info, error)
func SignalMatching(ctx context.Context, pattern *regexp.Regexp, sig syscall.Signal, extra ...string) ([]Info, error)

type Info struct {
    PID     int
    Command string    // the full command line, as pgrep -fl reports it
}
```

```go
// Everything matching, anywhere
found, _ := proc.Find(ctx, regexp.MustCompile(`myserver --port`))

// Scoped — extra reaches every pgrep flag
mine, _ := proc.Find(ctx, regexp.MustCompile("node"), "-u", os.Getenv("USER"))

// Signal them, and learn what happened
found, err := proc.SignalMatching(ctx, pattern, syscall.SIGTERM)
```

`Find` **never returns the calling process**, so "is my service already running?" cannot answer yes because of you. A process that exits between the scan and the signal is not an error, that is the outcome wanted, but a refused signal is reported, with the matches still returned so a caller can say what happened.

⚠️ **There are no signals on Windows.** `Signal` terminates the process instead, because Go accepts only `os.Kill` there and anything else fails with *"not supported by windows"*. That failure used to propagate, which meant `Stop`, `Timeout` and context cancellation all did nothing on that platform: a cancelled command sat until it finished on its own. A process nobody can stop is worse than no `Stop` at all, so the signal becomes a kill — and the cost is stated rather than hidden. `GracefulStopSupported` is the constant to read: `false` there means a process gets no chance to flush, close or release a port, and no amount of `grace` changes it.

⚠️ **`Find` and `SignalMatching` need `pgrep` and are unsupported on Windows**, returning `ErrUnsupported`. They report that rather than "nothing is running", because a caller must never be told nothing matched when the truth is "could not look". Everything else in the package is portable.

## Errors

| | |
|---|---|
| `ErrNotFound` | the program is not on PATH |
| `ErrTimeout` | `proc.Timeout` elapsed and the process was killed |
| `ErrUnsupported` | this platform cannot do it (`Find` on Windows) |

## Gotchas

**`sh -c 'sleep 30 # marker'` loses the marker.** The shell execs `sleep` directly, replacing itself, so the comment is gone from argv and `Find` will not match it. Use a compound command, `: marker; sleep 30`, to keep the shell alive.

**Large output does not deadlock.** Output is drained while the process runs, so a command producing megabytes completes rather than blocking on a full pipe at around 64KB.
