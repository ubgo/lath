# pipeline, the engine

`github.com/ubgo/lath/pipeline`

An ordered sequence of steps, the values that flow between them, and the wiring check that proves the order is possible **before** anything runs.

Zero external dependencies. Nothing in it knows about docker, git, ssh or lath. It is a library any Go program can use to sequence work.

```go
p := pipeline.Pipeline{Name: "deploy-prod", Steps: []pipeline.Step{
    common.ResolveEnv{Environment: "prod"},
    git.ResolveCommit{},
    docker.Build{Repository: "ghcr.io/acme/app"},
}}
st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(os.Stdout))
if err := p.Run(ctx, st); err != nil {
    return err
}
```

---

## The four concepts

| | What it is |
|---|---|
| **`Step`** | One unit of work. Declares what it reads, what it writes, and how to do it. |
| **`State`** | The bag of values flowing between steps, plus the output sink. |
| **`Key`** | The name of one value in that bag. `KeyCommit`, `KeyImage`. |
| **`Reporter`** | Where human-facing output goes. Terminal, buffer, JSON, nowhere. |

---

## Step

```go
type Step interface {
    Name() string             // stable identifier, used in output and errors
    Requires() []Key          // the state keys Run reads
    Provides() []Key          // the state keys Run sets
    Run(ctx context.Context, s *State) error
}
```

`Requires` and `Provides` are **not** bookkeeping. `Validate` uses them to prove the pipeline can work, so putting `Build` before `ResolveCommit` is an error printed before any step executes rather than a failure three minutes in.

A step may also implement `Validator` to check its own configuration:

```go
type Validator interface {
    Validate() error          // a missing field, a value outside a closed set
}
```

### Func, a step without a type

```go
pipeline.Func{
    Label: "write-env-file",
    Needs: []pipeline.Key{common.KeyEnv},
    Gives: nil,
    Do: func(ctx context.Context, s *pipeline.State) error { ... },
}
```

The fields are `Label`/`Needs`/`Gives`/`Do` rather than `Name`/`Requires`/`Provides`/`Run` because Go forbids a field and a method sharing a name. `Needs` and `Gives` carry exactly the same obligations as a named step's, `Validate` trusts them.

---

## State

```go
func NewState(mode Mode, r Reporter, opts ...StateOption) *State

func Set[T any](s *State, k Key, v T)
func Get[T any](s *State, k Key) (T, error)
func Has(s *State, k Key) bool

func (s *State) Mode() Mode
func (s *State) DryRun() bool
func (s *State) Detailf(format string, args ...any)
```

`Get` **errors on absence** rather than returning a zero value. A zero value here is an empty image tag or an empty host, which does not fail at the read. It fails several steps later as an inexplicable `docker: invalid reference`, far from the cause. Failing at the read names the missing key.

⚠️ **Not safe for concurrent use.** Steps run sequentially by design. A step that fans out internally must not share `State` across goroutines.

### Mode and the dry-run contract

```go
const (
    ModeExecute Mode = "execute"    // performs real side effects
    ModeDryRun  Mode = "dry-run"    // runs the logic, suppresses the effects
)
var ModeValues = []Mode{ModeExecute, ModeDryRun}
```

⚠️ **A step MUST still `Set` every key it declares in `Provides`, even under dry run.** Skipping the `Set` makes every downstream step fail with `ErrKeyMissing`, so the dry run exercises a different code path than the real one, which defeats the purpose of having it.

```go
func (b Build) Run(ctx context.Context, s *pipeline.State) error {
    image := fmt.Sprintf("%s:%s", b.Repository, commit)
    pipeline.Set(s, common.KeyImage, image)     // BEFORE the dry-run branch
    if s.DryRun() {
        s.Detailf("would build %s", image)
        return nil
    }
    return client.Build(ctx, opts)
}
```

---

## Reporter, where output goes

This is the piece people find surprising, so it gets the longest section.

### The rule: steps never print

Real code from `steps/git`, the line that produces `commit=0f3747e branch=dev`:

```go
s.Detailf("commit=%s branch=%s", commit, branch)
```

Not `fmt.Println`. The step hands the message to `State`, which hands it to whatever `Reporter` was plugged in. **The step has no idea where the text ends up**, terminal, test buffer, socket, nowhere.

### The interface

```go
type Reporter interface {
    StepStart(index, total int, name string)                     // about to run a step
    StepDone(index, total int, name string, took time.Duration)  // it finished
    Detailf(format string, args ...any)                          // a step describing itself
}
```

