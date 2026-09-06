package pipeline_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

func TestFuncValidate(t *testing.T) {
	t.Parallel()
	noop := func(context.Context, *pipeline.State) error { return nil }
	for _, tc := range []struct {
		name string
		step pipeline.Func
		want string
	}{
		{"no label", pipeline.Func{Do: noop}, "Label"},
		{"nil closure", pipeline.Func{Label: "x"}, "Do is nil"},
		{"complete", pipeline.Func{Label: "x", Do: noop}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.step.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v; want nil", err)
				}
				return
			}
			if err == nil || !contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v; want a message mentioning %q", err, tc.want)
			}
		})
	}
}

// TestFuncNilClosureCaughtBeforeRun is the reason Func implements Validator: a
// nil Do would otherwise be a nil-pointer dereference at the moment the step
// executes, which for a deploy is the worst available time to find out.
func TestFuncNilClosureCaughtBeforeRun(t *testing.T) {
	t.Parallel()
	var laterRan bool
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		pipeline.Func{Label: "broken"}, // Do is nil
		fakeStep{name: "later", ran: &laterRan},
	}}
	err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, pipeline.ErrStepConfig) {
		t.Fatalf("Run() = %v; want errors.Is(_, ErrStepConfig)", err)
	}
	if laterRan {
		t.Error("a later step ran; config faults must abort before any execution")
	}
}

// TestFuncParticipatesInWiring proves an inline step is a first-class citizen:
// its Needs and Gives are checked exactly like a named step's.
func TestFuncParticipatesInWiring(t *testing.T) {
	t.Parallel()
	const kOut pipeline.Key = "produced-by-func"

	// A Func may PROVIDE a key that a later named step consumes.
	ok := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		pipeline.Func{
			Label: "produce",
			Gives: []pipeline.Key{kOut},
			Do: func(_ context.Context, s *pipeline.State) error {
				pipeline.Set(s, kOut, "value")
				return nil
			},
		},
		fakeStep{name: "consume", requires: []pipeline.Key{kOut}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid pipeline rejected: %v", err)
	}

	// And a Func that REQUIRES an unprovided key is caught like any other.
	bad := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		pipeline.Func{Label: "consume", Needs: []pipeline.Key{"absent"},
			Do: func(context.Context, *pipeline.State) error { return nil }},
	}}
	if err := bad.Validate(); !errors.Is(err, pipeline.ErrNotWired) {
		t.Fatalf("Validate() = %v; want ErrNotWired", err)
	}
}

func TestFuncRunsTheClosure(t *testing.T) {
	t.Parallel()
	const k pipeline.Key = "from-closure"
	var called bool
	s := pipeline.NewState(pipeline.ModeExecute, nil)

	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		pipeline.Func{Label: "work", Gives: []pipeline.Key{k},
			Do: func(_ context.Context, s *pipeline.State) error {
				called = true
				pipeline.Set(s, k, 42)
				return nil
			}},
	}}
	if err := p.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("the closure never ran")
	}
	if got, err := pipeline.Get[int](s, k); err != nil || got != 42 {
		t.Errorf("Get = %v, %v; want 42, nil", got, err)
	}
}

// TestFuncErrorIsWrapped proves an inline step's failure is distinguishable
// from a wiring fault, so tooling can branch without parsing text.
func TestFuncErrorIsWrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	p := pipeline.Pipeline{Name: "t", Steps: []pipeline.Step{
		pipeline.Func{Label: "fails",
			Do: func(context.Context, *pipeline.State) error { return boom }},
	}}
	err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, pipeline.ErrStepFailed) || !errors.Is(err, boom) {
		t.Fatalf("Run() = %v; want it to wrap both ErrStepFailed and the closure's error", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
