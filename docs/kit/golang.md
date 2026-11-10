# kit/golang

Compiling Go binaries on this machine, for an image to copy rather than build.

```go
import "github.com/ubgo/lath/kit/golang"
```

**Why outside docker.** A `go build` inside a fresh BuildKit builder starts from an empty build cache every time, so a service with a few binaries recompiles everything on every deploy: minutes, for code that did not change. Built on the host, the machine's build cache (or a CI cache) carries over and an incremental build takes seconds. The Dockerfile then only `COPY`s the result.

Needs `go` on PATH. Runs on **this machine only**: the folder it clears and checks is local, so there is no Runner to point elsewhere.

## API

```go
func Build(ctx context.Context, o BuildOptions, out io.Writer) error
func ReadToolchain(dir string) (string, error)
func (o BuildOptions) PinnedToolchain(dir string) (string, error)   // Toolchain, or ReadToolchain as the build sees GOWORK

func (o BuildOptions) Validate() error
func (o BuildOptions) BuildArgs() []string                 // the go command line, for display or a test
func (o BuildOptions) BuildEnv(toolchain string) []string  // CGO_ENABLED, GOOS, GOARCH, GOTOOLCHAIN
func (o BuildOptions) Binaries() []string                  // the file names Build checks for
func (o BuildOptions) Platform() string                    // "linux/amd64", defaults applied
```

```go
type BuildOptions struct {
    Dir       string            // the module or workspace root; empty = working directory
    Packages  []string          // main packages, "./cmd/api"; one binary each
    Output    string            // a folder inside Dir, DELETED and recreated on every build
    GOOS      string            // empty = DefaultGOOS ("linux")
    GOARCH    string            // empty = DefaultGOARCH ("amd64")
    Toolchain string            // GOTOOLCHAIN; empty = ReadToolchain(Dir)
    Vars      map[string]string // -X stamps: "github.com/ubgo/buildinfo.Commit" -> "a3f1c2d"
    Strip     bool              // -s -w
    Extra     []string          // raw go build flags: -tags, -race, -p
    Env       []string          // added after the pinned environment, so it wins: "GOAMD64=v3"
}
```

```go
err := golang.Build(ctx, golang.BuildOptions{
    Packages: []string{"./cmd/api", "./cmd/worker", "./cmd/migrate"},
    Output:   ".docker/bin",
    Strip:    true,
    Vars: map[string]string{
        golang.BuildinfoCommit: commit,
        golang.BuildinfoBranch: branch,
    },
}, os.Stdout)
```

One `go build -o .docker/bin/ ./cmd/api ./cmd/worker ./cmd/migrate`, so shared packages compile once and the binaries link in parallel. `golang.Build` in the steps package binds the stamps to pipeline state instead.

| Symbol | What |
|---|---|
| `Program` | `go` |
| `DefaultGOOS` · `DefaultGOARCH` | The target when `BuildOptions` names none: `linux/amd64`, the usual server. A local rehearsal passes `runtime.GOOS` / `runtime.GOARCH` |
| `BuildinfoVersion` · `BuildinfoCommit` · `BuildinfoBranch` · `BuildinfoBuildTime` | The variables of `github.com/ubgo/buildinfo`, spelt once |
| `ErrNoToolchain` | The workspace or module names no toolchain exact enough to pin |
| `MarkerFile` | Written into `Output` by every build, so the next one knows the folder is its own to delete |

## What a build guarantees

Each of these is a way a hand-written build script goes wrong.

