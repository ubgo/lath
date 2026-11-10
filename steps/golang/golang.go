// Package golang adapts Go builds to the pipeline.
//
// A thin wrapper over kit/golang, which any Go program can use without a
// pipeline: this package adds a name for the plan, the state the build reads,
// and dry-run behaviour.
package golang

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	golangkit "github.com/ubgo/lath/kit/golang"
	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/pipeline"
)

// Build compiles Go binaries on this machine, for an image to copy.
//
// It is the half of a thin-image build that docker cannot do quickly: a go
// build inside a fresh BuildKit builder starts from an empty build cache, so
// it recompiles everything on every deploy. Here the machine's (or the CI
// cache's) build cache carries over. A docker.Build after it then only copies
// Options.Output into the image. See kit/golang for what the build guarantees:
// a pinned toolchain, a cleared output folder, every binary proven present.
//
// Requires whatever VarsFromState reads, and provides nothing: its product is
// a folder on disk, which the image build reads by path.
type Build struct {
	// Options is everything kit/golang accepts, passed through: Packages,
	// Output, GOOS and GOARCH, Toolchain, Vars, Strip, and the Extra escape
	// hatch. Embedded rather than mirrored for the same reason as
	// docker.Build.Options: a mirrored field list would lag every addition
	// to the library.
	//
	// An empty Options.Dir means the project root (pipeline.ProjectRoot), the
	// directory that holds .lath, so Packages and Output read the way they
	// do in the repository.
	Options golangkit.BuildOptions
	// VarsFromState binds -X stamps to pipeline state: the key is the
	// variable (golangkit.BuildinfoCommit), the value the state key holding
	// what to stamp (common.KeyCommit).
	//
	// The same mechanism as docker.Build.ArgsFromState, for the same reason:
	// the commit and branch are known only once an earlier step has read
	// them, and a binding declared as data joins Requires, so a missing
	// producer fails validation instead of shipping a binary that reports
	// "dev". These override Options.Vars on a name collision.
	VarsFromState map[string]pipeline.Key
	// BuildTimeVar is a variable to stamp with the time of the build, UTC,
	// RFC 3339 (golangkit.BuildinfoBuildTime). Empty stamps none.
	//
	// Read from the clock at run time rather than from state, because it is
	// the one stamp no earlier step can know. It overrides Options.Vars and
	// VarsFromState for the same variable.
	BuildTimeVar string
}

// now is the clock BuildTimeVar reads. A var so a test can pin it.
var now = time.Now

func (Build) Name() string { return "build-binaries" }

// Docs explains the step in a plan. See pipeline.Documented.
func (b Build) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "compile the Go binaries on this machine, reusing its build cache",
		Detail: fmt.Sprintf("%s for %s into %s/",
			strings.Join(b.Options.Packages, " "), b.Options.Platform(), b.Options.Output),
	}
}

// Requires is every state key VarsFromState reads, deduped and in a stable
// order.
func (b Build) Requires() []pipeline.Key {
	var keys []pipeline.Key
	seen := map[pipeline.Key]bool{}
	for _, sym := range sortedNames(b.VarsFromState) {
		if key := b.VarsFromState[sym]; !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

func (Build) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Same source, same binaries; the build cache makes the
// repeat cheap.
func (Build) Replayable() bool { return true }

// Validate checks the author-supplied configuration, before anything runs.
func (b Build) Validate() error {
	if err := b.Options.Validate(); err != nil {
		return err
	}
	for _, sym := range sortedNames(b.VarsFromState) {
		if b.VarsFromState[sym] == "" {
			return fmt.Errorf("VarsFromState[%q] has no state key", sym)
		}
	}
	// The state values are checked when they are known, at run time; the
	// variable names can be checked now, with a placeholder value.
	probe := golangkit.BuildOptions{Packages: b.Options.Packages, Output: b.Options.Output, Vars: map[string]string{}}
	for sym := range b.VarsFromState {
		probe.Vars[sym] = "x"
	}
	if b.BuildTimeVar != "" {
		probe.Vars[b.BuildTimeVar] = "x"
	}
	return probe.Validate()
}

func (b Build) Run(ctx context.Context, s *pipeline.State) error {
	opts := b.Options
	// Copied rather than mutated: Options belongs to the caller, and a step
	// that edited it in place would leak one run's commit into the next.
	vars := make(map[string]string, len(opts.Vars)+len(b.VarsFromState)+1)
	for k, v := range opts.Vars {
		vars[k] = v
	}
	for _, sym := range sortedNames(b.VarsFromState) {
		value, err := pipeline.Get[string](s, b.VarsFromState[sym])
		if err != nil {
			return fmt.Errorf("build-binaries: %s: %w", sym, err)
		}
		vars[sym] = value
	}
	if b.BuildTimeVar != "" {
		vars[b.BuildTimeVar] = now().UTC().Format(time.RFC3339)
	}
	opts.Vars = vars
	if opts.Dir == "" {
		root, err := pipeline.ProjectRoot()
		if err != nil {
			return fmt.Errorf("build-binaries: %w", err)
		}
		opts.Dir = root
	}
	if err := opts.Validate(); err != nil {
		// The state values are only now known; one may not render.
		return fmt.Errorf("build-binaries: %w", err)
	}
	// Read under dry run too: it touches nothing, and a rehearsal that skipped
	// it would pass on a repository the real build refuses.
	toolchain, err := opts.PinnedToolchain(opts.Dir)
	if err != nil {
		return fmt.Errorf("build-binaries: %w", err)
	}
	opts.Toolchain = toolchain
	// Also read-only, and refused the same way the real build would refuse it.
	if err := opts.CheckOutput(opts.Dir); err != nil {
		return fmt.Errorf("build-binaries: %w", err)
	}
	line := strings.Join(opts.BuildEnv(toolchain), " ") + " " +
		proc.CommandLine(golangkit.Program, opts.BuildArgs()...)
	if s.DryRun() {
		s.Detailf("would run in %s: %s", opts.Dir, line)
		return nil
	}
	s.Detailf("%s", line)
	out := s.Output()
	defer func() { _ = out.Close() }()
	if err := golangkit.Build(ctx, opts, out); err != nil {
		return fmt.Errorf("build-binaries: %w", err)
	}
	s.Detailf("built %s into %s", strings.Join(opts.Binaries(), ", "), filepath.ToSlash(opts.Output))
	return nil
}

func sortedNames(m map[string]pipeline.Key) []string {
	// Map order is random, and it reaches Requires and every plan.
	return slices.Sorted(maps.Keys(m))
}
