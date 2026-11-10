// Package golang compiles Go binaries on this machine, for an image to copy
// rather than build.
//
// Why outside docker: a `go build` inside a fresh BuildKit builder starts from
// an empty build cache every time, so a service with a few binaries spends
// minutes of every deploy compiling what has not changed. Built on the host,
// the machine's Go build cache (or a CI cache) carries over, and an
// incremental build takes seconds. The Dockerfile then only copies the result.
//
// What it guarantees, each one a way a hand-written build script goes wrong:
//
//   - the toolchain is pinned to the one the module or workspace names, so a
//     newer local Go never builds a different binary than CI does;
//   - the output folder is cleared first, so a binary that failed to build
//     cannot be replaced by a stale one from an earlier run;
//   - every expected binary is checked for afterwards, by name;
//   - the build is reproducible across machines: no cgo, no build paths
//     (-trimpath), no VCS stamp that depends on whether .git is present.
//
// Depends on the go command being on PATH. It runs on this machine only: the
// folder it clears and checks is a local one, so there is no Runner to point
// somewhere else.
package golang

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
)

// Program is the command this package drives.
const Program = "go"

// The platform a build targets when BuildOptions names none: the usual
// server. A local rehearsal that runs the binaries on this machine passes its
// own (runtime.GOOS / runtime.GOARCH).
const (
	DefaultGOOS   = "linux"
	DefaultGOARCH = "amd64"
)

// The variables of github.com/ubgo/buildinfo, for BuildOptions.Vars and the
// steps that fill them from pipeline state. Named because -X against a path
// or symbol that does not exist is SILENTLY IGNORED by the linker: a typo here
// ships an unstamped binary that reports "dev", with nothing failing.
const (
	BuildinfoVersion   = "github.com/ubgo/buildinfo.Version"
	BuildinfoCommit    = "github.com/ubgo/buildinfo.Commit"
	BuildinfoBranch    = "github.com/ubgo/buildinfo.Branch"
	BuildinfoBuildTime = "github.com/ubgo/buildinfo.BuildTime"
)

// ErrNoToolchain reports a module or workspace that names no toolchain
// exact enough to pin a build to.
var ErrNoToolchain = errors.New("no toolchain to pin the build to")

// BuildOptions describes one `go build` of several main packages.
type BuildOptions struct {
	// Dir is the directory the build runs in: the module or workspace root
	// the Packages are relative to. Empty means the working directory.
	Dir string
	// Packages are the main packages to build, as paths relative to Dir
	// ("./cmd/api"). Each becomes one binary named after its last path
	// element, the way `go build -o dir/` names them.
	//
	// Patterns ("./cmd/...") and "." are refused: the binaries they produce
	// cannot be named in advance, and naming them is what lets Build prove
	// every one was produced.
	Packages []string
	// Output is the folder the binaries land in, relative to Dir. Required.
	//
	// ⚠️ It is DELETED and recreated on every build. That is the point: a
	// leftover binary from an earlier run would otherwise survive a build
	// that failed to replace it. Which is why it must be a folder inside Dir,
	// and never Dir itself.
	Output string
	// GOOS and GOARCH are the target platform. Empty means DefaultGOOS and
	// DefaultGOARCH.
	GOOS   string
	GOARCH string
	// Toolchain is the GOTOOLCHAIN value the build runs under ("go1.26.4").
	// Empty means the one the workspace or module names (see ReadToolchain).
	//
	// Settable for the case the files cannot answer, and as an explicit,
	// visible opt-out: "local" builds with whatever Go is installed.
	Toolchain string
	// Vars are -X linker stamps: the key is the fully qualified variable
	// ("github.com/ubgo/buildinfo.Commit"), the value what it is set to.
	Vars map[string]string
	// Strip drops the symbol table and DWARF data (-s -w): smaller binaries,
	// and stack traces that still name functions.
	Strip bool
	// Extra passes raw go build flags through, before the packages: "-tags",
	// "-race", "-p". -o, -ldflags, -trimpath and -buildvcs are refused there;
	// this package owns them, and a second -ldflags would silently discard
	// Vars.
	Extra []string
	// Env adds environment variables to the build, after the ones BuildEnv
	// pins, so an entry here wins ("GOAMD64=v3" for a fleet known to have
	// it, "GOWORK=off"). The escape hatch for the pinned baseline. A GOWORK
	// here also decides which file the toolchain is read from.
	//
	// ⚠️ Printed: the steps package shows the whole environment in its plan
	// and log lines, so never put a credential here.
	Env []string
}

