package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

// recorder is a Step that appends its name to a shared slice when run, so a
// test can assert the exact execution order including replays.
type recorder struct {
	name   string
	log    *[]string
	fail   error
	replay bool
	runs   int
}

func (r *recorder) Name() string             { return r.name }
func (r *recorder) Requires() []pipeline.Key { return nil }
func (r *recorder) Provides() []pipeline.Key { return nil }
func (r *recorder) Replayable() bool         { return r.replay }
func (r *recorder) Run(context.Context, *pipeline.State) error {
	r.runs++
	*r.log = append(*r.log, r.name)
	return r.fail
}

// plain is a Step that does NOT implement Replayable, which is the default
// every existing step is in.
type plain struct{ name string }

func (p plain) Name() string                               { return p.name }
func (p plain) Requires() []pipeline.Key                   { return nil }
func (p plain) Provides() []pipeline.Key                   { return nil }
func (p plain) Run(context.Context, *pipeline.State) error { return nil }

func threeSteps(log *[]string) pipeline.Pipeline {
	return pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: log, replay: true},
		&recorder{name: "two", log: log, replay: true},
		&recorder{name: "three", log: log, replay: true},
	}}
}

func run(t *testing.T, p pipeline.Pipeline, d pipeline.Debugger) error {
	t.Helper()
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.Debug(d))
	return p.Run(context.Background(), st)
}

// TestUnarmedIsUnchanged is the most important test here: attaching nothing
// must behave exactly as before this feature existed. A debugger that alters
// ordinary runs is worse than no debugger.
func TestUnarmedIsUnchanged(t *testing.T) {
	t.Parallel()
	var log []string
	p := threeSteps(&log)
	st := pipeline.NewState(pipeline.ModeExecute, nil)
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,three" {
		t.Errorf("order = %q", got)
	}
}

// TestUnarmedFailureStillAborts pins the other half of "unchanged": without a
// debugger a failing step ends the run, as it always has.
func TestUnarmedFailureStillAborts(t *testing.T) {
	t.Parallel()
	var log []string
	boom := errors.New("boom")
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log},
		&recorder{name: "two", log: &log, fail: boom},
		&recorder{name: "three", log: &log},
	}}
	err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if !errors.Is(err, pipeline.ErrStepFailed) || !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(log, ","); got != "one,two" {
		t.Errorf("step three ran after a failure: %q", got)
	}
}

func TestNextWalksEveryStep(t *testing.T) {
	t.Parallel()
	var log []string
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext, pipeline.ActNext,
	}}
	if err := run(t, threeSteps(&log), d); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,three" {
		t.Errorf("order = %q", got)
	}
	// Two pauses for three steps: the last one has nothing to decide.
	if got := strings.Join(d.Steps(), ","); got != "one,two" {
		t.Errorf("paused after = %q", got)
	}
}

// TestRerunReplaysTheSameStep covers the index arithmetic, which is the part
// most likely to be off by one in either direction.
func TestRerunReplaysTheSameStep(t *testing.T) {
	t.Parallel()
	var log []string
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext,  // after one
		pipeline.ActRerun, // after two -> run two again
		pipeline.ActNext,  // after two (2nd time)
		pipeline.ActNext,  // after three
	}}
	if err := run(t, threeSteps(&log), d); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,two,three" {
		t.Errorf("order = %q, want two replayed exactly once", got)
	}
}

// TestRerunRestoresState proves a replay sees the inputs the step originally
// saw, not the ones it left behind. Without the snapshot, a step that reads
// then writes the same key would compound on every replay.
func TestRerunRestoresState(t *testing.T) {
	t.Parallel()
	const k pipeline.Key = "n"
	var seen []int

	seed := pipeline.Func{Label: "seed", Gives: []pipeline.Key{k},
		Do: func(_ context.Context, s *pipeline.State) error {
			pipeline.Set(s, k, 1)
			return nil
		}}
	bump := pipeline.Func{Label: "bump", Needs: []pipeline.Key{k},
		Do: func(_ context.Context, s *pipeline.State) error {
			n, err := pipeline.Get[int](s, k)
			if err != nil {
				return err
			}
			seen = append(seen, n)
			pipeline.Set(s, k, n+10)
			return nil
		}}

	// A trailing step so bump is not last: there is no pause after the final
	// step, and a rerun needs a pause to be requested at.
	tail := pipeline.Func{Label: "tail", Do: func(context.Context, *pipeline.State) error { return nil }}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext,  // after seed
		pipeline.ActRerun, // after bump
		pipeline.ActNext,  // after bump again
	}}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{seed, bump, tail}}
	if err := run(t, p, d); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 1 {
		t.Errorf("bump saw %v, want [1 1]: state was not restored before the replay", seen)
	}
}

