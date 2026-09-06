package main

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/session"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/pipeline/debug"
)

// newPanel builds a panel with the given plan, as drivePanel would.
func newPanel(names ...string) *panel {
	p := &panel{
		status: map[int]string{}, took: map[int]string{},
		detail: map[int][]string{}, output: map[int][]string{}, dropped: map[int]int{},
		began: map[int]time.Time{}, showState: true,
		project: "proj", target: "deploy local",
	}
	for i, n := range names {
		p.steps = append(p.steps, pipeline.PlanEntry{Position: i + 1, Total: len(names), Name: n})
	}
	return p
}

// TestAddOutputKeepsATailAndCountsTheRest. The panel has no scrollback, so a
// build's hundreds of lines must not push the step list off the screen. What
// is dropped has to be COUNTED, or the panel silently implies it showed
// everything.
func TestAddOutputKeepsATailAndCountsTheRest(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	const total = outputTail + 20
	for i := 0; i < total; i++ {
		p.addOutput(1, "line")
	}
	if got := len(p.output[1]); got != outputTail {
		t.Errorf("kept %d lines, want %d", got, outputTail)
	}
	if got := p.dropped[1]; got != total-outputTail {
		t.Errorf("dropped count = %d, want %d", got, total-outputTail)
	}
}

func TestAddOutputBelowTheTailDropsNothing(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	p.addOutput(1, "a")
	p.addOutput(1, "b")
	if p.dropped[1] != 0 {
		t.Errorf("dropped %d with only two lines", p.dropped[1])
	}
	if len(p.output[1]) != 2 {
		t.Errorf("kept %v", p.output[1])
	}
}

// TestCurrentAttributesOutputToTheRunningStep, detail and output lines arrive
// without a step number, so they are attributed to whatever is running. Wrong
// attribution puts a build's output under the wrong heading.
func TestCurrentAttributesOutputToTheRunningStep(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two", "three")
	p.status[1] = statusOK
	p.status[2] = statusRunning
	if got := p.current(); got != 2 {
		t.Errorf("current() = %d, want the running step", got)
	}
	// With nothing running, between steps, or at the end, output belongs to
	// the last step rather than being dropped.
	p.status[2] = statusOK
	if got := p.current(); got != 3 {
		t.Errorf("current() with nothing running = %d, want the last step", got)
	}
}

// TestLiveRespectsTheYield holds the rule that keeps the panel from painting
// through a tool's own progress display.
func TestLiveRespectsTheYield(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	p.yielded = true
	before := p.lastDraw
	p.live()
	if !p.lastDraw.Equal(before) {
		t.Error("panel drew while the running step owned the screen")
	}
}

// TestLiveThrottles. A build emits output faster than a terminal can usefully
// repaint, and drawing per line tears the screen.
func TestLiveThrottles(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	p.lastDraw = time.Now()
	before := p.lastDraw
	p.live()
	if !p.lastDraw.Equal(before) {
		t.Error("repainted twice inside the throttle window")
	}
}

func TestKeyHelpListsEveryAcceptedKey(t *testing.T) {
	t.Parallel()
	help := (&panel{}).keyHelp(false)
	for _, ka := range keyActions {
		if !strings.Contains(help, ka.Key) || !strings.Contains(help, ka.Label) {
			t.Errorf("key help omits %q/%q: %s", ka.Key, ka.Label, help)
		}
	}
	if !strings.Contains(help, keyState) {
		t.Errorf("key help omits the state toggle: %s", help)
	}
}

// TestKeyHelpAfterFailureOffersOnlyWhatWorks. Once a step has failed there is
// no next step to advance to, so the help must stop naming one: a prompt that
// offers a key the parser refuses is worse than one that offers nothing.
func TestKeyHelpAfterFailureOffersOnlyWhatWorks(t *testing.T) {
	t.Parallel()
	help := (&panel{}).keyHelp(true)
	for _, ka := range keyActions {
		switch {
		case ka.AfterFailure:
			if !strings.Contains(help, ka.Key) || !strings.Contains(help, ka.FailureLabel) {
				t.Errorf("failure help omits %q/%q: %s", ka.Key, ka.FailureLabel, help)
			}
		case strings.Contains(help, ka.Label):
			t.Errorf("failure help still offers %q (%s): %s", ka.Key, ka.Label, help)
		}
	}
}

// TestPromptAfterFailureRefusesToAdvance is the regression guard for the
// footgun that prompted all of this: an operator who pressed n at every one of
// nine steps pressed it once more at the failure, and the run they meant to
// retry ended instead. Neither n nor Enter nor c may resolve the prompt while
// a step is failed; only r and q may.
func TestPromptAfterFailureRefusesToAdvance(t *testing.T) {
	t.Parallel()
	failure := debug.Event{N: 1, Err: "docker run: exit 125"}
	for _, key := range []string{keyNext + "\n", "\n", keyContinue + "\n"} {
		p := newPanel("one", "two")
		// The refused key, then a retry: reaching the retry proves the first
		// keystroke re-prompted rather than resolving.
		in := bufio.NewReader(strings.NewReader(key + keyRerun + "\n"))
		got, quit := p.prompt(in, failure)
		if quit || got != pipeline.ActRerun {
			t.Errorf("key %q at a failed step gave %q/quit=%v, want a re-prompt then rerun",
				strings.TrimSpace(key), got, quit)
		}
	}
}