- **The toolchain is pinned.** Under the default `GOTOOLCHAIN=auto` the go command uses the newer of the installed Go and the one the files name, so a laptop with a newer Go builds a different binary than CI from the same commit. `Build` sets `GOTOOLCHAIN` to what `ReadToolchain` finds.
- **The output folder is cleared first.** A binary that failed to build cannot be replaced by a stale one from an earlier run, which the image would otherwise copy without complaint.
- **Every binary is proven present** afterwards, by the name `go build -o dir/` gives it: the package's last path element, or the one before it for a `/v2` (and up) folder, plus `.exe` for Windows. `v0` and `v1` are ordinary names, as the go command treats them.
- **The build is reproducible.** `CGO_ENABLED=0` (static binaries that run on a base image with no C library, and cross-compile with no C toolchain), `-trimpath` (no build machine paths in stack traces), `-buildvcs=false` (no stamp that depends on whether `.git` is in the checkout).
- **The shell cannot change the target.** The CPU level is pinned to go's baseline (`GOAMD64=v1`, `GOARM64=v8.0`, `GOARM=7`) and `GOFLAGS` and `GOEXPERIMENT` are cleared: a laptop exporting `GOAMD64=v3` would otherwise ship a binary that dies with an illegal instruction on an older server. `Env` is the explicit way to choose otherwise; it is printed in the step's plan and log lines, so it must never carry a credential. Other architectures' levels (`GO386`, `GORISCV64`, `GOPPC64`) are not pinned and still come from the shell. Settings written with `go env -w` are not overridden by an empty variable; `go env -u` removes them.

## ReadToolchain

Finds the file the go command would read, the same way it does: `GOWORK` when set (`off` skips the workspace), otherwise the nearest `go.work` at or above `dir`, otherwise the nearest `go.mod`. Its `toolchain` line wins, unless a complete `go` line names a newer release, which the go command would run instead. Without one, a `go` line naming a complete release (`go 1.26.4`) is the implied toolchain, which is how the go command reads it too. A language version alone (`go 1.24`) names nothing downloadable and is `ErrNoToolchain`, with the command that fixes it:

```
golang: no toolchain to pin the build to: /src/app/go.mod has no toolchain line and its go line ("1.24")
names no release; add one with `go mod edit -toolchain=go1.N.P`, or set Toolchain
```

`PinnedToolchain` is what `Build` uses: `Toolchain` when set, otherwise the file the **build** will read, so a `GOWORK=off` in `Env` pins the module's Go rather than the workspace's. `Toolchain` is the escape hatch, and `"local"` is the visible way to say "whatever Go is installed".

## Gotchas

⚠️ **`Output` is deleted on every build.** That is the guarantee, so it is guarded three ways. It must be a relative folder inside `Dir`, never `Dir` itself, never inside `.git`, and never a folder that holds one of the `Packages` (`../bin`, `/tmp/bin`, `cmd` are refused). And an existing folder is deleted only if a previous build marked it (`MarkerFile`) or it holds nothing but files named like this build's binaries; a `bin/` with a tracked script in it is refused, before anything is deleted. A relative `GOWORK` is refused, as the go command refuses it.

⚠️ **`MarkerFile` lives inside `Output`.** A Dockerfile that copies the whole folder (`COPY .docker/bin/ /app/`) copies the marker too. Harmless, but copy the binaries by name, or add `.docker/bin/.golang-build` to `.dockerignore`.

⚠️ **`-X` against a symbol that does not exist is silently ignored by the linker.** A typo in a `Vars` key ships an unstamped binary that reports `dev`, with nothing failing. Use the `Buildinfo*` constants, and check the stamp after a deploy (a `/version` endpoint, `binary --version`). `Validate` catches only a key that is not shaped like `import/path.Name`.

⚠️ **Patterns and `.` are refused as packages.** `./cmd/...` and the module root build binaries whose names cannot be known in advance, and knowing them is what lets `Build` prove each one was produced. Name each main package by its folder.

⚠️ **`-o`, `-ldflags`, `-trimpath` and `-buildvcs` are refused in `Extra`.** This package renders them; the go command keeps only the last `-ldflags`, so a second one would silently discard every stamp, and `-trimpath=false` would quietly undo the reproducibility above.

⚠️ **A stamp value is quoted for the linker** when it holds a space or a quote, because the go command splits `-ldflags` on spaces. A value holding both kinds of quote, or a newline, cannot be represented and is refused rather than mangled.
