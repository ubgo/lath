# kit/wait

Polling until a condition holds or an endpoint answers.

```go
import "github.com/ubgo/lath/kit/wait"
```

## Defaults

| Constant | Value | Why |
|---|---|---|
| `DefaultInterval` | 250ms | Fast enough that a ready service is noticed immediately, slow enough not to hammer a starting process with probes |
| `DefaultTimeout` | 30s | Bounds every wait, so a condition that never becomes true fails with a message instead of hanging a deploy |

Both are overridable per call, `wait.Interval(d)`, `wait.Timeout(d)`. A non-positive value falls back to the default rather than spinning.

## API

```go
func Until(ctx context.Context, cond func(context.Context) (bool, error), opts ...Option) error
func HTTPOK(ctx context.Context, url string, opts ...Option) error

func Interval(d time.Duration) Option     // default 250ms
func Timeout(d time.Duration) Option      // default 30s
func Header(key, value string) Option     // HTTPOK
func Method(m string) Option              // HTTPOK, default GET
func Client(hc *http.Client) Option       // HTTPOK
func Accept(fn func(status int) bool) Option
```

```go
err := wait.HTTPOK(ctx, "http://localhost:8080/health", wait.Timeout(90*time.Second))

err = wait.Until(ctx, func(ctx context.Context) (bool, error) {
    st, err := docker.On(box).Inspect(ctx, name)
    if err != nil {
        return false, err          // an error ABORTS; it does not retry
    }
    return st.Health == docker.HealthHealthy, nil
}, wait.Interval(time.Second), wait.Timeout(2*time.Minute))
```

## Semantics worth knowing

**The condition runs before any waiting.** A condition already true returns immediately. A wait that always sleeps first adds latency to every task.

**A condition error aborts immediately.** Retrying a malformed request or a permission failure burns the whole timeout to reach the same answer. Return `false, nil` for "not yet", `false, err` for "never".

**Cancellation and timeout are distinct.** A cancelled parent returns `context.Canceled`; only an elapsed budget returns `ErrTimeout`. Conflating them would tell an operator their service was too slow when the run was interrupted.

**The error names the budget**, `wait: after 30s: condition not met before timeout`. "Timed out" alone leaves the reader guessing whether it was 1s or 10m.

**A non-positive `Interval` or `Timeout` falls back to the default** rather than panicking. `Interval(budget/attempts)` floors to zero as soon as attempts exceeds the budget in nanoseconds, and `time.NewTicker(0)` panics. A task runner should not take down a build over arithmetic.

## HTTPOK specifics

A connection refused is **retried**. That is exactly what waiting is for. A malformed URL **aborts**, because it will never become valid.

Response bodies are drained and closed, so the connection pool is reused. Leaking them exhausts it, and a long wait starts failing for a reason unrelated to the service being watched.

`Accept` exists because "ready" is not universal. An endpoint behind auth answering 401 is perfectly alive:

```go
err := wait.HTTPOK(ctx, url,
    wait.Header("Authorization", "Bearer "+token.Reveal()),
    wait.Accept(func(code int) bool { return code == 200 || code == 401 }),
)
```

⚠️ The default accepts **2xx only**. A 3xx means the service answered but is not serving that path, and treating it as ready starts a deploy against a server that is not up.