// TestPromptAfterFailureQuitIsNotAnAbandon. The run failed because the STEP
// failed; declining to retry is not the cause, and reporting it as one hides
// the real one. See pipeline.Run, which reports the step error for both.
func TestPromptAfterFailureQuitIsNotAnAbandon(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	in := bufio.NewReader(strings.NewReader(keyQuit + "\n"))
	got, quit := p.prompt(in, debug.Event{N: 1, Err: "boom"})
	if quit || got != pipeline.ActQuit {
		t.Errorf("quit after a failure gave %q/quit=%v, want a quit action reported as the step failure", got, quit)
	}
}

// TestPromptAfterFailureSkipsTheReplayWarning. A step that failed did not
// finish, so there is nothing to repeat and nothing to warn about; asking
// anyway puts one more key between the operator and the fix they just made.
func TestPromptAfterFailureSkipsTheReplayWarning(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	// Replayable is false: unprompted, this would demand a confirmation, and
	// the reader holds no answer to give it.
	in := bufio.NewReader(strings.NewReader(keyRerun + "\n"))
	got, quit := p.prompt(in, debug.Event{N: 1, Err: "boom", Replayable: false})
	if quit || got != pipeline.ActRerun {
		t.Errorf("retrying a failed step gave %q/quit=%v, want an unprompted rerun", got, quit)
	}
}

// TestPromptAcceptsEveryOfferedKey is the drift guard between what the prompt
// OFFERS and what it ACCEPTS: a key shown in the help that the parser rejects
// is a bug the help itself hides.
func TestPromptAcceptsEveryOfferedKey(t *testing.T) {
	t.Parallel()
	for _, ka := range keyActions {
		p := newPanel("one", "two")
		in := bufio.NewReader(strings.NewReader(ka.Key + "\n"))
		got, quit := p.prompt(in, debug.Event{N: 1, Replayable: true})
		if ka.Action == pipeline.ActQuit {
			if !quit {
				t.Errorf("key %q did not quit", ka.Key)
			}
			continue
		}
		if quit || got != ka.Action {
			t.Errorf("key %q gave %q/quit=%v, want %q", ka.Key, got, quit, ka.Action)
		}
	}
}

// TestPromptBareEnterMeansNext. The commonest keystroke, and it must not be
// an error.
func TestPromptBareEnterMeansNext(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	got, quit := p.prompt(bufio.NewReader(strings.NewReader("\n")), debug.Event{N: 1})
	if quit || got != pipeline.ActNext {
		t.Errorf("Enter gave %q/quit=%v, want next", got, quit)
	}
}

// TestPromptRepromptsOnNonsense. The alternative to re-prompting is a
// default, and the only sensible default would be "carry on". Advancing a
// deploy because someone leaned on a key is the failure this design exists to
// prevent.
func TestPromptRepromptsOnNonsense(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	in := bufio.NewReader(strings.NewReader("zzz\nq\n"))
	got, quit := p.prompt(in, debug.Event{N: 1})
	if !quit || got != pipeline.ActQuit {
		t.Errorf("unrecognised input was not re-prompted: got %q/quit=%v", got, quit)
	}
}

// TestPromptEOFQuits, Ctrl-D means "I am done here", not a broken terminal.
// The same reading kit/confirm gives it.
func TestPromptEOFQuits(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	got, quit := p.prompt(bufio.NewReader(strings.NewReader("")), debug.Event{N: 1})
	if !quit || got != pipeline.ActQuit {
		t.Errorf("EOF gave %q/quit=%v, want a quit", got, quit)
	}
}

// TestRerunOfAnUnmarkedStepAsksFirst is the safety prompt: replaying a step
// that starts containers must not happen on one keystroke.
func TestRerunOfAnUnmarkedStepAsksFirst(t *testing.T) {
	t.Parallel()
	// "r" then "n", decline the confirmation, then quit.
	p := newPanel("one", "two")
	in := bufio.NewReader(strings.NewReader("r\nn\nq\n"))
	got, quit := p.prompt(in, debug.Event{N: 1, Name: "start-processes", Replayable: false})
	if got == pipeline.ActRerun {
		t.Error("replayed a step that is not marked replayable, without asking")
	}
	if !quit {
		t.Errorf("after declining, got %q", got)
	}
}

func TestRerunOfAnUnmarkedStepProceedsOnYes(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	in := bufio.NewReader(strings.NewReader("r\ny\n"))
	got, quit := p.prompt(in, debug.Event{N: 1, Name: "start-processes", Replayable: false})
	if quit || got != pipeline.ActRerun {
		t.Errorf("confirming gave %q/quit=%v, want a rerun", got, quit)
	}
}