// symbolPattern is what an -X target looks like: an import path, a dot, and
// an identifier. Checked because the linker ignores one it cannot resolve.
var symbolPattern = regexp.MustCompile(`^[^\s=]+\.[\pL_][\pL\pN_]*$`)

// ownedFlags are the go build flags this package renders itself and refuses
// in Extra: -o and -ldflags because a second one silently replaces ours, and
// the reproducibility pair because "-trimpath=false" in Extra would quietly
// undo the guarantee the package documents.
var ownedFlags = []string{"-o", "-ldflags", "-trimpath", "-buildvcs"}

// Validate checks the options without touching the filesystem.
func (o BuildOptions) Validate() error {
	if len(o.Packages) == 0 {
		return fmt.Errorf("golang: Packages is empty; name at least one main package")
	}
	seen := map[string]string{}
	for _, pkg := range o.Packages {
		name, err := binaryName(pkg)
		if err != nil {
			return err
		}
		if other, dup := seen[name]; dup {
			return fmt.Errorf("golang: %s and %s would both build a binary named %q", other, pkg, name)
		}
		seen[name] = pkg
	}
	if o.Output == "" {
		return fmt.Errorf("golang: Output is required")
	}
	if clean := filepath.Clean(o.Output); !filepath.IsLocal(clean) || clean == "." {
		// Refused, not cleaned up: Output is deleted on every build, and a
		// value that reaches Dir itself or outside it deletes the project.
		return fmt.Errorf("golang: Output %q must be a folder inside Dir (it is deleted on every build)", o.Output)
	}
	out := filepath.ToSlash(filepath.Clean(o.Output))
	// Compared without case: on the usual macOS and Windows disks Cmd and
	// cmd, .GIT and .git are the same folder.
	if top := strings.Split(out, "/")[0]; strings.EqualFold(top, ".git") {
		return fmt.Errorf("golang: Output %q is inside .git (it is deleted on every build)", o.Output)
	}
	for _, pkg := range o.Packages {
		p := filepath.ToSlash(filepath.Clean(pkg))
		if lp, lo := strings.ToLower(p), strings.ToLower(out); lp == lo || strings.HasPrefix(lp+"/", lo+"/") {
			// The build would delete the sources it is about to compile,
			// uncommitted work included, and only then fail.
			return fmt.Errorf("golang: Output %q holds the package %s; it is deleted on every build", o.Output, pkg)
		}
	}
	for sym, value := range o.Vars {
		if !symbolPattern.MatchString(sym) {
			return fmt.Errorf("golang: Vars key %q is not an import path and a variable name, e.g. %s", sym, BuildinfoCommit)
		}
		if _, err := ldflagWord(sym + "=" + value); err != nil {
			return err
		}
	}
	for _, f := range o.Extra {
		name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
		for _, owned := range ownedFlags {
			if "-"+name == owned {
				return fmt.Errorf("golang: Extra may not carry %s; this package sets it (Output, Vars, or always)", owned)
			}
		}
	}
	return nil
}

// binaryName is the file `go build -o dir/` writes for one package path: its
// last element, or the one before it when the last is a major version
// suffix (./cmd/tool/v2 builds "tool").
func binaryName(pkg string) (string, error) {
	if strings.Contains(pkg, "...") {
		return "", fmt.Errorf("golang: %q is a pattern; name each main package", pkg)
	}
	parts := strings.Split(strings.Trim(filepath.ToSlash(filepath.Clean(pkg)), "/"), "/")
	last := parts[len(parts)-1]
	if majorVersion.MatchString(last) && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	if last == "." || last == ".." || last == "" {
		return "", fmt.Errorf("golang: cannot name the binary %q builds; give the package by its folder, e.g. ./cmd/api", pkg)
	}
	return last, nil
}