…plus one optional upgrade for raw command output, covered below:

```go
type OutputReporter interface {
    Reporter
    Output(line string)
}
```

Three methods. Anything implementing them can receive a pipeline's entire output.

It is deliberately **not a logger.** This is the command's *product*, what a person reads while watching a deploy. Not diagnostic logging, which belongs behind the importing application's own logging stack.

### The implementations that ship

**`TextReporter`**, formats as text to any `io.Writer`:

```go
func (r *TextReporter) StepStart(index, total int, name string) {
    fmt.Fprintf(r.W, "→ %d/%d %s\n", index, total, name)
}
```

That one `Fprintf` is the entire reason your terminal shows `→ 2/14 resolve-commit`.

**`discardReporter`**. The unexported zero-value fallback, so a `State` built without a reporter cannot nil-panic mid-run. A crash in output plumbing must never abort a real deploy. `NewTextReporter(io.Discard)` is the supported way to ask for silence explicitly.

### One line, end to end

```
steps/git/git.go
   s.Detailf("commit=%s branch=%s", "0f3747e", "dev")
        │
        ▼
   State.Detailf  ──▶  forwards to whichever Reporter NewState was given
        │
        ├── TextReporter(os.Stdout) →  "    commit=0f3747e branch=dev"
        ├── TextReporter(&buf)      →  captured in a test, asserted on
        ├── discardReporter         →  nothing
        └── your own                →  JSON, a socket, a log aggregator, a TUI
```

The step is byte-identical in every case. Only the thing plugged in at the bottom differs.

### Why it is an interface

Had `ResolveCommit` called `fmt.Println` directly, that text would go to the terminal and **nowhere else, ever**. Routing it anywhere would mean editing every step.

Because it calls `s.Detailf`, sending an entire pipeline's output somewhere new means writing **one type with three methods** and touching no steps at all:

```go
// Send everything to a TUI over a socket, as newline-delimited JSON.
type socketReporter struct{ enc *json.Encoder }

func (r *socketReporter) StepStart(i, total int, name string) {
    r.enc.Encode(event{T: "start", N: i, Total: total, Name: name})
}
func (r *socketReporter) StepDone(i, total int, name string, took time.Duration) {
    r.enc.Encode(event{T: "done", N: i, Name: name, Took: took.String()})
}
func (r *socketReporter) Detailf(f string, a ...any) {
    r.enc.Encode(event{T: "detail", Msg: fmt.Sprintf(f, a...)})
}
```

Twelve lines, stdlib only. This is the events half of the [step debugger](../DEBUGGER.md).

⚠️ **A `Reporter` must not panic and should not block.** It sits on the path of every step in a running deploy. Anything slow, a network write, an fsync, belongs behind a buffer the reporter owns.

---

## Debugger, stepping through a run

With a `Debugger` attached, `Run` pauses after each step and asks what to do next. This is what `lath run <target> --debug` plus `lath tui` drive; see [`../DEBUGGER.md`](../DEBUGGER.md) for the whole design.

```go
type Debugger interface {
    Plan(pipeline string, steps []PlanEntry) error
    Pause(p Pause) (Action, error)      // BLOCKS until the operator decides
    Finish(f Finished) error
}

const (
    ActNext     Action = "next"      // run the next step, pause again
    ActRerun    Action = "rerun"     // run the step that just ran, again
    ActContinue Action = "continue"  // stop pausing, run to the end
    ActQuit     Action = "quit"      // abandon the run
)
var ActionValues = []Action{ActNext, ActRerun, ActContinue, ActQuit}

func AttachDebugger(d Debugger)     // arms every State created afterwards
func Debug(d Debugger) StateOption  // arms one State explicitly
```

`AttachDebugger` is how the generated dispatcher arms a session, so **a definition needs no changes to be steppable**. `Debug` is the explicit form, for an embedder or a test. A process-wide value cannot be used by parallel tests.

⚠️ **Without a debugger, behaviour is byte-identical to before.** With one, a failing step **pauses** instead of ending the run, so it can be retried after fixing whatever broke.

⚠️ **There is no pause after the last step succeeds.** Nothing is left to decide, and offering the choice would let closing a window turn a completed run into an abandoned one. The final state rides on `Finished` instead. A failure still pauses wherever it happens.