func TestContinueStopsPausing(t *testing.T) {
	t.Parallel()
	var log []string
	// One scripted answer only: if the loop paused again, the fake errors on
	// an exhausted script and the run fails.
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{pipeline.ActContinue}}
	if err := run(t, threeSteps(&log), d); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,three" {
		t.Errorf("order = %q", got)
	}
	if n := len(d.Pauses()); n != 1 {
		t.Errorf("paused %d times after continue, want 1", n)
	}
}

func TestQuitAbandonsTheRun(t *testing.T) {
	t.Parallel()
	var log []string
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{pipeline.ActNext, pipeline.ActQuit}}
	err := run(t, threeSteps(&log), d)
	if !errors.Is(err, pipeline.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if got := strings.Join(log, ","); got != "one,two" {
		t.Errorf("order = %q, want the run to stop at two", got)
	}
}

// TestFailurePausesInsteadOfAborting is the one behavioural change to the
// engine, and it applies ONLY with a debugger attached.
func TestFailurePausesInsteadOfAborting(t *testing.T) {
	t.Parallel()
	var log []string
	boom := errors.New("i/o timeout")
	failing := &recorder{name: "two", log: &log, fail: boom, replay: true}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, replay: true},
		failing,
		&recorder{name: "three", log: &log, replay: true},
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext,  // after one
		pipeline.ActRerun, // after two FAILED -> retry it
		pipeline.ActNext,  // after two failed again -> surface the failure
	}}
	err := run(t, p, d)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the step's error", err)
	}
	if failing.runs != 2 {
		t.Errorf("failing step ran %d times, want 2 (the retry)", failing.runs)
	}
	// The pause on the failure must carry it, or a client cannot show why.
	pauses := d.Pauses()
	if len(pauses) < 2 || !errors.Is(pauses[1].Err, boom) {
		t.Errorf("pause did not carry the step error: %+v", pauses)
	}
}

// TestFailureRecoversOnRetry is the case the feature exists for: fix whatever
// broke, replay the step, and the run continues to the end.
func TestFailureRecoversOnRetry(t *testing.T) {
	t.Parallel()
	var log []string
	flaky := &recorder{name: "two", log: &log, fail: errors.New("transient"), replay: true}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, replay: true},
		flaky,
		&recorder{name: "three", log: &log, replay: true},
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext,  // after one
		pipeline.ActRerun, // after two failed: the operator has just fixed it
		pipeline.ActNext,  // after two succeeded; three needs no pause
	}}
	// fixOnPause stands in for the operator repairing the box while the run
	// sits paused on the failure, which is the entire point of pausing there.
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.Debug(&fixOnPause{
		inner: d, at: 2, fix: func() { flaky.fail = nil },
	}))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatalf("run should have recovered: %v", err)
	}
	if got := strings.Join(log, ","); got != "one,two,two,three" {
		t.Errorf("order = %q", got)
	}
}

// fixOnPause wraps a Debugger and runs fix the first time it pauses at step
// `at`, standing in for an operator repairing something on the box before
// pressing rerun.
type fixOnPause struct {
	inner pipeline.Debugger
	at    int
	fix   func()
	done  bool
}

func (f *fixOnPause) Plan(name string, steps []pipeline.PlanEntry) error {
	return f.inner.Plan(name, steps)
}
func (f *fixOnPause) Pause(p pipeline.Pause) (pipeline.Action, error) {
	if p.Position == f.at && !f.done {
		f.done = true
		f.fix()
	}
	return f.inner.Pause(p)
}
func (f *fixOnPause) Finish(fin pipeline.Finished) error { return f.inner.Finish(fin) }

