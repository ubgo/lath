package common_test

import (
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/docker"
	"github.com/ubgo/lath/steps/git"
)

// TestReplayableClassification pins which steps declare themselves safe to
// replay, in one place, because the classification is a safety claim rather
// than an implementation detail.
//
// A step wrongly marked replayable means a debugger replays it without asking,
// and for a step that starts containers that means a second container in
// production. A step wrongly left unmarked costs one keystroke. The asymmetry
// is why the default is unmarked and why this list is asserted rather than
// assumed.
func TestReplayableClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		step pipeline.Step
		want bool
		why  string
	}{
		{common.ResolveEnv{}, true, "reads configuration, touches nothing"},
		{common.EnsureDir{}, true, "mkdir -p"},
		{common.PutFile{}, true, "same bytes, same path, same mode"},
		{common.Request{}, true, "sends the same body to the same URL"},
		{git.ResolveCommit{}, true, "reads git"},
		{docker.Build{}, true, "same tag from the same context"},
		{docker.Login{}, true, "one credential either way"},
		{docker.Push{}, true, "same tag, identical digest"},
		{docker.Pull{}, true, "image already present"},
		{docker.WaitHealthy{}, true, "observes, never changes"},
		{docker.StopPrevious{}, true, "nothing left to stop the second time"},
		{docker.Prune{}, true, "removes whatever is dangling now"},

		// The two that must NOT be replayable without asking.
		{docker.Start{}, false, "starts ANOTHER container"},
		{docker.RunOnce{}, false, "runs an arbitrary caller-supplied Cmd"},
	}
	for _, tc := range cases {
		t.Run(tc.step.Name(), func(t *testing.T) {
			t.Parallel()
			r, ok := tc.step.(pipeline.Replayable)
			got := ok && r.Replayable()
			if got != tc.want {
				t.Errorf("Replayable() = %v, want %v: %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestEveryStepIsClassified fails when a new step type is added without
// deciding whether it is replayable. Without this, a new mutating step
// silently inherits the safe default (unmarked), which is correct, but a new
// read-only step silently loses the ability to be replayed without a prompt,
// and nobody notices.
//
// The list here is the same one TestReplayableClassification asserts on; this
// test exists to make the two drift loudly rather than quietly.
func TestEveryStepIsClassified(t *testing.T) {
	t.Parallel()
	known := map[string]bool{
		"resolve-env": true, "ensure-dirs": true, "put-file": true,
		"request": true, "resolve-commit": true, "build-image": true,
		"registry-login": true, "push-image": true, "pull-image": true,
		"wait-healthy": true, "stop-previous": true, "prune-image": true,
		"start-processes": true, "run-once": true,
	}
	all := []pipeline.Step{
		common.ResolveEnv{}, common.EnsureDir{}, common.PutFile{}, common.Request{},
		git.ResolveCommit{},
		docker.Build{}, docker.Login{}, docker.Push{}, docker.Pull{},
		docker.WaitHealthy{}, docker.StopPrevious{}, docker.Prune{},
		docker.Start{}, docker.RunOnce{},
	}
	for _, s := range all {
		if !known[s.Name()] {
			t.Errorf("step %q is not in the classification list", s.Name())
		}
		delete(known, s.Name())
	}
	for name := range known {
		t.Errorf("classification lists %q, which no longer exists", name)
	}
}

// TestEveryStepDeclaresItsWiring covers Name/Requires/Provides for every step.
//
// These are not bookkeeping. Validate proves a pipeline can work from them, so
// a wrong Provides makes a correctly-ordered pipeline fail validation, and a
// missing one lets a mis-ordered pipeline run and fail three minutes in.
func TestEveryStepDeclaresItsWiring(t *testing.T) {
	t.Parallel()
	steps := []pipeline.Step{
		common.ResolveEnv{}, common.EnsureDir{}, common.PutFile{}, common.Request{},
		git.ResolveCommit{},
		docker.Build{}, docker.Login{}, docker.Push{}, docker.Pull{},
		docker.WaitHealthy{}, docker.StopPrevious{}, docker.Prune{},
		docker.Start{}, docker.RunOnce{},
	}
	known := map[pipeline.Key]bool{}
	for _, k := range common.KeyValues {
		known[k] = true
	}

	names := map[string]bool{}
	for _, s := range steps {
		name := s.Name()
		if name == "" {
			t.Errorf("%T has no name, so it cannot be referred to in a plan or an error", s)
		}
		if names[name] {
			t.Errorf("two steps are both called %q", name)
		}
		names[name] = true

		// Every key a step declares must be one the package defines, or
		// nothing can ever provide it and Validate rejects the pipeline.
		for _, k := range append(s.Requires(), s.Provides()...) {
			if !known[k] {
				t.Errorf("%s declares key %q, which is not in common.KeyValues", name, k)
			}
		}
	}
}

// TestEveryKeyHasAProvider. A key nothing provides can only ever fail
// validation, so its presence in KeyValues would be a lie.
func TestEveryKeyHasAProvider(t *testing.T) {
	t.Parallel()
	provided := map[pipeline.Key]bool{}
	for _, s := range []pipeline.Step{
		common.ResolveEnv{}, git.ResolveCommit{}, docker.Build{}, docker.Start{},
	} {
		for _, k := range s.Provides() {
			provided[k] = true
		}
	}
	for _, k := range common.KeyValues {
		if !provided[k] {
			t.Errorf("no shipped step provides %q", k)
		}
	}
}

// TestEveryStepDocumentsItself is the same drift guard as the replayable
// classification, for the other thing a reader needs from a plan. A step whose
// name leaves a question, and most do, should answer it here rather than in a
// document that will not be open at the time.
func TestEveryStepDocumentsItself(t *testing.T) {
	t.Parallel()
	all := []pipeline.Step{
		common.ResolveEnv{}, common.EnsureDir{}, common.PutFile{}, common.Request{},
		git.ResolveCommit{},
		docker.Build{}, docker.Login{}, docker.Push{}, docker.Pull{},
		docker.WaitHealthy{}, docker.StopPrevious{}, docker.Prune{},
		docker.Start{}, docker.RunOnce{},
	}
	for _, s := range all {
		d, ok := s.(pipeline.Documented)
		if !ok {
			t.Errorf("%s does not implement pipeline.Documented", s.Name())
			continue
		}
		docs := d.Docs()
		if docs.Summary == "" {
			t.Errorf("%s has no summary, so a plan shows only its name", s.Name())
		}
		// A summary is a purpose, not a restatement of the label: "build-image
		// builds the image" tells a reader nothing they did not have.
		if strings.EqualFold(docs.Summary, s.Name()) {
			t.Errorf("%s summary just repeats the name", s.Name())
		}
	}
}
