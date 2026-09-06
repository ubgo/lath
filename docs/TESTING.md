# Testing, coverage and platforms

How this repository proves it works, what "supported" means here, and how to check Linux from a Mac before pushing.

## The gate

```sh
task check
```

One command, six checks, and it is what CI runs — the workflow installs `task` and calls this rather than re-listing the steps, because a workflow that re-lists them becomes a second definition of the gate and the two drift until CI passes what a developer's machine rejects.

| Check | What it proves |
|---|---|
| `fmt:check`, `go vet` | Formatting, and the mistakes vet catches |
| `go test -race` | Every module, **including the definition module outside the workspace** — a gate that silently skips a module is worse than no gate |
| `crosscheck` | It still **compiles and vets** for linux, darwin and windows — vet included because `go build` skips test files, and a test using a unix-only call leaves the test binary unbuildable elsewhere |
| `docverify` | Every symbol the reference docs name exists, every exported symbol is documented somewhere, and the package count the kit index states is true |
| `sigverify` | Every function signature the docs *show* is the signature that exists |

The two documentation checks exist because prose rots silently. `docverify` was added after a docs pass referenced three symbols from memory and all three were wrong; `sigverify` after an audit found nine signatures `docverify` had called clean, including one missing a whole parameter.

## Coverage

```sh
task cover              # per package, and the statement-weighted total
task cover:badge        # rewrite the README badge to the measured number
task cover:check        # fail if that badge is stale, or below the floor
task cover -- --min 90  # any floor you like, for a one-off check
```

`go test -cover` cannot measure this repository: it is four modules plus a definition module deliberately outside the workspace, so no single command sees all of it, and a number that quietly omits a module reads as a measurement of the whole. `scripts/coverage.py` runs each module set and weights the total by **statements**, not by averaging percentages — a 100% package with nine statements and a 50% package with nine hundred do not average to 75% in any sense a reader would accept.

Packages with no test files are **named**, not scored zero. A package that is pure type declarations should not be punished, and hiding it is how "no tests here" stays true.

⚠️ **The number moves slightly between platforms, and that is honest.** Tests that need a tool skip where it is absent — `git` for the publish tests, `sha256sum` for the manifest interop check, `ruby` for the Homebrew formula check. So CI enforces a **floor** rather than the exact figure; pinning the figure would fail every time a runner image changed which tools it ships. The badge records whatever `--badge` last measured.

The floor sits deliberately below the current number. A gate pinned to today's coverage fails on any honest refactor that moves a statement, which teaches people to lower the floor rather than fix the test.

## Linux, from a Mac

```sh
task test:linux                        # the whole gate on linux, in docker
task test:linux:shell                  # a shell in the same image, to debug a failure
task test:linux PLATFORM=linux/amd64   # the other ARCHITECTURE, under emulation
```

Linux and macOS are both supported, and a developer on one cannot check the other. Waiting for CI to find a Linux-only failure costs a push, a red badge and a fix commit for something catchable in twenty seconds.

It mounts your working tree — uncommitted changes included — and runs `task check` and `task cover` **inside** the container. The same gate, not a copy of its steps. Go's build and module caches live in named volumes, which is the difference between a twenty-second re-run and a five-minute one.

### What the image contains, and the rule that decides

`python3` for the documentation and coverage scripts, `git` for the tests that drive a real repository, `task` pinned to the version CI installs. Nothing else.

> **Install a tool in this image only when its absence would let a *Linux-specific* difference through.**

`ruby` failed that test and was removed after being added: `kit/brew` pipes its rendered formula through `ruby -c`, which is worth doing — a syntax error in a formula reaches a user at `brew install` and never at release — but rendering a formula is pure string building with no platform behaviour in it, and the check already runs on macOS and on GitHub's ubuntu runner. Measured identical coverage with and without, and 36 MB lighter without.

**The image runs as a non-root user**, and that one does pass the rule. Root is permitted to do the things several tests exist to prove are refused — reading a file with mode `0000`, writing into a directory with no write bit — so those tests skip under root and a root run silently checks *less* than a developer's machine. Fixing it moved the Linux figure from 87.8% to 88.3%.

### The other architecture

`PLATFORM=linux/amd64` on an arm64 Mac (or the reverse) runs everything under QEMU. It is **slow** — minutes rather than the usual twenty seconds, and a separate set of cache volumes has to fill first — so it is not part of anyone's normal loop. Run it when touching anything that could plausibly differ by architecture, or when a CI failure appears on a runner whose architecture is not yours.

