# kit/docker

Building images, running containers, talking to registries, over any `Runner`, so the same call works locally or on a deploy target.

```go
import "github.com/ubgo/lath/kit/docker"
```

## Client

```go
func On(r runner.Runner) Client     // nil Runner means this machine
func Available() bool               // is the docker CLI on PATH (locally)
func (c Client) Where() runner.Runner
func (c Client) WithOutput(w io.Writer) Client
```

```go
local  := docker.On(nil)
remote := docker.On(runner.Remote{Host: ssh.Host{Addr: "box-1", User: "deploy"}})
```

`Program` is a constant (`"docker"`), so a drop-in replacement, podman, nerdctl, is one edit rather than a search.

### Seeing what it printed

By default a command's output is captured, not shown, which means a two-minute build prints nothing, indistinguishable from a hang.

```go
client := docker.On(box).WithOutput(os.Stderr)
```

Output then streams as it happens **and** is still captured, so a failure quotes what docker actually said, `proc.Capture` composes with `proc.Out` for exactly this.

Off by default because a library that writes to a stream nobody asked for cannot be embedded. Inside a pipeline, hand it `pipeline.State.Output()` and every line reaches the terminal and any attached debugger.

### …and getting docker's real progress table

```go
client := docker.On(nil).WithTerminal(os.Stdout)
```

`WithOutput` still gives you a **pipe**, and BuildKit renders a flat line-per-event log to a pipe rather than the live `[+] Building 28.8s (14/28)` table it draws on a terminal. `WithTerminal` connects docker's output to the file descriptor directly, so it renders exactly as it would if you had typed the command yourself.

**Only the progress-drawing subcommands get the terminal**: `build`, `buildx`, `push`, `pull`. Everything else, `run`, `stop`, `rm`, `ps`, `inspect`, `login`, stays captured and is echoed to the same file, so its errors still quote docker and the output this package parses still arrives.

⚠️ **For the subcommands that do get it, output is not captured**, so an error from a build or a pull cannot quote what docker said. That is the whole trade, and it is only acceptable when the operator is watching the terminal it went to. `pipeline.State.Passthrough` decides that: it returns a terminal only when nothing else needs the output as lines.

That split was learned the hard way. Applying the terminal to every subcommand meant a `docker run` that exits 125 printed its reason to a screen the panel then repainted over, and the error read `exit 125 after 55ms:` with nothing after the colon. Quieter and worse: `Inspect` and `Names` parse captured stdout, which in that mode is empty, so a running container reported as missing with no error at all.

⚠️ **Any wrapper defeats it.** `os/exec` passes an `*os.File` to the child by duplicating its descriptor, but wraps every other `io.Writer` in a pipe, so capturing the output, or serialising it, is itself what makes docker drop to plain mode. This is not theoretical: a fix for a concurrent-write race wrapped every shared writer, files included, and silently cost docker its table.

## Constants and defaults

| Symbol | What |
|---|---|
| `HealthValues` | Canonical order of `HealthNone`, `HealthStarting`, `HealthHealthy`, `HealthUnhealthy`, iterate this rather than listing them, so a switch and this package cannot drift |
| `PruneImages` · `PruneVolumes` · `PruneNetworks` · `PruneContainers` · `PruneSystem` | What `Prune` can reclaim. Constants rather than literals because the step layer labels itself from the chosen target, and a literal at either site is a label that disagrees with the command actually issued |
| `PruneTargets` | The five above as a canonical list, so a caller validating input and this package agree about what is accepted |
| `DefaultPruneTarget` | What an empty `PruneOptions.Target` means: images, because dangling images are what a deploy accumulates |
| `DefaultStopGrace` | 10s, how long `Stop` waits for a container to exit before docker kills it |
| `RunOptions.NetworkAliases` | Extra DNS names on the joined network. A container named after its commit is unaddressable by anything written in advance; an alias gives it a stable name. ⚠️ During a rolling deploy the old and new containers both answer to it, so traffic splits across two releases until the old one stops |

## Images

```go
func (c Client) Build(ctx context.Context, o BuildOptions) error
func (c Client) Push(ctx context.Context, image string, extra ...string) error
func (c Client) Pull(ctx context.Context, image string, extra ...string) error
func (c Client) Images(ctx context.Context, repository string) ([]string, error)
func (c Client) RemoveImage(ctx context.Context, reference string, extra ...string) error
func (c Client) Prune(ctx context.Context, o PruneOptions) (string, error)

func Reference(repository, tag string) string
func TagOf(reference string) string
```