// TestRerunOfAReplayableStepDoesNotAsk. A prompt on a safe step trains the
// operator to answer without reading, which is how the unsafe prompt stops
// working.
func TestRerunOfAReplayableStepDoesNotAsk(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	// Only "r" is supplied: needing a second answer would exhaust the input
	// and return a quit instead.
	in := bufio.NewReader(strings.NewReader("r\n"))
	got, quit := p.prompt(in, debug.Event{N: 1, Name: "resolve-commit", Replayable: true})
	if quit || got != pipeline.ActRerun {
		t.Errorf("a replayable step asked for confirmation: got %q/quit=%v", got, quit)
	}
}

// TestStateToggle, 's' shows and hides the pane rather than being an action.
func TestStateToggle(t *testing.T) {
	t.Parallel()
	p := newPanel("one", "two")
	p.showState = true
	in := bufio.NewReader(strings.NewReader(keyState + "\nq\n"))
	if _, quit := p.prompt(in, debug.Event{N: 1}); !quit {
		t.Fatal("expected the quit that followed")
	}
	if p.showState {
		t.Error("'s' did not hide the state pane")
	}
}

func TestElapsed(t *testing.T) {
	t.Parallel()
	if got := elapsed(time.Time{}); got != "" {
		t.Errorf("elapsed(zero) = %q, want empty: a step that never started has no clock", got)
	}
	if got := elapsed(time.Now().Add(-3 * time.Second)); got != "3s" {
		t.Errorf("elapsed = %q, want 3s", got)
	}
	if got := elapsed(time.Now().Add(-95 * time.Second)); got != "1m35s" {
		t.Errorf("elapsed = %q, want 1m35s", got)
	}
}

func TestSince(t *testing.T) {
	t.Parallel()
	if got := since(time.Now().Add(-5 * time.Second)); got != "5s" {
		t.Errorf("since = %q, want 5s", got)
	}
	if got := since(time.Now().Add(-90 * time.Second)); !strings.Contains(got, "m") {
		t.Errorf("since = %q, want minutes for 90s", got)
	}
}

func TestSortedKeysIsStable(t *testing.T) {
	t.Parallel()
	m := map[string]string{"image": "x", "commit": "y", "branch": "z"}
	a := strings.Join(sortedKeys(m), ",")
	for i := 0; i < 20; i++ {
		if b := strings.Join(sortedKeys(m), ","); b != a {
			t.Fatalf("state pane order is unstable: %q then %q", a, b)
		}
	}
	if a != "branch,commit,image" {
		t.Errorf("sortedKeys = %q, want alphabetical", a)
	}
}

// TestPanelLineMarksEveryStatus. The gutter marker is how a reader scans the
// list, so each state needs its own glyph.
func TestPanelLineMarksEveryStatus(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	entry := p.steps[0]

	seen := map[string]string{}
	for _, st := range []string{"", statusOK, statusFailed, statusRunning} {
		p.status[1] = st
		seen[st] = strings.Fields(p.line(entry))[0]
	}
	if seen[statusOK] == seen[statusFailed] {
		t.Error("success and failure render the same marker")
	}
	if seen[statusFailed] == seen[""] {
		t.Error("a failed step is indistinguishable from one not yet reached")
	}
	if !strings.Contains(p.line(entry), "one") {
		t.Error("the step name is missing from its row")
	}
}

// TestPanelLineShowsARunningClock. The number is the reassurance: a build at
// 1m20s is working, a panel frozen at 1m20s is not.
func TestPanelLineShowsARunningClock(t *testing.T) {
	t.Parallel()
	p := newPanel("one")
	p.status[1] = statusRunning
	p.began[1] = time.Now().Add(-7 * time.Second)
	if got := p.line(p.steps[0]); !strings.Contains(got, "7s") {
		t.Errorf("running row %q carries no elapsed time", got)
	}
}

// TestChooseSessionAttachesToTheOnlyOne, ambiguity should cost a keystroke,
// and its absence should cost nothing.
func TestChooseSessionAttachesToTheOnlyOne(t *testing.T) {
	t.Parallel()
	only := session.Session{ID: "a-1", Meta: session.Meta{"project": "p"}}
	got, err := chooseSession([]session.Session{only})
	if err != nil || got.ID != only.ID {
		t.Errorf("chooseSession = %+v, %v", got, err)
	}
}

// TestFailureHintNamesTheKeysThatWork. The hint is printed every time a
// keystroke is refused, so it is the only instruction an operator gets at the
// moment they are stuck. It must name exactly the keys that resolve a failed
// pause, and it is derived rather than written out so it cannot outlive a
// rebinding.
func TestFailureHintNamesTheKeysThatWork(t *testing.T) {
	t.Parallel()
	hint := failureHint()
	for _, ka := range keyActions {
		switch {
		case ka.AfterFailure:
			if !strings.Contains(hint, ka.Key) || !strings.Contains(hint, ka.FailureLabel) {
				t.Errorf("hint omits %q/%q: %s", ka.Key, ka.FailureLabel, hint)
			}
		case strings.Contains(hint, "press "+ka.Key+" "):
			t.Errorf("hint offers %q, which does nothing at a failed step: %s", ka.Key, hint)
		}
	}
}
