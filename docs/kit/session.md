# session

`github.com/ubgo/lath/kit/session`

Advertise a running process's endpoint so another process can find and attach to it.

**Needs:** `ps` (via `kit/lock`, for the liveness check).

## Why it exists

A long-running command wants to expose a socket, and something started later (a viewer, a debugger, an attach command) has to discover it without being told where to look. Doing that by hand means inventing a naming scheme, then discovering that a crashed run leaves its files behind and the next listing offers a socket nobody is serving.

Liveness is the part worth reusing. A PID alone is not enough: PIDs are recycled, so an unrelated process inheriting the number makes a dead session look live forever. This delegates to [`lock`](lock.md), which records the holder's process **start time** for exactly that reason.

Nothing here knows what travels over the socket. The endpoint is a path and the description is a string map, so a pipeline debugger, a log tail and a profiler all use it unchanged.

## API

```go
func Dir() (string, error)
func ID(key string, pid int) string
func Advertise(ctx context.Context, key string, meta Meta) (*Advertised, error)
func List() ([]Session, error)

func (a *Advertised) Socket() string
func (a *Advertised) Close() error

type Meta map[string]string
type Session struct {
    ID     string
    Socket string
    Meta   Meta
    PID    int
    Since  time.Time
}

const DirEnv = "LATH_SESSION_DIR"
```

## Advertising

```go
adv, err := session.Advertise(ctx, projectDir+"|deploy prod", session.Meta{
    "project": "acme_api",
    "target":  "deploy prod",
})
if err != nil {
    return err
}
defer adv.Close()

ln, err := net.Listen("unix", adv.Socket())
```

`Advertise` reserves the name and clears any leftover socket, but does **not** listen. The caller owns that, because this package has no opinion about the protocol.

## Discovering

```go
for _, s := range must(session.List()) {
    fmt.Printf("%-12s %-28s pid %d\n", s.Meta["project"], s.Meta["target"], s.PID)
}
```

`List` sweeps dead sessions as it goes. That happens here rather than in a separate cleanup command because a listing is the only moment anything is guaranteed to look: a directory that is only ever appended to fills with sockets nobody serves, and the first symptom is a picker offering choices that hang.

Newest first. The session someone is looking for is almost always the one they just started.

## Naming

`ID` is a hash of the key plus the PID. Both halves matter:

| Without | What breaks |
|---|---|
| the key | two projects running the same target collide |
| the PID | one project stepped in two terminals collides |

So a key should identify the *work*: a project path plus a target, not a target alone.

## ⚠️ Gotchas

**The directory is owner-only, and enforced rather than assumed.** `MkdirAll` leaves an existing directory's mode alone, and the process umask can widen a new one, either way the result would be a directory carrying control channels into running deploys that other users on the machine can reach. `Dir` chmods it and **refuses** if it cannot. Filesystem permissions are the only thing scoping who may attach.

**`Close` unlinks the socket.** A listener that exits without unlinking leaves a path that accepts connections from nobody, which is indistinguishable from a live session until something tries to use it.

**An advertisement survives `syscall.Exec`.** The lock is a file, not a held descriptor, and exec preserves both the PID and the process start time, so a session advertised just before handing off still describes the process that replaced it. This is what lets `lath` advertise a debug session and then exec the definition.

**A malformed `Meta` does not hide a live session.** It just has nothing to say about itself.

## Errors

| Situation | Result |
|---|---|
| the directory cannot be secured | error from `Dir` and `Advertise`, a refusal, not a warning |
| the same key and PID advertised twice | error from `lock.Acquire` |
| a stale socket from a crashed run | removed by `Advertise`, so `Listen` does not fail with "address already in use" |
| a session whose holder is gone | swept by `List`, files removed |