// majorVersion is a module major-version suffix. v0 and v1 are not: the go
// command names a binary built from ./cmd/tool/v1 "v1", and only v2 and up
// make it fall back to the folder above.
var majorVersion = regexp.MustCompile(`^v([2-9]|[1-9][0-9]+)$`)

// Binaries returns the file names the build will write into Output, in
// Packages order. Windows binaries carry .exe, as go names them.
func (o BuildOptions) Binaries() []string {
	out := make([]string, 0, len(o.Packages))
	for _, pkg := range o.Packages {
		name, err := binaryName(pkg)
		if err != nil {
			// Validate refuses these; Binaries reports what it can.
			continue
		}
		if o.goos() == "windows" {
			name += ".exe"
		}
		out = append(out, name)
	}
	return out
}

func (o BuildOptions) goos() string {
	if o.GOOS == "" {
		return DefaultGOOS
	}
	return o.GOOS
}

func (o BuildOptions) goarch() string {
	if o.GOARCH == "" {
		return DefaultGOARCH
	}
	return o.GOARCH
}

// Platform is the os/arch the build targets, defaults applied.
func (o BuildOptions) Platform() string { return o.goos() + "/" + o.goarch() }

// BuildArgs renders the go command line for these options, without the
// program name.
//
// Exported so a caller can show exactly what will run (a dry run does), and
// so the flags can be asserted without compiling anything.
func (o BuildOptions) BuildArgs() []string {
	args := []string{"build", "-trimpath", "-buildvcs=false"}
	var ld []string
	if o.Strip {
		ld = append(ld, "-s", "-w")
	}
	for _, sym := range sortedKeys(o.Vars) {
		// Validate has proven each word renders.
		word, _ := ldflagWord(sym + "=" + o.Vars[sym])
		ld = append(ld, "-X", word)
	}
	if len(ld) > 0 {
		args = append(args, "-ldflags", strings.Join(ld, " "))
	}
	args = append(args, o.Extra...)
	// The trailing separator is what makes -o a folder for one package as
	// well as several; without it a single package is written to a FILE
	// named after the folder.
	args = append(args, "-o", filepath.ToSlash(filepath.Clean(o.Output))+"/")
	return append(args, o.Packages...)
}

// BuildEnv is the environment the build runs with, toolchain given: the
// pinned baseline, then Options.Env.
//
// CGO is always off: the binaries are static, so they run on a minimal base
// image with no C library, and cross-compiling needs no C toolchain for the
// target.
//
// The CPU level is pinned to go's own baseline for the architecture
// (GOAMD64=v1, GOARM64=v8.0, GOARM=7) and GOFLAGS and GOEXPERIMENT are
// cleared, because all of them are otherwise inherited from the shell: a
// laptop exporting GOAMD64=v3 would ship a binary that dies with an illegal
// instruction on an older server. Settings written with `go env -w` are not
// overridden by an empty variable; `go env -u` removes them.
func (o BuildOptions) BuildEnv(toolchain string) []string {
	env := []string{
		"CGO_ENABLED=0",
		"GOOS=" + o.goos(),
		"GOARCH=" + o.goarch(),
		"GOTOOLCHAIN=" + toolchain,
		"GOFLAGS=",
		"GOEXPERIMENT=",
	}
	if level, ok := cpuBaseline[o.goarch()]; ok {
		env = append(env, level)
	}
	return append(env, o.Env...)
}

// cpuBaseline is go's default microarchitecture level per architecture,
// pinned so the shell cannot raise it.
var cpuBaseline = map[string]string{
	"amd64": "GOAMD64=v1",
	"arm64": "GOARM64=v8.0",
	"arm":   "GOARM=7",
}

