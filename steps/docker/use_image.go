package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"

	dockerkit "github.com/ubgo/lath/kit/docker"

	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// UseImage names an image that already exists, so a pipeline can ship it
// without building it.
//
// Why it exists: Build is otherwise the only producer of common.KeyImage,
// which quietly means every pipeline that deploys must first build. That is
// wrong for the cases where the artifact is the whole point of not
// rebuilding, rolling back to the tag that was running yesterday, promoting
// the exact image staging approved, redeploying after a host was rebuilt. A
// rebuild in those situations does not reproduce the artifact, it produces a
// new one from whatever the build inputs resolve to today, and calling that a
// rollback is a lie.
//
// It deliberately does not decide WHICH tag: see Tag and TagFrom. "The
// previous one" means the last deployed commit in one project and the last
// tag that passed a health check in another, and a step that picked would be
// picking for both.
type UseImage struct {
	// Repository is the image name without a tag, matching Build.Repository.
	Repository string
	// Tag is the version to deploy, when the caller knows it at the time the
	// pipeline is assembled (a command-line argument, typically).
	//
	// Exactly one of Tag or TagFrom must be set.
	Tag string
	// TagFrom reads the tag from pipeline state instead, for when an earlier
	// step decides it.
	//
	// Why both: choosing a tag is project policy. A definition that wants
	// "the newest tag on the box that is not running" writes that step itself
	// and points TagFrom at its output; one that takes the tag from the user
	// sets Tag. Either way the choice is stated where the project can see it
	// rather than buried in this step.
	TagFrom pipeline.Key
	// MustExist verifies the tag is present on Runner before the pipeline
	// commits to it, and on failure reports the tags that ARE present.
	//
	// Why it is worth a round trip: without it a mistyped or pruned tag fails
	// several steps later inside docker pull or docker run, as a registry
	// error that says nothing about what could have been used instead. The
	// answer a caller needs at that moment is the list of candidates, and
	// this is the only step positioned to give it.
	//
	// Skipped under dry run, which must not require the host to be reachable.
	MustExist bool
	// Runner is where MustExist looks. Nil means this machine.
	Runner runner.Runner
}

func (UseImage) Name() string { return "use-image" }

// Docs explains the step in a plan. See pipeline.Documented.
func (u UseImage) Docs() pipeline.Docs {
	tag := u.Tag
	if tag == "" {
		// The key's name stands in for a value only the run will know. Angle
		// brackets because a plan is read by a person: "<rollback.target>"
		// reads as a placeholder, a bare key name reads as a literal tag.
		tag = fmt.Sprintf("<%s>", u.TagFrom)
	}
	return pipeline.Docs{
		Summary: "deploy an image that already exists, without building it",
		Detail:  dockerkit.Reference(u.Repository, tag),
	}
}

// Requires is the state key holding the tag, when the tag comes from state.
func (u UseImage) Requires() []pipeline.Key {
	if u.TagFrom == "" {
		return nil
	}
	return []pipeline.Key{u.TagFrom}
}

func (UseImage) Provides() []pipeline.Key { return []pipeline.Key{common.KeyImage} }

// Replayable reports that re-running this step is indistinguishable from
// running it once. It names an image; naming it twice names the same one.
func (UseImage) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (u UseImage) Validate() error {
	if u.Repository == "" {
		return fmt.Errorf("Repository is required")
	}
	switch {
	case u.Tag == "" && u.TagFrom == "":
		return fmt.Errorf("one of Tag or TagFrom is required")
	case u.Tag != "" && u.TagFrom != "":
		// Refused rather than ranked: a caller who set both has two answers in
		// mind and no way to know which one this step would honour.
		return fmt.Errorf("Tag and TagFrom are alternatives; set exactly one")
	}
	return nil
}

func (u UseImage) Run(ctx context.Context, s *pipeline.State) error {
	tag := u.Tag
	if u.TagFrom != "" {
		var err error
		if tag, err = pipeline.Get[string](s, u.TagFrom); err != nil {
			return fmt.Errorf("use-image: %w", err)
		}
		if tag == "" {
			return fmt.Errorf("use-image: %s is empty; nothing to deploy", u.TagFrom)
		}
	}
	image := dockerkit.Reference(u.Repository, tag)

	if u.MustExist && !s.DryRun() {
		client, done := newClient(u.Runner, s)
		defer done()
		tags, err := client.Images(ctx, u.Repository)
		if err != nil {
			return fmt.Errorf("use-image: listing %s %s: %w", u.Repository, client.Where().Describe(), err)
		}
		if !slices.Contains(tags, tag) {
			return fmt.Errorf("use-image: %s is not %s; available: %s",
				image, client.Where().Describe(), available(tags))
		}
	}

	// Set even under dry run: a later step reading the image must see one, or
	// the rehearsal fails for a reason the real run would not have. See
	// pipeline.State.DryRun.
	pipeline.Set(s, common.KeyImage, image)
	s.Detailf("using %s", image)
	return nil
}

// available renders the candidate tags for an error message.
//
// The empty case gets prose rather than an empty list, because "available: "
// followed by nothing reads like the message was truncated when in fact it is
// the finding: the repository has no images here at all, which points at a
// different problem than a wrong tag.
func available(tags []string) string {
	if len(tags) == 0 {
		return "none (no images for this repository)"
	}
	return strings.Join(tags, ", ")
}
