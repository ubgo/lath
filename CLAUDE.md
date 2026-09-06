# lath, agent & developer guide

lath is a task runner whose definitions are **type-safe Go**, not YAML. A repository keeps its tasks in `.lath/`; lath discovers the exported functions with `go/ast`, generates a dispatcher, compiles it, caches by content hash, and `syscall.Exec`s the result.

Its purpose is one pipeline that runs identically on a laptop and in CI. A deploy you cannot rehearse locally is a deploy you find out about in production.

**New session? Read `docsi/HANDOVER.md` first, if the overlay is set up.** It is the orientation page: why lath exists, what state it is in, who actually uses it, where the last session left off, and what is open. It lives in the private overlay rather than the repository because it names local paths and the private consumers that drive lath's development, and this repository is public. See [The private overlay](#the-private-overlay). This file is the working guide and stands alone without it.

## Repository layout

```
go.work             the ONLY file at the root — the root is not a module
cmd/lath/           github.com/ubgo/lath/cmd/lath   the runner
kit/                github.com/ubgo/lath/kit        libraries
pipeline/           github.com/ubgo/lath/pipeline   the Step contract
steps/              github.com/ubgo/lath/steps      common/ docker/ git/
example/            a stand-in for a user's repository (its own module)
docs/               design documents
```

Four modules, each versioned independently. **The repository root is deliberately not a module**, so a `go.mod` and a `go.work` never sit side by side leaving it ambiguous which governs. `go build ./...` therefore does not work from the root, use `go build github.com/ubgo/lath/...`, which resolves across the workspace.

⚠️ **Packages live exactly where their import paths say, and this is load-bearing.** Go requires a module path to equal the repository path plus its subdirectory. A module declaring `github.com/ubgo/lath/kit` while sitting in `modules/kit` is unfetchable by anyone outside this checkout. The proxy would demand `github.com/ubgo/lath/modules/kit`. An earlier layout had exactly that, and it worked only because `replace` directives and `go.work` hid the mismatch. Never move a package without moving its import path with it.

Dependencies: `kit` and `pipeline` depend on nothing; `steps` depends on both. `cmd/lath` compiles a definition and execs it, so it does not import the libraries a definition uses. With one deliberate exception: `kit/session` and `pipeline/debug`, which `lath tui` needs to discover a debug session and speak its protocol. That cost was placed on the runner on purpose; the alternative was putting the client side in `pipeline`, which would have added a dependency to every project that ever uses lath.

A definition requires `pipeline` and `steps`, and picks up `kit` indirectly. Install the runner with `go install github.com/ubgo/lath/cmd/lath@latest`.

## Where does new code go?, one question

**Does it import `pipeline`?**

- **No** → it is a library. It belongs in `kit/`, and any Go program can use it whether or not that program has heard of lath.
- **Yes** → it is a pipeline adapter. It belongs in `steps/`.

That is the entire rule. It is checked mechanically: no `kit` package imports `pipeline`, and every `steps` package does.

A kit package **may** depend on an external program, `proc` needs `pgrep`, `lock` needs `ps`, `ssh` needs `ssh`, `docker` needs `docker`. It must document that at the point of use and error clearly when the program is absent. It may not depend on `pipeline`.

⚠️ **Two placement rules were tried and removed. Do not reintroduce them.**

**"Nothing in kit may name a vendor."** This exiled `docker` and `git` to their own modules while `kit` was already shelling out to `pgrep`, `ps`, `ssh`, `scp` and `sh`. The distinction it claimed to draw did not exist, and the resulting layout could not be explained from a directory listing.

**"Step packages live outside the module tree to signal they are not core."** A directory cannot carry that meaning, and the move broke this repo's own stated convention. What actually keeps step packages unprivileged is structural: the runner has no build-time knowledge of them, there is no registry, and nothing there implements an interface a third party could not. A third party's steps live in their own repository regardless of how this one is arranged.

## The three layers

| Layer | Test | Examples |
|---|---|---|
| `kit/` | could a program that never heard of lath use it? | `proc`, `docker`, `git`, `secret`, `ssh`, `runner` |
| `pipeline/` | is it the Step contract or the engine? | `Step`, `State`, `Pipeline`, `Key` |
| `steps/` | does it import `pipeline`? | `common.ResolveEnv`, `docker.Build`, `git.ResolveCommit` |

