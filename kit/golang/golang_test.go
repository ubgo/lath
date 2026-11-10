package golang_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/golang"
)

// TestBuildArgsPinTheReproducibleFlags pins the command line. Each flag is a
// way the same commit builds differently on two machines: build paths in
// stack traces without -trimpath, a VCS stamp that depends on whether .git
// exists without -buildvcs=false.
func TestBuildArgsPinTheReproducibleFlags(t *testing.T) {
	t.Parallel()
	o := golang.BuildOptions{
		Packages: []string{"./cmd/api", "./cmd/cron"},
		Output:   ".docker/bin",
		Strip:    true,
		Vars: map[string]string{
			golang.BuildinfoVersion: "main",
			golang.BuildinfoCommit:  "abc1234",
		},
		Extra: []string{"-tags", "netgo"},
	}
	got := strings.Join(o.BuildArgs(), " ")
	want := "build -trimpath -buildvcs=false " +
		"-ldflags -s -w -X github.com/ubgo/buildinfo.Commit=abc1234 -X github.com/ubgo/buildinfo.Version=main " +
		"-tags netgo -o .docker/bin/ ./cmd/api ./cmd/cron"
	if got != want {
		t.Errorf("BuildArgs =\n  %s\nwant\n  %s", got, want)
	}
}

// TestOutputAlwaysEndsInASeparator pins the trailing slash. Without it a
// single package is written to a FILE named after the folder, and the image
// copies a file where it expected a directory of binaries.
func TestOutputAlwaysEndsInASeparator(t *testing.T) {
	t.Parallel()
	args := golang.BuildOptions{Packages: []string{"./cmd/api"}, Output: "bin/"}.BuildArgs()
	if i := slices.Index(args, "-o"); i < 0 || args[i+1] != "bin/" {
		t.Errorf("-o is not a folder: %v", args)
	}
}

// TestBuildEnvTurnsCgoOffAndPinsEverything pins the environment: cgo off so
// the binary runs on an image with no C library, and every platform variable
// set explicitly so the parent environment cannot change the target.
func TestBuildEnvTurnsCgoOffAndPinsEverything(t *testing.T) {
	t.Parallel()
	got := golang.BuildOptions{}.BuildEnv("go1.26.4")
	want := []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOTOOLCHAIN=go1.26.4",
		"GOFLAGS=", "GOEXPERIMENT=", "GOAMD64=v1"}
	if !slices.Equal(got, want) {
		t.Errorf("BuildEnv = %v, want %v", got, want)
	}
	// Env comes last, so an explicit choice beats the pinned baseline.
	v3 := golang.BuildOptions{Env: []string{"GOAMD64=v3"}}.BuildEnv("local")
	if v3[len(v3)-1] != "GOAMD64=v3" || slices.Index(v3, "GOAMD64=v1") > slices.Index(v3, "GOAMD64=v3") {
		t.Errorf("Env does not override the baseline: %v", v3)
	}
	arm := golang.BuildOptions{GOOS: "darwin", GOARCH: "arm64"}
	if !slices.Contains(arm.BuildEnv("local"), "GOARCH=arm64") || arm.Platform() != "darwin/arm64" {
		t.Errorf("an explicit platform was not honoured: %v", arm.BuildEnv("local"))
	}
}

// TestBinariesAreNamedTheWayGoNamesThem pins the names Build checks for. A
// wrong rule here fails every build that worked, or passes one that did not.
func TestBinariesAreNamedTheWayGoNamesThem(t *testing.T) {
	t.Parallel()
	o := golang.BuildOptions{Packages: []string{"./cmd/api", "./tools/gen/v2", "cmd/migrate/", "./cmd/tool/v1", "./x/v10"}, Output: "bin"}
	if got, want := o.Binaries(), []string{"api", "gen", "migrate", "v1", "x"}; !slices.Equal(got, want) {
		t.Errorf("Binaries = %v, want %v (v2 and up name the folder above; v0 and v1 are ordinary names)", got, want)
	}
	o.GOOS = "windows"
	if got := o.Binaries(); got[0] != "api.exe" {
		t.Errorf("Windows binaries = %v, want .exe names", got)
	}
}

