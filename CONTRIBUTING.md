# Contributing to lath

Thanks for looking. This document is the short version of how the repository works; [CLAUDE.md](CLAUDE.md) is the long version and is worth reading before a substantial change, because it explains *why* things are shaped the way they are.

## The one question that decides where code goes

**Does it import `pipeline`?**

No → it is a library, it belongs in `kit/`, and any Go program can use it whether or not it has ever heard of lath. Yes → it is a pipeline adapter and belongs in `steps/`.

That is the whole rule. A vendor name in a package (`kit/docker`, `kit/cloudflare`) does not make it project-specific; depending on the framework does.

## The second question, before adding to `steps/`

**Would two projects want it to mean different things?**

If yes, ship the pieces and let each project say what it means. This is why there is no `deploy`, `rollback` or `promote` verb: "roll back" means the previous git commit in one project and the last image that passed a health check in another, and a built-in verb would have had to pick one and be wrong for the rest. `docker.Migrate` became `RunOnce` for the same reason.

## Setup

```sh
go version          # 1.24 or newer
task --list         # every command in the repository
task build          # a development binary at bin/lath
task check          # the gate: fmt, vet, race tests, cross-build, docs
```

There is no vendored toolchain and nothing to install beyond Go, `task`, and `python3` for the documentation gates.

## Before you open a pull request

Run `task check`. It must pass, and it covers more than tests:

| Gate | What it proves |
|---|---|
| `fmt:check`, `go vet` | Formatting and the obvious mistakes |
| `go test -race` | Every module, including the definition module outside the workspace |
| `crosscheck` | It still **compiles and vets** for linux, darwin and windows — vet as well as build, because `go build` skips `_test.go` files and a unix-only call in a test leaves the test binary unbuildable elsewhere. Compiling is not support: linux and macOS run the full gate in CI; Windows is checked statically only, and `gh workflow run windows.yml` is how that changes |
| `docverify` | Every symbol named in the reference docs exists, and every exported symbol is documented somewhere |
| `sigverify` | Every signature shown in the docs is the signature that exists |

Then `task cover` for the coverage number, and `task cover:check` to confirm the README badge is not stale. The figure moves a little between platforms — tests that need `ruby`, `sha256sum` or `git` skip where those are absent — so CI enforces a floor rather than the exact number.

**`task test:linux` runs the whole gate on Linux, in Docker.** Linux and macOS are both supported, and a developer on one cannot check the other; this closes that gap before a push rather than after a red CI badge. It runs `task check` and `task cover` — the same gate, not a copy of its steps — against your working tree, with the build cache in a named volume so repeat runs are quick. `task test:linux:shell` drops you into the same image to reproduce a failure by hand.

It earns its place: the first run of it found a step that shelled out to `docker` during a **dry run**, which made a rehearsal impossible on any machine without docker installed. Every test passed on macOS, where docker exists.

[`docs/TESTING.md`](docs/TESTING.md) has the rest: what each gate proves, how coverage is measured across modules, what the Linux image contains and the rule that decides, and what "supported platform" means here.

## Standards

- **Tests pin behaviour, not statements.** A test named for what breaks when it fails is worth ten that raise a percentage. Every test in this repository states, in its comment, what goes wrong in the real world if the assertion stops holding.
- **Comment the *why* and the *invariant*.** The code says what it does. A comment that repeats it adds nothing; one that explains why it is this way, and what a caller must not break, is why a stranger can change this repository safely.
- **Named constants for closed sets and wire formats.** A string docker prints, a header GitHub reads, a separator a manifest uses — those get names, because a literal at the comparison looks like a language constant and is silently wrong when the other end changes.
- **No suppressed diagnostics.** Fix the cause rather than silencing the tool. A `//nolint` in a pull request needs a paragraph explaining why the tool is wrong.
- **Every escape hatch is typed and documented.** A primitive with no way out gets abandoned wholesale the first time it does not quite fit, taking its guarantees with it.

## Commits

Conventional commits: `type(scope): description` — `feat(kit):`, `fix(pipeline):`, `docs:`, `test:`, `refactor(steps):`.

The body is where the reasoning goes. A commit that changes a default, reverses a documented decision, or removes something should say what it was, why it changed, and what would make someone change it back.

## Versioning

Modules are versioned independently and are **not tagged yet**. Until they are, a definition resolves them through `replace` directives pointing at a checkout. When tagging begins: a module tags by directory path (`kit/v0.2.0`), which the Go module proxy requires; the runner tags bare.

## Reporting a bug

Include what `lath plan <target>` printed, what actually happened, and the output of `lath version`. A pipeline definition that reproduces it is the fastest possible path to a fix — it is Go, so it compiles on our machine too.

Security issues go to [SECURITY.md](SECURITY.md), never to a public issue.
