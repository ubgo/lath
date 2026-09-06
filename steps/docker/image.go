package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// Build produces the deployable artifact.
//
// Requires common.KeyCommit so the build is stamped; provides common.KeyImage for everything
// that ships or runs it. Under dry run it still provides common.KeyImage, see
// pipeline.State.DryRun for why skipping the Set would be wrong.
type Build struct {
	// Repository is the image name without a tag; the commit supplies the tag.
	//
	// This is the one build setting the STEP owns, because the tag is derived
	// from pipeline state rather than chosen by the caller.
	Repository string
	// Options is everything else docker build accepts, passed through
	// untouched, Dockerfile, Context, Platforms, Args, Target, Labels,
	// NoCache, Pull, and the Extra escape hatch.
	//
	// Embedded rather than mirrored field by field. A step that re-declared
	// each option would silently lag every addition to the library, and the
	// caller would have no way to reach the new one short of abandoning the
	// step. Options.Tag is overwritten; everything else is the caller's.
	Options dockerkit.BuildOptions
	// ArgsFromState binds build arguments to pipeline state: the key is the
	// --build-arg name, the value is the state key holding its value.
	//
	// Why it exists: the values worth stamping into an image, the commit, the
	// branch, the target environment, are produced by earlier steps at run
	// time, so they cannot be written into Options.Args, which is fixed when
	// the pipeline is assembled. Without this a build silently ships an
	// unstamped binary: the Dockerfile's ARG defaults win, and the result
	// reports itself as "dev"/"unknown" with nothing failing to say so.
	//
	// Declared as keys rather than a callback so the bindings are data: they
	// join Requires, which means a missing producer is a validation error
	// before anything builds rather than a failure minutes in.
	//
	// Invariant: these override Options.Args on a name collision, a value
	// resolved from state is by definition the more specific one.
	ArgsFromState map[string]pipeline.Key
	// Runner is where the build runs. Nil means this machine.
	Runner runner.Runner
}

func (Build) Name() string { return "build-image" }

// Docs explains the step in a plan. See pipeline.Documented.
func (b Build) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "build the image and tag it with the commit",
		Detail:  fmt.Sprintf("%s from %s", b.Repository, b.Options.Dockerfile),
	}
}

// Requires is the commit plus every state key ArgsFromState reads, so the
// pipeline can prove those producers run first.
func (b Build) Requires() []pipeline.Key {
	keys := []pipeline.Key{common.KeyCommit}
	// Deduped: two build args may legitimately read the same key, a version
	// and a branch are often the same value, and a repeated Requires entry
	// would show up in every plan as a doubled read for no reason.
	seen := map[pipeline.Key]bool{common.KeyCommit: true}
	for _, name := range sortedArgNames(b.ArgsFromState) {
		key := b.ArgsFromState[name]
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}
func (Build) Provides() []pipeline.Key { return []pipeline.Key{common.KeyImage} }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Rebuilds the same tag from the same context, and layer caching
// makes the repeat cheap. The image that results is the image that was
// already there.
func (Build) Replayable() bool { return true }

// sortedArgNames returns the build-arg names in a stable order.
//
// Map iteration order is random, and it reaches both the Requires list and the
// generated command line; an unstable order would make plans and error
// messages differ between identical runs for no reason.
func sortedArgNames(m map[string]pipeline.Key) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate checks the author-supplied configuration.
func (b Build) Validate() error {
	if b.Repository == "" {
		return fmt.Errorf("Repository is required")
	}
	if b.Options.Context == "" {
		return fmt.Errorf("Options.Context is required")
	}
	for _, name := range sortedArgNames(b.ArgsFromState) {
		if b.ArgsFromState[name] == "" {
			return fmt.Errorf("ArgsFromState[%q] has no state key", name)
		}
	}
	if b.Options.Tag != "" {
		// Refused rather than ignored: a caller who set it expects it to be
		// used, and silently overwriting it would produce an image tagged
		// something they never asked for.
		return fmt.Errorf("Options.Tag is derived from Repository and the commit; leave it empty")
	}
	return nil
}

func (b Build) Run(ctx context.Context, s *pipeline.State) error {
	commit, err := pipeline.Get[string](s, common.KeyCommit)
	if err != nil {
		return fmt.Errorf("build-image: %w", err)
	}
	// Tagged by commit, never by a moving tag alone: a moving tag makes "which
	// image is running" unanswerable after the fact, and rollback a guess.
	image := dockerkit.Reference(b.Repository, commit)
	pipeline.Set(s, common.KeyImage, image)

	opts := b.Options
	opts.Tag = image
	if len(b.ArgsFromState) > 0 {
		// Copied rather than mutated: Options belongs to the caller, and a step
		// that edited it in place would leak values into a second run of the
		// same pipeline value.
		args := make(map[string]string, len(opts.Args)+len(b.ArgsFromState))
		for k, v := range opts.Args {
			args[k] = v
		}
		for _, name := range sortedArgNames(b.ArgsFromState) {
			value, err := pipeline.Get[string](s, b.ArgsFromState[name])
			if err != nil {
				return fmt.Errorf("build-image: build arg %s: %w", name, err)
			}
			args[name] = value
		}
		opts.Args = args
	}
	client, done := newClient(b.Runner, s)
	defer done()
	if s.DryRun() {
		s.Detailf("would run %s: %s", client.Where().Describe(),
			proc.CommandLine(dockerkit.Program, opts.BuildArgs()...))
		return nil
	}
	s.Detailf("building %s %s", image, client.Where().Describe())
	if err := client.Build(ctx, opts); err != nil {
		return err
	}
	s.Detailf("built %s", image)
	return nil
}

// Push publishes the built image to its registry.
//
// Separate from Build because they fail for unrelated reasons, a build
// failure is the code, a push failure is credentials or the network, and
// because a local rehearsal wants the build without the publish.
type Push struct {
	// Attempts defaults to DefaultRegistryAttempts.
	Attempts int
	// Backoff and MaxBackoff override the retry schedule; zero means the
	// package defaults.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// Extra passes raw docker push flags through.
	Extra []string
	// Runner is where the push runs. Nil means this machine.
	Runner runner.Runner
}

func (Push) Name() string { return "push-image" }

// Docs explains the step in a plan. See pipeline.Documented.
func (Push) Docs() pipeline.Docs {
	return pipeline.Docs{Summary: "publish the image to its registry"}
}
func (Push) Requires() []pipeline.Key { return []pipeline.Key{common.KeyImage} }
func (Push) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Pushes the same tag to the same registry; the second push is a
// no-op against an identical digest.
func (Push) Replayable() bool { return true }

func (p Push) Run(ctx context.Context, s *pipeline.State) error {
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		return fmt.Errorf("push-image: %w", err)
	}
	client := dockerkit.On(p.Runner)
	if s.DryRun() {
		s.Detailf("would push %s %s", image, client.Where().Describe())
		return nil
	}
	s.Detailf("pushing %s", image)
	return retryRegistry(ctx, s, retrySpec{attempts: p.Attempts, backoff: p.Backoff, max: p.MaxBackoff}, "push-image", func(ctx context.Context) error {
		return client.Push(ctx, image, p.Extra...)
	})
}

