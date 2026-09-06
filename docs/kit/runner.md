# kit/runner

Where a command executes. This is the type that makes one pipeline work on a laptop and on a server.

```go
import "github.com/ubgo/lath/kit/runner"
```

## Why it exists

Code that asks "am I local or remote?" ends up asking it everywhere, and every place is a chance to get it wrong. Code that takes a `Runner` never asks. It hands the command over and the Runner knows.

```go
type Runner interface {
    Run(ctx context.Context, name string, args []string, opts ...proc.Option) (proc.Result, error)
    Describe() string
}
```

Two implementations ship:

```go
runner.Local{}                                        // this machine
runner.Remote{Host: ssh.Host{Addr: "box", User: "deploy"}}   // over ssh
```

Swapping one for the other is the entire difference between rehearsing a deploy against a scratch container and performing it against production:

```go
var where runner.Runner = runner.Local{}
if env != "local" {
    where = runner.Remote{Host: ssh.Host{Addr: env + ".example.com", User: "deploy"}}
}

docker.On(where).Prune(ctx, docker.PruneOptions{})    // same call, either machine
```

Because it is an interface, a third implementation is yours to write. A container exec, a serial console, a job queue, and everything built on `Runner` works with it unchanged.

## Helpers

```go
func OrLocal(r Runner) Runner
func Check(what string, where Runner, r proc.Result, err error) error
func Tail(b []byte) string
```

`OrLocal` means **the zero value of anything holding a Runner runs locally**, which is the right default for the case these packages exist to enable: rehearsing something in full before it touches a server.

```go
type MyStep struct{ Runner runner.Runner }   // nil is fine

func (s MyStep) do(ctx context.Context) error {
    where := runner.OrLocal(s.Runner)
    r, err := where.Run(ctx, "echo", []string{"hi"}, proc.Capture())
    return runner.Check("echo", where, r, err)
}
```

`Check` turns a `Result` into an error **naming where it ran**:

```
docker build on deploy@box-1:2222: exit 1 after 4.2s: no such file
```

A message that omits the machine sends an operator to the wrong host. `Tail` bounds a runaway stderr to the last ~800 bytes. The end, because that is where the cause is, marked with a leading `…`.

## Remote quoting

`Remote.Run` quotes every argument before handing the command to a remote login shell. Without that, a path containing a space is re-split on the far side and a value containing a semicolon is *interpreted* there:

```go
where.Run(ctx, "cat", []string{"/srv/my app/.env"})
// ssh box "'cat' '/srv/my app/.env'"
```

## Fake, the shared test double

```go
type Fake struct {
    Reply []Scripted   // matched on a substring of the joined command line
    Fail  error        // make every Run report "could not start"
}
type Scripted struct{ Match, Stdout, Stderr string; Exit int }

func (f *Fake) Commands() []string
func (f *Fake) Ran(substr string) bool
```

Exported deliberately, and not in a `_test` file: `docker`, `git` and the step packages all need it, and three copies of the same fake is how they drift apart.

It is what lets every command line be asserted with **no docker, no git and no network**, which is where the flags review misses actually get checked:

```go
f := &runner.Fake{Reply: []runner.Scripted{
    {Match: "inspect", Stdout: "true false 0 0 healthy"},
    {Match: "ps", Exit: 1, Stderr: "daemon not running"},
}}

if err := docker.On(f).Pull(ctx, "app:1"); err != nil {
    t.Fatal(err)
}
if !f.Ran("docker pull app:1") {
    t.Errorf("commands = %v", f.Commands())
}
```

An unscripted command succeeds silently, so a test scripts only what it cares about.

⚠️ **A fake proves you built the command you intended. Only a real run proves the command exists.** `docker rm -f -t 10` passed its unit test and is not a valid command, `docker rm` has no `-t`. Exercise the real thing before believing it.