### Replayable, which steps may be replayed without asking

```go
type Replayable interface{ Replayable() bool }
```

An optional upgrade. A step that does not implement it is treated as unsafe, and a client confirms before replaying it. The honest default, since wrongly assuming safe costs a duplicate container and wrongly assuming unsafe costs one keystroke. `Func` declares it with the `Idempotent` field.

⚠️ **`rerun` replays a step; it does not undo one.** State is snapshotted and restored, so the step sees the inputs it originally saw, but nothing can un-`docker run` a container.

### ReporterSource, output reaching a client

```go
type ReporterSource interface{ Reporter() Reporter }
type DebugReporter interface{ Reporter; IsDebugReporter() }
```

A definition builds its own `State` with its own `Reporter`, and the dispatcher runs before that call, so `NewState` wraps the caller's Reporter alongside the debugger's whenever one is armed. The caller's is never replaced, only accompanied: the terminal a run was started in keeps scrolling. A caller who passes the debugger's reporter explicitly is detected via `DebugReporter` and it is not applied twice.

### FakeDebugger, driving pipelines in tests

```go
d := &pipeline.FakeDebugger{Script: []pipeline.Action{
    pipeline.ActNext, pipeline.ActRerun, pipeline.ActNext,
}}
st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.Debug(d))
err := p.Run(ctx, st)

d.Steps()       // the step at each pause, replays included
d.Pauses()      // every Pause, with state and Replayable
d.FinalState()  // what the run ended up producing
```

Not in a `_test.go` file, for the same reason `runner.Fake` is not: every package building on the debugger needs to drive a pipeline without a socket or a terminal. Running out of script is an error rather than an implicit continue, because a pipeline that quietly ran past the end of its script asserts nothing.

### Output, what a command printed

`Detailf` is a step describing itself. **`State.Output()` is what the tool it ran actually printed**, and the two are deliberately separate: a step emits two or three Detailf lines worth always showing, while one `docker build` emits hundreds worth collapsing.

```go
func (b Build) Run(ctx context.Context, s *pipeline.State) error {
    stream := s.Output()
    defer stream.Close()                    // flushes a trailing partial line

    return dockerkit.On(b.Runner).WithOutput(stream).Build(ctx, opts)
}
```

Every line the command prints now reaches the terminal, and any attached debugger, as it happens. Before this existed a two-minute build printed nothing at all, which is indistinguishable from a hang and impossible to debug from.

```go
type OutputReporter interface {
    Reporter
    Output(line string)      // one line, no trailing newline
}
```

An **optional** upgrade: a Reporter that does not implement it still receives the output, through `Detailf`. Nothing is ever silently dropped, only rendered less precisely.

⚠️ **Close the writer.** A final line without a trailing newline is otherwise lost, and that is not a rare case, it is exactly how a progress bar's last frame and an unterminated prompt arrive.

### OutputWriter. The writer a step hands to a command

`State.Output()` returns an `*OutputWriter`. It splits arbitrary write chunks into lines. A process's output arrives half a line at a time, and carries each to the reporter.

⚠️ **Close it.** A final line with no trailing newline is otherwise lost, and that is exactly how a progress bar's last frame and an unterminated prompt arrive. `Close` is idempotent, so a deferred call after an explicit one is safe.

### Passthrough, letting a tool own the terminal

```go
if f := s.Passthrough(); f != nil {
    client = dockerkit.On(nil).WithTerminal(f)   // docker's own live progress
} else {
    client = dockerkit.On(nil).WithOutput(s.Output())
}
```

`State.Passthrough` returns the terminal a run's output goes to, or nil. A tool given a real terminal renders far better output. Docker draws a self-updating build table on a TTY and a flat log on a pipe, and capturing is precisely what downgrades it, because `os/exec` connects an `*os.File` by descriptor and wraps every other writer in a pipe.

It returns nil whenever something needs the output as *lines* rather than bytes on a screen: a debugger is attached and its panel would be corrupted by cursor-movement escapes; the reporter is not a plain `TextReporter`; or the destination is not a character device.

⚠️ **Never pass an output line as `Detailf`'s format string.** Docker output is full of percent signs; `Detailf(line)` renders `100%` as `%!(NOVERB)` and loses the content. `State.Output` passes it as an argument for exactly this reason.

Carriage returns are stripped, so a progress bar redrawing itself does not leave control characters in a log file.

### Yielding the terminal to a step

