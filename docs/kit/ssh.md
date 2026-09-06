# kit/ssh

Commands and files on another machine.

```go
import "github.com/ubgo/lath/kit/ssh"
```

Needs an `ssh` client, and `scp` for `Copy`. **Carries no authentication logic**: keys, agents and `known_hosts` are ssh's own configuration, and duplicating any of it here would mean two places to get security wrong.

## Host

```go
type Host struct {
    Addr           string          // required
    User           string          // empty uses ssh's default
    Port           int             // 0 uses ssh's default
    KeyFile        string          // empty uses the agent or ssh's config
    ConnectTimeout time.Duration   // 0 means 10s; bounds the HANDSHAKE only
    Extra          []string        // raw flags
}
func (h Host) String() string      // "deploy@box:2222"
```

Everything but `Addr` falls through to ssh's own configuration, so a host already described in `~/.ssh/config` needs only its alias:

```go
ssh.Host{Addr: "prod-box"}     // ~/.ssh/config supplies the rest
```

**`Extra` is deliberate, not an admission of a leaky abstraction.** An exhaustive `Host` would need a field for every ssh flag and would still be missing the one somebody needs the day they need `ProxyJump`:

```go
ssh.Host{
    Addr:  "10.0.0.5",
    User:  "deploy",
    Extra: []string{"-o", "ProxyJump=bastion.example.com", "-o", "StrictHostKeyChecking=accept-new"},
}
```

Without it, a caller abandons the package the first time it does not fit, losing its `BatchMode` and timeout defaults along with the gap.

## Running

```go
func Run(ctx context.Context, h Host, command string, opts ...proc.Option) (proc.Result, error)
func Output(ctx context.Context, h Host, command string, opts ...proc.Option) (string, error)
func Copy(ctx context.Context, h Host, localPath, remotePath string, opts ...proc.Option) error
func Reachable(ctx context.Context, h Host, timeout time.Duration) error
func Quote(s string) string
```

`proc.Option` is reused rather than a parallel option set, so `Dir`, `Timeout`, `Out` and `Capture` mean the same thing locally and remotely.

```go
host := ssh.Host{Addr: "box-1", User: "deploy"}

r, err := ssh.Run(ctx, host, "systemctl is-active myapp", proc.Capture())
uptime, err := ssh.Output(ctx, host, "uptime")
err = ssh.Copy(ctx, host, "./bundle.tar.gz", "/srv/app/bundle.tar.gz")
err = ssh.Reachable(ctx, host, 10*time.Second)
```

⚠️ **`BatchMode=yes` is always set.** Essential, not optional: without it ssh prompts for a password on a stdin nothing is attached to, and an automated task hangs until something kills it, with nothing in the log to explain the silence.

**`Reachable` runs `true` rather than only opening a socket**, so the answer covers authentication too. A host that accepts TCP but refuses the key is not usable, and calling it reachable sends the caller looking in the wrong place.

## Quoting

`Run` passes the command to a **remote login shell**. That is the only calling convention ssh offers. A caller interpolating a value is building a shell command:

```go
// WRONG: a path with a space, or a value with a semicolon, is reinterpreted there
ssh.Run(ctx, host, "cat "+userPath)

// RIGHT
ssh.Run(ctx, host, "cat "+ssh.Quote(userPath))
```

`Quote` uses single quotes with the `'\''` escape, the only form a POSIX shell treats literally throughout. It is verified against a real shell, not just asserted:

```go
ssh.Quote(`'; rm -rf /; echo '`)   // survives as literal text
```

## Errors

| | |
|---|---|
| `ErrNoClient` | no `ssh` or `scp` on PATH |
| `ErrUnreachable` | the host did not answer, or refused the key |

## Gotcha

**`scp` spells the port `-P`; `ssh` spells it `-p`.** `Copy` handles that. Getting it wrong silently copies to port 22. The package has a test pinning it, because this is the kind of thing that works in review and fails at 2am.
