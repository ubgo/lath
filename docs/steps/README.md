# steps, the pipeline adapters

Thin wrappers that let the [kit](../kit/README.md) libraries participate in a `pipeline`. Each one reads what it needs from pipeline state, honours dry run, reports progress, and delegates the actual work to a package that knows how to do it.

```
common/   vendor-neutral steps, plus the shared state keys
docker/   steps over kit/docker
git/      steps over kit/git
```

⚠️ **These are not privileged.** lath ships them the way anyone else would ship theirs: a Go module that imports `pipeline` and implements `Step`. There is no registry to join, no interface only these can satisfy, and no build-time knowledge of them anywhere in the runner. A third-party set is structurally identical, see [`../../steps/doc.go`](../../steps/doc.go).

## Are they even necessary?

No, and that is worth knowing. A definition can call the kit directly:

```go
pipeline.Func{
    Label: "prune-images",
    Do: func(ctx context.Context, s *pipeline.State) error {
        summary, err := docker.On(box).Prune(ctx, docker.PruneOptions{})
        if err != nil {
            return err
        }
        s.Detailf("pruned: %s", summary)
        return nil
    },
}
```

A step adds exactly three things: **a name for the plan, a declaration of what state it reads and writes, and dry-run behaviour**. The second is what earns it, `Validate()` rejects a pipeline whose wiring is wrong *before it runs*, so putting `Build` before `ResolveCommit` is an error at validation rather than a failure three minutes into a deploy.

## State keys

Defined in `common`, so both other packages and any third-party set share one vocabulary.

| Key | Type | Provided by | Consumed by |
|---|---|---|---|
| `KeyCommit` | `string` | `git.ResolveCommit` | `docker.Build` |
| `KeyBranch` | `string` | `git.ResolveCommit` | `docker.Build` (via `ArgsFromState`) |
| `KeyEnv` | `string` | `common.ResolveEnv` | `docker.Start` |
| `KeyImage` | `string` | `docker.Build` or `docker.UseImage` | `Push`, `Pull`, `RunOnce`, `Start` |
| `KeyContainers` | `[]string` | `docker.Start` | `WaitHealthy`, `StopPrevious` |
| `KeyRouted` | `[]string` | `docker.Start` | whatever points a proxy at the release, see the recipe below |

## The steps

### common

| Step | Reads | Writes |
|---|---|---|
| `ResolveEnv` |, | `KeyEnv` |
| `PutFile` |, |, |
| `EnsureDir` |, |, |
| `Request` | whatever it declares in `Reads` |, |

### git

| Step | Reads | Writes |
|---|---|---|
| `ResolveCommit` |, | `KeyCommit`, `KeyBranch` |

### docker

| Step | Reads | Writes |
|---|---|---|
| `Build` | `KeyCommit` + every key in `ArgsFromState` | `KeyImage` |
| `UseImage` | `TagFrom`, when the tag comes from state | `KeyImage` |
| `Push` / `Pull` | `KeyImage` |, |
| `Login` |, |, |
| `RunOnce` | `KeyImage` |, |
| `Start` | `KeyImage`, `KeyEnv` | `KeyContainers`, `KeyRouted` |
| `WaitHealthy` | `KeyContainers` |, |
| `StopPrevious` | `KeyContainers` |, |
| `Prune` |, |, |

## A whole deploy

```go
box := runner.Runner(runner.Local{})
if env != "local" {
    box = runner.Remote{Host: ssh.Host{Addr: host, User: user}}
}

p := pipeline.Pipeline{Name: "deploy-" + env, Steps: []pipeline.Step{
    common.ResolveEnv{Environment: env, Secrets: []string{"GHCR_PAT", "VPS_HOST"}},
    git.ResolveCommit{},
    docker.Build{
        Repository: "ghcr.io/acme/app",
        Options:    dockerkit.BuildOptions{Context: ".", Platforms: []string{"linux/amd64"}},
    },
    docker.Login{Registry: "ghcr.io", Username: actor, Password: pat},
    docker.Push{},
    docker.Pull{Runner: box},
    common.EnsureDir{Paths: dirs, Owner: "1000:1000", Runner: box},
    common.PutFile{Path: "/srv/app/.env", Secret: cfg, Runner: box},
    docker.RunOnce{Label: "migrate", Cmd: []string{"app", "migrate"}, Runner: box},
    docker.Start{Runner: box, NamePrefix: "app", EnvFile: "/srv/app/.env",
        Processes: []docker.Process{{Name: "web", Port: 8080, Route: true}}},
    docker.WaitHealthy{Runner: box, Timeout: 90 * time.Second},
    docker.StopPrevious{Runner: box, NamePrefix: "app", Drain: 5 * time.Second},
    docker.Prune{Runner: box, Options: dockerkit.PruneOptions{Filter: []string{"until=24h"}}},
}}

if err := p.Validate(); err != nil {   // wiring checked BEFORE anything runs
    return err
}
return p.Run(ctx, pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(os.Stdout)))
```

## Defaults

Every one is overridable per step; these are what you get by not setting the field.

