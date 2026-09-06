package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/docker"
)

const (
	// repo and the tags a rollback would be choosing between.
	repo       = "ghcr.io/acme/app"
	runningTag = "a3f1c2d"
	priorTag   = "0375fcf"
)

// errNoHost stands in for an unreachable machine, so a dry-run test fails
// loudly if the step reaches out at all.
var errNoHost = errors.New("the host is unreachable")

// TestUseImageLetsAPipelineShipWithoutBuilding is the reason the step exists:
// Build was the only producer of KeyImage, so every deploy had to rebuild,
// which for a rollback produces a new artifact rather than the old one.
func TestUseImageLetsAPipelineShipWithoutBuilding(t *testing.T) {
	t.Parallel()
	s := newState(pipeline.ModeExecute, nil)

	step := docker.UseImage{Repository: repo, Tag: priorTag}
	if err := step.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	got, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		t.Fatal(err)
	}
	if want := repo + ":" + priorTag; got != want {
		t.Errorf("image = %q, want %q", got, want)
	}
}

// TestUseImageTakesTheTagFromState covers the other half of the policy split:
// a project step decides which version "previous" means and this step ships
// whatever it decided.
func TestUseImageTakesTheTagFromState(t *testing.T) {
	t.Parallel()
	const keyTarget pipeline.Key = "rollback.target"
	s := newState(pipeline.ModeExecute, map[pipeline.Key]any{keyTarget: priorTag})

	step := docker.UseImage{Repository: repo, TagFrom: keyTarget}
	if err := step.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	got, _ := pipeline.Get[string](s, common.KeyImage)
	if want := repo + ":" + priorTag; got != want {
		t.Errorf("image = %q, want %q", got, want)
	}
	if reqs := step.Requires(); len(reqs) != 1 || reqs[0] != keyTarget {
		t.Errorf("Requires = %v; want the tag's producer to be provable", reqs)
	}
}

// TestUseImageRefusesAnEmptyTagFromState. An empty key would otherwise build
// the name "repo:" and fail much later as an opaque docker error.
func TestUseImageRefusesAnEmptyTagFromState(t *testing.T) {
	t.Parallel()
	const keyTarget pipeline.Key = "rollback.target"
	s := newState(pipeline.ModeExecute, map[pipeline.Key]any{keyTarget: ""})

	err := docker.UseImage{Repository: repo, TagFrom: keyTarget}.Run(context.Background(), s)
	if err == nil {
		t.Fatal("an empty tag was accepted")
	}
	if !strings.Contains(err.Error(), "nothing to deploy") {
		t.Errorf("err = %v; want it to say the tag was empty", err)
	}
}

// TestUseImageNamesTheCandidatesWhenTheTagIsMissing. The answer a caller needs
// when a rollback target is gone is what they could have used instead.
func TestUseImageNamesTheCandidatesWhenTheTagIsMissing(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{
		{Match: "image ls", Stdout: runningTag + "\n" + priorTag + "\n"},
	}}
	s := newState(pipeline.ModeExecute, nil)

	err := docker.UseImage{
		Repository: repo, Tag: "deadbee", MustExist: true, Runner: r,
	}.Run(context.Background(), s)
	if err == nil {
		t.Fatal("a tag that is not on the host was accepted")
	}
	for _, want := range []string{"deadbee", runningTag, priorTag} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to mention %q", err, want)
		}
	}
	// A pipeline that cannot deploy must not claim it can.
	if _, err := pipeline.Get[string](s, common.KeyImage); err == nil {
		t.Error("the image was published to state despite not existing")
	}
}

// TestUseImageReportsAnEmptyRepositoryAsSuch. "available: " with nothing after
// it reads as a truncated message when it is in fact the finding.
func TestUseImageReportsAnEmptyRepositoryAsSuch(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "image ls", Stdout: ""}}}

	err := docker.UseImage{
		Repository: repo, Tag: priorTag, MustExist: true, Runner: r,
	}.Run(context.Background(), newState(pipeline.ModeExecute, nil))
	if err == nil || !strings.Contains(err.Error(), "no images for this repository") {
		t.Errorf("err = %v; want the empty case spelled out", err)
	}
}

// TestUseImageDryRunDoesNotNeedTheHost. A rehearsal must not fail for a reason
// the real run would not have, and must still publish the image so the steps
// after it rehearse too.
func TestUseImageDryRunDoesNotNeedTheHost(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Fail: errNoHost}
	s := newState(pipeline.ModeDryRun, nil)

	step := docker.UseImage{Repository: repo, Tag: priorTag, MustExist: true, Runner: r}
	if err := step.Run(context.Background(), s); err != nil {
		t.Fatalf("dry run reached the host: %v", err)
	}
	if _, err := pipeline.Get[string](s, common.KeyImage); err != nil {
		t.Errorf("dry run left the image unset, so later steps cannot rehearse: %v", err)
	}
}

// TestUseImageValidateRefusesAmbiguity. Both fields set means the caller has
// two answers in mind and no way to know which one wins.
func TestUseImageValidateRefusesAmbiguity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		step docker.UseImage
		want string
	}{
		{"no repository", docker.UseImage{Tag: priorTag}, "Repository"},
		{"no tag at all", docker.UseImage{Repository: repo}, "Tag or TagFrom"},
		{"both", docker.UseImage{Repository: repo, Tag: priorTag, TagFrom: "k"}, "exactly one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.step.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
	if err := (docker.UseImage{Repository: repo, Tag: priorTag}).Validate(); err != nil {
		t.Errorf("a valid step was refused: %v", err)
	}
}