```go
pipeline.YieldTerminal()          // set by the runner when it owns this terminal
pipeline.ResetTerminalYield()     // undo; for tests, which cannot otherwise reset it
```

A debugger's panel and a tool's own progress display cannot share a screen: docker redraws its build table with cursor movement, twenty thousand escape sequences in one build, and those would shred a frame drawn around them, so by default an attached debugger takes the output as lines.

`YieldTerminal` chooses the other arrangement: **take turns.** The panel draws only when the run is paused; while a step runs it stays silent and the tool draws instead. Nothing overlaps, because at any moment exactly one of them is executing. Set only when the runner launched the run into a terminal it owns, never when attaching to a run started elsewhere, whose output goes to *its* terminal.

```go
if err := pipeline.DebuggerError(); err != nil { … }
```

`DebuggerError` reports why an armed debugger could not be opened, almost always "nobody attached". `NewState` cannot fail, so a failure to attach must not abort a run mid-construction; it is reported here instead, and the runner turns it into a non-zero exit. A run nobody could supervise is a failed run, not a quiet one.

## Validate, the check that runs first

```go
func (p Pipeline) Validate() error
```

Called automatically by `Run`, and worth calling yourself in a `plan` command. It rejects:

| Error | Meaning |
|---|---|
| `ErrNoSteps` | An empty pipeline. An error rather than a no-op success, because "ran fine, changed nothing" is indistinguishable from a real deploy in a log. |
| `ErrNotWired` | A step requires a key no earlier step provides. The check that turns a class of 2am failures into a message printed before anything runs. |
| `ErrDuplicateProvider` | Two steps both claim one key. Banned because it makes the value a step reads depend on ordering nothing enforces. The second write silently wins. |
| `ErrStepConfig` | A step's own `Validate` failed. The definition file is wrong; retrying will not help. |

⚠️ **`Validate` proves the order is POSSIBLE, not correct.** It cannot know that migrations must precede start, or that a health check must precede a traffic switch. Nothing in `Requires`/`Provides` expresses those. Ordering invariants belong in a comment where the pipeline is assembled.

---

## Plan, describe without executing

```go
func (p Pipeline) Plan() []PlanEntry

type PlanEntry struct {
    Position int    `json:"position"`   // 1-based
    Total    int    `json:"total"`
    Name     string `json:"name"`
    Requires []Key  `json:"requires,omitempty"`
    Provides []Key  `json:"provides,omitempty"`
}
```

Runs nothing. The JSON tags are there so a plan can be handed to another program.

```
  1/14  resolve-env        writes=[env]
  2/14  resolve-commit     writes=[commit branch]
  3/14  build-image        reads=[commit branch env] writes=[image]
```

---

## Listing what would run, without running it

`lath plan <target>` lists a pipeline's steps and executes none of them. `lath dry-run <target>` runs every step's logic with its side effects suppressed. **Neither needs the definition to offer anything**: the runner sets `LATH_MODE` and the engine does the rest.

```
$ lath plan deploy run prod

deploy-prod: 19 step(s), nothing executed

   1/19  resolve-env          writes=[env]
   2/19  resolve-commit       writes=[commit branch]
   3/19  build-image          reads=[commit branch env] writes=[image]
   …
```

| Symbol | What |
|---|---|
| `ModeEnvVar` | `LATH_MODE`. The runner sets it; a definition never sets it |
| `ModeEnvPlan` · `ModeEnvDryRun` | The two values it takes |
| `Planning()` | The one question a definition may ask: "am I only being described?" |
| `PlanWriter(w)` | A `StateOption` sending the listing somewhere other than stderr |
| `PlanFormatEnvVar` | `LATH_PLAN_FORMAT`, set by `lath plan --json` |
| `PlanFormatText` · `PlanFormatJSON` | The two renderings. Anything else, including empty, is text |
| `Documented` | The optional interface a step implements to explain itself |
| `Docs` | `Summary` and `Detail`, also on `PlanEntry` for a client reading `Plan()` |

A step's **name** says what kind of thing happens. Its **summary** says what it is for, fixed, exactly like a CLI command's one-line help. Its **detail** says what will happen this time, with the values resolved:

```
   7/19  ensure-dns           point this environment's hostname at the box
         cloudflare: A acme-api.example.com -> 203.0.113.7, proxied=true
```

