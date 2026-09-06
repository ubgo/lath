# lath, technical design

The design as it stands: what each piece does, why it is shaped that way, which alternatives were rejected, and what remains unsolved. The [README](../README.md) is the short version.

---

## 1 · What this is

A deploy is a sequence of actions, resolve a commit, build an image, move it to a host, migrate, start processes, health-check, switch traffic, retire the old version. lath expresses that sequence as **ordinary Go**, and runs it.

Two properties follow from that choice, and everything in this document exists to serve them:

**Mistakes fail before execution.** A field given the wrong type fails at compile. A missing required field, or a step reading a value nothing produces, fails at `plan`. The alternative, a config file the tool interprets, makes every one of those a runtime failure, and a deploy that fails *after* the old containers stopped is worse than one that never started.

**The same code runs everywhere.** A laptop, a CI runner, or the target host all execute the identical binary. Nothing can live in a CI config file, which is what keeps the CI config eight lines and permanently prevents it growing conditionals and loops until it becomes the system it replaced.

Status: **experiment**. The mechanism works end to end; no step performs real work. Every one describes what it would do.

---

## 2 · Package structure

```
go.work             the workspace — the ONLY file at the repository root
cmd/lath/           module github.com/ubgo/lath/cmd/lath  — the runner: discover, generate, hash, compile, cache, hand off
kit/                module github.com/ubgo/lath/kit       — the libraries, usable without a pipeline at all
pipeline/           module github.com/ubgo/lath/pipeline  — the engine: Step, Pipeline, State, Key, Reporter
steps/              module github.com/ubgo/lath/steps     — the adapters: common/, docker/, git/
example/            module example                            — a stand-in for a user's repository
example/.lath/      module deploydef                          — THE DEFINITION, outside the workspace
docs/               design documents
```

Four modules, versioned independently. **Every package sits exactly where its import path says**, and that is load-bearing rather than cosmetic: Go requires a module path to equal the repository path plus its subdirectory, so a module declaring `github.com/ubgo/lath/kit` while living in `modules/kit` is unfetchable by anyone outside this checkout. The proxy would demand `github.com/ubgo/lath/modules/kit`.

An earlier layout had exactly that mismatch. It worked locally only because `replace` directives and `go.work` hid it, and it would have failed the first time a stranger ran `go get`. The `apps/` + `modules/` split used in our other Go repositories is fine for a repository nobody imports; it does not survive publication. Never move a package without moving its import path with it.

Dependency direction: `kit` and `pipeline` depend on nothing; `steps` depends on both. `cmd/lath` compiles a definition and execs it, so it does not import the libraries a definition uses, except `kit/session` and `pipeline/debug`, which `lath tui` needs. See [`DEBUGGER.md`](DEBUGGER.md) for why that cost belongs on the runner rather than on every definition.

### Why the root is not a module

A workspace root holding both `go.work` and `go.mod` conflates two roles: the thing that lists members, and a member itself. Keeping the root free of `go.mod` means every unit of code lives in a named directory with an explicit boundary, and there is no ambiguous "root package" for stray files to accumulate in.

The cost is one piece of friction worth knowing: **`go build ./...` fails at the root**, *"directory prefix . does not contain modules listed in go.work"*. Because a relative pattern needs the working directory to be inside a module. The module-path pattern `github.com/ubgo/lath/...` works from anywhere and matches every workspace member. The Taskfile uses it and says why.

### Why separate modules rather than one module with packages

A user's definition must be able to require the engine and the steps **without** pulling in the runner. As packages in one module that is a convention nobody enforces; as separate modules it is structural, `deploydef`'s `go.mod` simply has no line for `github.com/ubgo/lath/cli`.

The graph runs one way, and `task graph` prints it:

```
github.com/ubgo/lath/pipeline   -> nothing of ours
github.com/ubgo/lath/steps      -> github.com/ubgo/lath/pipeline
github.com/ubgo/lath/cli        -> nothing of ours      (it COMPILES a definition; it never imports one)
deploydef          -> pipeline, steps      (never cli)
```

### Why `pipeline` and `steps` are separate

`pipeline` has no idea what a container is. It sequences steps, validates their data dependencies, and reports progress. That makes it reusable for any staged process, release, migration, teardown, and it means the deploy primitives can be replaced wholesale without touching execution semantics.

