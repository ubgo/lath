package docker

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// CheckImage refuses the image in common.KeyImage unless it is for the
// expected platform and carries the expected labels.
//
// Why it exists: when an image is built in one place and shipped from
// another (one CI job builds and `docker save`s it, the next `docker load`s
// and pushes it), the tag alone cannot prove it is the right image. A local
// rehearsal writes the same repository:commit tag, perhaps from a dirty tree,
// for the laptop's architecture, and UseImage would ship it. The platform and
// the labels a build stamped are what tell them apart: dockerkit.LabelRevision
// for the exact commit (see Build.OmitRevision), and a label the project adds
// itself, such as the environment a build was for, which is the one that
// tells a rehearsal of this commit from the real build of it.
//
// It checks; it does not decide what to check. Which labels prove an image is
// "ours" is project policy, stated in Labels and LabelsFromState.
type CheckImage struct {
	// Platform is the os/arch the image must be for ("linux/amd64"), with an
	// optional /variant. A platform without a variant matches any variant.
	// Empty skips the platform check.
	Platform string
	// Labels the image must carry, with these exact values.
	Labels map[string]string
	// LabelsFromState binds expected label values to pipeline state: the key
	// is the label, the value the state key holding what it must equal. The
	// usual one is {dockerkit.LabelRevision: common.KeyRevision}.
	//
	// Keys rather than values for the same reason as Build.ArgsFromState: the
	// commit is known only once an earlier step has read it, and a binding
	// declared as data joins Requires, so a missing producer is a validation
	// error before anything runs.
	LabelsFromState map[string]pipeline.Key
	// Hint is appended to the refusal, to say how to produce the right image
	// ("build it with `lath run deploy image prod --apply`"). Optional.
	Hint string
	// Runner is where the image is inspected. Nil means this machine.
	Runner runner.Runner
}

func (CheckImage) Name() string { return "check-image" }

// Docs explains the step in a plan. See pipeline.Documented.
func (c CheckImage) Docs() pipeline.Docs {
	var want []string
	if c.Platform != "" {
		want = append(want, c.Platform)
	}
	want = append(want, c.labelNames()...)
	return pipeline.Docs{
		Summary: "refuse an image that is not for this platform or lacks the expected labels",
		Detail:  strings.Join(want, ", "),
	}
}