// TestDebuggerErrorAbortsTheRun holds the disconnect contract: a debugger that
// has lost its operator stops the run rather than continuing unattended.
func TestDebuggerErrorAbortsTheRun(t *testing.T) {
	t.Parallel()
	var log []string
	// An empty script: the fake errors on the first pause, standing in for a
	// client that vanished.
	d := &pipeline.FakeDebugger{}
	err := run(t, threeSteps(&log), d)
	if !errors.Is(err, pipeline.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if got := strings.Join(log, ","); got != "one" {
		t.Errorf("order = %q, want the run to stop immediately", got)
	}
}

// TestPauseCarriesItsContext checks every field a client renders. A wrong
// Total or Next is invisible in the engine and obvious in the panel.
func TestPauseCarriesItsContext(t *testing.T) {
	t.Parallel()
	var log []string
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, replay: true},
		plain{name: "two"}, // does NOT implement Replayable
		&recorder{name: "three", log: &log, replay: true},
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{pipeline.ActNext, pipeline.ActNext}}
	if err := run(t, p, d); err != nil {
		t.Fatal(err)
	}
	got := d.Pauses()
	if len(got) != 2 {
		t.Fatalf("got %d pauses", len(got))
	}
	if got[0].Position != 1 || got[0].Total != 3 || got[0].Next != "two" {
		t.Errorf("pause 1 = %+v", got[0])
	}
	if !got[0].Replayable {
		t.Error("step one declared Replayable, pause says otherwise")
	}
	// A step that never considered the question is unsafe, the honest default.
	if got[1].Replayable {
		t.Error("a step not implementing Replayable must not be reported replayable")
	}
	if got[1].Next != "three" {
		t.Errorf("Next = %q, want the step that would run", got[1].Next)
	}
}

func TestPlanAndFinishAreCalledOnce(t *testing.T) {
	t.Parallel()
	var log []string
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext, pipeline.ActRerun, pipeline.ActNext,
	}}
	if err := run(t, threeSteps(&log), d); err != nil {
		t.Fatal(err)
	}
	if n := d.PlannedTimes(); n != 1 {
		t.Errorf("Plan called %d times, want 1 even with a replay", n)
	}
	if len(d.PlanEntries()) != 3 {
		t.Errorf("plan had %d entries, want 3", len(d.PlanEntries()))
	}
	done, err := d.Finished()
	if !done || err != nil {
		t.Errorf("Finish(%v), done=%v; want called with nil", err, done)
	}
}

// TestFinishReceivesTheFailure. A client must learn how a run ended rather
// than inferring it from a closed connection.
func TestFinishReceivesTheFailure(t *testing.T) {
	t.Parallel()
	var log []string
	boom := errors.New("boom")
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, fail: boom},
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{pipeline.ActNext}}
	_ = run(t, p, d)
	done, err := d.Finished()
	if !done || !errors.Is(err, boom) {
		t.Errorf("Finish(%v), done=%v; want the run's error", err, done)
	}
}

// redacting stands in for secret.Value: it refuses to render its contents
// through fmt. pipeline cannot import kit, so the containment property is
// pinned against the same mechanism secret.Value uses.
type redacting struct{ plaintext string }

func (redacting) String() string               { return "redacting(hidden)" }
func (r redacting) Format(f fmt.State, _ rune) { fmt.Fprint(f, r.String()) }

// TestDisplayCannotLeakARedactedValue is a redaction claim, so it gets a test.
// State.Display renders through fmt precisely so a value that closes its own
// formatting cannot have its plaintext pulled out by a debugger.
func TestDisplayCannotLeakARedactedValue(t *testing.T) {
	t.Parallel()
	const secretText = "ghp_thisMustNeverAppear"
	var captured map[string]string

	step := pipeline.Func{Label: "put", Gives: []pipeline.Key{"cred"},
		Do: func(_ context.Context, s *pipeline.State) error {
			pipeline.Set(s, "cred", redacting{plaintext: secretText})
			return nil
		}}
	d := &pipeline.FakeDebugger{}

	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{step}}
	if err := run(t, p, d); err != nil {
		t.Fatal(err)
	}
	captured = d.FinalState()
	for k, v := range captured {
		if strings.Contains(v, secretText) {
			t.Fatalf("Display leaked the plaintext under %q: %q", k, v)
		}
	}
	if captured["cred"] != "redacting(hidden)" {
		t.Errorf("cred rendered as %q", captured["cred"])
	}
}

func TestActionValid(t *testing.T) {
	t.Parallel()
	for _, a := range pipeline.ActionValues {
		if !a.Valid() {
			t.Errorf("%q is in ActionValues but not Valid", a)
		}
	}
	if pipeline.Action("proceed").Valid() {
		t.Error("an undeclared action must not validate")
	}
}