The dependency direction is one-way: `steps` imports `pipeline`; `pipeline` imports nothing of ours.

### Why the runner does not import either

`github.com/ubgo/lath/cli` never imports `pipeline` or `steps`. It knows only that a Go module lives in `.lath/` and that it should be built and run. That separation is what lets a project define steps the runner has never heard of, `NotifySlack` in the example works without the runner knowing it exists.

---

## 2.5 · The command model

One rule: **targets go behind `run`; everything else is lath's own.**

```
lath run <target> [args...]     anything the definition exports
lath list                       what it exposes
lath init                       scaffold a definition
lath cache [prune|clean]        inspect or prune the compiled-pipeline cache
lath version
lath help                       (-h, --help)
```

The consequence that matters: **there are no reserved words.** A definition may export `Init`, `Cache`, or `Run`, because its targets never share a namespace with lath's commands, `lath init` is lath's, `lath run init` is the definition's.

The rejected alternative was the reverse: bare targets, with lath's commands behind a prefix. It keeps the common path four characters shorter, and costs a reserved word, a fatal error when a definition collides with it, and a prefix that means nothing until explained. Four characters is not worth a class of build failure.

Typing a target without `run` is the likeliest slip, so the unknown-command error says so before anything else.

---

## 3 · The `Step` interface

```go
type Step interface {
    Name() string
    Requires() []Key
    Provides() []Key
    Run(ctx context.Context, s *State) error
}
```

`Name` and `Run` are obvious. `Requires` and `Provides` are the design.

### Why steps declare their data dependencies

A step could simply read from `State` and fail if a value is absent. The cost of that is *when* it fails: mid-run, possibly after irreversible work. Declaring the dependencies lets `Validate` walk the list before anything executes and prove that every consumer follows its producer.

```
FAILED: pipeline "deploy-api": step 5 (start-processes) requires "env",
        but no earlier step provides it
```

Structurally this is a dependency graph, but the author writes a flat ordered list and validation only confirms that ordering is *possible*, never that it is optimal. Deciding migrations run before processes start is a judgement the tool does not second-guess.

### Why steps are structs, not functions

A struct's fields are typed configuration the compiler checks:

```
Timeout: "60s"           → cannot use "60s" (untyped string) as time.Duration
NotifySlack{Chanel: …}   → unknown field Chanel in struct literal
```

A function taking `map[string]any` would accept both. This is the single largest practical difference from YAML, and it is worth the ceremony of a type per step.

### Two forms

A step may be a **named type** implementing the four methods, or an inline **`pipeline.Func`** carrying `Label`, `Needs`, `Gives` and a `Do` closure. The engine cannot tell them apart, and neither gets special treatment.

The inline form exists because a named type is the right shape for something reused, but heavy ceremony for a one-off. A project-specific notification, a temporary workaround. Without it, authors either write forty lines of boilerplate for a three-line action or, worse, wedge the logic into a neighbouring step where nobody will find it.

### Optional self-validation

A step may also implement `Validator`:

```go
type Validator interface{ Validate() error }
```

`Pipeline.Validate` calls it when present, which splits the checking by what is knowable when:

| | checks | when |
|---|---|---|
| `Validate()` | what the **author** wrote, struct fields | at plan time, no State needed |
| `Run()` | what the **pipeline** produced, values read from State | at execution, unavoidable |

A check that belongs in `Validate` but sits in `Run` still works; it just fires later than it needed to, which for a deploy can mean after the previous version was already stopped. It is optional rather than part of `Step` because not every step has configuration to check, and an empty method on those would be noise.

### The four implementer obligations

1. `Requires` MUST list every key `Run` reads. Under-reporting defeats `Validate` and reintroduces the failure mode it exists to prevent.
2. `Provides` MUST list every key `Run` sets, and no two steps may provide the same key.
3. `Run` MUST honour `State.DryRun` by suppressing side effects **while still setting every declared `Provides` key**, see §5.
4. `Run` MUST respect `ctx` cancellation for blocking work. The engine checks between steps; it cannot interrupt one already running.

