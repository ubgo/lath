// Package gitstep adapts git operations to the pipeline.
//
// Thin wrappers: they read what they need from pipeline state, honour dry run,
// report progress, and delegate the work to the git package, which any Go
// program can use without a pipeline at all.
package git

import (
	"context"
	"fmt"

	gitkit "github.com/ubgo/lath/kit/git"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// ResolveCommit records which commit is being deployed.
//
// Carried through state rather than recomputed, so every later step stamps the
// same value: two calls to git during one deploy could straddle a commit and
// produce an image whose tag does not match its contents.
type ResolveCommit struct {
	// Dir is the repository to inspect. Empty means the working directory.
	Dir string
	// AllowDirty permits deploying with uncommitted changes. See
	// gitkit.Client.RequireClean for why this is off by default.
	AllowDirty bool
	// ShortLength is how many hex characters name the commit. Zero means
	// gitkit.DefaultShortLength.
	//
	// Settable because a monorepo large enough to make seven ambiguous needs
	// more, and discovering that as a mysterious tag collision is worse than
	// having the field.
	ShortLength int
	// Extra passes raw flags to every git invocation, "-c",
	// "safe.directory=*" for a repository owned by another user.
	Extra []string
	// Runner is where git runs. Nil means this machine, which is almost always
	// right. The repository is here, not on the deploy target.
	Runner runner.Runner
}

func (ResolveCommit) Name() string { return "resolve-commit" }

// Docs explains the step in a plan. See pipeline.Documented.
func (ResolveCommit) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "read the commit and branch once, so every later stamp agrees",
	}
}
func (ResolveCommit) Requires() []pipeline.Key { return nil }
func (ResolveCommit) Provides() []pipeline.Key {
	return []pipeline.Key{common.KeyCommit, common.KeyBranch}
}

// Replayable reports that re-running this step is indistinguishable from
// running it once. Reads git; the same tree yields the same commit.
func (ResolveCommit) Replayable() bool { return true }

// Run reads the commit and, unless AllowDirty, refuses an unclean tree.
//
// Read-only, so it behaves identically under dry run, and it must, because
// every later step's description depends on the tag it produces.
func (c ResolveCommit) Run(ctx context.Context, s *pipeline.State) error {
	client := gitkit.On(c.Runner, c.Dir).WithFlags(c.Extra...)

	commit, err := client.ShortCommit(ctx, c.ShortLength)
	if err != nil {
		return fmt.Errorf("resolve-commit: %w", err)
	}
	if !c.AllowDirty {
		if err := client.RequireClean(ctx); err != nil {
			return fmt.Errorf("resolve-commit: %w (set AllowDirty to deploy anyway)", err)
		}
	}

	// Read after the cleanliness check so a refused deploy does not spend an
	// extra git invocation, and from the same client so both values describe
	// one working tree.
	branch, err := client.Branch(ctx)
	if err != nil {
		return fmt.Errorf("resolve-commit: %w", err)
	}

	pipeline.Set(s, common.KeyCommit, commit)
	pipeline.Set(s, common.KeyBranch, branch)
	s.Detailf("commit=%s branch=%s", commit, branch)
	return nil
}