Its value is not really architecture. It is that emulation makes everything take longer, which is the cheapest way to find a test whose timing assumptions are too tight: real machines get slow too, under load, on a shared CI runner, inside a container with a CPU quota. A test that fails here is a test that would eventually have failed there, at random, with nobody able to reproduce it.

That is exactly what it found — three times in one run:

- **`TestStatsAccumulateRuntime`** asked which of two outcomes happened — is backoff counted as runtime — but bounded the answer 80ms from the wrong one. It now measures against the wall clock of its own run, so the assertion calibrates itself: a slow machine inflates both figures together and the property survives.
- **`TestBackoffGrowsAndIsCapped`** compared consecutive delays 20ms apart while the machine added ~20ms of noise to each. It measured 43ms then 42ms — the delays *had* grown, and the measurement could not see it. Durations are five times larger now, so the signal dwarfs the noise.
- **`kit/download` had a real bug**, not a test one. Its documentation promised it never depends on process-global HTTP settings, and it built a fresh `http.Client` — with a nil `Transport`, which silently means `http.DefaultTransport`. The connection pool was shared with the whole process, so anything calling `CloseIdleConnections` (which `httptest.Server.Close` does on every close) could break an unrelated download mid-flight. It now clones its own transport. On a fast machine that window is too small to hit; under emulation it opened wide enough that a parallel test's server closed while another test was still reading.

The last one is the argument for this mode in one paragraph: the bug was in shipped code, it contradicted a promise in its own package comment, and nothing on a developer's machine would ever have shown it.

### It earned its place on the first run

Two bugs, both invisible on macOS:

1. **`stop-previous` shelled out to `docker ps` during a dry run**, making a rehearsal impossible on any machine without docker installed. The read stays — naming the containers a real run would retire is most of what makes a rehearsal worth reading — but under dry run a failure to list is now reported and the run continues. A real run still fails, as it must: proceeding without knowing which containers exist would leave two releases answering to the same alias.
2. **Two `proc` tests asserted a premise instead of checking it.** They signal pid 1 expecting `EPERM`, which holds on a normal machine and is false in a container, where pid 1 *is* the test's own shell. They now ask the kernel whether pid 1 is signallable and skip when it is, rather than reporting a product bug that does not exist.

## Platforms

Last measured on 2026-09-06, running the full gate on each:

| Platform | How | Result | Coverage |
|---|---|---|---|
| macOS · arm64 | `task check` on the host | ✅ pass | 88.2% |
| Linux · arm64 | `task test:linux` (Docker, native) | ✅ pass | 88.3% |
| Linux · amd64 | `task test:linux PLATFORM=linux/amd64` (QEMU) | ✅ pass | 88.2% |
| Windows | `gh workflow run windows.yml` | ⚠️ **unsupported** — 25 of 33 packages pass, see below | — |

Linux measures marginally higher because the container runs as a non-root user, so the permission tests that skip elsewhere actually execute. The amd64 figure matches macOS for the same reason in reverse — see the note on platform variance above.

| | Supported | Compiles + vets | Tests run |
|---|---|---|---|
| Linux | ✅ full gate in CI, and locally via `task test:linux` | ✅ | ✅ |
| macOS | ✅ full gate in CI | ✅ | ✅ |
| Windows | ❌ **not yet** — one real bug, one documented limitation, and a set of POSIX-only test assumptions | ✅ every commit, via `crosscheck` | ⚠️ on demand, 25 of 33 packages pass |

**Compiling is not support.** `crosscheck` keeps Windows building so the `!unix` fallbacks do not rot, and that is all it claims. What is already known to differ there: the runner cannot replace its own process image, so it spawns a child and forwards the exit code instead of `exec`; terminal detection is a stub that always answers yes, which affects `--debug`; `proc.Find` needs `pgrep` and reports `ErrUnsupported`; `kit/lock` falls back to `StaleAfter` because it cannot verify liveness.

### Testing Windows

There is no way to run Windows tests from a Mac or a Linux box — Docker's Windows containers need a Windows host, and a VM is a heavier commitment than this question deserves. So there are two levels:

**Statically, on every run.** `crosscheck` builds *and vets* for all three platforms. The vet half matters more than it sounds: `go build` ignores `_test.go` files, so a test using a unix-only call compiles cleanly here and leaves the package's test binary unbuildable on Windows. That happened — a `syscall.Kill` helper — and it is the same failure `crosscheck` was created for, one directory over. Vet type-checks the tests, so it is caught now.

