# lath

<p align="center">
  <a href="https://pkg.go.dev/github.com/ubgo/lath/kit"><img src="https://pkg.go.dev/badge/github.com/ubgo/lath/kit.svg" alt="Go Reference on pkg.go.dev"></a>
  <a href="https://goreportcard.com/report/github.com/ubgo/lath/kit"><img src="https://goreportcard.com/badge/github.com/ubgo/lath/kit" alt="Go Report Card"></a>
  <a href="https://github.com/ubgo/lath/actions/workflows/ci.yml"><img src="https://github.com/ubgo/lath/actions/workflows/ci.yml/badge.svg" alt="Build and test status"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-2ea44f" alt="License: MIT"></a>
  <img src="https://img.shields.io/badge/go-1.24%2B-2ea44f" alt="Requires Go 1.24 or newer">
  <img src="https://img.shields.io/badge/dependencies-zero-2ea44f" alt="Zero dependencies — kit and pipeline are stdlib only">
  <img src="https://img.shields.io/badge/coverage-87.8%25-2ea44f" alt="Statement coverage across every module: 87.8%">
  <img src="https://img.shields.io/badge/platforms-linux%20%C2%B7%20macOS-2ea44f" alt="Supported on linux and macOS">
</p>

*A lath is a thin strip nailed in sequence to form the base everything else is applied to.*

**Type-safe, Go-defined deploy pipelines.** A runner that discovers the exported functions of a repository's deploy definition, compiles it, and executes it.

The point is that **the same deploy runs anywhere**: on your laptop against local Docker, from any machine over ssh, or in CI. One command, one definition, identical behaviour. CI stops being where your deploy *lives* and becomes just another machine that runs it.

```sh
lath run deploy run prod --apply     # from your laptop, over ssh
lath run deploy local --apply        # the same steps, against local Docker
lath tui deploy local --apply        # ...stepping through them one at a time
```

That last one is the part YAML cannot give you. A deploy you cannot rehearse is a deploy you find out about in production.

## Contents