// TestNoPauseAfterTheLastSuccessfulStep pins the rule a live session exposed:
// pausing when nothing is left to decide lets an operator turn a completed run
// into an abandoned one by closing a window, and reports FAILED for a pipeline
// that did all of its work.
func TestNoPauseAfterTheLastSuccessfulStep(t *testing.T) {
	t.Parallel()
	var log []string
	// One scripted answer for three steps. A pause after the last one would
	// exhaust the script and fail the run.
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{pipeline.ActNext, pipeline.ActNext}}
	if err := run(t, threeSteps(&log), d); err != nil {
		t.Fatalf("a completed run reported %v", err)
	}
	if n := len(d.Pauses()); n != 2 {
		t.Errorf("paused %d times for 3 steps, want 2", n)
	}
	done, err := d.Finished()
	if !done || err != nil {
		t.Errorf("Finish(%v), done=%v; want a clean finish", err, done)
	}
}

// TestLastStepStillPausesOnFailure is the other half: retrying a failure is
// the whole reason for pausing on one, and the last step fails as often as any.
func TestLastStepStillPausesOnFailure(t *testing.T) {
	t.Parallel()
	var log []string
	flaky := &recorder{name: "three", log: &log, fail: errors.New("transient"), replay: true}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, replay: true},
		&recorder{name: "two", log: &log, replay: true},
		flaky,
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext,  // after one
		pipeline.ActNext,  // after two
		pipeline.ActRerun, // after three FAILED: must have paused
	}}
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.Debug(&fixOnPause{
		inner: d, at: 3, fix: func() { flaky.fail = nil },
	}))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatalf("the retry should have succeeded: %v", err)
	}
	if got := strings.Join(log, ","); got != "one,two,three,three" {
		t.Errorf("order = %q", got)
	}
}

// TestFinishCarriesTheFinalState. With no pause after the last step, this is
// the only place a client can learn what the run produced.
func TestFinishCarriesTheFinalState(t *testing.T) {
	t.Parallel()
	set := pipeline.Func{Label: "set", Gives: []pipeline.Key{"image"},
		Do: func(_ context.Context, s *pipeline.State) error {
			pipeline.Set(s, "image", "ghcr.io/acme/app:abc1234")
			return nil
		}}
	d := &pipeline.FakeDebugger{}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{set}}
	if err := run(t, p, d); err != nil {
		t.Fatal(err)
	}
	if got := d.FinalState()["image"]; got != "ghcr.io/acme/app:abc1234" {
		t.Errorf("final state image = %q", got)
	}
}

// TestQuitAfterAFailureReportsTheFailure. The run failed because the STEP
// failed; the operator only declined to retry it. Reporting that as ErrAborted
// names the wrong cause, and "aborted" is exactly what someone reading the log
// afterwards would not investigate.
func TestQuitAfterAFailureReportsTheFailure(t *testing.T) {
	t.Parallel()
	var log []string
	boom := errors.New("exit 125")
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, replay: true},
		&recorder{name: "two", log: &log, fail: boom, replay: true},
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActNext, // after one
		pipeline.ActQuit, // after two FAILED: give up rather than retry
	}}
	err := run(t, p, d)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the step's error", err)
	}
	if errors.Is(err, pipeline.ErrAborted) {
		t.Errorf("err = %v, want the failure named as the cause, not the choice not to fix it", err)
	}
}

// TestContinueKeepsTheDebuggerForFailures. Continue means "stop asking me
// between steps that work", not "detach". Treating it as a detach threw away
// the pause at the one moment it is worth the most: a later failure, which
// would then abort a run the operator could have retried.
func TestContinueKeepsTheDebuggerForFailures(t *testing.T) {
	t.Parallel()
	var log []string
	flaky := &recorder{name: "three", log: &log, fail: errors.New("transient"), replay: true}
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		&recorder{name: "one", log: &log, replay: true},
		&recorder{name: "two", log: &log, replay: true},
		flaky,
		&recorder{name: "four", log: &log, replay: true},
	}}
	d := &pipeline.FakeDebugger{Script: []pipeline.Action{
		pipeline.ActContinue, // after one: run the rest unattended
		pipeline.ActRerun,    // reached ONLY if the failure still pauses
	}}
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.Debug(&fixOnPause{
		inner: d, at: 3, fix: func() { flaky.fail = nil },
	}))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatalf("the failure did not pause, so it could not be retried: %v", err)
	}
	if got := strings.Join(log, ","); got != "one,two,three,three,four" {
		t.Errorf("order = %q, want the failed step retried and the run finished", got)
	}
	// Two pauses only: the one that chose continue, and the failure. Steps two
	// and four succeeded and must not have asked.
	if n := len(d.Pauses()); n != 2 {
		t.Errorf("paused %d times, want 2 (the continue and the failure)", n)
	}
}