**Actually running them: `.github/workflows/windows.yml`, on demand.**

```sh
gh workflow run windows.yml
gh run watch
```

Manually dispatched, never on push — because a red badge for a platform nobody claims to support teaches people to ignore red badges. Not for cost: GitHub-hosted standard runners are free and unmetered on public repositories.

#### What it found, first real run (2026-09-06)

**25 of 33 packages pass**, including every network and API package, the whole `pipeline` and `steps` layer, and the example definition end to end. Windows is considerably closer to working than "unsupported" suggests.

| Failing package | Why |
|---|---|
| `kit/fsx`, `kit/archive`, `kit/hashtree`, `kit/lock` | **Test assumptions.** They assert that a file with mode `0000` cannot be read and a directory with no write bit cannot be written. Windows does not honour POSIX mode bits that way, so the operation *succeeds* and the test reports a failure that is not one. |
| `cmd/lath` (cache, manifest) | Same cause, plus two path-shape assertions that hardcode forward slashes. |
| `cmd/lath` (`TestBuildCompilesADefinition`) | **A real bug.** `cacheEntryPath` builds the compiled definition with no `.exe` suffix, so the runner cannot exec what it just built: *executable file not found in %PATH%*. |
| `cmd/lath` (`isTerminal`) | **A known limitation, behaving as documented.** The `!unix` stub always answers "yes", so tests asserting that a pipe is not a terminal fail. The stub is deliberate — see `tty_other.go` — but it means `--debug` cannot refuse a session it should. |
| `kit/proc`, `kit/scan`, `kit/session` | `pgrep`/`ps` are absent, and the session tests assume POSIX paths. |

So the work to support Windows is smaller than it looks and splits cleanly: **one product bug** (the `.exe` suffix), **one product limitation** (terminal detection), and a pile of tests that need to state *"this asserts POSIX permission semantics"* and skip where those do not exist — the same shape as the `pid 1` fix the Linux container forced.

None of it is scheduled. It is written down so the next person to want Windows starts from a list rather than from a green tick that meant nothing.

#### The first run of this workflow tested nothing

Worth recording, because the failure mode is general. It ran `go test ./...` from the repository root, which fails with *"directory prefix . does not contain modules listed in go.work"* — the root is a workspace root, not a module, which this repository documents in two places and which the workflow walked straight into. `continue-on-error` then turned that setup failure into a **green job**.

Two lessons, both now built in: the module-path pattern is used everywhere, and the workflow writes each suite's real `outcome` into the job summary, because `continue-on-error` makes a step's *conclusion* success no matter what happened, and a result that has to be dug out of the logs is a result nobody reads.

When it passes consistently, Windows earns a place in the main CI matrix and a line in the README — in that order. A badge is a claim, and the gate is what makes a claim true.

## How tests are written here

- **A test is named for what breaks in the real world when it fails**, and its comment says so. `TestARehearsalDoesNotRequireDocker` beats `TestStopPreviousDryRun`: the first tells you what you lose, the second only where to look.
- **Behaviour, not statements.** Coverage is a diagnostic for finding *unexercised* code, never a target. The example definition sat at 38% while every one of its pipelines validated — the tests asserted wiring and never ran the commands a reader copies.
- **`runner.Fake` is the shared double**, exported deliberately rather than living in a `_test.go` file, because `docker`, `git` and `steps` all need it and three copies is how they drift apart. It records every command and replies from a script, so command lines are asserted with no docker, no git and no network.
- **Interop is checked against the real tool.** `kit/checksum` runs the system `sha256sum` against a manifest it wrote; `kit/brew` pipes its formula through `ruby -c`; `kit/git`'s write half runs against real git with a bare repository as the remote. The claim in each case is "the other end accepts this", and only the other end can make it.
- **A skip states its reason**, and the reason is a fact about the machine — no `git` here, no terminal, running as root — never a way around a failure.
- **Timing assertions state which of two outcomes happened, and leave a wide gap between them.** Never a precise duration: those fail on a busy machine and tell nobody anything. Two tests in `kit/supervise` were fixed this way — one by moving the outcomes 120ms apart, the other by measuring against the wall clock of its own run rather than an absolute figure.
- **Gates are verified by breaking them.** A check nobody has watched fail is itself an unverified claim. `sigverify` was sabotaged deliberately before being trusted, and the sabotage found that it silently skipped every method in the docs: 113 "verified" signatures were 113 plain functions and zero methods.
