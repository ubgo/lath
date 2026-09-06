package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/pipeline"
)

// fakeStep is a configurable Step for exercising the engine without any real
// primitive. Kept in the test package so nothing ships it by accident.
type fakeStep struct {
	name     string
	requires []pipeline.Key
	provides []pipeline.Key
	err      error
	// ran records execution, so a test can assert a step did NOT run after an
	// earlier failure, the fail-fast guarantee.
	ran *bool
}

func (f fakeStep) Name() string             { return f.name }
func (f fakeStep) Requires() []pipeline.Key { return f.requires }
func (f fakeStep) Provides() []pipeline.Key { return f.provides }

func (f fakeStep) Run(_ context.Context, s *pipeline.State) error {
	if f.ran != nil {
		*f.ran = true
	}
	if f.err != nil {
		return f.err
	}
	for _, k := range f.provides {
		pipeline.Set(s, k, "value-of-"+k.String())
	}
	return nil
}

const (
	keyA pipeline.Key = "a"
	keyB pipeline.Key = "b"
)

func TestPipelineValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		steps   []pipeline.Step
		wantErr error
	}{
		{
			name:    "empty pipeline is an error, not a silent success",
			steps:   nil,
			wantErr: pipeline.ErrNoSteps,
		},
		{
			name: "producer before consumer is valid",
			steps: []pipeline.Step{
				fakeStep{name: "produce", provides: []pipeline.Key{keyA}},
				fakeStep{name: "consume", requires: []pipeline.Key{keyA}},
			},
		},
		{
			name: "consumer before producer is not wired",
			steps: []pipeline.Step{
				fakeStep{name: "consume", requires: []pipeline.Key{keyA}},
				fakeStep{name: "produce", provides: []pipeline.Key{keyA}},
			},
			wantErr: pipeline.ErrNotWired,
		},
		{
			name: "a step may consume a key it also provides later in the list only if provided first",
			steps: []pipeline.Step{
				fakeStep{name: "produce-a", provides: []pipeline.Key{keyA}},
				fakeStep{name: "a-to-b", requires: []pipeline.Key{keyA}, provides: []pipeline.Key{keyB}},
				fakeStep{name: "consume-b", requires: []pipeline.Key{keyB}},
			},
		},
		{
			name: "two providers of one key is ambiguous",
			steps: []pipeline.Step{
				fakeStep{name: "first", provides: []pipeline.Key{keyA}},
				fakeStep{name: "second", provides: []pipeline.Key{keyA}},
			},
			wantErr: pipeline.ErrDuplicateProvider,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := pipeline.Pipeline{Name: "t", Steps: tc.steps}.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v; want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v; want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestValidateMessageNamesBothSteps pins the detail that makes a duplicate
// actionable: naming the step that already provides the key, not just the
// offender.
func TestValidateMessageNamesBothSteps(t *testing.T) {
	t.Parallel()
	err := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		fakeStep{name: "first", provides: []pipeline.Key{keyA}},
		fakeStep{name: "second", provides: []pipeline.Key{keyA}},
	}}.Validate()
	for _, want := range []string{"first", "second", string(keyA)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q missing %q", err, want)
		}
	}
}

func TestPipelineRunStopsAtFirstFailure(t *testing.T) {
	t.Parallel()
	var laterRan bool
	boom := errors.New("boom")
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		fakeStep{name: "ok", provides: []pipeline.Key{keyA}},
		fakeStep{name: "fails", requires: []pipeline.Key{keyA}, err: boom},
		fakeStep{name: "later", ran: &laterRan},
	}}

	err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, pipeline.ErrStepFailed) {
		t.Fatalf("Run() = %v; want errors.Is(_, ErrStepFailed)", err)
	}
	// The underlying cause must survive wrapping, or a caller cannot tell one
	// step failure from another.
	if !errors.Is(err, boom) {
		t.Errorf("Run() = %v; want it to wrap the step's own error", err)
	}
	if laterRan {
		t.Error("a step after the failure ran; fail-fast is the guarantee that " +
			"stops a traffic switch following a failed health check")
	}
}

