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

	golangkit "github.com/ubgo/lath/kit/golang"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/golang"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project writes a module whose one binary prints the three stamps, and names
// the Go that built this test as its toolchain, so the step reads a real pin
// and the build never downloads one.
func project(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	if !strings.HasPrefix(runtime.Version(), "go1.") {
		t.Skipf("a development Go (%s) names no release to pin", runtime.Version())
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/svc\n\ngo 1.24\n\ntoolchain "+runtime.Version()+"\n")
	write(t, filepath.Join(root, "cmd", "api", "main.go"), `package main

var commit, branch, built = "unknown", "unknown", "unknown"

func main() { println(commit, branch, built) }
`)
	return root
}

func step(root string) golang.Build {
	return golang.Build{
		Options: golangkit.BuildOptions{
			Dir: root, Packages: []string{"./cmd/api"}, Output: "bin",
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
			Vars: map[string]string{"main.commit": "overridden"},
			// A developer's own GOWORK must not pull the temporary module
			// into an unrelated workspace.
			Env: []string{"GOWORK=off"},
		},
		VarsFromState: map[string]pipeline.Key{"main.commit": common.KeyCommit, "main.branch": common.KeyBranch},
		BuildTimeVar:  "main.built",
	}
}

func state(mode pipeline.Mode) *pipeline.State {
	s := pipeline.NewState(mode, nil)
	pipeline.Set(s, common.KeyCommit, "a3f1c2d")
	pipeline.Set(s, common.KeyBranch, "main")
	return s
}

// TestBuildStampsFromState runs the real compiler through the step: the
// values earlier steps resolved reach the binary, state wins over a static
// Var of the same name, and the build time is the step's clock.
func TestBuildStampsFromState(t *testing.T) {
	t.Parallel()
	root := project(t)
	b := step(root)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := b.Run(context.Background(), state(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(root, "bin", b.Options.Binaries()[0])).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 3 || fields[0] != "a3f1c2d" || fields[1] != "main" || !strings.HasSuffix(fields[2], "Z") {
		t.Errorf("the binary reports %q; want the commit, the branch and a UTC time", out)
	}
	// Options is the caller's: a second pipeline built from the same value
	// must not inherit this run's commit.
	if b.Options.Vars["main.commit"] != "overridden" || len(b.Options.Vars) != 1 {
		t.Errorf("Run mutated the caller's Vars: %v", b.Options.Vars)
	}
}

// TestDryRunBuildsNothingButStillReadsTheToolchain pins the dry-run contract
// both ways: nothing is written, and a repository the real build would refuse
// is refused here too, so the rehearsal proves something.
func TestDryRunBuildsNothingButStillReadsTheToolchain(t *testing.T) {
	t.Parallel()
	root := project(t)
	if err := step(root).Run(context.Background(), state(pipeline.ModeDryRun)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dry run created the output folder: %v", err)
	}

	write(t, filepath.Join(root, "go.mod"), "module example.com/svc\n\ngo 1.24\n")
	err := step(root).Run(context.Background(), state(pipeline.ModeDryRun))
	if !errors.Is(err, golangkit.ErrNoToolchain) {
		t.Errorf("err = %v; want ErrNoToolchain under dry run as well", err)
	}
}

// TestRequiresIsWhatVarsFromStateReads pins the wiring Validate relies on: a
// stamp bound to a key no step provides must fail validation, not ship "dev".
func TestRequiresIsWhatVarsFromStateReads(t *testing.T) {
	t.Parallel()
	b := golang.Build{
		Options: golangkit.BuildOptions{Packages: []string{"./cmd/api"}, Output: "bin"},
		VarsFromState: map[string]pipeline.Key{
			golangkit.BuildinfoVersion: common.KeyBranch,
			golangkit.BuildinfoBranch:  common.KeyBranch,
			golangkit.BuildinfoCommit:  common.KeyCommit,
		},
	}
	if got, want := b.Requires(), []pipeline.Key{common.KeyBranch, common.KeyCommit}; !slices.Equal(got, want) {
		t.Errorf("Requires = %v, want %v (deduped, stable)", got, want)
	}
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{b}}
	if err := p.Validate(); err == nil {
		t.Error("a pipeline with no producer for the stamps validated")
	}
}

// TestValidateChecksTheBoundNames pins that a malformed variable name is
// caught at validation, before the run, even though its value comes later.
func TestValidateChecksTheBoundNames(t *testing.T) {
	t.Parallel()
	base := golangkit.BuildOptions{Packages: []string{"./cmd/api"}, Output: "bin"}
	for name, b := range map[string]golang.Build{
		"a bare name":     {Options: base, VarsFromState: map[string]pipeline.Key{"Commit": common.KeyCommit}},
		"an unbound name": {Options: base, VarsFromState: map[string]pipeline.Key{"main.commit": ""}},
		"a bad time name": {Options: base, BuildTimeVar: "built"},
		"invalid options": {Options: golangkit.BuildOptions{Output: "bin"}},
	} {
		if b.Validate() == nil {
			t.Errorf("%s: Validate passed", name)
		}
	}
}

// TestTheStepIsNamedAndDocumented pins the plan's view of the step: its name
// and a summary that sits in a column beside it.
func TestTheStepIsNamedAndDocumented(t *testing.T) {
	t.Parallel()
	b := step("/x")
	if b.Name() != "build-binaries" {
		t.Errorf("Name = %q", b.Name())
	}
	docs := b.Docs()
	if docs.Summary == "" || strings.HasSuffix(docs.Summary, ".") {
		t.Errorf("Summary = %q", docs.Summary)
	}
	if !strings.Contains(docs.Detail, "./cmd/api") || !strings.Contains(docs.Detail, "bin/") {
		t.Errorf("Detail = %q", docs.Detail)
	}
	if !b.Replayable() {
		t.Error("a rebuild of the same source is not replayable")
	}
}
