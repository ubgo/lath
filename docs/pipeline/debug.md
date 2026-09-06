# pipeline/debug, the debug transport

`github.com/ubgo/lath/pipeline/debug`

Carries a pipeline's progress to an out-of-process debugger and its decisions back.

**Stdlib only**, `net` and `encoding/json`. That is load-bearing: whatever a definition imports, every definition imports forever, so this side of the wire must never add a dependency to anyone's `go.mod`.

Using the debugger is [`DEBUGGING.md`](../DEBUGGING.md). This page is the package.

---

## Why out of process

`cmd/lath` replaces itself with the compiled definition via `syscall.Exec`, so by the time a pipeline runs there is no runner left to draw a panel, and a terminal-UI library placed on this side would land in every project's `go.mod` whether or not it is ever used. Splitting the renderer into a separate process solves both: the client lives in `cmd/lath`, where a dependency costs only people who install lath.

---

## API

```go
// Server — the pipeline side. Implements pipeline.Debugger.
func NewServer(rw io.ReadWriteCloser) *Server
func (s *Server) Plan(name string, steps []pipeline.PlanEntry) error
func (s *Server) Pause(p pipeline.Pause) (pipeline.Action, error)   // BLOCKS
func (s *Server) Finish(f pipeline.Finished) error
func (s *Server) Reporter() pipeline.Reporter
func (s *Server) Close() error

// Client — the debugger side.
func NewClient(rw io.ReadWriteCloser) *Client
func Dial(socket string) (*Client, error)
func (c *Client) Next() (Event, error)
func (c *Client) Send(cmd pipeline.Action, ack int) error
func (c *Client) Close() error

// Sockets.
func Listen(path string, advertisement io.Closer) (*Listener, error)
func (l *Listener) Socket() string
func (l *Listener) Accept(ctx context.Context, timeout time.Duration) (*Server, error)
func (l *Listener) Close() error

// Reporters.
func NewReporter(send func(Event) error) pipeline.Reporter
type MultiReporter []pipeline.Reporter

var ErrNoClient = errors.New("debug: no client attached")
func IsNoClient(err error) bool

const DefaultAttachTimeout = 2 * time.Minute
```

---

## Serving a session

```go
ln, err := debug.Listen(socketPath, nil)
if err != nil {
    return err
}
defer ln.Close()

fmt.Fprintf(os.Stderr, "waiting for a debugger (%s)\n", debug.DefaultAttachTimeout)
srv, err := ln.Accept(ctx, debug.DefaultAttachTimeout)
if err != nil {
    if debug.IsNoClient(err) {
        return fmt.Errorf("nobody attached: %w", err)
    }
    return err
}
defer srv.Close()

pipeline.AttachDebugger(srv)     // every State built afterwards pauses
```

`Listen` and `Accept` are split so the attach instructions can be printed **with the real path in them** before blocking. The operator needs to read the hint while the run is waiting, not after.

⚠️ **One client per session.** The listener closes as soon as one attaches. A second connection would need a rule for whose keystroke wins, and there is no answer to that which is safe on a deploy.

⚠️ **`Accept` unblocks on context cancellation**, so Ctrl-C at the attach prompt exits rather than sitting out the timeout.

---

## Attaching as a client

```go
cli, err := debug.Dial(socket)
if err != nil {
    return err
}
defer cli.Close()

for {
    e, err := cli.Next()
    if err != nil {
        return err          // io.EOF when the session ends
    }
    switch e.Kind {
    case debug.KindPlan:
        render(e.Pipeline, e.Steps)
    case debug.KindStart:
        markRunning(e.N)
    case debug.KindDetail:
        appendLine(e.Msg)
    case debug.KindDone:
        markDone(e.N, e.Took)
    case debug.KindPaused:
        act := ask(e)                       // n / r / c / q
        if err := cli.Send(act, e.N); err != nil {
            return err
        }
    case debug.KindFinished:
        return report(e.Err, e.State)
    }
}
```

`Next` blocks rather than returning a channel: a render loop must know precisely when the stream ended, and a closed channel cannot carry the reason it closed.

⚠️ **`Close` aborts the run.** The pipeline sees the disconnect and stops, losing the supervisor is not consent to proceed. That is the intended meaning of closing a debugger mid-run.

---

## The wire

One JSON object per line, both directions, so a broken client can be bypassed with `nc -U` and the transcript read by eye.

### Events, pipeline to client

