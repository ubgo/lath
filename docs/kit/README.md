# kit, the library reference

27 packages that any Go program can import, whether or not it uses lath. None of them imports `pipeline`; that is the line between a library and a pipeline adapter, and it is the only rule deciding what belongs here.

Every package follows the same conventions: `context.Context` first, `error` last, functional options or an options struct for configuration, and **one typed escape hatch** so a caller who needs something the package does not model is never stuck.

## The packages

| Package | For | Needs |
|---|---|---|
| [`proc`](proc.md) | running processes, signals, finding them by pattern | `pgrep` for `Find` |
| [`runner`](runner.md) | deciding **where** a command runs, local or over ssh |, |
| [`ssh`](ssh.md) | commands and files on another machine | `ssh`, `scp` |
| [`remotefs`](remotefs.md) | reading and writing files wherever a Runner points | POSIX `sh` |
| [`docker`](docker.md) | building images, running containers, registries | `docker` |
| [`git`](git.md) | reading a working tree, and writing to one: clone, commit, push, tag | `git` |
| [`secret`](secret.md) | credentials that cannot be printed by accident |, |
| [`env`](env.md) | required environment variables, reported together |, |
| [`fsx`](fsx.md) | atomic writes and copies |, |
| [`scan`](scan.md) | walking, filtering and grepping a tree |, |
| [`session`](session.md) | advertise a running process's endpoint so another process can find and attach to it | `ps` |
| [`hashtree`](hashtree.md) | content digests for cache keys |, |
| [`archive`](archive.md) | tar.gz with extraction guards |, |
| [`caddy`](caddy.md) | install a site file and reload, validating first | `docker`, `caddy` in the container |
| [`certs`](certs.md) | private keys and signing requests for any CA |, |
| [`settings`](settings.md) | a config file the environment can override |, |
| [`cloudflare`](cloudflare.md) | DNS records and origin certificates |, |
| [`checksum`](checksum.md) | the `checksums.txt` manifest, in the format `sha256sum` reads |, |
| [`github`](github.md) | releases, artifacts, and reading back what was attached |, |
| [`brew`](brew.md) | rendering a Homebrew formula for a released binary |, |
| [`download`](download.md) | fetching a URL, verified before it lands |, |
| [`wait`](wait.md) | polling until a condition or an endpoint is ready |, |
| [`supervise`](supervise.md) | restart with backoff; one-shot retry |, |
| [`lock`](lock.md) | single-instance guard with stale detection | `ps` |
| [`confirm`](confirm.md) | typed confirmation that never hangs in CI |, |
| [`out`](out.md) | log files and line-prefixed output |, |
| [`repo`](repo.md) | locating the repository root |, |

## Conventions

**A non-zero exit is data, not an error.** `proc.Run` and `runner.Runner.Run` return a `Result` describing how a command ended. An `error` means it could not be started at all. The program is missing, the host is unreachable. Deciding whether exit 1 is a failure is the caller's job, because for `grep` it means "no match" and for `docker build` it means the build broke.

**The zero value works.** A nil `Runner` means this machine. An empty `scan.Filter` matches everything. A zero `supervise.Policy` uses bounded defaults. You should never have to construct something just to accept the defaults.

**Escape hatches are typed and documented.** `ssh.Host.Extra`, `docker.BuildOptions.Extra`, `git.Client.WithFlags`, `proc.Find`'s variadic flags, `wait.Accept`. Real systems have configurations nobody anticipated, and a primitive with no escape hatch gets abandoned the first time it does not quite fit, taking its safety guarantees with it.

**External programs are named at the point of use.** `proc` needs `pgrep` for `Find`; `lock` needs `ps` to verify liveness; `docker` needs `docker`. Each reports a clear error when the program is absent rather than pretending to work.

## A composed example

The packages are designed to be used together. This builds an image, ships it, and waits for it, locally or on a server, decided by one variable:

```go
where := runner.Runner(runner.Local{})
if env != "local" {
    where = runner.Remote{Host: ssh.Host{Addr: "box-1", User: "deploy"}}
}

commit, err := git.On(nil, "").ShortCommit(ctx, 7)          // always local: the repo is here
if err := git.On(nil, "").RequireClean(ctx); err != nil {    // a dirty tree makes the tag a lie
    return err
}

image := "ghcr.io/acme/app:" + commit
if err := docker.On(nil).Build(ctx, docker.BuildOptions{Tag: image, Context: "."}); err != nil {
    return err
}

token, err := secret.FromEnv("GHCR_PAT")                     // never printable
if err := docker.On(where).Login(ctx, "ghcr.io", "acme", token.Reveal()); err != nil {
    return err
}
if err := remotefs.WriteFile(ctx, where, "/srv/app/.env", cfg, true); err != nil {
    return err                                               // 0600, never touches local disk
}
if err := docker.On(where).Run(ctx, docker.RunOptions{
    Name: "app-" + commit, Image: image, Detach: true, EnvFile: "/srv/app/.env",
}); err != nil {
    return err
}
return wait.HTTPOK(ctx, "http://box-1/health", wait.Timeout(90*time.Second))
```

Swap `where` and the same code runs against local docker. That is the whole point of `runner`.
