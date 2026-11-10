package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/docker"
)

// inspected is docker's image inspect output for an image with these facts.
func inspected(arch, revision, target string) string {
	return `[{"Os": "linux", "Architecture": "` + arch + `", "Config": {"Labels": {` +
		`"` + dockerkit.LabelRevision + `": "` + revision + `", "deploy.target": "` + target + `"}}}]`
}

func checkStep(r runner.Runner) docker.CheckImage {
	return docker.CheckImage{
		Platform:        "linux/amd64",
		Labels:          map[string]string{"deploy.target": "prod"},
		LabelsFromState: map[string]pipeline.Key{dockerkit.LabelRevision: common.KeyRevision},
		Hint:            "rebuild it from this commit",
		Runner:          r,
	}
}

// TestCheckImagePassesTheRightImage pins the happy path, and that the check
// asks about the image in state, not some other reference.
func TestCheckImagePassesTheRightImage(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "image inspect", Stdout: inspected("amd64", testRevision, "prod")}}}
	if err := checkStep(r).Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	if !r.Ran("docker image inspect ghcr.io/acme/app:a3f1c2d") {
		t.Errorf("commands = %v", r.Commands())
	}
}

// TestCheckImageRefusesALookAlike is the reason the step exists: an image
// with the right tag, built on a laptop for another architecture, from
// another commit, for another target. Every difference is named, so the
// reader learns all of what is wrong from one failure, and the hint says
// what to do.
func TestCheckImageRefusesALookAlike(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "image inspect", Stdout: inspected("arm64", "ffff", "local")}}}
	err := checkStep(r).Run(context.Background(), deployState(pipeline.ModeExecute))
	if err == nil {
		t.Fatal("a look-alike image passed")
	}
	for _, want := range []string{
		"platform is linux/arm64, want linux/amd64",
		`label ` + dockerkit.LabelRevision + ` is "ffff", want "` + testRevision + `"`,
		`label deploy.target is "local", want "prod"`,
		"rebuild it from this commit",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

// TestCheckImageRefusesAnUnlabelledImage pins that a missing label fails as
// "absent", not as an empty value that might match an empty expectation.
func TestCheckImageRefusesAnUnlabelledImage(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "image inspect",
		Stdout: `[{"Os": "linux", "Architecture": "amd64", "Config": {"Labels": null}}]`}}}
	err := checkStep(r).Run(context.Background(), deployState(pipeline.ModeExecute))
	if err == nil || !strings.Contains(err.Error(), "label deploy.target is absent") {
		t.Errorf("err = %v", err)
	}
}

// TestCheckImageReportsAMissingImage pins that an image that was never loaded
// says so, as dockerkit.ErrNoSuchImage, rather than as a parse failure.
func TestCheckImageReportsAMissingImage(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "image inspect", Stderr: "Error: No such image", Exit: 1}}}
	err := checkStep(r).Run(context.Background(), deployState(pipeline.ModeExecute))
	if !errors.Is(err, dockerkit.ErrNoSuchImage) {
		t.Errorf("err = %v; want ErrNoSuchImage", err)
	}
}

// TestCheckImageDryRunRunsNothing pins the dry-run contract: a rehearsal must
// work on a machine with no docker, so the step describes and returns.
func TestCheckImageDryRunRunsNothing(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	if err := checkStep(r).Run(context.Background(), deployState(pipeline.ModeDryRun)); err != nil {
		t.Fatal(err)
	}
	if len(r.Commands()) != 0 {
		t.Errorf("dry run ran %v", r.Commands())
	}
}

// TestCheckImagePlatformIsNormalised pins the platform rule: a variant counts
// only when the expectation names one, and both sides are spelt the way
// containerd spells them. Docker reports a real arm64 image with NO variant,
// which is v8, so a literal comparison would refuse the right image.
func TestCheckImagePlatformIsNormalised(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		arch, variant, platform string
		pass                    bool
	}{
		{"arm64", "", "linux/arm64", true},
		{"arm64", "", "linux/arm64/v8", true},
		{"arm64", "v8", "linux/arm64/v8", true},
		{"arm64", "v8", "linux/arm64/v9", false},
		{"x86_64", "", "linux/amd64", true},
		{"amd64", "", "linux/arm64", false},
	} {
		out := `[{"Os": "linux", "Architecture": "` + tc.arch + `", "Variant": "` + tc.variant + `", "Config": {}}]`
		r := &runner.Fake{Reply: []runner.Scripted{{Match: "image inspect", Stdout: out}}}
		err := docker.CheckImage{Platform: tc.platform, Runner: r}.Run(context.Background(), deployState(pipeline.ModeExecute))
		if (err == nil) != tc.pass {
			t.Errorf("%s/%s against %s: err = %v, want pass=%v", tc.arch, tc.variant, tc.platform, err, tc.pass)
		}
	}
}

// TestCheckImageWiring pins Requires and the configuration refusals: the
// bound state keys join Requires so Validate proves their producers run
// first, and a check with nothing to compare is refused because it would
// pass every image while reading like a guarantee.
func TestCheckImageWiring(t *testing.T) {
	t.Parallel()
	req := checkStep(nil).Requires()
	if len(req) != 2 || req[0] != common.KeyImage || req[1] != common.KeyRevision {
		t.Errorf("Requires = %v", req)
	}
	if err := checkStep(nil).Validate(); err != nil {
		t.Errorf("valid step: %v", err)
	}
	for name, step := range map[string]docker.CheckImage{
		"nothing to check":  {},
		"a bare platform":   {Platform: "amd64"},
		"an empty part":     {Platform: "linux/"},
		"an unbound label":  {LabelsFromState: map[string]pipeline.Key{"x": ""}},
		"a label set twice": {Labels: map[string]string{"x": "a"}, LabelsFromState: map[string]pipeline.Key{"x": common.KeyEnv}},
	} {
		if step.Validate() == nil {
			t.Errorf("%s: Validate passed", name)
		}
	}
}

// TestCheckImageRefusesAnEmptyExpectation pins that a state value of "" is an
// error rather than a comparison: it would pass an image whose label is
// empty, which proves nothing.
func TestCheckImageRefusesAnEmptyExpectation(t *testing.T) {
	t.Parallel()
	s := newState(pipeline.ModeExecute, map[pipeline.Key]any{
		common.KeyImage: "app:1", common.KeyRevision: "",
	})
	err := docker.CheckImage{LabelsFromState: map[string]pipeline.Key{dockerkit.LabelRevision: common.KeyRevision}}.
		Run(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("err = %v", err)
	}
}
