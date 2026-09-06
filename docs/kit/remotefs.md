# kit/remotefs

Filesystem operations wherever a `Runner` points. The same call on this machine or a deploy target.

```go
import "github.com/ubgo/lath/kit/remotefs"
```

Needs POSIX `sh` on the target.

## Why not scp

The content is usually a **credential** rather than a file that already exists. Staging it on disk to copy it would put a secret in a temporary file. The exact thing `kit/secret` exists to prevent. Here the bytes travel over the runner's stdin and never touch the caller's disk.

## API

```go
func WriteFile(ctx context.Context, r runner.Runner, path, content string, sensitive bool) error
func WriteFileMode(ctx context.Context, r runner.Runner, path, content string, mode os.FileMode) error
func WriteFileOwner(ctx context.Context, r runner.Runner, path, content string, mode os.FileMode, owner string) error

const SensitiveMode os.FileMode = 0o600   // anything carrying a credential
const PublicMode    os.FileMode = 0o644   // content that is not a secret
const LocalMode     os.FileMode = 0o755   // a directory this package creates
func ReadFile(ctx context.Context, r runner.Runner, path string) ([]byte, error)
func MkdirAll(ctx context.Context, r runner.Runner, owner string, paths ...string) error
func MkdirAllMode(ctx context.Context, r runner.Runner, owner string, mode os.FileMode, paths ...string) error
func Exists(ctx context.Context, r runner.Runner, path string) (bool, error)
func Quote(s string) string
func Dir(path string) string
```

```go
// A credential onto a server: 0600, never on the local disk, never in argv
err := remotefs.WriteFile(ctx, box, "/srv/app/.env", cfg.Reveal(), true)

// A script that must be executable
err = remotefs.WriteFileMode(ctx, box, "/srv/app/run.sh", script, 0o755)

// A credential a container running as uid 1000 must read.
err = remotefs.WriteFileOwner(ctx, box, "/srv/app/_keys/id", key.Reveal(), 0o600, "1000:1000")

// Directories the container's non-root user can write to
err = remotefs.MkdirAll(ctx, box, "1000:1000", "/srv/app/logs", "/srv/app/artifacts")
```

## Guarantees

**Content is base64'd in transit**, so newlines, quotes, `$VAR`, backticks and binary data survive a remote shell unharmed.

**The bytes appear only on stdin, never in argv**, so they stay out of the process list and any shell history.

**The umask is set before the redirect**, so a sensitive file is never briefly readable between creation and a `chmod`.

⚠️ **`mkdir` runs *before* the umask is narrowed**, and that ordering is the subtlety. A umask of `177`, the complement of 0600, strips the execute bit from anything it creates, so a directory made under it cannot be entered and the redirect that follows fails with a permission error on a path that was just created.

**The chown is best-effort.** A directory that already has the right owner, on a host where this user cannot chown, must not fail a deploy.

**An absent path is an answer, not an error.** `Exists` reports through `test -e`'s exit status, so a negative result is `false, nil`.

## Ownership

Mode alone is not enough for a file another user must read. A `0600` credential written by the deploy user and then bind-mounted into a container running as uid 1000 is unreadable inside that container, and the failure surfaces as an *application* error ("no such credential") rather than a permission problem, which is a genuinely hard trail to follow.

`WriteFileOwner` and `MkdirAll` both take an `owner` string (`"uid:gid"`); empty leaves ownership alone.

⚠️ **The chown is best-effort, by design.** A host where this user cannot chown, or a file that already has the right owner, must not fail the caller, losing the file would be far worse than having it owned by the wrong user. On macOS, chowning to `1000:1000` as a normal user always fails, so a local rehearsal writes files owned by *you*; on a Linux box the deploy user usually can. Verify ownership on the target if it matters, rather than assuming the call enforced it.

⚠️ **The owner is quoted before it reaches the shell.** It comes from configuration, so a value containing shell syntax must not execute. It won't, but a bogus owner then simply fails to be a valid owner and is swallowed by the best-effort branch, which looks exactly like success.

The chown happens while the file is already at its final mode, so it is never both world-readable and owned by the target user.

## Path handling

`Dir` is not `filepath.Dir`: the target may be Linux while this runs on macOS or Windows, so the separator is the **remote's**, not this machine's.

Every path interpolated into a command goes through `Quote`. A deploy path is usually boring, but it comes from configuration, and a space or a quote in one would re-split the command on the far side.