func TestPipelineRunRespectsCancellation(t *testing.T) {
	t.Parallel()
	var secondRan bool
	ctx, cancel := context.WithCancel(context.Background())
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		stepFunc{name: "cancels", fn: func() error { cancel(); return nil }},
		fakeStep{name: "second", ran: &secondRan},
	}}

	err := p.Run(ctx, pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v; want errors.Is(_, context.Canceled)", err)
	}
	if secondRan {
		t.Error("step ran after cancellation; the between-step check did not fire")
	}
}

// stepFunc adapts a closure to Step for the cancellation test.
type stepFunc struct {
	name string
	fn   func() error
}

func (s stepFunc) Name() string                               { return s.name }
func (stepFunc) Requires() []pipeline.Key                     { return nil }
func (stepFunc) Provides() []pipeline.Key                     { return nil }
func (s stepFunc) Run(context.Context, *pipeline.State) error { return s.fn() }

func TestPlanDescribesWithoutRunning(t *testing.T) {
	t.Parallel()
	var ran bool
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		fakeStep{name: "one", provides: []pipeline.Key{keyA}, ran: &ran},
		fakeStep{name: "two", requires: []pipeline.Key{keyA}},
	}}

	entries := p.Plan()
	if ran {
		t.Fatal("Plan executed a step; it must be side-effect free")
	}
	if len(entries) != 2 {
		t.Fatalf("Plan() returned %d entries; want 2", len(entries))
	}
	if entries[0].Position != 1 || entries[1].Position != 2 {
		t.Errorf("positions are not 1-based sequential: %+v", entries)
	}
	if entries[1].Total != 2 {
		t.Errorf("Total = %d; want 2", entries[1].Total)
	}
}

func TestReporterReceivesProgress(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		stepFunc{name: "only", fn: func() error { return nil }},
	}}
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&buf))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "only") {
		t.Errorf("reporter output %q does not mention the step", buf.String())
	}
}

// TestNilReporterDoesNotPanic pins the deliberate choice that output plumbing
// must never be the reason a deploy aborts.
func TestNilReporterDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		stepFunc{name: "only", fn: func() error { return nil }},
	}}
	if err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestStateGet(t *testing.T) {
	t.Parallel()
	s := pipeline.NewState(pipeline.ModeExecute, nil)
	pipeline.Set(s, keyA, "text")
	pipeline.Set(s, keyB, 42*time.Second)

	if got, err := pipeline.Get[string](s, keyA); err != nil || got != "text" {
		t.Errorf("Get[string](a) = %q, %v; want \"text\", nil", got, err)
	}
	if _, err := pipeline.Get[string](s, "absent"); !errors.Is(err, pipeline.ErrKeyMissing) {
		t.Errorf("Get of absent key = %v; want ErrKeyMissing", err)
	}
	if _, err := pipeline.Get[int](s, keyA); !errors.Is(err, pipeline.ErrKeyType) {
		t.Errorf("Get with wrong type = %v; want ErrKeyType", err)
	}
	if !pipeline.Has(s, keyA) || pipeline.Has(s, "absent") {
		t.Error("Has disagrees with what was Set")
	}
}

func TestModeValid(t *testing.T) {
	t.Parallel()
	for _, m := range pipeline.ModeValues {
		if !m.Valid() {
			t.Errorf("%q is in ModeValues but Valid() is false", m)
		}
	}
	if pipeline.Mode("nonsense").Valid() {
		t.Error("an undeclared mode reported valid")
	}
	// An invalid mode must fall back to execute, never silently to dry-run:
	// a deploy that quietly does nothing is the worse failure.
	if pipeline.NewState("nonsense", nil).DryRun() {
		t.Error("invalid mode fell back to dry-run; must fall back to execute")
	}
}