// ldflagWord quotes one -X argument the way the go command splits -ldflags:
// on spaces, with single or double quotes grouping, and no escapes. A value
// that holds both kinds of quote, or a newline, cannot be represented and is
// refused rather than mangled.
func ldflagWord(s string) (string, error) {
	switch {
	case strings.ContainsAny(s, "\n\r"):
		return "", fmt.Errorf("golang: -X %q holds a newline, which -ldflags cannot carry", s)
	case !strings.ContainsAny(s, " \t'\""):
		return s, nil
	case !strings.Contains(s, "'"):
		return "'" + s + "'", nil
	case !strings.Contains(s, `"`):
		return `"` + s + `"`, nil
	}
	return "", fmt.Errorf("golang: -X %q holds both kinds of quote, which -ldflags cannot carry", s)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sorted so two identical builds print an identical command line, and a
	// log diff means a real change.
	sort.Strings(keys)
	return keys
}

// Build compiles the binaries into Output, after clearing it, and proves each
// one was produced. Progress and compiler errors stream to out when it is not
// nil.
func Build(ctx context.Context, o BuildOptions, out io.Writer) error {
	if err := o.Validate(); err != nil {
		return err
	}
	dir, err := o.dir()
	if err != nil {
		return err
	}
	toolchain, err := o.PinnedToolchain(dir)
	if err != nil {
		return err
	}
	if !proc.Exists(Program) {
		return fmt.Errorf("golang: %s is not on PATH", Program)
	}
	target := filepath.Join(dir, o.Output)
	if err := o.mayClear(target); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("golang: clearing %s: %w", o.Output, err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("golang: creating %s: %w", o.Output, err)
	}
	if err := os.WriteFile(filepath.Join(target, MarkerFile), []byte(markerText), 0o644); err != nil {
		return fmt.Errorf("golang: marking %s: %w", o.Output, err)
	}
	opts := []proc.Option{proc.Dir(dir), proc.Env(o.BuildEnv(toolchain)...), proc.Capture()}
	if out != nil {
		opts = append(opts, proc.Out(out))
	}
	where := runner.Local{}
	r, err := where.Run(ctx, Program, o.BuildArgs(), opts...)
	if err := runner.Check("go build", where, r, err); err != nil {
		return err
	}
	var missing []string
	for _, name := range o.Binaries() {
		info, err := os.Stat(filepath.Join(target, name))
		if err != nil || !info.Mode().IsRegular() {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("golang: go build succeeded but did not produce %s in %s", strings.Join(missing, ", "), o.Output)
	}
	return nil
}

// MarkerFile is written into Output by every Build, so the next one knows the
// folder is its own to delete.
const MarkerFile = ".golang-build"

const markerText = "Written by github.com/ubgo/lath/kit/golang. Everything in this folder is deleted on the next build.\n"

// mayClear refuses to delete an existing folder Build did not create.
//
// Validate keeps Output away from the sources and .git, but a folder such as
// bin/ may hold tracked scripts, and deleting it is not undone by a rebuild.
// A folder is Build's when it carries MarkerFile, or holds nothing but files
// named like the binaries this build produces (an earlier hand-written build
// of the same packages). Anything else is somebody's, and is refused.
func (o BuildOptions) mayClear(target string) error {
	entries, err := os.ReadDir(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("golang: reading %s: %w", o.Output, err)
	}
	expected := map[string]bool{MarkerFile: true}
	for _, name := range o.Binaries() {
		expected[name] = true
	}
	for _, e := range entries {
		if e.Name() == MarkerFile {
			return nil
		}
	}
	for _, e := range entries {
		if !expected[e.Name()] || !e.Type().IsRegular() {
			return fmt.Errorf("golang: %s holds %s, which this build did not write; "+
				"it is deleted on every build, so empty it or choose another Output", o.Output, e.Name())
		}
	}
	return nil
}

func (o BuildOptions) dir() (string, error) {
	if o.Dir != "" {
		return o.Dir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("golang: working directory: %w", err)
	}
	return wd, nil
}