Dependencies point one way only: `steps → kit + pipeline`. Never the reverse.

`kit/docker` is "how to drive docker". `steps/docker` is "how a docker operation fits into a pipeline". Same subject, different job, which is why the step package imports the kit package under the alias `dockerkit`, and never the other way round.

## Definitions

A definition is a **nested Go module** at `.lath/`, deliberately absent from any `go.work`, so its dependencies never enter the application's module graph or `vendor/`. Build tags were measured and rejected for this: `go mod tidy` adds the dependencies anyway, and a tagged file cannot compile without them.

**Every exported function is a command.** There is no registration list and no `func main`. Only these shapes are dispatchable:

```go
func Name()
func Name() error
func Name(ctx context.Context) error
func Name(args ...string) error
func Name(ctx context.Context, args ...string) error
```

⚠️ `args ...string`, **not** `args []string`. A slice is silently not a command. lath warns about rejected functions on every run. That warning exists because three targets with the wrong signature once vanished without a word.

One subdirectory level is a command namespace: `.lath/secrets/push.go` → `lath run secrets push`. `internal/` is skipped, so helper packages live there.

Commands are `lath run <target>`; lath's own verbs are `run`, `list`, `init`, `cache`, `version`, `help`. There are **zero reserved words**. A target may be named `Init`, `Cache`, even `Run`.

`go build ./...` fails inside a definition and that is expected: it is `package main` with no `func main`, because lath generates the entry point. `go vet`, `go test` and `gofmt` all work normally.

## lath builds steps; projects build workflows

lath has `run`, `plan`, `dry-run`, `list`, `tui`, `init`, `cache`, `version`. It will not grow `deploy`, `rollback`, `promote`, `restart` or `scale`, and refusing them is a position, not an omission.

Every one of those is a sentence with a project-specific object. "Roll back" to WHICH version: the previous git commit, the previous image on the box, the last one that passed a health check, the one before the migration? And does it re-run migrations, or is it code-only because the schema moved on? Those answers differ per project and change over time, and a verb that guessed them would be wrong in a way nobody could see from the outside.

So the engine ships MECHANISMS, `docker.UseImage`, `RunOnce`, `WaitHealthy`, `StopPrevious`, and a project assembles them into a target it owns:

```go
// lath run deploy rollback prod 0375fcf
func Rollback(ctx context.Context, args ...string) error { … }
```

That target is thirty lines of the deploy pipeline with `build-image`, `push-image` and the migration left out, and the leaving-out is the interesting part. It is where a project states that its migrations are additive so older code tolerates a newer schema, or that they are not and a rollback needs a restore. A built-in verb has nowhere to put that sentence.

The same rule already decided two names here. `docker.Migrate` became `RunOnce` because running a command in the new image is the mechanism and migrating is one use of it. `common.SwitchTraffic` became `Request` because calling an endpoint is the mechanism and switching traffic is one use of it. When a proposed feature is a *use* rather than a *mechanism*, it belongs in a definition.

**The test before adding anything to `steps/`:** would two projects want it to mean different things? If yes, ship the pieces and let each say what it means.

## The compile cache

The key covers the definition's `.go`/`go.mod`/`go.sum` files, **plus** lath's own version, `runtime.Version()`, the executable's size and mtime, **and every locally-`replace`d module, transitively**.

Each of those was added after a real stale-binary incident. The worst was the last: a definition under development replaces its engine modules with local paths, so editing the engine changed nothing the hash could see. lath reused a stale binary while the evidence said the new code was running, worse than a plain failure.

## Rules learned the hard way

**A step must never narrate.** Every step in `steps` once described what it would do and returned nil, so a real deploy printed `docker build -f Dockerfile . -> repo:abc123` and exited zero having built nothing. If a step cannot do its job it must fail. `Plan` describes intent; `Run` does the work or errors.

