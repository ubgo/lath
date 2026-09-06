# kit/lock

A single-instance guard backed by a file.

```go
import "github.com/ubgo/lath/kit/lock"
```

Needs `ps` to verify liveness (best-effort on Windows, see below).

## Defaults

`DefaultStaleAfter` is 2 hours. A lock whose holder is gone is reclaimed immediately via the PID and start-time check; this is the backstop for the case that check cannot answer. A holder on another machine, or a platform where the process state is unreadable.

## Why it exists

Two supervisors each restarting a dev server that took a port with `--force`, so each killed the other's process, forever. Nothing arbitrated who owned the resource. A lock is that arbiter.

## API

```go
func Acquire(ctx context.Context, path string, opts ...Option) (*Lock, error)
func Read(path string) (Info, error)      // who holds it, without taking it
func Held(path string) bool               // advisory only

func Wait(d time.Duration) Option         // block instead of failing
func StaleAfter(d time.Duration) Option   // default 2h
func Note(s string) Option                // operator-facing context

func (l *Lock) Release() error
func (l *Lock) Path() string
func (l *Lock) Info() Info
```

```go
l, err := lock.Acquire(ctx, "/srv/app/.deploy.lock", lock.Note("deploy prod by "+user))
if errors.Is(err, lock.ErrHeld) {
    holder, _ := lock.Read("/srv/app/.deploy.lock")
    return fmt.Errorf("a deploy is already running: %s", holder)
}
defer l.Release()
```

## Fails fast by default

`Acquire` returns `ErrHeld` immediately, naming the holder. Defaulting to waiting would turn a forgotten lock into a CI job that hangs until the runner's global timeout, with no output explaining why.

`Wait(d)` opts into blocking, and the context always wins. A cancelled ctx aborts a wait at once, so Ctrl-C is honoured.

## Stale detection. Where most implementations are wrong

A lock whose owner is gone must be reclaimable, or one crash wedges the tool until somebody finds and deletes a file they do not know about.

⚠️ **A PID alone is not enough.** PIDs are recycled, so a naive liveness check concludes a lock is live when the original holder died and an unrelated process inherited the number. `Info` records the PID **and the process start time**, and a lock is stale when the process is absent *or* its start time does not match.

```go
type Info struct {
    PID     int
    Started time.Time    // what makes the liveness check trustworthy
    Since   time.Time
    Host    string       // diagnoses a lock on a shared filesystem
    Note    string
}
```

Degradation is always toward caution: when a start time cannot be established, existence alone decides. That risks honouring a lock slightly too long, which is recoverable; the opposite risks breaking a live one, which is not.

**A corrupt or unreadable lock file is treated as stale.** It cannot be attributed to anyone, and refusing forever on the strength of unparseable bytes is worse than reclaiming it.

**A lock from another host is honoured until it ages out**, since its liveness cannot be checked from here.

**`Release` only removes the file if it is still yours.** Between `Acquire` and `Release` the lock may have been judged stale and taken by someone else; deleting it then would strip a live holder of their claim.

## Notes

The lock file is `0644` on purpose, its contents are diagnostic, not secret, and another user must be able to **read** who holds it to be told something useful. Never put a credential in `Note`.

⚠️ On Windows liveness is best-effort, so a lock is governed by `StaleAfter` rather than by liveness. Documented rather than silently weaker.

`Held` is advisory only: the answer can be stale the instant it returns. Use it for reporting, never for deciding whether to proceed. That is what `Acquire` is for.
