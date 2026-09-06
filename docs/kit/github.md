# `kit/github` — releases and their artifacts

Publishes a release and, crucially, **reads back what was attached to it**. Plain HTTP against the REST API, not the `gh` CLI: a CLI must be installed and logged in as somebody, while a token and a round trip work the same on a laptop, in CI, and in a container holding nothing but this binary.

```go
c := github.Client{Token: token}
repo := github.Repo{Owner: "acme", Name: "app"}

rel, outcome, err := c.EnsureRelease(ctx, repo, github.Release{
    TagName: "v1.2.0", Name: "v1.2.0", Body: notes,
})

f, _ := os.Open(archive)
info, _ := f.Stat()
_, err = c.UploadAsset(ctx, repo, rel.ID, filepath.Base(archive), f, info.Size())

got, err := c.Assets(ctx, repo, rel.ID)   // prove it actually arrived
```

| Symbol | What |
|---|---|
| `Client` | `Token`, and optional `BaseURL` / `UploadURL` / `HTTP` |
| `Repo` | `Owner`, `Name`; `String()` renders `owner/name` |
| `Release(ctx, repo, tag)` | Reads a release; `ErrNoRelease` when the tag has none |
| `EnsureRelease(ctx, repo, r)` | Creates or updates, returning an `Outcome` |
| `Assets(ctx, repo, releaseID)` | What is attached, with the sizes GitHub stored |
| `UploadAsset(ctx, repo, releaseID, name, body, size)` | Attaches a file; `ErrAssetExists` if the name is taken |
| `DeleteAsset(ctx, repo, assetID)` | The explicit half of a replacement |
| `Release` · `Asset` | The two payloads |
| `Outcome` · `OutcomeValues` | `created` · `updated` · `unchanged` |
| `API` · `Uploads` · `DefaultTimeout` | Endpoints and the per-request bound |
| `ErrNoRelease` · `ErrAssetExists` | Both ordinary states of a re-run, not faults |

## Gotchas

⚠️ **Uploads go to a different host.** GitHub serves assets from `uploads.github.com`, not `api.github.com`. Sending an asset to the API host fails in a way that reads like a permission problem — hence two fields, and a test that pins the split.

⚠️ **`size` is required and is not a convenience.** GitHub rejects a chunked upload, so the length must be known before the body is sent. A reader that cannot report one has to be buffered by the caller, because that is a decision about memory.

⚠️ **Nothing is replaced silently.** A name already attached returns `ErrAssetExists`; an artifact someone may already have downloaded is not something to overwrite on a caller's behalf. `DeleteAsset` first if that is the intent.

`EnsureRelease` is idempotent because publishing is the step most likely to be re-run: everything that can fail after a tag exists leaves a release a retry must walk back into. Identical notes report `unchanged` rather than issuing a silent `PATCH`. `DefaultTimeout` is ten minutes because an asset upload is a file transfer, not a JSON call.