**"Unsupported" in a comment is not "unsupported" to a compiler.** `proc` documented `Find` as unsupported on Windows and returned `ErrUnsupported`, but the file called `syscall.Kill`, which does not exist there, so the package and everything importing it failed to *build* for that platform, undetected for a month. `task crosscheck` now builds every module for linux, darwin and windows and is wired into `task check`. Verify a guard by breaking it: delete the shim, confirm the gate fails, restore it.

**A container being up is not an application working.** `wait-healthy` twice reported success on a dead app: once because a crash-looping container is briefly running between `docker run` and its first fatal error, and once because supervisord stayed up while every process it managed was in `FATAL`. It now prefers the image's own `HEALTHCHECK` when declared, and otherwise requires no restarts plus a settle window.

**`docker --env-file` is not a shell.** It takes everything after `=` literally, so `NATS_STATUS="false"` arrives as the seven-character string `"false"`. Generated env files must not quote.

**Extracting a primitive does not shrink the first caller.** Measured: the secrets pipeline went 375 → 369 lines while gaining pruning and partial-failure reporting. The saving is at the *second* caller. Never justify an extraction with the first call site's line count.

**A vendor name in a function is not proof it is policy.** A 43-line "push to GitHub" was 6 lines of `gh` and 37 lines of reusable orchestration. Ask what remains when the vendor call is replaced by an interface. Check the mirror case too: code naming no vendor at all can be pure policy.

## Safety invariants

These are load-bearing. Changing one needs a reason written down.

- **`secret.Value` cannot be printed.** `String`, `GoString`, `Format`, `MarshalJSON`, `MarshalText` and `LogValue` are all closed; JSON has no plaintext path at all. `Reveal()` is the only door, and it is named to be greppable: `grep -rn "\.Reveal()" --include="*.go" | grep -v _test` lists every point where plaintext leaves the type. Inside lath that is three, `secret.Set.WriteFile`, the registry login, and `common.PutFile`. Each a deliberate handoff to something that needs the real value. Definitions add their own; the number is meant to stay small enough to read in one screen, and a growing one is the signal to look.
- **Credentials go on stdin, never argv.** An argument is visible in the process list to every user on the host.
- **Files holding credentials are written 0600**, with the `umask` set *before* the redirect so there is no readable window. `secret.Set.WriteFile` refuses a looser mode rather than warning.
- **Published ports bind `127.0.0.1`**, never `0.0.0.0`. A container bound to all interfaces is reachable from the internet regardless of the proxy in front of it.
- **`StopPrevious` requires a `NamePrefix`** so it cannot stop containers it does not own.
- **Images are tagged by commit**, never by a moving tag alone, or "what is running in production" becomes unanswerable.
- **A dry run performs no writes, no network mutations, and no provisioning**, but still `Set`s every key it `Provides`, or a rehearsal exercises a different path than the real thing.

## Testing conventions

`kit/runner.Fake` is the shared test double, exported deliberately, not in a `_test` file, because `docker`, `git` and `steps` all need it and three copies is how they drift apart. It records every command and replies from a script, so command lines are asserted with **no docker, no git and no network**.

Command-line construction is exported (`docker.BuildOptions.BuildArgs`, `RunOptions.RunArgs`) so the flags can be tested directly. That is where review misses things.

⚠️ A fake proves you built the command you intended. Only a real run proves the command exists. `docker rm -f -t 10` passed its unit test and is not a valid command. The test had encoded the bug. Exercise the real thing before believing it.

Test seams are unexported (`withTTYCheck`, a `var` instead of a `const` for a size cap) and exist where a guard is otherwise unreachable. Prefer that to leaving a guard untested.

## The private overlay

`docsi/` is a **symlink into `private-repo/lath/docsi`**, managed by `repolink` and gitignored. It holds the session handover, project memory, and anything naming a private path, a client or an unpublished plan. This repository is public; that directory is how a session keeps context that must not be.

- `repolink status` shows whether the link is live; `repolink sync` recreates it after a fresh clone, which gets the ignore rule but not the symlink.
- Write private notes to `docsi/`, never to the tracked tree. The test: would a stranger be *helped* by this, or would they *learn about another project*? The first is public, the second belongs in the overlay.
- If `docsi/` is absent, the overlay simply is not set up on this machine — nothing in the repository depends on it.

