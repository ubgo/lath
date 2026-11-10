# Changelog

All notable changes to this project are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each module is versioned independently, because they are independently importable: `kit`, `pipeline`, `steps` and `cmd/lath` can move at different speeds. A tag names the module it belongs to (`kit/v0.2.0`), except the runner, which tags bare.

## [Unreleased]

## [kit/v0.1.2] · [steps/v0.1.1] — 2026-11-10

### Added

- `kit/golang` — compiles Go binaries on the host for an image to copy, so the machine's build cache carries over instead of every image build starting cold. One `go build` for every main package, `CGO_ENABLED=0`, `-trimpath -buildvcs=false`, `-X` stamps, and `GOTOOLCHAIN` pinned to the toolchain the workspace or module names (`ReadToolchain`), so a newer local Go never builds a different binary than CI. The output folder is cleared first and every expected binary is proven present afterwards.
- `steps/golang.Build` — the pipeline step over it (`build-binaries`). `VarsFromState` binds `-X` stamps to state the way `docker.Build.ArgsFromState` binds build args; `BuildTimeVar` stamps the build time. The example definition uses it.
- `steps/docker.CheckImage` — refuses the image in `KeyImage` unless it is for the expected platform and carries the expected labels, for an image built in one job and shipped from another, where the tag alone cannot prove it is the right one.
- `common.KeyRevision` — the full commit SHA, provided by `git.ResolveCommit` beside `KeyCommit`.
- `steps/docker.Build` labels every image with `org.opencontainers.image.revision` (the full commit, from `KeyRevision`). `OmitRevision` turns it off.
- `kit/docker` — `BuildOptions.NoProvenance` (`--provenance=false`: one manifest instead of an index with an attestation, which survives `docker save` / `docker load` whole), `Client.InspectImage` and `ImageInfo`, and the `LabelRevision` / `LabelSource` OCI label keys.
- `kit/git` — `Client.Revision`, the full SHA of HEAD.
- `kit/argv` — splits a target's arguments into positionals and flags and refuses a flag it does not know, suggesting the closest one, so `--prebuit` is an error instead of silently ignored.

### Changed

- `steps/docker.Build` now requires `KeyRevision` unless `OmitRevision` is set. A pipeline that produces `KeyCommit` with its own step rather than `git.ResolveCommit` fails `Validate` until it provides `KeyRevision` too or sets `OmitRevision`. A revision label in `Options.Labels` needs `OmitRevision`.
- `git.ResolveCommit` refuses to continue if HEAD moved between reading the short and the full commit, which would tag and label the image with different commits.

- `task lint` runs `golangci-lint` (default linters, `.golangci.yml`) on every module and is part of `task check`.

- Licensed under Apache-2.0 (was MIT), matching the rest of the ubgo libraries.

### Upgrading to steps/v0.1.1

Every one of these fails loudly (a validation error or a failing test), none silently:

- **A pipeline that sets `KeyCommit` with its own step** (not `git.ResolveCommit`) and uses `docker.Build`: provide `KeyRevision` too, or set `OmitRevision`.
- **A revision label already in `docker.Build`'s `Options.Labels`:** delete it (Build now adds it from `KeyRevision`), or set `OmitRevision` to keep yours.
- **Tests that fake git with `runner.Fake`:** `ResolveCommit` now also runs `git rev-parse HEAD`. Script a reply whose output starts with the short commit, or the step reports `ErrNoCommits` or that HEAD moved.
- **Tests that assert `docker.Build`'s exact command line:** it now carries `--label org.opencontainers.image.revision=<full commit>`.

### Fixed

- `kit/lock` — the lock file is published with its contents in one step, so a racing `Acquire` never sees it empty.
- `kit/lock` — the Windows build no longer spawns `ps`. It is usually present there (Git for Windows ships MSYS's) yet cannot report a Windows process's start time, so every `Acquire` paid for up to three useless spawns capped at two seconds each, and a fail-fast refusal took 1.9s on CI. `processStart` and `isZombie` are unix-only; Windows returns the documented "unverifiable" answers without running anything.

## [kit/v0.1.1] — 2026-09-19

### Fixed

- `kit/remotefs` — a write lands in a temporary beside the target and is renamed over it, never redirected into it. Redirecting opened the existing file for writing, so a deploy user could not replace a root-owned 0644 file in a directory it owned, and a production deploy stopped at `ensure-route` with `Permission denied` while trying to write a file whose bytes had not changed. Renaming needs only the directory and is atomic. A directory at the target path is refused rather than moved into.
- `kit/caddy.EnsureSite` — skips the write when the file on disk is already byte-identical, and on a failed validation restores the previous file rather than deleting it, so a rejected replacement no longer takes a working route down.

## [kit/v0.1.0] · [pipeline/v0.1.0] · [steps/v0.1.0] · [cmd/lath/v0.1.1] — 2026-09-06

The first tagged release of every module, so a definition resolves them from the module proxy rather than through `replace` directives pointing at one machine's checkout.

### Added

- `kit/github` — GitHub releases and their artifacts, over the REST API rather than the `gh` CLI, with the assets readable back so a publish can verify itself.
- `kit/brew` — renders a Homebrew formula for a released binary. Rendering only; which tap, and what the description says, stay with the caller.
- `kit/checksum` — the `checksums.txt` manifest, in the format `sha256sum -c` reads.
- `kit/git` — the write half: `Clone`, `Commit`, `Push`, `Tag`, `PushTag`, `WithIdentity`. The package was read-only while a deploy was its only caller; publishing needs writes, and the alternative was project code shelling out to git.
- `kit/docker` — `Images`, `RemoveImage`, `Reference`, `TagOf`.
- `steps/docker.UseImage` — deploy an image that already exists, so a rollback ships the artifact that was running rather than a rebuild of it.
- `task cover` — per-package coverage and a statement-weighted total across every module, with a README badge and a CI floor.
- `task sigverify` — proves every function signature in the reference docs matches the one that exists.

### Changed

- `docker.Migrate` is now `docker.RunOnce`, and `common.SwitchTraffic` is now `common.Request`. Both were named for a *use* rather than for the mechanism; see [CLAUDE.md](CLAUDE.md).
