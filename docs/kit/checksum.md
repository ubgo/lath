# `kit/checksum` — the manifest that ships beside artifacts

Writes and verifies `checksums.txt` in the format `sha256sum` and `shasum -a 256` already read. That choice is the package: a manifest anyone can check by hand, on any machine, with nothing from this project installed.

```sh
sha256sum -c checksums.txt
```

```go
manifest, err := checksum.Write(distDir, artifacts)   // → distDir/checksums.txt
entries, err := checksum.Parse(downloaded)            // read one back
err = checksum.VerifyFile(archive, entries)           // check the one file you fetched
```

| Symbol | What |
|---|---|
| `Of(path)` | One file's sha256, streamed — an artifact is never held in memory to hash it |
| `Manifest(paths)` | `[]Entry`, sorted by name |
| `Render(entries)` · `Parse(r)` | The sha256sum text format, both directions |
| `Write(dir, paths)` | Manifest for `paths`, written into `dir` as `DefaultName` |
| `Verify(dir, manifest)` | Every listed file, in `dir` |
| `VerifyFile(path, manifest)` | One file against a whole manifest — what a downloader needs |
| `Entry` | `Name` (a **basename**) and `SHA256` |
| `DefaultName` | `checksums.txt` |
| `ErrMismatch` · `ErrNotListed` | A corrupted file and an unlisted one — opposite problems, and a caller retries only one |

## Gotchas

⚠️ **Names are basenames, never paths.** A manifest is consumed where the files were *downloaded*, which is rarely where they were built; a leaked build path turns verification into a puzzle about someone else's filesystem.

⚠️ **A listed file that is absent is a mismatch, not a skip.** A manifest describing artifacts that were never uploaded is the failure this exists to catch, and silence there reports a broken release as a good one.

Entries are sorted, so two runs over the same files produce identical bytes — manifests get committed and diffed, and a diff full of reordering hides the line that changed. `Parse` accepts the `*` binary marker other tools write, and refuses anything it cannot understand rather than parsing to fewer entries than the file has lines.
