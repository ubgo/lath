# Why the definition is a nested module

A deploy definition is Go code that lives inside the repository it deploys. That raises one hard question: **how does the definition's dependency tree stay out of the application's?**

This document records the requirement, the three approaches that satisfy part of it, the measurements that separate them, and what the chosen one costs.

---

## 1 · The requirement

A definition imports the pipeline engine and the step library. Those, in turn, will eventually pull in an SSH client, a Docker API client, and whatever a secret backend needs.

None of that belongs in the application's dependency graph. Concretely, it must not:

- appear in the application's `go.mod` or `go.sum`
- be copied into `vendor/` by `go mod vendor` or `go work vendor`
- be compiled by `go build ./...`
- constrain the application's own dependency versions

That last one is the least obvious and the least escapable. Two packages in one module share one version of every dependency. If the application pins a library at v1 and a deploy step needs v2, a single module cannot express it. There is no flag, no tag, and no ordering that resolves it.

At the same time the definition must stay **first-class Go**: the editor has to type-check it, offer completion, and rename across it. A definition the compiler checks but the editor cannot is worse than no type safety at all, because the errors arrive late.

---

## 2 · Candidate: a build constraint

Mark the definition with a build tag so ordinary builds skip it:

```go
//go:build deploy

package main

func Deploy() error { … }
```

**Measured on Go 1.24:**

| Check | Result |
|---|---|
| `go build ./...` compiles it | ✅ excluded, as intended |
| `go mod tidy` leaves `go.mod` alone | ❌ **the import is added** |
| `go mod vendor` skips it | ❌ **the package is copied into `vendor/`** |

`go mod tidy` and `go mod vendor` consider *all* build configurations, not just the default one. A tag hides a file from the compiler's default build; it does not hide it from the module graph.

That could be dismissed as tidy being over-eager, except the dependency is not optional. With the import absent from `go.mod`:

```
$ go build -tags deploy ./...
definition.go:5:8: no required module provides package …
```

The definition **cannot compile** unless its imports are in the application's `go.mod`. Their presence there is a precondition of the approach working at all, not an artefact of a tool being conservative. Everything downstream, vendoring, version resolution, dependency scanning, follows from that.

It also fails the version-constraint requirement outright: one module, one version of everything.

**Rejected.** It satisfies exactly one of the four constraints.

---

## 3 · Candidate: a dot-prefixed directory

Go's package walker skips directories whose names begin with `.` or `_`. A definition in `.lath/`, with no module of its own, is therefore invisible to the tooling:

| Check | Result |
|---|---|
| `go list ./...` sees it | ✅ excluded |
| `go mod tidy` adds its imports | ✅ **no**, `go.mod` stays empty |
| the definition compiles | ❌ **no** |

The same invisibility that keeps its imports out of `go.mod` prevents them from resolving *against* `go.mod`:

```
$ cd .deploy && go build ./...
d.go:2:8: no required module provides package …
```

The directory is unreachable by the tooling in both directions at once.

**Rejected.** It buys isolation by making the definition unbuildable.

---

## 4 · Decision: a nested module

The definition directory carries its own `go.mod` and is deliberately absent from the workspace's `use` list.

| Check | Result |
|---|---|
| `go build ./...` at the root compiles it | ✅ excluded, *"directory prefix . does not contain modules listed in go.work"* |
| `go work vendor` copies its dependencies | ✅ **no**, `vendor/modules.txt` holds only `## workspace` |
| the definition compiles | ✅ |
| the editor type-checks it | ✅ |
| independent dependency versions | ✅ |

Two Go behaviours make this work, and both are documented guarantees rather than side effects:

1. **A directory containing its own `go.mod` is excluded from the parent module.** The parent's package patterns never descend into it.
2. **`go work vendor` vendors only the modules named in `go.work`'s `use` directives.** A module that is not listed is not reached.

### Editor support does not depend on vendoring

A frequent objection is that skipping `vendor/` costs type safety. It does not. Those are unrelated mechanisms:

```
type checking needs the dependency's SOURCE on disk

  the module cache   ~/go/pkg/mod/<module>@<version>/     ← always present
  vendor/            ./vendor/<module>/                    ← an optional copy
```

The language server resolves types from the module cache. Jump-to-definition on a definition's import lands in `~/go/pkg/mod/…`, completion works, and rename works. With no `vendor/` anywhere. Vendoring exists for hermetic and offline builds, not for types.

### `go.sum` must be committed

Go defaults to `-mod=readonly`: nothing that only *reads* will write `go.sum`. Without it committed, both the compiler and the language server refuse rather than fetching. With it committed, a cold machine downloads and builds with no intervention.

---

## 5 · What this costs

| Cost | Mitigation |
|---|---|
| A second `go.mod` and `go.sum` to maintain | Written by `lath init`, which copies the repository's `go` directive so the toolchains match, and runs `go mod tidy` so `go.sum` exists. A dependency bump is one `go mod tidy` in that directory |
| **Invisible to root tooling**, linters, dependency scanners, and `go test ./...` do not reach it | The build gate runs a second pass with workspace mode disabled. A dependency scanner needs the directory added to its configuration, or the definition's dependencies age silently. **This is the sharpest cost.** |
| Local development needs a `replace`, or a published module | Identical under every candidate; not a differentiator |
| Package patterns must be module paths, not `./...` | The repository root is a workspace root and not itself a module, so `./...` cannot resolve there regardless |
| `go build ./...` fails inside the definition directory | Expected, see below. Every other Go tool works unchanged |

### `go build` in the definition directory

The definition is `package main` with **no `func main`**. The entry point is generated at build time, compiled alongside the author's files, and removed again. Building the package directly therefore has nothing to link:

```
$ cd .lath && go build ./...
runtime.main_main·f: function main is undeclared in the main package
```

Measured, with no generated dispatcher present:

| Command | Result |
|---|---|
| `go build ./...` | **fails**, no entry point to link |
| `go vet ./...` | ✅ |
| `go test ./...` | ✅ |
| `gofmt -l .` | ✅ |
| language server diagnostics | ✅ **none**, the editor stays clean |

A missing `main` is a **link-time** fault, not a type error, which is why analysis, tests, formatting and editor tooling are all unaffected. Only the final link needs the generated file.

⚠️ If a generated dispatcher happens to be present, left behind by an interrupted run, `go build` **succeeds** instead, and writes an executable named after the definition's module (`./deploydef` for a module named `deploydef`) into the source directory. `lath` never does this; it always builds to the cache with `-o`, but a person running the Go tools by hand will produce one, so the definition directory is worth a `.gitignore` entry.

Two consequences worth stating plainly, because both look like bugs otherwise:

- **The build gate must use `go vet` and `go test`, never `go build`,** on the definition module. This is why `task check` runs those two.
- **Use `lath` to build a definition.** It is the only thing that supplies the entry point.

---

## 6 · When to revisit

The decision rests on the definition having a dependency tree worth isolating. Two situations would change it:

- **The definition never imports anything beyond the standard library.** Isolation would then buy nothing, and a build constraint would be cheaper. Unlikely to hold once steps talk to a host, a registry, or a secret store.
- **Go gains per-file or per-directory dependency scoping.** The module boundary is the only mechanism the language currently offers.

Nothing else about the design depends on this choice. The engine, the step interface, and the runner are unaffected by where the definition lives.
