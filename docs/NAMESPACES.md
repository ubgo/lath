# Spec, namespaced targets

**Status: built.** Everything below describes behaviour that exists. Where a decision was made, the reason is recorded with it so a later change is a deliberate reversal rather than an accident.

---

## 1 · The problem

Every `.go` file in `.lath/` is one `package main`, so every exported function shares one namespace and one flat command list. Two consequences as a definition grows:

- **Name collisions.** `Push` cannot exist in both `secrets.go` and `db.go`. The workaround is prefixed function names, `SecretsPush`, `DbPush`, which produce commands like `secretspush`.
- **A flat list.** Fifteen unrelated commands in one undifferentiated `lath list` has no structure to read.

Note what is *not* a problem: **code organisation is already solved.** A definition is an ordinary Go module, so helpers can live in subpackages today with no change to lath:

```go
// .lath/secrets.go
import "deploydef/internal/secrets"

func SecretsPush() error { return secrets.Push() }
```

Only the *command names* are flat. This spec is about names, not about where code lives.

---

## 2 · The design

**One subdirectory level becomes one namespace.** Root files keep behaving exactly as they do today.

```
.lath/
  deploy.go          func Deploy      →  lath run deploy
                     func Plan        →  lath run plan
  secrets/push.go    func Push        →  lath run secrets push
  secrets/diff.go    func Diff        →  lath run secrets diff
  db/migrate.go      func Migrate     →  lath run db migrate
```

Three properties, in order of importance:

**Compiler-enforced uniqueness.** `secrets/` and `db/` are separate Go packages, so `Push` in both is legal Go. Uniqueness is the language's guarantee, not a convention lath has to police.

**Additive.** Existing definitions are unaffected. A definition with no subdirectories behaves identically to today, and the feature costs nothing until used.

**Reads as a sentence.** `lath run secrets push` beats `lath run secretspush`, and beats a colon syntax that would have to be parsed and escaped.

### Naming

| Source | Command |
|---|---|
| root file, `func Deploy` | `deploy` |
| `secrets/`, `func Push` | `secrets push` |

The namespace is the **directory name**, lowercased. The target is the **function name**, lowercased. The same rule already applied at root.

---

## 3 · Constraints

### One level only

`.lath/a/b/` is an error, not a deeper namespace.

`lath run secrets push` is the readable limit. `lath run infra secrets aws push` is a command line nobody enjoys typing, and supporting arbitrary depth means the dispatcher, the listing, and the help output all grow a tree walk to serve a case that should not exist. A directory nested deeper is reported by name.

### `internal/` is not a namespace

Go gives `internal/` a defined meaning, packages importable only from within the parent tree. A definition using `.lath/internal/secrets/` for helpers is doing the normal Go thing, and those helpers are not commands.

`internal/` is skipped by namespace discovery entirely, and remains importable by the definition as usual.

### A namespace may not collide with a root target

```
.lath/deploy.go   → func Deploy   → lath run deploy
.lath/deploy/     → namespace     → lath run deploy <target>
```

Both claim `deploy`. This is rejected at discovery, naming both sources, rather than resolved by a precedence rule. A precedence rule means one of the two silently becomes unreachable, which is exactly the failure mode the `run` namespace was introduced to remove.

### A namespace with no valid targets is not a namespace

A directory containing no exported functions of a supported shape is skipped: not imported, not listed. Importing it would produce an unused import and the generated dispatcher would not compile.

Unsupported-shape functions inside it are still **reported**, as they are at root, silently skipping a function the author meant to expose is the worst available outcome.

---

## 4 · What changes

### Discovery

`Target` gains a namespace field. Discovery walks one directory level and records which package each target came from.

```go
type Target struct {
    Func      string  // Go identifier
    Namespace string  // "" for a root target
    Command   string  // "deploy" or "secrets push"
    Package   string  // Go package name, for the qualified call
    // … Doc, Sig, File unchanged
}
```

`Package` is recorded separately from `Namespace` because a directory name and its package clause need not match, `.lath/my-scripts/` cannot be `package my-scripts`, since Go identifiers exclude dashes. The generated import is therefore always aliased.

### The hash

⚠️ **`scan` currently reads only the top level.** It must include every file in every namespace directory, or editing `secrets/push.go` would not invalidate the cache and lath would silently run a stale binary.

The framing stays the same, sorted, name-and-length prefixed. With paths relative to the definition root so the hash does not depend on where the repository sits on disk.

### Generation

The dispatcher gains one import per namespace and qualifies those calls:

```go
import (
    "context"
    "fmt"
    "os"

    secrets "deploydef/secrets"
    db "deploydef/db"
)

    switch cmd {
    case "deploy":         err = Deploy(ctx, args...)
    case "secrets push":   err = secrets.Push(ctx)
    case "db migrate":     err = db.Migrate(ctx)
    }
```

⚠️ **Every import must be used.** A namespace whose targets were all skipped must not be imported, or the generated file fails to compile. With an error pointing at code the author never wrote. This is the same class of bug as the `args`/`ctx` declarations, and needs the same treatment: computed in Go, covered by a matrix test.

Command matching becomes two words for a namespaced target. The dispatcher joins `os.Args[1]` and `os.Args[2]` when the first matches a known namespace, so argument passthrough shifts by one for those.