| `t` | Fields | When |
|---|---|---|
| `plan` | `pipeline`, `steps[]` | once, on connect |
| `start` | `n`, `total`, `name` | before each step |
| `detail` | `msg` | a step describing itself |
| `output` | `msg` | one line a command printed, separate from `detail` because the volumes differ by orders of magnitude and a client renders, collapses or scrolls them differently |
| `done` | `n`, `total`, `name`, `took` | after a step succeeds |
| `paused` | `n`, `total`, `name`, `next`, `state{}`, `replayable`, `err` | waiting for a decision |
| `finished` | `err`, `state{}` | the run ended |

```go
type Event struct {
    Kind       EventKind            `json:"t"`
    Pipeline   string               `json:"pipeline,omitempty"`
    Steps      []pipeline.PlanEntry `json:"steps,omitempty"`
    N          int                  `json:"n,omitempty"`
    Total      int                  `json:"total,omitempty"`
    Name       string               `json:"name,omitempty"`
    Msg        string               `json:"msg,omitempty"`
    Took       string               `json:"took,omitempty"`
    Next       string               `json:"next,omitempty"`
    State      map[string]string    `json:"state,omitempty"`
    Replayable bool                 `json:"replayable,omitempty"`
    Err        string               `json:"err,omitempty"`
}

const (
    KindPlan     EventKind = "plan"
    KindStart    EventKind = "start"
    KindDetail   EventKind = "detail"
    KindOutput   EventKind = "output"
    KindDone     EventKind = "done"
    KindPaused   EventKind = "paused"
    KindFinished EventKind = "finished"
)
var EventKindValues = []EventKind{KindPlan, KindStart, KindDetail, KindOutput, KindDone, KindPaused, KindFinished}
func (k EventKind) Valid() bool
```

One struct rather than a type per kind, because the format is line-delimited JSON and a client decodes before it knows which kind arrived. `Err` is a string because errors do not survive JSON. The client shows it rather than branching on it.

### Control, client to pipeline

```go
type Control struct {
    Cmd pipeline.Action `json:"cmd"`
    Ack int             `json:"ack"`
}
```

### ⚠️ `Ack` is not optional

Without it, key-mashing silently deploys. Four `next` presses at one pause queue in the socket buffer; the loop reads them one after another and runs the next three steps unpaused. On a fourteen-step deploy, step 6 starts containers.

```
P → {"t":"paused","n":3,"next":"build-image"}
T → {"cmd":"next","ack":3}      accepted
T → {"cmd":"next","ack":3}      DISCARDED — pause 3 is over
```

`Server.Pause` discards any control whose `Ack` is not the pause it is sitting at, and any command that is not a declared `pipeline.Action`. Both are discarded rather than obeyed, because the fallthrough would be "keep going". The one outcome nobody asked for.

---

## Reporters

```go
func NewReporter(send func(Event) error) pipeline.Reporter
type MultiReporter []pipeline.Reporter
```

`Server.Reporter()` is the events half, three methods turning `pipeline.Reporter` calls into `start` / `detail` / `done`. You rarely need it directly: `pipeline.NewState` attaches it automatically whenever a debugger is armed, which is what keeps definitions unchanged.

`MultiReporter` fans out to several, for a caller wiring output to more than one place by hand.

⚠️ **Reporter errors are dropped deliberately.** Output plumbing must never abort a deploy. A client that has gone away is detected at the next `Pause`, which is where it matters.

---

## Testing over a pipe

`NewServer` and `NewClient` take an `io.ReadWriteCloser`, not a `net.Conn`, so the whole protocol runs in-process:

```go
a, b := connPair(t)                  // a connected pair
srv, cli := debug.NewServer(a), debug.NewClient(b)

go clientLoop(cli)

st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter(), pipeline.Debug(srv))
err := p.Run(context.Background(), st)
```

⚠️ **Do not use `net.Pipe` for this.** It has no buffering, so a second write blocks until the first is read, which makes it unable to model the case these tests care most about, an operator whose keystrokes are queued in a socket buffer. A test written over it deadlocks instead of failing. Use a loopback TCP pair, or a real unix socket.

---

## Errors

| Situation | Result |
|---|---|
| nobody attaches before the timeout | `ErrNoClient`, wrapped, check with `IsNoClient` |
| the context is cancelled while waiting | `ErrNoClient` wrapping `context.Canceled` |
| the client disconnects mid-run | `ErrNoClient` from `Pause`, which aborts the run with `pipeline.ErrAborted` |
| a control names a different pause | discarded silently; `Pause` keeps waiting |
| a control names an unknown action | discarded silently |
| an event arrives with an unknown kind | error from `Client.Next` |
| `Send` given an undeclared action | error, before anything is written |