// TestValidateRefusesWhatCannotBeChecked pins each refusal. Every one is a
// build that would otherwise delete the wrong folder, ship an unstamped
// binary, or pass without proving its output.
func TestValidateRefusesWhatCannotBeChecked(t *testing.T) {
	t.Parallel()
	base := func() golang.BuildOptions {
		return golang.BuildOptions{Packages: []string{"./cmd/api"}, Output: "bin"}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("the valid base fails: %v", err)
	}
	for name, mutate := range map[string]func(*golang.BuildOptions){
		"no packages":            func(o *golang.BuildOptions) { o.Packages = nil },
		"a pattern":              func(o *golang.BuildOptions) { o.Packages = []string{"./cmd/..."} },
		"the module root":        func(o *golang.BuildOptions) { o.Packages = []string{"."} },
		"two binaries, one name": func(o *golang.BuildOptions) { o.Packages = []string{"./a/api", "./b/api"} },
		"no output":              func(o *golang.BuildOptions) { o.Output = "" },
		"output is the project":  func(o *golang.BuildOptions) { o.Output = "." },
		"output escapes":         func(o *golang.BuildOptions) { o.Output = "../bin" },
		"output is absolute":     func(o *golang.BuildOptions) { o.Output = "/tmp/bin" },
		"a bare symbol":          func(o *golang.BuildOptions) { o.Vars = map[string]string{"Version": "1"} },
		"both quotes":            func(o *golang.BuildOptions) { o.Vars = map[string]string{"main.v": `a'b"c`} },
		"a newline":              func(o *golang.BuildOptions) { o.Vars = map[string]string{"main.v": "a\nb"} },
		"a second -ldflags":      func(o *golang.BuildOptions) { o.Extra = []string{"-ldflags=-s"} },
		"a second --ldflags":     func(o *golang.BuildOptions) { o.Extra = []string{"--ldflags", "-s"} },
		"a second -o":            func(o *golang.BuildOptions) { o.Extra = []string{"-o", "elsewhere"} },
		"undoing -trimpath":      func(o *golang.BuildOptions) { o.Extra = []string{"-trimpath=false"} },
		"a VCS stamp":            func(o *golang.BuildOptions) { o.Extra = []string{"-buildvcs=true"} },
		"output holds sources":   func(o *golang.BuildOptions) { o.Output = "cmd" },
		"output is the package":  func(o *golang.BuildOptions) { o.Output = "cmd/api" },
		"output is Cmd":          func(o *golang.BuildOptions) { o.Output = "Cmd" },
		"output inside .git":     func(o *golang.BuildOptions) { o.Output = ".git/bin" },
		"output inside .GIT":     func(o *golang.BuildOptions) { o.Output = ".GIT/bin" },
	} {
		o := base()
		mutate(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: Validate passed", name)
		}
	}
}

// TestAValueWithSpacesIsQuotedForTheLinker pins the quoting: the go command
// splits -ldflags on spaces, so an unquoted "built by ci" stamps "built" and
// passes "by" and "ci" to the linker as arguments.
func TestAValueWithSpacesIsQuotedForTheLinker(t *testing.T) {
	t.Parallel()
	args := golang.BuildOptions{
		Packages: []string{"./cmd/api"}, Output: "bin",
		Vars: map[string]string{"main.a": "built by ci", "main.b": "it's"},
	}.BuildArgs()
	ld := args[slices.Index(args, "-ldflags")+1]
	if !strings.Contains(ld, "-X 'main.a=built by ci'") || !strings.Contains(ld, `-X "main.b=it's"`) {
		t.Errorf("-ldflags = %s", ld)
	}
}

