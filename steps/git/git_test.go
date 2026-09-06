package git_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	gitkit "github.com/ubgo/lath/kit/git"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/git"
)

func TestResolveCommitProvidesTheSHA(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "rev-parse", Stdout: "a3f1c2d\n"},
		{Match: "status", Stdout: ""},
	}}
	s := pipeline.NewState(pipeline.ModeExecute, nil)

	if err := (git.ResolveCommit{Runner: f}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.Get[string](s, common.KeyCommit)
	if err != nil || got != "a3f1c2d" {
		t.Errorf("KeyCommit = %q, %v", got, err)
	}
}

// TestDirtyTreeIsRefused pins the guard's reason: the commit names the image,
// so with a dirty tree the tag describes bytes that were never committed ,
// "what is running in production" becomes unanswerable.
func TestDirtyTreeIsRefused(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "rev-parse", Stdout: "a3f1c2d\n"},
		{Match: "status", Stdout: " M apps/api/main.go\n"},
	}}

	err := git.ResolveCommit{Runner: f}.
		Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, gitkit.ErrDirty) {
		t.Fatalf("err = %v; want ErrDirty", err)
	}
	// The error must name the file AND the way out, or the reader has to guess
	// both.
	for _, want := range []string{"apps/api/main.go", "AllowDirty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to mention %q", err, want)
		}
	}
}

func TestAllowDirtySkipsTheCheck(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "rev-parse", Stdout: "a3f1c2d\n"},
		{Match: "status", Stdout: " M dirty.go\n"},
	}}
	if err := (git.ResolveCommit{Runner: f, AllowDirty: true}).
		Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil)); err != nil {
		t.Fatalf("AllowDirty did not skip the check: %v", err)
	}
	if f.Ran("status") {
		t.Error("the status check ran despite AllowDirty: a wasted call")
	}
}

// TestReadOnlyUnderDryRun pins that the commit is resolved even in a rehearsal:
// every later step's description depends on the tag it produces.
func TestReadOnlyUnderDryRun(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "rev-parse", Stdout: "deadbee\n"},
		{Match: "status", Stdout: ""},
	}}
	s := pipeline.NewState(pipeline.ModeDryRun, nil)
	if err := (git.ResolveCommit{Runner: f}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got, _ := pipeline.Get[string](s, common.KeyCommit); got != "deadbee" {
		t.Errorf("a dry run did not provide KeyCommit: %q", got)
	}
}

func TestFailureSurfaces(t *testing.T) {
	t.Parallel()
	boom := errors.New("git is not installed")
	err := git.ResolveCommit{Runner: &runner.Fake{Fail: boom}}.
		Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the cause to stay unwrappable", err)
	}
}

// TestResolveCommitWiring covers the declarations Validate uses to prove a
// pipeline can work. They are not bookkeeping: a wrong Provides here makes a
// correctly-ordered pipeline fail validation, and a missing one lets a
// mis-ordered pipeline run.
func TestResolveCommitWiring(t *testing.T) {
	t.Parallel()
	var s pipeline.Step = git.ResolveCommit{}

	if s.Name() != "resolve-commit" {
		t.Errorf("Name = %q", s.Name())
	}
	if len(s.Requires()) != 0 {
		t.Errorf("Requires = %v; reading git needs no earlier step", s.Requires())
	}
	provides := map[pipeline.Key]bool{}
	for _, k := range s.Provides() {
		provides[k] = true
	}
	for _, want := range []pipeline.Key{common.KeyCommit, common.KeyBranch} {
		if !provides[want] {
			t.Errorf("Provides omits %q, so a step reading it fails validation", want)
		}
	}

	// Reading a working tree twice yields the same answer, so a debugger may
	// replay it without asking.
	r, ok := s.(pipeline.Replayable)
	if !ok || !r.Replayable() {
		t.Error("ResolveCommit should declare itself replayable")
	}
}

// TestTheStepIsNamedAndDocumented. `lath plan` is assembled entirely from
// these methods, and it is what an operator reads before letting something
// touch production. A step with no Docs renders as a blank line, which is
// worse than absent: the reader sees a step they cannot identify and either
// stops trusting the listing or approves something they did not read.
func TestTheStepIsNamedAndDocumented(t *testing.T) {
	t.Parallel()
	var step pipeline.Step = git.ResolveCommit{}

	if step.Name() == "" {
		t.Fatal("the step has no name; it renders as a blank line in every plan")
	}
	documented, ok := step.(pipeline.Documented)
	if !ok {
		t.Fatal("the step is not Documented; a plan cannot say what it does")
	}
	docs := documented.Docs()
	if strings.TrimSpace(docs.Summary) == "" {
		t.Error("the summary is empty")
	}
	// A summary is help text, not a sentence: it sits in a column beside the
	// step name, where a trailing period reads as a typo.
	if strings.HasSuffix(docs.Summary, ".") {
		t.Errorf("the summary ends in a period: %q", docs.Summary)
	}
	// The keys are the other half of the plan: they are what Validate uses to
	// prove ordering, so a wrong answer here is a pipeline that validates and
	// then fails mid-run.
	if len(step.Provides()) != 2 {
		t.Errorf("Provides = %v, want the commit and the branch", step.Provides())
	}
}