| Constant | Value | Why |
|---|---|---|
| `docker.DefaultHealthTimeout` | 60s | How long `WaitHealthy` gives a container to prove itself |
| `docker.DefaultSettle` | 5s | With no image `HEALTHCHECK`, how long a container must stay up with no restarts before it counts as healthy |
| `docker.DefaultRegistryAttempts` | 10 | Registry login and pull are retried: a fresh host's egress to a registry is intermittently throttled, and one failure is not a broken deploy |
| `docker.DefaultRegistryBackoff` | 4s | First retry delay |
| `docker.DefaultRegistryMaxBackoff` | 30s | Backoff ceiling |
| `common.DefaultRequestMethod` | `POST` | What `Request` sends when the caller names no method. The step knows nothing about the receiver, and POST is what "here is a body, do something with it" means to the widest range of them |
| `common.DefaultRequestTimeout` | 15s | Bounds a `Request`. A service that will not answer promptly is not going to do the work either |
| `common.DefaultRequestLabel` | `request` | What an unlabelled `Request` is called in plans and logs |
| `common.DefaultContentType` | `application/json` | Sent when the caller names none |
| `docker.DefaultRunOnceLabel` | `run-once` | What an unlabelled `RunOnce` is called in plans and logs. Names the mechanism, not the commonest use: a plan reading "migrate" for a cache warm is a plan that lies |

`common.KeyValues` is the canonical list of every state key this package defines. Adding a key means adding it here too, so tooling can enumerate the data model rather than rediscovering it.

## Ordering invariants Validate cannot infer

- `RunOnce` before `Start` when it migrates, so processes boot against a ready schema, and exactly once, not once per container.
- `WaitHealthy` before whatever switches traffic, so traffic never moves to a release that has not proven itself.
- `StopPrevious` **last**, so in-flight requests finish on the old version. Stopping first means a window with nothing running, which is the difference between a deploy and an outage.

## Redeploying the same commit

Container names carry the commit, so deploying a commit that is already running collides with itself and docker refuses:

```
docker run acme-local-app-cad9372-0 locally: exit 125:
  Conflict. The container name "/acme-local-app-cad9372-0" is already in use
```

That is the correct default. "This name is taken" normally means something is running that the step knows nothing about, and quietly removing it is how a deploy takes down a service it did not deploy.

It is wrong for a **rehearsal**, which redeploys one commit over and over. `docker.Start{Replace: true}` stops and removes the holder of the name before starting, on the reasoning that a name collision on a commit-derived name means the container already there IS this release, so replacing it is a restart rather than a swap. It costs a gap, the old container is gone before the new one is up, which is why it is opt-in and why a production pipeline that always ships a new commit should leave it off.

## Two ways to route to a release

Container names carry the commit, so there are only two shapes and they trade against each other:

| | Upstream is the container name | Upstream is a stable `Process.Alias` |
|---|---|---|
| Proxy config | rewritten every deploy | written once |
| Traffic switch | atomic, at the reload | overlaps: both releases answer during the drain |
| Config can live in git | no | yes |

Pick the first when the switch must be clean, the second when the routing configuration belongs in version control and a few seconds of split traffic is acceptable.

## Recipe: switching traffic

There is no `SwitchTraffic` step, and there was one until it was noticed for what it is. Pointing a proxy at a release is an HTTP call with a body, and so is purging a CDN, registering with service discovery, posting a deploy marker, or opening a change ticket. Naming the mechanism after the first of those made the rest look like they needed a step that does not exist.

So the mechanism is `common.Request` and the proxy-specific half lives in your definition, where the name of the proxy already is:

```go
common.Request{
    Label:  "switch-traffic",
    URL:    proxyAdminAPI + "/config/apps/http/servers/srv0/routes/0/handle/0/upstreams",
    Method: http.MethodPatch,           // Caddy wants a partial update
    Reads:  []pipeline.Key{common.KeyRouted},
    Body:   caddyUpstreams,
}

// Reads KeyRouted, not KeyContainers: workers dial out, and a proxy given
// their names would route public requests to something with no listener.
func caddyUpstreams(s *pipeline.State) ([]byte, error) {
    containers, err := pipeline.Get[[]string](s, common.KeyRouted)
    if err != nil {
        return nil, err
    }
    // Refusing an empty set is policy, and it belongs here: an empty upstream
    // list takes the site down while the request itself succeeds. It is not
    // true of a purge, which is why the step does not assume it.
    if len(containers) == 0 {
        return nil, fmt.Errorf("no routed containers: mark the process that takes inbound traffic with Route")
    }
    ...
}
```

`Reads` is what keeps this checkable: those keys join `Requires`, so a `Body` reading a key nothing produces fails validation before the deploy starts rather than three minutes in. The whole recipe is in [`example/.lath/deploy.go`](../../example/.lath/deploy.go), and it is about fifteen lines.

## Options pass through

Steps **embed** the library's options struct rather than mirroring its fields:

