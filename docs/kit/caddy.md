# `kit/caddy` — install configuration and reload

Writes a site file into the directory a running Caddy imports, proves the configuration parses, and swaps it in without dropping connections.

What the file SAYS is yours: a site block is policy and every project's differs. What this owns is the sequence, which is the same everywhere and is easy to get wrong in a way that stays hidden for hours.

```go
c := caddy.Client{
    Container: "caddy_public",              // where caddy runs
    SitesDir:  "/srv/stack/external/sites", // HOST path, mounted into the container
    Where:     box,                         // nil means this machine
}

err := c.EnsureSite(ctx, "api.example.com", siteFileContents)
```

## API

```go
func (c Client) EnsureSite(ctx context.Context, name, content string) error
func (c Client) Validate(ctx context.Context) error
func (c Client) Reload(ctx context.Context) error
func (c Client) SitePath(name string) string
```

| Symbol | What |
|---|---|
| `Client` | `Container`, `SitesDir`, `ConfigPath`, `Where` |
| `SitePath` | Where a named site lands. Exported so a caller can report it, or remove a site, without rebuilding the naming rule |
| `Program` · `DockerProgram` | `caddy` and `docker`, named so a podman host is one edit |
| `DefaultConfigPath` | `/etc/caddy/Caddyfile` |
| `ConfigAdapter` | `caddyfile`, since the config is not JSON |
| `SiteSuffix` | `.caddy` |

## Why the order is write, validate, reload

⚠️ **Caddy refuses to START on an invalid configuration.** A bad file dropped into an imported directory does not merely fail to work: it takes down every OTHER site that Caddy serves the next time it restarts, which may be hours later and will look entirely unrelated to whoever deployed it.

So `EnsureSite` writes the file, validates the whole configuration, and only then reloads. **On a validation failure it puts the directory back as it found it**: the previous file is restored when there was one, and the new file removed when there was not. Leaving the bad file arms precisely that delayed failure; deleting a previously good one would take a working route down to report a broken replacement. The error carries Caddy's own diagnostic, which names the line.

**An identical file is not rewritten.** A site file rarely changes between deploys, and rewriting one that has not is the write most likely to be refused for a reason unrelated to the deploy: a root-owned file in a git-tracked directory, say. When the bytes already match, the write is skipped and validate and reload still run, so the route is live when `EnsureSite` returns even if the file was placed by hand and never loaded.

⚠️ **Reload, not restart.** A restart drops every connection Caddy is serving, including those belonging to other sites on a shared instance. Deploying one service should not do that to its neighbours.

⚠️ **`SitesDir` is the HOST path**, not the path inside the container. The file is written by the runner, which is outside the container, and mounted in, normally read-only. `SitePath` adds `.caddy` when the name lacks it, because the conventional `import sites/*.caddy` silently ignores anything else.