### Listing

`lath list` groups by namespace, root targets first:

```
targets (from ./.lath):
  deploy             Deploy builds and ships the API
  plan               Plan lists the steps without executing any of them

  secrets
    push             Push writes secrets to the host
    diff             Diff compares declared secrets against live ones

  db
    migrate          Migrate applies pending schema changes

run one with: lath run <target> [args...]
```

---

## 5 · What does not change

- Root-level definitions behave identically. No migration, no flag, no opt-in.
- The `Step` interface, the pipeline engine, and every shipped step are untouched.
- Supported function shapes are unchanged.
- `run` remains the only way to reach a target, so namespaces cannot collide with lath's own verbs.
- The definition is still one Go module; namespaces are packages within it.

---

## 6 · Test plan

Beyond the obvious happy path:

| Case | Asserts |
|---|---|
| namespace + root target with the same name | rejected, naming both sources |
| directory nested two levels deep | rejected by name |
| `internal/` present | not a namespace, and still importable |
| namespace with no valid targets | not imported, not listed, dispatcher still compiles |
| namespace whose package name differs from its directory | aliased import, correct call |
| directory name with a dash | valid command, valid alias |
| every combination of namespaced / root / ctx / args targets | generated dispatcher parses |
| edit a file inside a namespace | hash changes, cache misses |
| two namespaces both exporting `Push` | both reachable, neither shadowed |

The last two are the ones that would silently break: a hash that ignores namespace files runs stale code, and a shadowed target is unreachable with no error.

---

## 7 · Cost

Roughly 80–100 lines across discovery, generation and listing, plus the test matrix. The permanent cost is in the generator, which becomes responsible for a computed import set. The part most likely to produce a confusing failure, and the reason the unused-import case gets its own test.

---

## 8 · Decided

These were open when the spec was first written; all three follow from rules that already exist.

### `lath run secrets` with no target lists that group

`lath run` alone already lists every target. The same rule one level down: an incomplete command shows what would complete it.

```
$ lath run secrets
targets in "secrets":
  push      Push writes secrets to the host
  diff      Diff compares declared against live
```

Erroring instead would be gratuitous. The information needed to recover is already in hand.

### A namespace heading comes from its package comment

If `secrets/doc.go` carries a package comment, its first sentence becomes the group's heading in `lath list`. Absent one, the group shows just its name.

This is the rule already used for target descriptions, applied one level up, and it reuses the same doc-comment parsing.

### `--help` needs no special case

The existing rule already covers the extra depth: **a help flag belongs to lath only at the position immediately after a complete command name.**

```
lath run secrets --help        lath's help for the group
lath run secrets push --help   passed to the target, untouched
```

At depth two the command name is two words, so the boundary simply sits one position further along. Nothing to add.

---

## 9 · Decisions made while building

These were not in the original spec. Each was a choice with an alternative, and each is recorded so a later change is deliberate.

### Variadic targets receive `os.Args[N:]` inline, not a declared variable

The dispatcher does not declare `args`. A root target's call renders as `Deploy(ctx, os.Args[2:]...)`, a namespaced one as `secrets.Push(ctx, os.Args[3:]...)`.

The alternative, declaring `args := os.Args[2:]` once, breaks whenever no target is variadic, because Go rejects an unused variable. That was a real bug once already, when a scaffolded definition of two non-variadic targets failed to compile. An inline expression cannot be unused, so the entire failure mode disappears rather than being guarded against.

The depth-dependent offset is passed into `Signature.Statement` for the same reason: the generator computes it, so the template contains no arithmetic.

### Imports are always aliased, with a prefix

A namespace is imported as `ns_secrets "deploydef/secrets"`, never as bare `secrets`.

Two reasons. A directory named `my-scripts` cannot be `package my-scripts`, so relying on the declared package name would require parsing it out and hoping the two agree, and an unprefixed alias could collide with an identifier the author wrote in the definition root. A variable named `secrets` would shadow the import. The `ns_` prefix makes both impossible.

### A namespace's package name is recorded but not used for calls

`Target.Package` holds the declared package name. Calls are qualified with the **alias**, not this. The field is kept because it is what a future diagnostic would need, "directory `secrets` declares `package foo`" is worth being able to say, but nothing depends on the two matching.

### `internal/` is skipped at both levels

Not only as a namespace, but also when checking depth: `.lath/secrets/internal/` does not trip the too-deep error, because a namespace is entitled to its own helpers under Go's normal rules.

### The hash walks everything, including `internal/`

`hashableFiles` covers the definition root, every namespace, and `internal/`. Helpers are compiled into the dispatcher, so a change to one changes the result, excluding them would reintroduce the stale-binary failure at one level down.

Paths are hashed **relative** to the definition root, so the hash does not depend on where the repository sits on disk. Two checkouts of the same commit produce the same hash and share a cache entry, which is correct.

### A group with nothing after it is answered by the runner, not the dispatcher

`lath run secrets` lists that group without compiling anything. The runner already knows every target from discovery, so paying a compile to print a list it can already produce would be wasted.

The generated dispatcher has its own handling for the same case, because it is reachable directly when the binary is invoked outside lath.