## Commands

- `task check`. The gate: fmt, vet, race tests across every module **and** both definitions, then `crosscheck`, `docverify` and `sigverify`.
- `task crosscheck`, build every module for linux, darwin and windows.
- `task docverify` / `task sigverify`, prove the reference docs name only symbols that exist, and that every signature they SHOW is the one that exists.
- `task test:linux`, run the whole gate on linux in docker, against the working tree. Linux and macOS are both supported and a Mac cannot check the other half; `task test:linux:shell` is the same image interactively. See [`docs/TESTING.md`](docs/TESTING.md).
- `task cover`, coverage per package plus the statement-weighted total across every module. `task cover:badge` rewrites the README badge; `task cover:check` fails when it is stale or coverage is below the floor.
- `task build` / `task build:release`, dev binary (engine from this checkout) / release binary (engine from the module proxy).
- `task install`, symlink `bin/lath` onto PATH. `task which` shows what an installed `lath` resolves to.
- `task isolation`, prove the definition module never enters the parent module graph.
- `task graph`, show which module depends on which.
- `task list` / `task plan -- prod` / `task dryrun -- prod` / `task deploy -- prod`, drive the example definition.
- `task tidy`, `task fmt`, `task clean`.

Run a definition's targets with `lath run <target> [args...]`, or `lath list` to see them.

## Code standards

Follow the global rules in `~/.claude/CLAUDE.md`. In this repository specifically:

- **Zero external dependencies in `cmd/lath` and `kit`.** Standard library only. A definition may depend on anything it likes. That is the point of the nested module.
- **No `interface{}`/`any` escape hatches** outside a documented boundary. `pipeline.State` uses `map[Key]any` because Go lacks variadic generics; access is through `Set[T]`/`Get[T]` with a checked assertion returning `ErrKeyType`, and `Validate` proves the wiring before anything runs.
- **Named constants for closed sets**, with a canonical `*Values` slice so read and write paths cannot drift.
- **Why-and-invariant doc comments** on every exported symbol. Comment intent and constraints, not mechanics.
- **No dead scaffolding.** No `var _ = pkg.Symbol` to silence an unused import, no parameter that is never read, no stub that returns success.

## Git

Conventional commits: `type(scope): description`. Never include AI attribution. No "Generated with", no `Co-Authored-By`. Never commit without being asked.

## Docs

- `docsi/HANDOVER.md` in the private overlay, **start here on a new session**, when present: what lath is, why it was built, current state, the local consumers, where work stopped, and the open items. Outside the repository by design, so a fresh clone will not have one until `repolink sync` runs.
- [`docs/DEBUGGING.md`](docs/DEBUGGING.md), **stepping through a pipeline**: `lath tui`, the keys, rerunning, failures, recipes, troubleshooting.
- [`docs/DEBUGGER.md`](docs/DEBUGGER.md). The debugger's design record: why two processes, the wire protocol, and the nine things building it corrected.
- [`docs/pipeline/README.md`](docs/pipeline/README.md), **the engine**: Step, State, Key, Reporter, Validate, Plan, and the dry-run contract.
- [`docs/kit/README.md`](docs/kit/README.md), **the library reference**: one page per package, with the API, worked snippets, use cases and gotchas.
- [`docs/steps/README.md`](docs/steps/README.md). The pipeline adapters, the state keys, and a whole deploy.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), how the runner works: discovery, generation, caching, exec.
- [`docs/DEFINITION-ISOLATION.md`](docs/DEFINITION-ISOLATION.md), why the definition is a nested module, with the measurements that rejected build tags.
- [`docs/NAMESPACES.md`](docs/NAMESPACES.md), one-level command namespaces.
- [`docs/PRIMITIVES-STDLIB.md`](docs/PRIMITIVES-STDLIB.md). The first eight kit packages and the defects found building them.
- [`docs/PRIMITIVES-NEXT.md`](docs/PRIMITIVES-NEXT.md). The later kit packages, the admission test, and what implementation changed.
- [`kit/doc.go`](kit/doc.go) and [`steps/doc.go`](steps/doc.go). The placement rules, at the code.