`Images` lists the tags of one repository present on that machine, newest first, skipping untagged ones because a name that cannot be run is not a candidate. The question it answers is "what could I run without pulling" — which for a deploy is "what could I roll back to". The registry can answer a fuller version of it, at the cost of a credential and a round trip; the machine answers the one that matters, because an image that is here will start whatever the registry currently thinks. Note that `Prune` with the default target reclaims only *dangling* images, so tagged ones accumulate and stay listable — that is what makes a rollback possible at all.

`RemoveImage` is the other end of that accumulation. `Prune` reclaims only *dangling* images, so tagged ones pile up forever — deliberate, since it is what makes a rollback possible, but not free, and nothing else here can end it. It **never forces**: docker refuses to delete an image a container holds, and that refusal is the only thing between an unattended retention sweep and the release currently serving. Pass `--force` through `extra` if you mean it.

Its errors are classified so a sweep can continue past the two outcomes that are not faults — `ErrNoSuchImage` (already gone, so re-running an interrupted sweep is safe) and `ErrImageInUse` (a container holds it). Anything else — a dead daemon, an unreachable host — arrives unclassified, because a sweep must not walk past a broken machine:

```go
for _, tag := range old {
    switch err := client.RemoveImage(ctx, docker.Reference(repo, tag)); {
    case err == nil, errors.Is(err, docker.ErrNoSuchImage):
    case errors.Is(err, docker.ErrImageInUse):
        log("kept %s, a container still holds it", tag)
    default:
        return err
    }
}
```

**Which tags are "old" is not this package's decision.** Newest-by-creation is the obvious rule and the wrong one for a deploy: an image built and never shipped sorts above one that served for a week. A project that records what it actually deployed should sweep by that record instead.

`Reference` and `TagOf` are inverses, and exist because the join was inlined in three places across two repositories, each one having to know the separator. `TagOf` splits from the **right**: a registry host may carry a port, so `registry:5000/acme/app:a3f1c2d` holds two colons and splitting from the left reports the port as the version. A reference whose only colon belongs to a port is untagged, and both functions agree on that.

### BuildOptions

```go
type BuildOptions struct {
    Tag        string
    Context    string
    Dockerfile string
    Platforms  []string              // multi-platform switches to buildx automatically
    Args       map[string]string     // --build-arg
    Target     string                // a stage of a multi-stage Dockerfile
    Labels     map[string]string
    NoCache    bool
    Pull       bool
    Extra      []string              // anything not modelled
}
func (o BuildOptions) BuildArgs() []string     // the command line, for display or a test
```

```go
err := docker.On(nil).Build(ctx, docker.BuildOptions{
    Tag:        "ghcr.io/acme/app:" + commit,
    Context:    ".",
    Dockerfile: ".docker/Dockerfile.prod",
    Platforms:  []string{"linux/amd64"},
    Labels:     map[string]string{"org.opencontainers.image.revision": commit},
    Extra:      []string{"--secret", "id=npm,src=.npmrc", "--cache-from", "type=gha"},
})
```

⚠️ **Never put a credential in `Args`.** Build arguments are recorded in the image's history and readable by anyone who can pull it. Use `--secret` via `Extra`.

`Platforms` switching to `buildx` matters because plain `docker build` cannot do multi-platform and fails with a message that does not say so. `Args` and `Labels` are emitted **sorted**, so two equivalent builds produce an identical command line and a log diff means a real change.

### PruneOptions

```go
type PruneOptions struct {
    Target      string     // image (default), volume, network, container, system
    All         bool       // everything unused, not only dangling
    Filter      []string   // "until=24h", "label!=keep"
    Interactive bool       // drop -f and let docker ask
    Extra       []string
}
```

```go
summary, err := docker.On(box).Prune(ctx, docker.PruneOptions{
    Filter: []string{"until=24h"},
})   // "Total reclaimed space: 4.76GB"
```

⚠️ `-f` is passed **unless** `Interactive` is set. With no `-f`, docker reads a confirmation from stdin, and in a pipeline nothing is attached to it. The command hangs rather than failing.