Without them a plan lists `ensure-dns` and the reader cannot tell what it is for, nor whether it means Cloudflare, Route 53 or a hosts file, which is precisely what they opened the plan to find out. `pipeline.Func` carries `Summary` and `Detail` fields for the same purpose, and every shipped step in `steps/` implements this.

⚠️ **Never put a credential in either**: both are printed.

**Forcing only ever makes a run safer.** `LATH_MODE` can turn an execute into a dry run, never the reverse, and an unrecognised value changes nothing. A runner flag that could turn someone's rehearsal into a real deploy would be a footgun with the safety filed off.

**`Validate` still runs.** A plan that describes a pipeline which could never work is worse than an error: it is a confident wrong answer.

```sh
lath plan deploy run staging --json | jq -r '.steps[] | "\(.position)\t\(.name)\t\(.detail)"'
```

JSON goes to **stdout**, where a pipe expects it; the human listing stays on stderr so it never mixes with what the definition itself prints. The steps are the same `PlanEntry` values `Plan()` returns, so a script and a reader see one answer rather than two renderings that can drift.

⚠️ **A definition that prints its own output should check `Planning()` first.** Its text lands on stdout beside the JSON otherwise, and one stray human line turns a machine format into a parse error.

⚠️ **`Planning()` is for skipping work, never for changing shape.** A definition may use it to skip credential checks or a network lookup that assembling would otherwise demand, which is what keeps `lath plan` working on a definition nobody has finished configuring. Skipping something that decides which steps exist would make the plan describe a pipeline other than the one that runs, which is the one thing a plan must never do.

⚠️ **Code before the pipeline still runs.** These modes act on `Pipeline.Run`, so a target that writes a file by hand before assembling anything still writes it. Every pipeline is forced to a dry run as well, which covers the steps, not the statements around them.

## Finding your own repository

A definition often needs a file from the project it deploys: the env file at the root, a credential under `_keys`, a Dockerfile path. It cannot find the root by walking upward for a `go.mod`, because **the definition is itself a module**, so that search stops at `.lath` and answers with the definition's own directory. That is wrong in the two places hardest to notice: `go test` inside the definition, and `lath run` issued from a subdirectory of a monorepo.

The runner already knows the answer, so it states it. `lath` sets `LATH_ROOT` in the environment of every definition it executes, and `ProjectRoot` reads it:

```go
root, err := pipeline.ProjectRoot()      // the directory CONTAINING .lath
if err != nil {
    return err
}
content, err := secret.FromFile(filepath.Join(root, ".env.prod"))
```

| Symbol | What |
|---|---|
| `ProjectRoot()` | The project root: `RootEnvVar` when set, otherwise an upward search for a directory containing `DefinitionDir`. Returns `ErrNoRoot` when neither answers |
| `MustProjectRoot()` | The same, panicking instead. For a definition that cannot proceed without one, which is most of them |
| `RootEnvVar` | `LATH_ROOT`, the variable the runner sets |
| `DefinitionDir` | `.lath`, what the fallback searches for |

The fallback exists for `go test`, where there is no runner to ask. It searches for a directory that CONTAINS `.lath`, which is the definition of a project root rather than a proxy for it. A test wanting a different answer sets `RootEnvVar`.

## Errors

Every error from this package wraps exactly one sentinel, so a caller can branch with `errors.Is` rather than matching message text.

| Sentinel | Cause |
|---|---|
| `ErrNoSteps` | empty pipeline |
| `ErrNotWired` | a required key has no provider |
| `ErrDuplicateProvider` | two steps provide one key |
| `ErrStepConfig` | a step's configuration is invalid |
| `ErrStepFailed` | a step's `Run` returned an error |
| `ErrKeyMissing` | a step read a key nothing provided |
| `ErrKeyType` | a key held a different type than the reader expected |
| `ErrAborted` | a debugger's operator abandoned the run, or the client vanished |

`ErrKeyMissing` is unreachable in a validated pipeline. Seeing it at run time means a step's `Requires()` under-reports what its `Run()` actually reads.

---

## See also

- [`../steps/README.md`](../steps/README.md). The adapters that implement `Step`
- [`../kit/README.md`](../kit/README.md). The libraries those adapters delegate to
- [`../DEBUGGING.md`](../DEBUGGING.md), stepping through a pipeline, from the operator's side
- [`debug.md`](debug.md). The debug transport: protocol, server, client
- [`../DEBUGGER.md`](../DEBUGGER.md), why the debugger is shaped the way it is