// Requires is the image plus every state key LabelsFromState reads.
func (c CheckImage) Requires() []pipeline.Key {
	keys := []pipeline.Key{common.KeyImage}
	seen := map[pipeline.Key]bool{common.KeyImage: true}
	for _, label := range sortedArgNames(c.LabelsFromState) {
		if key := c.LabelsFromState[label]; !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

func (CheckImage) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. It reads the image and changes nothing.
func (CheckImage) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (c CheckImage) Validate() error {
	if c.Platform == "" && len(c.Labels) == 0 && len(c.LabelsFromState) == 0 {
		// A check with nothing to compare passes every image, which reads in a
		// plan as a guarantee the step does not give.
		return fmt.Errorf("set Platform, Labels or LabelsFromState; with none, every image passes")
	}
	if c.Platform != "" {
		parts := strings.Split(c.Platform, "/")
		if len(parts) < 2 || len(parts) > 3 || slices.Contains(parts, "") {
			return fmt.Errorf("the Platform field %q is not os/arch or os/arch/variant", c.Platform)
		}
	}
	for _, label := range sortedArgNames(c.LabelsFromState) {
		if c.LabelsFromState[label] == "" {
			return fmt.Errorf("LabelsFromState[%q] has no state key", label)
		}
		if _, both := c.Labels[label]; both {
			return fmt.Errorf("label %q is in both Labels and LabelsFromState; keep one", label)
		}
	}
	return nil
}

// labelNames lists every label the step checks, sorted.
func (c CheckImage) labelNames() []string {
	names := slices.Collect(maps.Keys(c.Labels))
	names = append(names, slices.Collect(maps.Keys(c.LabelsFromState))...)
	slices.Sort(names)
	return slices.Compact(names)
}

func (c CheckImage) Run(ctx context.Context, s *pipeline.State) error {
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		return fmt.Errorf("check-image: %w", err)
	}
	want := make(map[string]string, len(c.Labels)+len(c.LabelsFromState))
	for label, value := range c.Labels {
		want[label] = value
	}
	for _, label := range sortedArgNames(c.LabelsFromState) {
		value, err := pipeline.Get[string](s, c.LabelsFromState[label])
		if err != nil {
			return fmt.Errorf("check-image: label %s: %w", label, err)
		}
		if value == "" {
			// An empty expectation would pass an image with an empty or
			// missing-but-defaulted label: the check would prove nothing.
			return fmt.Errorf("check-image: label %s: %s is empty; nothing to compare against", label, c.LabelsFromState[label])
		}
		want[label] = value
	}

	client := dockerkit.On(c.Runner)
	if s.DryRun() {
		s.Detailf("would check %s %s is %s", image, client.Where().Describe(), c.describe(want))
		return nil
	}
	info, err := client.InspectImage(ctx, image)
	if err != nil {
		return fmt.Errorf("check-image: %w", err)
	}

	var wrong []string
	if c.Platform != "" && !platformMatches(info, c.Platform) {
		wrong = append(wrong, fmt.Sprintf("platform is %s, want %s", info.Platform(), c.Platform))
	}
	for _, label := range slices.Sorted(maps.Keys(want)) {
		got, ok := info.Labels[label]
		switch {
		case !ok:
			wrong = append(wrong, fmt.Sprintf("label %s is absent, want %q", label, want[label]))
		case got != want[label]:
			wrong = append(wrong, fmt.Sprintf("label %s is %q, want %q", label, got, want[label]))
		}
	}
	if len(wrong) > 0 {
		msg := fmt.Sprintf("check-image: %s is not the image this pipeline expects: %s", image, strings.Join(wrong, "; "))
		if c.Hint != "" {
			msg += "; " + c.Hint
		}
		return fmt.Errorf("%s", msg)
	}
	s.Detailf("%s is %s", image, c.describe(want))
	return nil
}

// platformMatches compares os and architecture always, and the variant only
// when the expectation names one: "linux/arm64" is satisfied by any arm64
// image. Both sides are normalised the way containerd normalises a platform,
// because docker reports a real arm64 image with no variant at all, which
// containerd treats as v8, and some builders report x86_64 for amd64.
func platformMatches(info dockerkit.ImageInfo, platform string) bool {
	parts := strings.Split(platform, "/")
	wantVariant := ""
	if len(parts) == 3 {
		wantVariant = parts[2]
	}
	wantArch, wantVariant := normalizeArch(parts[1], wantVariant)
	gotArch, gotVariant := normalizeArch(info.Architecture, info.Variant)
	if info.OS != parts[0] || gotArch != wantArch {
		return false
	}
	return len(parts) < 3 || gotVariant == wantVariant
}

// normalizeArch maps an architecture and variant to containerd's canonical
// spelling: x86_64 is amd64, aarch64 is arm64, and arm64 with no variant is
// arm64/v8.
func normalizeArch(arch, variant string) (string, string) {
	switch arch {
	case "x86_64", "x86-64":
		return "amd64", variant
	case "aarch64", "arm64":
		if variant == "" || variant == "8" {
			variant = "v8"
		}
		return "arm64", variant
	}
	return arch, variant
}

// describe renders the expectation for the plan and the log.
func (c CheckImage) describe(want map[string]string) string {
	var parts []string
	if c.Platform != "" {
		parts = append(parts, c.Platform)
	}
	for _, label := range slices.Sorted(maps.Keys(want)) {
		parts = append(parts, label+"="+want[label])
	}
	return strings.Join(parts, ", ")
}