```go
docker.Build{
    Repository: "ghcr.io/acme/app",       // the step owns this: the tag comes from the commit
    Options: dockerkit.BuildOptions{      // everything else, untouched
        Context: ".", Target: "runtime", NoCache: true,
        Extra: []string{"--secret", "id=npm"},
    },
}
```

So an option added to `kit/docker` needs **no change here**. A step that re-declared each field would silently lag every addition, and a caller would have no way to reach the new one short of abandoning the step.

`docker.Start` layers them, deploy-wide `Options`, then each `Process`'s own, then the fields the step owns:

```go
docker.Start{
    Options:   dockerkit.RunOptions{Memory: "256m", Env: map[string]string{"LOG_LEVEL": "info"}},
    Processes: []docker.Process{
        {Name: "web"},
        {Name: "worker", Options: dockerkit.RunOptions{Memory: "2g",
            Env: map[string]string{"LOG_LEVEL": "debug"}}},
    },
}
```

Workers get 2g and debug logging; web keeps 256m and info; both keep everything shared. `Extra` accumulates rather than replacing.

### Shipping an image you are not building

`Build` is the usual producer of `KeyImage`, but not the only sensible one. Rolling back, promoting the exact image staging approved, or redeploying after a host was rebuilt all want a *specific existing* artifact, and a rebuild does not reproduce it, it produces a new one from whatever the build inputs resolve to today. `UseImage` names the artifact instead:

```go
docker.UseImage{
    Repository: "ghcr.io/acme/app",
    Tag:        target,      // or TagFrom: keyTarget, when a step decides it
    MustExist:  true,        // fail here, listing the tags that DO exist
    Runner:     box,
}
```

The whole target is in [`example/.lath/deploy.go`](../../example/.lath/deploy.go) as `rollbackPipeline`, compiled and validated by the gate rather than written out here as prose.

It deliberately does not decide *which* tag. "The previous one" means the last deployed commit in one project and the last tag that passed a health check in another, so the project writes that step and points `TagFrom` at its output. `MustExist` is worth its round trip: without it a mistyped or pruned tag fails several steps later as a registry error that never mentions what could have been used instead. `kit/docker`'s `Client.Images(ctx, repository)` is the same listing, if a definition wants to choose a tag itself.

⚠️ **An image rollback is not a schema rollback.** Deploying an older image runs older code against today's database. Additive migrations survive it; a rename or a drop does not. Whether the rollback pipeline re-runs `RunOnce` or omits it is a project decision, and the project is the only place that knows which kind of migrations it writes.

### Build args that come from state

`Options.Args` is fixed when the pipeline is assembled, but the values most worth stamping into an image (the commit, the branch, the target environment) are produced by earlier steps at run time. `ArgsFromState` binds a build-arg name to a state key:

```go
docker.Build{
    Repository: "ghcr.io/acme/app",
    Options: dockerkit.BuildOptions{
        Context: ".",
        Args:    map[string]string{"BUILD_TIME": time.Now().UTC().Format(time.RFC3339)},
    },
    ArgsFromState: map[string]pipeline.Key{
        "COMMIT":      common.KeyCommit,
        "BRANCH":      common.KeyBranch,
        "ENVIRONMENT": common.KeyEnv,
    },
}
```

The bindings are **data, not a callback**, and that is the point: they join `Requires()`, so a missing producer is a validation error before anything builds rather than a failure minutes in. Two args may read the same key (a version and a branch are often the same value), `Requires()` dedupes.

Values from state override `Options.Args` on a name collision, and `Options` is copied rather than mutated, so running one pipeline value twice does not accumulate args from the first run.

⚠️ **A missing build arg fails silently.** The Dockerfile's `ARG` defaults win, the build succeeds, and the binary reports itself as `dev`/`unknown` with nothing raising an error. This happened in production: images shipped unstamped for as long as nobody checked. If a Dockerfile has `ARG`s that feed ldflags, wire every one of them.

⚠️ **Step-owned fields cannot be overridden.** `Name`, `Image`, `Network`, and `Build`'s `Tag` are derived from pipeline state, setting them in `Options` is refused or discarded, so a container can never be named or imaged differently from what the pipeline recorded.

## WaitHealthy, the one to read carefully

⚠️ It has reported success on a dead application twice, for different reasons, and the current implementation is shaped by both.

**A crash-looping container is briefly running** between `docker run` and its first fatal error, so "is it running?" passes in milliseconds.

**A container running supervisord stays up while every process it manages is `FATAL`.** The container is running; nothing inside it is.

So, in order of preference:

1. `Probe`, an HTTP URL, if you have one
2. the image's own `HEALTHCHECK`, used automatically when declared
3. uptime: not restarting, `RestartCount` unchanged, and **still up after `Settle`** (5s)

A container that has already failed errors immediately rather than burning the timeout. The answer is known, and making an operator wait ninety seconds for it helps nobody.

## StopPrevious safety

`NamePrefix` is **required**. Without it the step cannot tell its own containers from anything else on the host, and stopping the wrong one is not fixed by retrying. Containers from the current release, and anything not carrying the prefix, are left alone.
