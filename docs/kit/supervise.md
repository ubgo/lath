# kit/supervise

Restarting something that should keep running, and retrying something that should finish.

```go
import "github.com/ubgo/lath/kit/supervise"
```

## Defaults

| Constant | Value | Why |
|---|---|---|
| `DefaultBackoff` | 1s | The first retry delay |
| `DefaultMax` | 8s | Backoff ceiling, beyond this, waiting longer only delays recovery |
| `DefaultHealthyAfter` | 15s | Survive this long and the restart counter resets: a process that ran fine for a while and then died is not in a crash loop |
| `DefaultMaxRestarts` | 10 | Bounds a genuine crash loop. `Unlimited` (-1) disables it |

`ErrTooManyRestarts` is returned when the limit is reached, so a caller can tell "gave up" from "the process exited cleanly".

## API

```go
func Loop(ctx context.Context, p Policy, fn func(context.Context) error) error
func LoopWithStats(ctx context.Context, p Policy, fn func(context.Context) error) (Stats, error)
func Retry(ctx context.Context, p Policy, fn func(context.Context) error) error
func RetryWithStats(ctx context.Context, p Policy, fn func(context.Context) error) (Stats, error)
func Permanent(err error) error
func IsPermanent(err error) bool

type Policy struct {
    Backoff      time.Duration   // first wait; doubles. 0 → 1s
    Max          time.Duration   // cap. 0 → 8s
    HealthyAfter time.Duration   // a run this long resets the backoff. 0 → 15s
    MaxRestarts  int             // 0 → 10; Unlimited (-1) never gives up
    Out          io.Writer       // retry notices. nil → discarded
}
```

## The contract that surprises people

⚠️ **An error from `fn` means RETRY, not stop.** That is the opposite of Go's usual convention, and it is what makes this a supervisor rather than a wrapper. Only a nil return, a cancelled context, an exhausted limit, or an explicitly `Permanent` error ends the loop.

```go
// A dev watchdog: restart forever, however often it crashes
policy := supervise.Policy{MaxRestarts: supervise.Unlimited, Out: os.Stdout}
err := supervise.Loop(ctx, policy, func(ctx context.Context) error {
    r, err := proc.Run(ctx, "task", []string{"dev"}, proc.Out(os.Stdout))
    if err != nil {
        return err
    }
    if r.OK() {
        return nil          // it finished cleanly; stop
    }
    return fmt.Errorf("dev %s", r)   // it crashed; go again
})
```

**`MaxRestarts` defaults to 10, not unlimited.** Unlimited is right for a watchdog and badly wrong for a CI step, where it turns a failing build into a job that runs until the runner is killed. `Unlimited` is `-1`, so the zero value can mean "use the default" and an endless loop has to be written out. **Any** negative value means unlimited. A computed limit going negative should relax the bound, never collapse it.

**`HealthyAfter` resets the backoff.** A process that ran a while and then died had a fresh problem and should restart at once; one that dies on boot probably still has the same problem. Without the reset, an occasional restart slowly walks the delay to `Max` and stays there.

**Cancellation is a clean stop**, `Loop` returns nil. Ctrl-C is the operator saying stop; surfacing it as a failure makes every clean shutdown exit non-zero.

## Retry, for things that finish

```go
err := supervise.Retry(ctx, supervise.Policy{Backoff: 4 * time.Second, MaxRestarts: 10},
    func(ctx context.Context) error {
        return client.Push(ctx, image)
    })
```

Same arithmetic, different intent, and the difference shows in exactly one place: `Loop` supervises something expected to run forever, so any return is a failure to retry; `Retry` drives an operation expected to finish, so a permanent error must stop it.

They share one engine, so the two cannot drift apart. Only the vocabulary differs: `Loop` logs `[supervise] … restarting`, `Retry` logs `[retry] … retrying`. Not cosmetic, calling a one-shot operation a restart makes a failed deploy step read like a crash-looping service.

## Permanent

```go
if resp.StatusCode == http.StatusUnauthorized {
    return supervise.Permanent(fmt.Errorf("bad credentials: %w", err))
}
```

Marks a failure no amount of waiting will fix. A 400, a rejected credential, a missing binary. Attempting it ten more times produces ten identical failures and delays the report by the whole backoff budget.

**Honoured by both `Loop` and `Retry`**, and nothing is inferred: an error is retried unless the caller *explicitly wrapped it*. That keeps "an error means retry" true as a rule while allowing an exception the caller chooses. It survives `%w` wrapping, so context can be added without losing the marking.

A **panic is not caught.** Restarting a function that panicked on a nil map would loop forever on a bug that needs a human.

## Stats

```go
type Stats struct {
    Restarts     int
    LastError    error
    TotalRuntime time.Duration   // time in fn; EXCLUDES the backoff waits
}
```