// ── toolchain ────────────────────────────────────────────────────────────

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadToolchain pins where the pin comes from. Not parallel: GOWORK is
// part of the answer, and t.Setenv cannot run beside parallel tests.
func TestReadToolchain(t *testing.T) {
	t.Setenv("GOWORK", "")

	t.Run("the workspace's toolchain line wins over the module", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "go.work"), "go 1.26.4\n\ntoolchain go1.26.5 // pinned\n\nuse ./svc\n")
		write(t, filepath.Join(root, "svc", "go.mod"), "module svc\n\ngo 1.25.0\n")
		got, err := golang.ReadToolchain(filepath.Join(root, "svc"))
		if err != nil || got != "go1.26.5" {
			t.Errorf("ReadToolchain = %q, %v; want go1.26.5 from the go.work above", got, err)
		}
	})
	t.Run("a complete go line is the implied toolchain", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "go.mod"), "module m\n\ngo 1.26.4\n")
		if got, err := golang.ReadToolchain(root); err != nil || got != "go1.26.4" {
			t.Errorf("ReadToolchain = %q, %v; want go1.26.4", got, err)
		}
	})
	t.Run("a language version alone pins nothing", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "go.mod"), "module m\n\ngo 1.24\n")
		_, err := golang.ReadToolchain(root)
		if !errors.Is(err, golang.ErrNoToolchain) || !strings.Contains(err.Error(), "go mod edit -toolchain") {
			t.Errorf("err = %v; want ErrNoToolchain naming the fix", err)
		}
	})
	t.Run("toolchain default pins nothing", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "go.work"), "go 1.24\n\ntoolchain default\n")
		_, err := golang.ReadToolchain(root)
		if !errors.Is(err, golang.ErrNoToolchain) || !strings.Contains(err.Error(), "go work edit") {
			t.Errorf("err = %v; want ErrNoToolchain naming go work edit", err)
		}
	})
	t.Run("a toolchain line older than a complete go line yields to it", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "go.mod"), "module m\n\ngo 1.26.4\n\ntoolchain go1.26.1\n")
		if got, err := golang.ReadToolchain(root); err != nil || got != "go1.26.4" {
			t.Errorf("ReadToolchain = %q, %v; want go1.26.4, the newer, as the go command runs", got, err)
		}
	})
	t.Run("no module at all", func(t *testing.T) {
		if _, err := golang.ReadToolchain(t.TempDir()); !errors.Is(err, golang.ErrNoToolchain) {
			t.Errorf("err = %v; want ErrNoToolchain", err)
		}
	})
}

// TestReadToolchainHonoursGOWORKOff pins that the answer follows the go
// command: with the workspace off, the module's line governs the build, so it
// must govern the pin too.
func TestReadToolchainHonoursGOWORKOff(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	write(t, filepath.Join(root, "go.work"), "go 1.26.4\n\ntoolchain go1.26.5\n")
	write(t, filepath.Join(root, "go.mod"), "module m\n\ngo 1.25.3\n")
	if got, err := golang.ReadToolchain(root); err != nil || got != "go1.25.3" {
		t.Errorf("ReadToolchain = %q, %v; want go1.25.3 from go.mod", got, err)
	}
}

// ── a real build ─────────────────────────────────────────────────────────

// module writes a small module with two main packages, one of them under a
// major-version folder, each printing the variable a stamp sets.
func module(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/stamp\n\ngo 1.24\n")
	const prog = "package main\n\nvar version = \"dev\"\n\nfunc main() { println(version) }\n"
	write(t, filepath.Join(root, "cmd", "hello", "main.go"), prog)
	write(t, filepath.Join(root, "tools", "gen", "v2", "main.go"), prog)
	return root
}

// host builds for this machine, so the test can run what it built, and under
// the installed Go, so it never downloads a toolchain.
//
// GOWORK=off so a developer's own GOWORK cannot pull the temporary module
// into an unrelated workspace.
func host(root string) golang.BuildOptions {
	return golang.BuildOptions{
		Dir: root, Packages: []string{"./cmd/hello", "./tools/gen/v2"}, Output: "out/bin",
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Toolchain: "local",
		Vars: map[string]string{"main.version": "stamped by ci"},
		Env:  []string{"GOWORK=off"},
	}
}

// TestBuildProducesStampedBinariesAndClearsStaleOnes runs the real compiler.
// A fake proves the command line; only a real build proves the go command
// reads it the same way: the folder form of -o, the binary names, and the
// quoted -X value actually reaching the variable.
func TestBuildProducesStampedBinariesAndClearsStaleOnes(t *testing.T) {
	t.Parallel()
	root := module(t)
	o := host(root)
	// A folder an earlier Build marked as its own, with a file in it that
	// this build does not produce.
	write(t, filepath.Join(root, "out", "bin", golang.MarkerFile), "")
	stale := filepath.Join(root, "out", "bin", "left-over")
	write(t, stale, "from an earlier run")

	if err := golang.Build(context.Background(), o, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a stale file survived the build: %v", err)
	}
	for _, name := range o.Binaries() {
		out, err := exec.Command(filepath.Join(root, "out", "bin", name)).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.TrimSpace(string(out)); got != "stamped by ci" {
			t.Errorf("%s printed %q; the -X stamp did not reach the variable", name, got)
		}
	}
}