⚠️ `All: true` on images is the difference between reclaiming a few layers and evicting **every image no container currently references**, much more space, and a much slower next build.

## Containers

```go
func (c Client) Run(ctx context.Context, o RunOptions, opts ...proc.Option) error
func (c Client) Stop(ctx context.Context, name string, grace time.Duration) error
func (c Client) Remove(ctx context.Context, name string) error
func (c Client) Names(ctx context.Context, match string, extra ...string) ([]string, error)
func (c Client) Inspect(ctx context.Context, name string) (State, error)
func (c Client) InspectField(ctx context.Context, name, format string) (string, error)
```

### RunOptions

```go
type RunOptions struct {
    Name, Image string
    Cmd         []string
    Network     string            // docker.HostNetwork to share the host's
    EnvFile     string
    Env         map[string]string // -e, applied AFTER EnvFile
    Volumes     []string
    ExtraHosts  []string          // "host.docker.internal:host-gateway"
    Port        int               // published on LOOPBACK
    Ports       []string          // anything more specific
    Restart     string            // default "unless-stopped"
    StopTimeout time.Duration
    User, Workdir string
    Labels      map[string]string
    Memory, CPUs string
    Detach, Remove bool
    Extra       []string
}
func (o RunOptions) RunArgs() []string
```

```go
err := docker.On(box).Run(ctx, docker.RunOptions{
    Name: "app-web-" + commit, Image: image, Detach: true,
    Network: "appnet", EnvFile: "/srv/app/.env",
    Volumes: []string{"/srv/app/logs:/app/logs"},
    Port:    8080,
    Env:     map[string]string{"LOG_LEVEL": "debug"},   // overrides the env file
    Memory:  "512m",
    Extra:   []string{"--cap-add", "SYS_NICE"},
})
```

⚠️ **`Port` publishes on `127.0.0.1` only.** A container bound to `0.0.0.0` is reachable from the internet regardless of any firewall the proxy sits behind. A way to expose a service nobody meant to expose. Use `Ports` for anything else, deliberately.

⚠️ **`-e` is emitted after `--env-file`**, so a single variable can be overridden without rewriting the file.

⚠️ Publishing is **omitted** with `HostNetwork`, because docker rejects `-p` alongside `--network host`.

`Restart` defaults to `unless-stopped`: a crashed container comes back, a deliberately stopped one stays stopped across a host reboot.

### Stopping

```go
err := docker.On(box).Stop(ctx, name, 30*time.Second)
err = docker.On(box).Remove(ctx, name)
```

⚠️ **Two calls, not `docker rm -f`.** `rm -f` sends SIGKILL immediately and accepts no timeout, so a process gets no chance to finish an in-flight request, which is the entire point of a drain window. `Stop` uses `stop --time`, then `Remove` deletes.

### Inspect

```go
type State struct {
    Running, Restarting bool
    RestartCount, ExitCode int
    Health Health     // HealthNone | HealthStarting | HealthHealthy | HealthUnhealthy
}
```

⚠️ **`Health` is the field that matters**, and the reason `State` exists rather than a bare bool. A container running supervisord stays up while every process it manages is dead: the container is running, nothing inside it is, and only a declared `HEALTHCHECK` can tell the difference.

```go
st, _ := docker.On(box).Inspect(ctx, name)
switch {
case st.Health == docker.HealthUnhealthy: // the image itself says it is broken
case st.RestartCount > 0:                 // crash-looping
case st.Running:                          // up — but "up" is not "working"
}

ip, _ := docker.On(box).InspectField(ctx, name, "{{.NetworkSettings.IPAddress}}")
```

## Registries

```go
func (c Client) Login(ctx context.Context, registry, username, password string) error
func AuthFailure(stderr []byte) bool
```

⚠️ The password goes on **stdin** (`--password-stdin`), never as an argument. An argument is visible in the process list to every user on the host.

`AuthFailure` distinguishes a credential problem from a transient one. Worth having because the two look identical from outside, and retrying bad credentials ten times turns an instant, obvious failure into a minute of noise ending in the same message:

```go
if err := client.Login(ctx, reg, user, token.Reveal()); err != nil {
    if docker.AuthFailure(stderr) {
        return supervise.Permanent(err)      // do not retry
    }
    return err
}
```