// Pull fetches the image where it will run.
//
// Kept distinct from Start so a slow pull is reported as a slow pull
// rather than a container that would not start.
type Pull struct {
	// Attempts defaults to DefaultRegistryAttempts.
	Attempts int
	// Backoff and MaxBackoff override the retry schedule; zero means the
	// package defaults.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// Extra passes raw docker pull flags through, e.g. "--platform=linux/amd64".
	Extra []string
	// Runner is where the pull runs, normally the deploy target.
	Runner runner.Runner
}

func (Pull) Name() string { return "pull-image" }

// Docs explains the step in a plan. See pipeline.Documented.
func (Pull) Docs() pipeline.Docs {
	return pipeline.Docs{Summary: "fetch the image onto the target"}
}
func (Pull) Requires() []pipeline.Key { return []pipeline.Key{common.KeyImage} }
func (Pull) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Fetches an image already present.
func (Pull) Replayable() bool { return true }

func (p Pull) Run(ctx context.Context, s *pipeline.State) error {
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		return fmt.Errorf("pull-image: %w", err)
	}
	client := dockerkit.On(p.Runner)
	if s.DryRun() {
		s.Detailf("would pull %s %s", image, client.Where().Describe())
		return nil
	}
	s.Detailf("pulling %s %s", image, client.Where().Describe())
	return retryRegistry(ctx, s, retrySpec{attempts: p.Attempts, backoff: p.Backoff, max: p.MaxBackoff}, "pull-image", func(ctx context.Context) error {
		return client.Pull(ctx, image, p.Extra...)
	})
}

// Prune reclaims disk from whatever its Options target: dangling images by
// default, but equally volumes, networks, containers or the whole system.
//
// The step is named for the mechanism, and so is its label: it reports
// "prune-volume" when it prunes volumes. It said "prune-images" whatever it
// was given until that was noticed, which is the kind of untruth a plan is
// read to avoid.
type Prune struct {
	// Options is what to reclaim, Target, All, Filter, and the Extra escape
	// hatch. The zero value prunes dangling images, which is the safe default.
	Options dockerkit.PruneOptions
	// Runner is where the prune happens. Nil means this machine.
	Runner runner.Runner
}

// Docs explains the step in a plan. See pipeline.Documented.
func (p Prune) Docs() pipeline.Docs {
	scope := "dangling only"
	if p.Options.All {
		scope = "everything unused"
	}
	detail := scope
	if len(p.Options.Filter) > 0 {
		detail += ", filtered by " + strings.Join(p.Options.Filter, " ")
	}
	return pipeline.Docs{
		Summary: "reclaim disk from what the deploy left behind",
		Detail:  detail,
	}
}

// Name reports what will actually be pruned, e.g. "prune-image".
func (p Prune) Name() string {
	target := p.Options.Target
	if target == "" {
		target = dockerkit.DefaultPruneTarget
	}
	return "prune-" + target
}

func (Prune) Requires() []pipeline.Key { return nil }
func (Prune) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Removes dangling images. Running it again removes whatever is
// dangling then, which is the same request, not a compounding one.
func (Prune) Replayable() bool { return true }

func (p Prune) Run(ctx context.Context, s *pipeline.State) error {
	client := dockerkit.On(p.Runner)
	if s.DryRun() {
		s.Detailf("would run %s: %s", client.Where().Describe(),
			proc.CommandLine(dockerkit.Program, p.Options.PruneArgs()...))
		return nil
	}
	summary, err := client.Prune(ctx, p.Options)
	if err != nil {
		return err
	}
	s.Detailf("pruned: %s", summary)
	return nil
}
