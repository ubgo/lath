# kit/download

Fetching a URL to a file, verified before it lands.

```go
import "github.com/ubgo/lath/kit/download"
```

## Defaults

`DefaultTimeout` is 5 minutes, generous, because this is for artifacts and installers rather than API calls, but still bounded so a stalled transfer fails instead of hanging a pipeline forever.

## API

```go
func ToFile(ctx context.Context, url, dst string, opts ...Option) error

func SHA256(hexDigest string) Option
func Header(key, value string) Option
func Timeout(d time.Duration) Option      // default 5m; bounds the WHOLE transfer
func Client(hc *http.Client) Option
```

```go
err := download.ToFile(ctx,
    "https://github.com/apple/pkl/releases/download/0.28.2/pkl-linux-amd64",
    "/usr/local/bin/pkl",
    download.SHA256("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"),
)
```

## Why the checksum is the point

An unverified binary fetched over the network and then executed is the supply-chain problem in miniature. The usual alternative, curl to a file, checked by eye, verifies nothing.

⚠️ **The bytes are verified before they reach the destination.** The body is read into memory, checked, and only then written through `fsx.WriteAtomic`. Streaming straight to `dst` would put unverified bytes at the destination path, which is exactly what verification exists to prevent, and a failed or mismatched download would leave a partial file that a later step finds and assumes is good.

The digest is compared case-insensitively with surrounding whitespace trimmed, because one pasted from a release page is often uppercase.

The error names **both** digests, so diagnosing a mismatch does not mean recomputing by hand.

## Other behaviour

**`Timeout` bounds the whole transfer, not the connection.** A stalled body that never errors is indistinguishable from a slow one otherwise, headers arrive instantly and then nothing does.

**Redirects are followed, up to five.** Stated rather than inherited, so the limit is a decision. A loop is stopped rather than hanging.

**The file lands non-executable (0644).** Marking a fetched artifact runnable is the caller's decision to make.

**`http.DefaultClient` is never used**, its settings are process-global and someone else's to change. `Client` supplies your own.

## Errors

| | |
|---|---|
| `ErrChecksumMismatch` | the bytes did not match; nothing was written |
| `ErrStatus` | a non-2xx response; the status is named |
