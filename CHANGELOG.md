# Changelog

All notable changes to this project are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each module is versioned independently, because they are independently importable: `kit`, `pipeline`, `steps` and `cmd/lath` can move at different speeds. A tag names the module it belongs to (`kit/v0.2.0`), except the runner, which tags bare.

## [Unreleased]

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