// TestBuildFailureLeavesNoStaleBinary pins the reason Output is cleared: a
// build that fails must not leave the previous binary for the image to copy.
func TestBuildFailureLeavesNoStaleBinary(t *testing.T) {
	t.Parallel()
	root := module(t)
	o := host(root)
	if err := golang.Build(context.Background(), o, nil); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "cmd", "hello", "main.go"), "package main\n\nfunc main() { undefined() }\n")
	err := golang.Build(context.Background(), o, nil)
	if err == nil {
		t.Fatal("a build that does not compile succeeded")
	}
	if !strings.Contains(err.Error(), "undefined") {
		t.Errorf("the error does not quote the compiler: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "bin", o.Binaries()[0])); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the previous binary survived a failed build: %v", err)
	}
}

// TestBuildProvesEveryBinaryWasProduced pins the final check. -n makes go
// print the build and exit zero having written nothing, which is exactly the
// "succeeded, produced nothing" case the check exists for.
func TestBuildProvesEveryBinaryWasProduced(t *testing.T) {
	t.Parallel()
	o := host(module(t))
	o.Extra = []string{"-n"}
	err := golang.Build(context.Background(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "did not produce hello, gen") {
		t.Errorf("err = %v; want both missing binaries named", err)
	}
}

// TestBuildRefusesToDeleteAFolderItDidNotWrite pins the guard on the one
// destructive thing Build does. A bin/ holding a tracked script is not undone
// by a rebuild, so a folder with anything Build would not have written in it
// is refused, before anything is deleted.
func TestBuildRefusesToDeleteAFolderItDidNotWrite(t *testing.T) {
	t.Parallel()
	root := module(t)
	o := host(root)
	script := filepath.Join(root, "out", "bin", "release.sh")
	write(t, script, "#!/bin/sh\n")
	err := golang.Build(context.Background(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "release.sh") {
		t.Fatalf("err = %v; want a refusal naming the foreign file", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Errorf("the foreign file was deleted: %v", err)
	}
}

// TestBuildTakesOverAFolderOfItsOwnBinaries pins the upgrade path: a folder
// holding only the binaries this build produces (from a hand-written build of
// the same packages) has no marker yet and is still cleared.
func TestBuildTakesOverAFolderOfItsOwnBinaries(t *testing.T) {
	t.Parallel()
	root := module(t)
	o := host(root)
	for _, name := range o.Binaries() {
		write(t, filepath.Join(root, "out", "bin", name), "old binary")
	}
	if err := golang.Build(context.Background(), o, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "bin", golang.MarkerFile)); err != nil {
		t.Errorf("the folder was not marked as Build's own: %v", err)
	}
}

// TestReadToolchainRefusesARelativeGOWORK pins that a GOWORK the go command
// would refuse is refused here too, rather than resolved from wherever lath
// happens to run.
func TestReadToolchainRefusesARelativeGOWORK(t *testing.T) {
	t.Setenv("GOWORK", "go.work")
	if _, err := golang.ReadToolchain(t.TempDir()); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("err = %v", err)
	}
}

// TestPinnedToolchainFollowsTheBuildsGOWORK pins that the pin is read from
// the file the BUILD reads. With GOWORK=off in Env the go command builds the
// module alone, so pinning the workspace's Go would build with a Go the
// module never named.
func TestPinnedToolchainFollowsTheBuildsGOWORK(t *testing.T) {
	t.Setenv("GOWORK", "")
	root := t.TempDir()
	write(t, filepath.Join(root, "go.work"), "go 1.26.4\n\ntoolchain go1.26.5\n")
	write(t, filepath.Join(root, "go.mod"), "module m\n\ngo 1.25.3\n")
	off := golang.BuildOptions{Env: []string{"GOWORK=off"}}
	if got, err := off.PinnedToolchain(root); err != nil || got != "go1.25.3" {
		t.Errorf("with GOWORK=off in Env: %q, %v; want go1.25.3 from go.mod", got, err)
	}
	if got, err := (golang.BuildOptions{}).PinnedToolchain(root); err != nil || got != "go1.26.5" {
		t.Errorf("without: %q, %v; want go1.26.5 from go.work", got, err)
	}
	if got, _ := (golang.BuildOptions{Toolchain: "go1.24.0"}).PinnedToolchain(root); got != "go1.24.0" {
		t.Errorf("an explicit Toolchain = %q; want it unchanged", got)
	}
}