Obligations 1 and 3 are convention rather than compiler-enforced, which is a real weakness, see [Open problems](#9--open-problems).

---

## 4 · Data flow, and the type-safety trade

Go cannot express a heterogeneous typed chain, `Step[A,B]` then `Step[B,C]` then `Step[C,D]`. Without variadic generics, which the language lacks. Pairwise composition (`Then(Then(Then(a,b),c),d)`) type-checks but nests unreadably at ten steps and makes inserting a step in the middle painful.

So values flow through a runtime-typed bag, with the guarantee moved elsewhere:

| Concern | Where it is checked |
|---|---|
| Step **configuration** (field names, field types) | **compile time** |
| Step **wiring** (does anything provide what this reads) | **construction time**, via `Validate`, run by `plan` and by a test |
| Value **types** in `State` | **run time**, via `Get[T]` |

Anyone claiming full compile-time safety for a pipeline of arbitrary step types in Go is hiding an `any` somewhere. This is the honest split: put the compile-time check where typos actually happen, and prove the rest before execution.

### `Get[T]` errors on absence rather than returning a zero value

A zero value here is an empty image tag or an empty host. That does not fail at the read. It fails several steps later as an inexplicable `docker: invalid reference`, far from the cause. Failing at the read names the missing key.

### `Key` is a defined type

`Requires` and `Provides` are compared against each other by `Validate`, so a typo on either side must be impossible to introduce silently. A defined type means the compiler rejects an untyped literal wherever a `Key` is expected. The type lives in `pipeline`; the concrete keys live in `steps`, because the engine has no opinion about what flows through it.

---

## 5 · Dry run

`Mode` is a defined type with a fixed value set, not a bool. Two reasons: `NewState(true)` says nothing at a call site, and "dry run" is unlikely to be the last non-executing mode we want.

An unrecognised mode falls back to `ModeExecute`, never to `ModeDryRun`. A deploy that silently does nothing is the worse failure, it looks like success.

**The contract that makes dry run useful:** a step must still compute and `Set` every key it declares in `Provides`, even under dry run. Skipping the `Set` would make every downstream step fail with `ErrKeyMissing`, so a dry run would exercise a different code path than a real one, which defeats the purpose. `TestDryRunStillProvidesKeys` pins this.

---

## 6 · Output

`pipeline` never writes to stdout, stderr, or any global. All human-facing output goes through a `Reporter` carried on `State`.

Why it matters: a package that prints to `os.Stdout` cannot be imported by a program that wants JSON, cannot be tested without capturing global state, and cannot be embedded in a larger tool. The interface is what keeps the engine a library.

It is **not** a logger. This is the command's product, what a person reads while watching a deploy. Not diagnostic logging, which belongs behind the importing application's own stack.

A nil `Reporter` is replaced with a discarding one rather than rejected: output plumbing must never be the reason a deploy aborts. `TestNilReporterDoesNotPanic` pins it.

`Plan` returns `[]PlanEntry`, structured values, not pre-formatted lines, so a caller can render text, JSON, or a table without parsing strings back apart. That is also what a future `--json` costs: nothing.

---

## 7 · The runner

### Commands are discovered, not registered

The runner parses every non-test `.go` file in `.lath/` with `go/ast` and treats each exported function as a command. It then generates a dispatcher, compiles it alongside the author's files, and deletes it.

Why parse rather than ask the author to register commands: a registration list is a second place to update, and when it drifts the failure is a command that silently does not exist. Reading the source means writing the function IS declaring the command. There is nothing to keep in sync, and no `main` for the author to write.

Five shapes are supported, as a closed set:

```go
func Name()
func Name() error
func Name(ctx context.Context) error
func Name(args ...string) error
func Name(ctx context.Context, args ...string) error
```

Anything exported with a different shape is **reported by name**, listing what is supported, never silently skipped, because a command that quietly does not exist is the worst available outcome. Methods, unexported functions, `_test.go` files and the generated dispatcher itself are all excluded.

### One subdirectory level is a namespace

A directory inside the definition becomes a command group: `secrets/push.go` is `lath run secrets push`. Root files are unaffected.

The reason is uniqueness the compiler enforces. Separate directories are separate Go packages, so `Push` can exist in `secrets/` and `db/` at once, in one flat package the second would not compile, and the workaround is prefixed function names producing commands like `secretspush`.

Four constraints, each rejecting rather than resolving:

- **One level.** `lath run infra secrets aws push` is a command line nobody enjoys, and arbitrary depth grows a tree walk through the dispatcher, the listing and the help. A deeper directory is reported by name.
- **`internal/` is not a namespace**. Go already gives it a meaning, and helpers are not commands.
- **A namespace may not share a word with a root target.** Resolved by precedence, one of the two would be silently unreachable.
- **A namespace with no usable targets is not one**, importing it would leave an unused import and the generated file would not compile.

See [NAMESPACES.md](NAMESPACES.md) for the full rules and the decisions made while building.

### The template contains no logic

Each case's call statement, `err = Deploy(ctx, args...)`, is computed in Go by `Signature.Statement`, an exhaustive switch, and handed to the template as a finished string. The template performs no conditionals and no string comparison.

The reason is failure locality: a mistake in template logic surfaces as a compile error in generated code the author never wrote, at a line number that means nothing to them. Keeping the logic in Go means it is typed, testable, and covered by `TestEverySignatureRenders`, which fails if a `Signature` is added without a call form rather than emitting a case with an empty body.

### A definition has no entry point of its own

The definition is `package main` with no `func main`. The dispatcher supplies it, and only during a build, so `go build ./...` inside the definition directory fails to link:

```
runtime.main_main·f: function main is undeclared in the main package
```

This is expected, not a defect. A missing `main` is a link-time fault rather than a type error, so `go vet`, `go test`, `gofmt`, and the language server all work on the package unchanged, verified with no generated file present. Only the final link needs it.

⚠️ Consequently the build gate runs `go vet` and `go test` against the definition module, never `go build`. See [DEFINITION-ISOLATION.md](DEFINITION-ISOLATION.md) for the full matrix.

### Content-hash caching

The runner hashes every `.go` file in the definition directory plus `go.mod` and `go.sum`, takes the first 12 hex characters, and names the cached binary with it.

- **Every `.go` file**, because a definition may be split across as many files as the author likes and a rebuild must trigger on any of them. Requiring a manifest of which files count would be configuration that exists only to be forgotten.
- **`go.mod` and `go.sum`**, because a dependency bump changes the compiled result even when no `.go` file did.
- **The generated dispatcher excluded**, because it is derived FROM the hashed files. Including it would make the hash depend on its own output, so every run would miss the cache.
- **Sorted file list**, so the hash does not depend on filesystem iteration order, which differs by platform and would cause spurious misses.
- **Name and length framed into the hash alongside content**, so moving bytes between two files changes the hash. Hashing concatenated content alone would not.

Measured: ~1.4s cold, ~20ms warm. A `touch` that leaves content unchanged is still a hit. The key is content, not mtime.

#### The build environment is part of the identity

The hash also covers lath's own version, the Go toolchain reported by `runtime.Version()`, and the size and modification time of the lath binary itself.

Without that, upgrading lath or Go leaves every cached binary in place: the definition is unchanged, so the hash is unchanged, so a binary generated by the previous dispatcher template and compiled by the previous compiler runs silently and indefinitely.

The executable's own size and mtime are included as well as the version string because **a development build always reports the same version** no matter how many times it is rebuilt, which is precisely the case that breaks. The cost is one extra compile after reinstalling an identical binary.

#### Entries are named, and described

A cache entry is `<name>-<hash>`, where the name is the git repository containing the definition, falling back to the directory holding it, falling back to a constant. Sanitised to `[a-z0-9._]` and capped, so an awkward directory name cannot produce a filename that breaks globbing.

Lookup matches on the hash **suffix**, not the whole filename. Two consequences, both deliberate:

- Renaming a repository does not orphan an entry that is still valid.
- The name never has to be computed on the warm path. Deriving it shells out to git; doing that on every invocation would tax every run to produce something only a human reading the cache directory ever sees.

Each entry carries a JSON sidecar recording the definition's absolute path, its targets, when it was built, and with which lath and Go versions. Without it a cache directory is a pile of anonymous binaries with no way to tell which project any of them came from, and therefore no safe way to prune.

`lath cache` lists entries; `lath cache prune` removes **only** those whose project directory no longer exists; `lath cache clean` removes everything. Prune is deliberately conservative: an entry whose project still exists may be the binary that project runs, and silently costing someone a rebuild would make prune something to hesitate over.

### `GOWORK=off` is required, not an optimisation

The definition module is intentionally not a `go.work` member. With workspace mode active, the toolchain refuses to resolve it at all.

### Cleanup cannot be deferred

⚠️ The generated dispatcher is removed **explicitly** after the build, on every path, never with `defer`. On unix the runner ends by calling `syscall.Exec`, which replaces the process image, so a deferred cleanup would never run and every invocation would leave a generated file behind.

`removeGenerated` refuses to delete any file lacking the generated header, so a hand-written file that happens to share the name survives.

### Process hand-off is build-tagged

On Unix the runner `exec`s the compiled binary, replacing its own process, so the pipeline inherits the terminal, signal disposition, and exit status directly. A wrapper process would have to forward `SIGINT` correctly to avoid leaving a half-finished deploy running after Ctrl-C, which is a well-known source of orphaned work.

Windows cannot replace a process image, so `exec_other.go` spawns a child and propagates its exit code. The build tags exist so that difference is visible rather than emulated badly.

### Exit codes are distinct

`2` usage · `3` definition did not compile · `4` the command failed · `1` internal error. A wrapper script can tell a definition bug (fix the file) from an operational failure (a retry may help) without parsing output. The generated dispatcher is handed the same numbers rather than hardcoding its own, so the two cannot drift.

On a compile failure the runner prints nothing of its own. The compiler already emitted the diagnostic, and restating it would push the useful line up-screen.

## 8 · Definition isolation

`.lath/` has its own `go.mod` and is absent from `go.work`. Two Go behaviours make this work, and both are documented guarantees:

1. **A directory containing its own `go.mod` is excluded from the parent module.** `go build ./...` at the root never descends into it.
2. **`go work vendor` only vendors modules listed in `go.work`'s `use` directives.** The definition is not listed, so it is invisible to vendoring.

Verified by `task isolation`, and by `go list ./...` at the root refusing outright: *"directory prefix . does not contain modules listed in go.work"*.

### Type safety without vendoring

Vendoring is not what provides type safety, `go.mod` + `go.sum` + the module cache is. `vendor/` is a *second copy* of source already in the cache, made so a build can run offline. The language server resolves types from the cache, which is why jump-to-definition on a definition's import lands in `~/go/pkg/mod/…` and completion works with no `vendor/` present.

**`.lath/go.sum` must be committed.** Go defaults to `-mod=readonly`: nothing that only *reads* will write `go.sum`, so without it both the language server and `go build` refuse rather than fetching. With it committed, a completely cold machine downloads and builds automatically.

### Why not a build constraint

A `//go:build` tag excludes a file from the default build but not from the module graph: its imports must still be in the application's `go.mod` for the file to compile at all, and `go mod vendor` copies them. See [DEFINITION-ISOLATION.md](DEFINITION-ISOLATION.md) for the alternatives considered and the measurements behind the choice.

---

## 9 · Open problems

Real, unsolved, and each one changes something.

| # | Problem | Consequence |
|---|---|---|
| 1 | **`Requires` is convention, not compiler-enforced.** A step can read a key it never declared. | `Validate` passes, `Run` fails with `ErrKeyMissing`. Mitigable with a `go vet`-style analyser over step bodies. |
| 2 | **Version skew.** The runner is version X; `.lath/go.mod` pins library version Y. | Definition and runtime disagree. The runner must compare the pin against itself and refuse loudly, or rewrite it. Nothing does this yet. |
| 3 | **No resume.** A run that dies at step 7 restarts at step 1. | Every step is idempotent in this experiment, so it is survivable, real steps will not all be. |
| 4 | **No concurrency control.** Two people can run a deploy simultaneously. | CI's concurrency group prevents this today only as a side effect of deploys being CI-only. A portable binary loses that; the lock has to live on the host. |
| 5 | **No audit trail.** A laptop deploy exists only in that person's scrollback. | Acceptable for one person, not for a team. |
| 6 | **Credentials must exist everywhere the binary runs.** | A laptop that can deploy production is a laptop holding production credentials. |
| 7 | **A Go definition is opaque to non-Go tooling.** | No dashboard or script can read the process list. Fixable with a `--json` describe mode, cheap now that `Plan` returns structured values, expensive once steps do I/O at construction time. |
| 8 | **Requires a Go toolchain wherever a deploy runs.** | Fine for a Go project's CI and laptop. Not fine if a non-Go machine ever needs to deploy. |
| 9 | **The generated dispatcher is written into the author's directory.** | A crashed run can leave it behind. Mitigated three ways, excluded from the hash, excluded from discovery, gitignored, but a temp-directory build would remove the failure mode entirely. It is not done that way because Go requires every file of a package to share one directory. |
| 10 | **Discovery does not type-check.** `context.Context` is matched by selector text, not resolved type. | A local type named `context.Context` would be misclassified. Deliberate: a definition that does not compile must fail with the compiler's own message, not a confusing parse-time complaint. |

Problems 4, 5 and 6 are properties of *any* portable deploy tool, not of this design. Problems 1, 2 and 7 are this design's own, and 2 is the one most likely to cause a confusing failure in real use.

---

## 10 · Rejected alternatives

| Approach | Why not |
|---|---|
| **Plain Go functions**, no step list | Maximum type safety, zero introspection. A function body cannot list what it is about to do, so no `plan` and no "step 4 of 12". That capability is the only reason steps are data. |
| **Generic `Step[In, Out]` with pairwise composition** | Genuinely type-safe data flow, but `Then(Then(Then(…)))` nested ten deep is unreadable and hostile to inserting a step. Correct and unusable. |
| **Shared mutable state struct with public fields** | Where most homegrown engines land. Every step can read and write everything, and nothing catches a step reading a field an earlier step forgot to set. |
| **YAML / a config DSL** | Every mistake is a runtime failure, and the file grows into a language. This is the situation being escaped. |
| **Go plugins** (`plugin.Open`) | Must match compiler version exactly, effectively Linux-only, notoriously fragile. |
| **Embedded Starlark** (as Bazel and Tilt use) | Genuinely good, pure-Go, sandboxed, but dynamically typed, which discards the property this design exists for, and adds a second language to the product. The strongest fallback if the nested module proves too heavy for real users. |
| **A second reverse proxy in front of the app** | For zero-downtime traffic switching. Adds a container, a hop, an `X-Forwarded-For` chain, and another suspect during an incident. Editing the existing proxy's admin API is strictly less machinery. |
| **A hand-written `main` and switch in each definition** | Makes every project re-implement argument parsing and a dispatch switch, boilerplate in every repository, which is what discovery exists to delete. |
| **Template logic branching on signature** | Comparing `.Sig` against string literals in the template means adding a `Signature` without a matching branch generates a case with an empty body, and any template mistake surfaces as a compile error in code the author never wrote. |
| **`defer` for generated-file cleanup** | Does not run: `syscall.Exec` replaces the process image. |

---

## 11 · Testing

`task check` runs `gofmt`, `go vet`, and race-enabled tests across **every** module. The workspace members via the `github.com/ubgo/lath/...` module-path pattern, then the definition module separately with `GOWORK=off`, because it is outside the workspace by design and no workspace pattern can reach it. A gate that silently skips a module is worse than no gate.

What the tests pin, beyond coverage:

- **Fail-fast**: a step after a failure must not run. This is what stops a traffic switch following a failed health check.
- **Cancellation**: the between-step context check actually fires.
- **`Plan` is side-effect free**, so it is safe to run against production as a review step.
- **Dry run still provides keys**, so it stays on the same code path as a real run.
- **Hash sensitivity**, including the byte-moved-between-files case a naive hash would miss.
- **An unset `Scale` means one replica**, never zero. A process silently not starting is the worst reading of a zero value.
- **An invalid mode falls back to execute**, not dry-run.
- **Error sentinels** are matchable with `errors.Is`, so tooling never parses message text.
- **Every `Signature` renders a call statement**. The exhaustiveness check a string-based enum cannot get from the compiler.
- **A missing definition directory reports `ErrNoDefinition`**, so the runner prints what a definition is rather than a raw filesystem error.
- **Wire values are pinned by hand.** Exactly one test spells the literal strings a user types. A typed constant protects use sites from typos but cannot protect its own value: a test that references the constant would agree with a wrong one.

`TestPipelinesAreWired` in the example definition runs `Validate` over every environment. That is the guarantee standing in for compile-time step chaining, and the maintenance cost of the whole approach: adding a pipeline means adding one line to that test.