- [What this replaces](#what-this-replaces)
- [Why you might want this](#why-you-might-want-this)
- [Working with an agent](#working-with-an-agent)
- [How this compares](#how-this-compares)
- [The idea in one screen](#the-idea-in-one-screen)
- [The command model](#the-command-model)
- [Why Go instead of YAML](#why-go-instead-of-yaml)
- [Layout](#layout)
- [Commands](#commands)
- [Adding your own step](#adding-your-own-step)
- [What makes it work](#what-makes-it-work)
- [What it does not do](#what-it-does-not-do)
- [FAQ](#faq)
- [Docs](#docs)
- [Contributing](#contributing)

## What this replaces

A real one, in a private repository: **two 300-line GitHub Actions workflows**, generated from a template because they differed on twelve lines. The whole deploy lived in embedded shell inside that YAML (build, push, ssh, write secrets, migrate, start, health-check, cut over, retire the old container, prune) and could only run on a GitHub runner.

It is now a Go definition that runs anywhere, and the workflow is this:

```yaml
- uses: netbirdio/actions/netbird-setup@main      # join the network
  with:
    netbird-setup-key: ${{ secrets.NETBIRD_SETUP_KEY }}
- run: lath run deploy run prod --apply           # the deploy
```

The workflow provisions the machine. Everything after that is one command you can also run yourself.

**Note the definition is BIGGER, about 1,400 lines of Go against 600 of YAML.** That is the honest trade and it is not a defect: those lines are typed, unit-tested, commented with the reasons, and they run on your laptop. Fewer lines was never the goal.

## Why you might want this

**You can run your production deploy on your laptop.** Not a linter, not a simulation. The same fourteen steps, against local Docker, before anything touches a server. That single property is what the rest of this is downstream of.

**Mistakes fail before step one, not after `stop-previous`.** A wrong type is a compile error; a step reading a value nothing produces is caught by `Validate` before the first container moves. In YAML both are runtime failures, and a deploy that dies halfway is worse than one that never starts.

**You can step through it.** `lath tui` pauses after each step, shows you the state, and lets you replay the step that just ran, against the same inputs, without redoing the two-minute build above it. That is why deploy code rots elsewhere: if iterating on step 10 costs steps 1–9 every time, nobody iterates.

**Custom logic is a value, not a shell blob.** A step your deploy needs and no tool models is fifteen lines of Go in the same list as `docker.Build`, type-checked, wired, unit-testable, and shown in `plan` like everything else.

**Nothing is hidden.** A dry run prints the literal `docker run …` line each step would execute, shell-quoted and ready to paste. `lath run deploy local | grep 'would run'` gives you the whole deploy as a script you can read, run by hand, or diff against what you expected.

**It is not a platform.** One binary, no daemon, no service, no account. Your deploy is Go in your repository, and lath compiles and runs it.

## Working with an agent

The same properties make lath unusually good to point an AI at, and the reason is narrow: **an agent editing a lath definition can check its own work.**

An agent editing YAML cannot. It produces something plausible, and nothing disagrees until the deploy runs in production. Here it gets four escalating checks it can run itself, compile, wiring validation, a dry run that prints exact commands, then the real deploy against local Docker, plus `lath tui` to step through what it wrote and replay a single failed step without repeating the rest.

`lath list --json` and `lath cache --json` make the listings machine-readable, and the debug protocol is newline-delimited JSON over a socket, so an agent can drive a run rather than scrape it. Credentials cannot leak into what it reads: `secret.Value` closes every formatting path.

See [`docs/AI.md`](docs/AI.md) for the loop, the custom-step contract, and where the autonomy boundary sits.

## How this compares

[Kamal](https://kamal-deploy.org/) is the closest thing in spirit, and if it fits your deploy you should probably use it. It is mature, it is opinionated, and it gives you zero-downtime deploys out of the box.

The difference is what you are handed. Kamal gives you a **deploy tool** configured by YAML. lath gives you a **task runner whose tasks are Go**, plus the primitives a deploy needs (Docker, ssh, secrets, health checks, locks, archives), and expects you to assemble the pipeline yourself.

That is worse when your deploy is ordinary, and better when it is not: when the ordering is peculiar to you, when a step must talk to something no deploy tool models, when you want the wiring checked at compile time, or when you want to step through the thing before trusting it. Deploying is also only one task; the same runner replaces the rest of your `scripts/` directory.

| | **lath** | CI YAML + shell | Kamal | Ansible / scripts |
|---|---|---|---|---|
| Runs the real deploy on your laptop | ✅ | ❌ | ⚠️ partially | ✅ |
| Mis-wired pipeline caught before it runs | ✅ compile + `Validate` | ❌ | ⚠️ config schema | ❌ |
| Step through it, replay one step | ✅ `lath tui` | ❌ | ❌ | ❌ |
| Custom logic without shelling out | ✅ Go values | ❌ embedded bash | ⚠️ hooks | ⚠️ modules |
| Shows the exact commands before running | ✅ dry run | ❌ | ⚠️ | ⚠️ |
| Zero-config for an ordinary deploy | ❌ you assemble it | ⚠️ | ✅ | ❌ |
| Needs a daemon, account or control plane | ❌ none | ⚠️ the CI provider | ❌ none | ❌ none |

If your deploy is ordinary and you want it working this afternoon, use Kamal. If it is peculiar, or you want to rehearse and debug it like the program it is, that is lath.

## The idea in one screen

A repository defines its deploy in Go, in a nested module that stays out of the application's module graph. **Every exported function is a command**, writing one declares it, and there is no `main`, no switch, and no registration list:

```go
// .lath/deploy.go
package main

// The steps are grouped by subject: git/, docker/ and common/. `box` is a
// runner.Runner — swap it for runner.Local{} and the same pipeline rehearses
// on this machine.

// Plan lists the steps for an environment without executing any of them.
func Plan(args ...string) error { … }

// Deploy builds and ships the API to an environment.
func Deploy(ctx context.Context, args ...string) error {
    return deployPipeline(env).Run(ctx, state)
}

func deployPipeline(env Environment) pipeline.Pipeline {
    return pipeline.Pipeline{
        Name: "deploy-api",
        Steps: []pipeline.Step{
            git.ResolveCommit{},
            docker.Build{
                Repository: "ghcr.io/acme/api",
                Options:    dockerkit.BuildOptions{Dockerfile: ".docker/Dockerfile.prod", Context: "."},
            },
            common.PutFile{Path: "/srv/acme/.env.prod", Secret: envFile, Runner: box},
            docker.RunOnce{Label: "migrate", Cmd: []string{"sync", "migrate"}, Runner: box},
            docker.Start{NamePrefix: "acme-prod", Processes: []docker.Process{{Name: "api"}}, Runner: box},
            docker.WaitHealthy{Timeout: 60 * time.Second, Runner: box},
            // Pointing a proxy at the release is an HTTP call with a body
            // only you can shape, so it is the generic step plus your payload.
            common.Request{Label: "switch-traffic", URL: caddyAdmin,
                Method: http.MethodPatch,
                Reads:  []pipeline.Key{common.KeyRouted}, Body: caddyUpstreams},
            docker.StopPrevious{NamePrefix: "acme-prod", Drain: 30 * time.Second, Runner: box},
        },
    }
}
```

Start a definition:

```sh
$ lath init           # scaffold ./.lath with a working starter, ready to run
```

Then:

```sh
$ lath list                 # what this definition exposes
targets (from ./.lath):
  deploy             Deploy builds and ships the API to an environment
  plan               Plan lists the steps for an environment without executing any of them

run one with: lath run <target> [args...]

$ lath run plan prod        # list the steps, execute nothing
$ lath run deploy prod      # run them
```

## The command model

**One rule: targets go behind `run`. Everything else is lath's own.**

```
lath run <target> [args...]     anything your definition exports
lath run <group> <target>       a target in a subdirectory
lath run <group>                what is inside that group
lath list                       what it exposes
lath init                       scaffold a definition
lath cache [prune|clean]        inspect or prune the compiled-pipeline cache
lath version
lath help                       (-h, --help)
```

Every command has its own help, `lath cache --help`, `lath run -h`, and so on. After `run <target>` a help flag belongs to the **target**, not to lath, so `lath run deploy --help` reaches your function untouched.

That split means **there are no reserved words**. A definition may export `Init`, `Cache`, even `Run`. They never share a namespace with lath's commands:

```sh
$ lath init          # lath's verb
$ lath run init      # your target named Init
$ lath run run       # your target named Run
```

The rejected alternative was the reverse, bare targets, with lath's commands behind a prefix. It keeps the common path four characters shorter, at the cost of a reserved word, a fatal error whenever a definition collides with it, and a prefix that means nothing until explained.

**Supported function shapes**. A closed set, because the generated dispatcher must know how to call each one:

```go
func Name()
func Name() error
func Name(ctx context.Context) error
func Name(args ...string) error
func Name(ctx context.Context, args ...string) error
```

Unexported functions are not commands, so helpers stay out of the list. Anything exported with an unsupported shape is **reported**, never silently skipped. The first sentence of the doc comment becomes the help text.

## Why Go instead of YAML

The definition is code, so mistakes fail at build time rather than mid-deploy:

```
Timeout: "60s"           → cannot use "60s" (untyped string) as time.Duration
NotifySlack{Chanel: …}   → unknown field Chanel in struct literal of type NotifySlack
```

And wiring mistakes fail before step one, courtesy of each step declaring what data it reads and writes:

```
FAILED: pipeline "deploy-api": step 5 (start-processes) requires "env",
        but no earlier step provides it
```

In YAML both of those are runtime failures, and a deploy that fails after `stop-previous` is worse than one that never starts.

## Layout

```
lath/
  go.work             the workspace — the ONLY file at the root
  cmd/lath/           module github.com/ubgo/lath/cmd/lath — the runner binary
  kit/                module github.com/ubgo/lath/kit — the libraries, no pipeline required
  pipeline/           module github.com/ubgo/lath/pipeline — the engine
  steps/              module github.com/ubgo/lath/steps — the adapters
  example/            module example — a stand-in for a user's repository
    .lath/            module deploydef — THE DEFINITION, outside the workspace
```

Every package sits exactly where its import path says. Go requires a module path to equal the repository path plus its subdirectory, so nesting these under a `modules/` directory would make them unfetchable by anyone outside this checkout, `go get github.com/ubgo/lath/kit` would 404 while the local build kept working, because `replace` and `go.work` hide the mismatch.

| Module | Depends on | Why it is separate |
|---|---|---|
| `github.com/ubgo/lath/pipeline` | nothing of ours | the engine is domain-free: no Docker, no SSH, no notion of a deploy. Reusable for any staged process. |
| `github.com/ubgo/lath/steps` | `github.com/ubgo/lath/pipeline` | the deploy primitives. A project can swap these wholesale without touching execution semantics. |
| `github.com/ubgo/lath/cmd/lath` | `kit`, `pipeline`, for the debugger only | the runner compiles a definition; it never imports one, and links no deploy library. The two exceptions are the step debugger's transport, which both ends must agree on. |
| `deploydef` | `pipeline`, `steps`, never the runner | the definition. A nested module outside `go.work`, so its dependencies never enter the application's module graph. |

Separate modules rather than one module with packages: a user's definition should be able to require the engine and the steps **without** pulling in the runner. Module boundaries make that structural instead of conventional.

## Commands

Using it, from a project with a `.lath/` definition:

```sh
lath list                          # every target this definition exposes
lath run deploy prod               # run one — arguments pass straight through
lath run deploy prod --apply       # without --apply, a target may dry-run itself
lath plan deploy prod              # describe the pipeline; execute nothing
lath dry-run deploy prod           # run every step with side effects suppressed
lath tui                           # pick a target and STEP through it, here
lath tui deploy local --apply      # step through that one
lath run deploy local --apply --debug   # start it here, attach from elsewhere
lath cache                         # what is compiled, and for which project
lath cache prune                   # drop entries whose project is gone
lath list --json                   # the same listings, machine-readable
lath plan deploy prod --json
lath cache --json
lath init                          # scaffold a definition
lath version
```

`plan` and `dry-run` are the two halves of "what would this do". `plan` describes the pipeline without running a step; `dry-run` runs every step with its side effects suppressed, which catches the faults that only appear once values are flowing. Both are the runner's, not the definition's, so they work on a definition that never thought to offer them.

Working on lath itself:

```sh
task check          # the gate: fmt, vet, race tests, crosscheck, docverify, sigverify
task build          # build bin/lath
task install        # symlink bin/lath onto PATH
task test:linux     # run the WHOLE gate on linux, in docker, before pushing
task test:linux PLATFORM=linux/amd64   # ...and on the other architecture, emulated
task cover          # coverage per package + the statement-weighted total
task cover:badge    # rewrite the README coverage badge to the measured number
task cover:check    # fail if that badge is stale, or coverage is below the floor
task crosscheck     # prove it still COMPILES and VETS for linux, darwin and windows
task docverify      # prove the docs and the API agree, in both directions
task sigverify      # prove every signature the docs SHOW is the one that exists
task isolation      # prove the definition never enters the workspace module graph
task graph          # print which module depends on which
task --list         # everything else
```

⚠️ `go build ./...` **inside a definition directory fails by design**. The definition is `package main` with no `func main`, because `lath` generates the entry point at build time. `go vet`, `go test`, `gofmt` and the editor all work on it normally. Use `lath` to build a definition.

⚠️ The repository root is a workspace root and **not** a module, so `go build ./...` there fails with *"directory prefix . does not contain modules listed in go.work"*. Use the module-path pattern instead, `go build github.com/ubgo/lath/...`, which is what the Taskfile does.

`install` creates a **symlink**, not a copy, into `$(go env GOPATH)/bin`, so every later `task build` is picked up with no reinstall. Override with `task install INSTALL_DIR=…`.

## Adding your own step

Two forms, chosen by whether the step deserves a name.

Inline, for a one-off:

```go
pipeline.Func{
    Label:      "record-deploy",
    Needs:      []pipeline.Key{common.KeyImage},
    Idempotent: true, // safe to replay under `lath tui`
    Do: func(ctx context.Context, s *pipeline.State) error {
        image, err := pipeline.Get[string](s, common.KeyImage)
        if err != nil { return err }
        s.Detailf("appended %s to the deploy log", image)
        return nil
    },
}
```

A named type, for something reused or configurable, implement `Name`, `Requires`, `Provides`, `Run`. Both are in the example pipeline (`notify-slack` and `record-deploy`), and the engine treats them identically to the shipped steps.

⚠️ `Needs` and `Gives` are not documentation. `Validate` uses them, so a closure that reads a key it did not declare defeats the wiring check for the **whole pipeline**, not just its own step.

## What makes it work

1. **Exported functions are the commands**, discovered by parsing the directory, so writing a function IS declaring a command. Nothing to register, nothing to keep in sync.
2. **No `go run` typed by hand**. The runner shells out to `go build`, caches by content hash, then execs. Measured on the example definition: **~1.9s cold, ~60ms warm**.
3. **Any number of definition files**. The hash covers every `.go` file in `.lath/`, so splitting the definition needs no configuration.
4. **Project-defined steps compose with shipped ones**, inline via `pipeline.Func` or as a named type, no shell-out escape hatch.
5. **Config errors fail at `plan`**, before anything is built or stopped. A `time.Duration` field given a string fails to compile; a missing required field is reported by `Validate`.
6. **Wiring errors fail at validation**, naming the step, its position, and the missing key.
7. **Introspection**, `plan` lists every step with its inputs and outputs, side-effect free, and prints the exact command each one would run.
8. **Step-through debugging**, `lath tui` runs a pipeline one step at a time, with docker's own progress table intact. Your definitions change by zero lines.
9. **Module isolation**, `go work vendor` never reaches `.lath/`, yet the editor type-checks it in full, because `gopls` resolves from the module cache and vendoring only duplicates what is already there.
10. **A one-way dependency graph**. The runner never imports the engine, and the definition never imports the runner. `task graph` prints it.

## What it does not do

**No rollback verb, on purpose.** Retiring the previous container happens only after the new one is healthy, so a *failed* deploy leaves the old one serving. Rolling back a *completed* one is a target your definition writes: `docker.UseImage` ships an image that already exists rather than rebuilding it, and [`example/.lath`](example/.lath/deploy.go) has a working `Rollback` you can copy. lath does not ship the verb because only your project knows what "back" means — the previous commit, the last image on the box, the last one that passed a health check — and whether your migrations survive the trip. An image rollback is a code rollback: it works while migrations are additive, and stops the moment one drops a column the old binary still reads.

**No resume.** A run that dies halfway has no memory of where it stopped. `lath tui` lets you retry a failed step interactively; unattended, a failure means starting again. Steps that declare themselves replayable make that cheap.

**No version-skew check** between the runner binary and the library pinned in `.lath/go.mod`. A definition built against a newer `pipeline` than the installed `lath` fails at compile time with a Go error rather than a helpful one.

**No orchestration beyond one host at a time.** A `Runner` points at one machine. Deploying to several means iterating, and nothing coordinates them.

**No Windows support yet.** It compiles for Windows on every commit, and `gh workflow run windows.yml` runs the tests there on demand: 25 of 33 packages pass today, including the whole pipeline and steps layer and the example definition. What stops it being supported is one real bug — the compiled definition is written without an `.exe` suffix, so the runner cannot exec it — one documented limitation, and a set of tests that assert POSIX permission semantics Windows does not have. [`docs/TESTING.md`](docs/TESTING.md) has the breakdown. What is already known to differ: the runner cannot replace its own process image, so it spawns a child and forwards the exit code instead of `exec`; terminal detection is a stub that always answers yes, which affects `--debug`; `proc.Find` needs `pgrep` and reports `ErrUnsupported`; and `kit/lock` falls back to `StaleAfter` because it cannot verify liveness. Deploying *to* a Linux host from a Windows machine is the plausible first target, and none of the above blocks it — it just has not been done.

**Nothing multi-tenant, no web UI, no daemon.** It is a binary you run.

## FAQ

**Does lath replace my CI?** No — it replaces the *deploy logic* your CI currently holds. The pipeline moves into your repository as Go, and CI becomes one more machine that runs `lath run deploy prod --apply`. The point is that the machine stops mattering: the same command does the same thing on your laptop.

**Can I really run my production deploy locally?** Yes, and it is the property everything else follows from. A step's `Runner` decides where its commands land; point it at `runner.Local{}` and the identical pipeline builds, migrates, starts containers and health-checks against Docker on your machine. Nothing is stubbed or simulated.

**Do I need Docker?** For the shipped `steps/docker` adapters, yes. The engine itself has no notion of a container — `pipeline` is domain-free, and plenty of `kit` packages (ssh, archive, checksum, github, brew, download, supervise) have nothing to do with Docker at all.

**Does it need a daemon, a server, or an account?** None of the three. It is one binary. There is no control plane to run, no service to log into, and nothing phones home.

**How is it different from Kamal?** Kamal is a deploy tool configured by YAML and is excellent when your deploy is ordinary. lath is a task runner whose tasks are Go, plus the primitives a deploy needs, and expects you to assemble the pipeline. See [How this compares](#how-this-compares) for the table.

**Am I locked in?** Less than you would expect. `kit` is an ordinary Go library with **zero dependencies** — `kit` and `pipeline` require nothing outside the standard library — so the Docker, ssh, archive, checksum and GitHub-release code is usable from any Go program that never heard of lath. What you would lose by leaving is the runner, the validation and the debugger, not the primitives.

**Does it do rollbacks?** There is no `rollback` verb, deliberately. `docker.UseImage` deploys an image that already exists, and a rollback target is about thirty lines in your own definition — [`example/.lath`](example/.lath/deploy.go) has one. Only your project knows what "back" means and whether your migrations survive it, which is exactly why the engine does not guess.

**What happens if a deploy dies halfway?** The previous release is still serving, because the new containers are health-checked before the old ones are retired. There is no automatic resume: `lath tui` lets you retry the failed step interactively, and steps that declare themselves replayable make that cheap. See [What it does not do](#what-it-does-not-do).

**Is it safe to point an AI agent at?** Safer than YAML, because an agent can check its own work here: it compiles, validates the wiring, dry-runs to see the exact commands, and rehearses against local Docker before anything real happens. Credentials cannot leak into what it reads — `secret.Value` closes every formatting path. See [`docs/AI.md`](docs/AI.md).

**Which platforms does it run on?** Linux and macOS, on both arm64 and amd64 — the full gate runs on each, so "supported" means something checked rather than assumed; [`docs/TESTING.md`](docs/TESTING.md) has the measurements. **Windows is not supported yet**: it compiles, and every commit proves that, but nothing runs there. See [What it does not do](#what-it-does-not-do). The deploy *targets* are separate and are whatever your `Runner` points at, so deploying from macOS to a Linux server is the ordinary case.

## Docs

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). The full design: every trade-off, the rejected alternatives, and eight open problems.
- [`docs/DEFINITION-ISOLATION.md`](docs/DEFINITION-ISOLATION.md), why the definition is a nested module rather than a build-constrained file, with the measurements behind it.
- [`docs/AI.md`](docs/AI.md), working on a deploy with an agent, and a comparison table against shell scripts, GitHub Actions and Kamal.
- [`docs/TESTING.md`](docs/TESTING.md), the gate, how coverage is measured, running the whole thing on Linux from a Mac, and what "supported platform" means here.
- [`docs/DEBUGGING.md`](docs/DEBUGGING.md), step through a pipeline one step at a time with `lath tui`.
- [`docs/DEBUGGER.md`](docs/DEBUGGER.md), why the debugger is built the way it is.
- [`docs/pipeline/README.md`](docs/pipeline/README.md). The engine: `Step`, `State`, `Key`, `Reporter`, and the wiring check.
- [`docs/pipeline/debug.md`](docs/pipeline/debug.md). The debug transport: the wire protocol, server and client.
- [`docs/kit/README.md`](docs/kit/README.md). The library reference: one page per `kit` package, with snippets and gotchas.
- [`docs/steps/README.md`](docs/steps/README.md). The pipeline adapters and a full deploy example.
- [`docs/PRIMITIVES-STDLIB.md`](docs/PRIMITIVES-STDLIB.md). The first eight `kit` packages: `proc`, `repo`, `supervise`, `wait`, `scan`, `out`, `fsx`, `env`. Zero dependencies.
- [`docs/PRIMITIVES-NEXT.md`](docs/PRIMITIVES-NEXT.md), **built**: the admission test for `kit`, the three tiers, and the design of the eight later packages, `secret`, `lock`, `hashtree`, `ssh`, `archive`, `confirm`, `supervise.Retry`, `download`.
- [`docs/NAMESPACES.md`](docs/NAMESPACES.md). One subdirectory level becomes a command namespace, so `secrets/push.go` is `lath run secrets push`. Includes every constraint and why it exists.
- [`docs/PRIMITIVES.md`](docs/PRIMITIVES.md). The plan: which primitives to build, and which ≈4,000 lines of existing deployment machinery each one retires.

## Contributing

Bug reports, pull requests and questions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the setup, the gates a change has to pass, and the one question that decides where new code goes. Security issues go to [SECURITY.md](SECURITY.md) rather than a public issue. Everyone participating is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

Licensed under the [MIT License](LICENSE).
