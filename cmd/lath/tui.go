package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/confirm"
	"github.com/ubgo/lath/kit/session"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/pipeline/debug"
)

// Keys the panel accepts. Single letters rather than arrows: this is a
// prompt, not a navigable list, and a single keystroke plus Enter works
// identically over ssh, in a recording, and when driven by a script.
const (
	keyNext     = "n"
	keyRerun    = "r"
	keyContinue = "c"
	keyQuit     = "q"
	keyState    = "s"
)

// keyActions maps a keystroke to the action it sends. The canonical source for
// both the prompt and the parser, so what is offered and what is accepted
// cannot drift.
//
// AfterFailure marks the keys that still mean something once a step has
// failed. "next" and "continue" do not: there is no next step to advance to
// and nothing to continue, so offering them is how an operator pressing n for
// the tenth time in a row abandons a run they meant to retry. FailureLabel is
// how the remaining two are named at that point, where "rerun" is really
// "retry" and "quit" is really "give up".
var keyActions = []struct {
	Key          string
	Action       pipeline.Action
	Label        string
	AfterFailure bool
	FailureLabel string
}{
	{keyNext, pipeline.ActNext, "next", false, ""},
	{keyRerun, pipeline.ActRerun, "rerun", true, "retry this step"},
	{keyContinue, pipeline.ActContinue, "continue", false, ""},
	{keyQuit, pipeline.ActQuit, "quit", true, "give up"},
}

// runTUI attaches to a debug session and drives it.
//
// Usage: lath tui [session-id]
func runTUI(args []string) int {
	sessions, err := session.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	// An argument names EITHER a waiting session or a target to run, and which
	// is decided by whether it matches a session, not by whether any session
	// happens to exist.
	//
	// Getting that backwards was a real bug: with a session waiting,
	// `lath tui deploy local --apply` was read as "attach to the session called
	// deploy" and failed. A target is the far commoner meaning, and a session
	// id is an opaque hash nobody types from memory, so the match has to come
	// first and the fallback has to be "run it".
	if len(args) > 0 {
		if chosen, ok := findSession(sessions, args[0]); ok {
			return attachTo(chosen)
		}
		return launchAndAttach(args)
	}

	// Nothing waiting means START something, not fail. Requiring a run to
	// already exist in another terminal made the common case, "I want to step
	// through this". A two-terminal ritual with a timeout in the middle, and
	// missing that window looked like the tool was broken.
	if len(sessions) == 0 {
		return launchAndAttach(nil)
	}

	chosen, err := chooseSession(sessions)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitUsage
	}
	return attachTo(chosen)
}

// findSession looks up a waiting session by id.
func findSession(sessions []session.Session, id string) (session.Session, bool) {
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return session.Session{}, false
}

// attachTo drives a session that is already running.
func attachTo(chosen session.Session) int {

	cli, err := debug.Dial(chosen.Socket)
	if err != nil {
		// The common cause is a session that ended between the listing and
		// the dial, which is a race no amount of locking removes, say so
		// rather than printing a bare connection error.
		fmt.Fprintln(os.Stderr, "lath:", err)
		fmt.Fprintln(os.Stderr, "      the run may have finished; try lath tui again")
		return exitInternalErr
	}
	defer cli.Close()

	return drivePanel(cli, debug.Event{}, false, chosen)
}

// chooseSession resolves which session to attach to.
//
// One waiting session attaches with no prompt: ambiguity should cost a
// keystroke, and its absence should cost nothing.
func chooseSession(sessions []session.Session) (session.Session, error) {
	if len(sessions) == 1 {
		return sessions[0], nil
	}

	fmt.Fprint(os.Stderr, "\n  waiting sessions:\n\n")
	for i, s := range sessions {
		fmt.Fprintf(os.Stderr, "    %d) %-12s %-28s %s ago\n",
			i+1, s.Meta["project"], s.Meta["target"], since(s.Since))
	}
	fmt.Fprintf(os.Stderr, "\n  attach [1-%d]: ", len(sessions))

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return session.Session{}, fmt.Errorf("reading a choice: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(sessions) {
		return session.Session{}, fmt.Errorf("pick a number between 1 and %d", len(sessions))
	}
	return sessions[n-1], nil
}

// panel is the render state: the plan, what has happened to each step, and the
// values steps have set.
type panel struct {
	pipelineName string
	steps        []pipeline.PlanEntry
	status       map[int]string
	took         map[int]string
	detail       map[int][]string
	state        map[string]string
	output       map[int][]string
	dropped      map[int]int
	began        map[int]time.Time
	yielded      bool
	lastDraw     time.Time
	frame        int
	showState    bool
	project      string
	target       string
}

// Step statuses, as rendered in the left gutter. The zero value is "not yet
// reached", which needs no name because nothing ever assigns it.
const (
	statusRunning = "running"
	statusOK      = "ok"
	statusFailed  = "FAILED"
)

// drivePanel reads events, renders, and sends the operator's decisions.
//
// first is the event that has already been read, the one that proved there was
// a session to drive, or the zero Event when the caller read nothing.
// yieldedTTY says the run owns the terminal: it draws while it works, and the
// panel draws only at pauses. See pipeline.YieldTerminal.
func drivePanel(cli *debug.Client, first debug.Event, yieldedTTY bool, s session.Session) int {
	p := &panel{
		status: map[int]string{}, took: map[int]string{},
		detail: map[int][]string{}, output: map[int][]string{}, dropped: map[int]int{},
		began:     map[int]time.Time{},
		yielded:   yieldedTTY,
		showState: true,
		project:   s.Meta["project"], target: s.Meta["target"],
	}
	in := bufio.NewReader(os.Stdin)
	// Tracked so an immediate EOF can be explained rather than reported as a
	// bare read error. A session that ends before its first event has almost
	// always timed out waiting to be attached to, and saying so is the
	// difference between a fix and a puzzle.
	sawAnything := first.Kind != ""
	pending := first

	// Events are read on their own goroutine so the loop below can also wake
	// on a timer. Without that, a step that goes quiet, and docker build is
	// silent for the whole of a Go compile, leaves the panel frozen on its
	// last frame, which is exactly what a hung run looks like.
	type incoming struct {
		e   debug.Event
		err error
	}
	events := make(chan incoming, eventBuffer)
	go func() {
		defer close(events)
		for {
			e, err := cli.Next()
			events <- incoming{e, err}
			if err != nil {
				return
			}
		}
	}()

	tick := time.NewTicker(spinnerEvery)
	defer tick.Stop()

	for {
		e := pending
		var err error
		if e.Kind == "" {
			var got bool
			for !got {
				select {
				case in, open := <-events:
					if !open {
						return exitPipeline
					}
					e, err, got = in.e, in.err, true
				case <-tick.C:
					// Nothing happened; repaint so the spinner turns and the
					// elapsed time advances, unless the running step owns the
					// screen, in which case it is drawing and we are not.
					// Calling render directly here once bypassed that check
					// and painted the panel through docker's own table.
					p.live()
				}
			}
		}
		pending = debug.Event{}
		if err != nil {
			if !sawAnything {
				fmt.Fprintln(os.Stderr, "lath: the run ended before this could attach.")
				fmt.Fprintf(os.Stderr,
					"      it waits %s for a debugger, then gives up. Start it again and\n"+
						"      run `lath %s` while it still says it is waiting.\n",
					debug.DefaultAttachTimeout, VerbTUI)
				return exitPipeline
			}
			// Ended mid-session: killed, or the socket died.
			fmt.Fprintln(os.Stderr, "\nlath: session ended:", err)
			return exitPipeline
		}
		sawAnything = true
		switch e.Kind {
		case debug.KindPlan:
			p.pipelineName, p.steps = e.Pipeline, e.Steps
			// Drawn immediately on attach. Without this the panel stays blank
			// until the first pause, which on a pipeline whose first step is a
			// two-minute docker build looks exactly like a hang.
			p.render()
			fmt.Fprintf(os.Stderr, "\n  attached: %d steps. Running…\n", len(e.Steps))
		case debug.KindStart:
			p.status[e.N] = statusRunning
			p.began[e.N] = time.Now()
			p.live()
		case debug.KindDetail:
			// Attributed to whichever step is running, so the detail lines
			// stay with their step when the panel redraws.
			p.detail[p.current()] = append(p.detail[p.current()], e.Msg)
			p.live()
		case debug.KindOutput:
			p.addOutput(p.current(), e.Msg)
			p.live()
		case debug.KindDone:
			p.status[e.N], p.took[e.N] = statusOK, e.Took
			p.live()
		case debug.KindPaused:
			if e.Err != "" {
				p.status[e.N] = statusFailed
				p.detail[e.N] = append(p.detail[e.N], e.Err)
			} else {
				p.status[e.N] = statusOK
			}
			p.state = e.State
			p.render()
			act, quit := p.prompt(in, e)
			if quit {
				return exitOK
			}
			if err := cli.Send(act, e.N); err != nil {
				fmt.Fprintln(os.Stderr, "lath:", err)
				return exitInternalErr
			}
			if act == pipeline.ActRerun {
				// The step is about to run again; clear its previous outcome
				// so the panel does not show a stale failure beside a retry,
				// nor the output of the attempt being replaced.
				delete(p.took, e.N)
				delete(p.dropped, e.N)
				p.detail[e.N] = nil
				p.output[e.N] = nil
			}
			if act == pipeline.ActContinue {
				fmt.Fprint(os.Stderr, "\n  continuing: no further pauses\n\n")
			}
		case debug.KindFinished:
			// The final state arrives here rather than at a pause: there is
			// no pause after the last step, so this is the only place the run
			// can say what it produced.
			if len(e.State) > 0 {
				p.state = e.State
			}
			p.render()
			if e.Err != "" {
				fmt.Fprintf(os.Stderr, "\n  FAILED: %s\n\n", e.Err)
				return exitPipeline
			}
			fmt.Fprintf(os.Stderr, "\n  %s finished\n\n", p.pipelineName)
			return exitOK
		}
	}
}

// outputTail is how many of a step's output lines the panel keeps.
//
// A docker build emits hundreds; rendering all of them would push the step
// list off the screen on every redraw, and this panel has no scrollback yet.
// The full text is in the run's log either way, so what is kept here is the
// part that is useful without it. The last thing the tool said before it
// stopped or moved on.
const outputTail = 8

// addOutput records a command line, keeping only the most recent ones and
// counting what was dropped so the panel can say so rather than pretending it
// showed everything.
func (p *panel) addOutput(step int, line string) {
	lines := append(p.output[step], line)
	if len(lines) > outputTail {
		p.dropped[step] += len(lines) - outputTail
		lines = lines[len(lines)-outputTail:]
	}
	p.output[step] = lines
}

// eventBuffer keeps the reader goroutine from blocking while the loop is busy
// rendering or waiting at a prompt. Sized for a burst of build output rather
// than for a whole run. A full buffer only slows the reader, never loses.
const eventBuffer = 256

// spinnerEvery is how often the panel wakes with nothing to report.
//
// Fast enough that the spinner reads as motion, slow enough to be free. This
// is the ONLY thing that distinguishes "docker is compiling and says nothing
// for a minute" from "this has hung", and before it existed there was no way
// to tell.
const spinnerEvery = 120 * time.Millisecond

// spinnerFrames is a braille dot cycle. One cell wide in every monospace font
// and legible without colour.
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// redrawEvery bounds how often the panel repaints while a step runs.
//
// A docker build emits output faster than a terminal can usefully redraw, and
// repainting per line makes the screen tear and burns CPU on escape codes. Ten
// frames a second reads as live to a person and costs nothing.
const redrawEvery = 100 * time.Millisecond

// live repaints if enough time has passed since the last frame.
//
// Called as events arrive rather than only at pauses. Without it the panel sat
// frozen for the whole of a long step, output accumulated invisibly and the
// screen only caught up when the run stopped, which is indistinguishable from
// a hang and defeats the point of streaming the output at all.
func (p *panel) live() {
	if p.yielded {
		// The running step owns the screen. Drawing now would paint the panel
		// over whatever it is rendering, and be painted over in turn.
		return
	}
	if time.Since(p.lastDraw) < redrawEvery {
		return
	}
	p.render()
}

// current returns the step presently running, for attributing detail lines.
func (p *panel) current() int {
	for i := 1; i <= len(p.steps); i++ {
		if p.status[i] == statusRunning {
			return i
		}
	}
	return len(p.steps)
}

// prompt asks what to do next and returns the action, or quit.
//
// Unrecognised input re-prompts rather than defaulting: the default would be
// to advance, and advancing a deploy because someone leaned on a key is the
// failure this whole design is arranged to prevent.
func (p *panel) prompt(in *bufio.Reader, e debug.Event) (pipeline.Action, bool) {
	failed := e.Err != ""
	for {
		if failed {
			fmt.Fprintf(os.Stderr,
				"\n  step %d failed. Fix whatever broke and retry it, or give up.\n", e.N)
		}
		fmt.Fprint(os.Stderr, "\n  "+p.keyHelp(failed)+" ▸ ")

		line, err := in.ReadString('\n')
		if err != nil {
			// EOF is Ctrl-D, which means "I am done here", a quit, not a
			// broken terminal. Same reading kit/confirm gives it.
			fmt.Fprintln(os.Stderr)
			return pipeline.ActQuit, true
		}
		switch key := strings.ToLower(strings.TrimSpace(line)); key {
		case keyState:
			p.showState = !p.showState
			p.render()
		case keyQuit:
			// Quitting a run that is fine is abandoning it; quitting after a
			// failure is only declining to retry, and the pipeline reports the
			// step failure as the cause either way.
			return pipeline.ActQuit, !failed
		case keyRerun:
			// A step that already failed is being retried, not replayed: it
			// did not finish, so the "may repeat what it did" warning does not
			// apply and asking for it would be one more key between the
			// operator and the fix they just made.
			if !failed && !e.Replayable && !p.confirmRerun(in, e) {
				continue
			}
			return pipeline.ActRerun, false
		case "":
			// A bare Enter is the commonest keystroke and means "carry on".
			if failed {
				// After a failure it is muscle memory, and must not be the
				// thing that ends the run.
				fmt.Fprintln(os.Stderr, "  this step failed: "+failureHint())
				continue
			}
			return pipeline.ActNext, false
		default:
			for _, ka := range keyActions {
				if ka.Key != key {
					continue
				}
				if failed && !ka.AfterFailure {
					fmt.Fprintf(os.Stderr,
						"  %q means nothing once a step has failed: %s\n", key, failureHint())
					break
				}
				return ka.Action, false
			}
			fmt.Fprintf(os.Stderr, "  ? %q: %s\n", key, p.keyHelp(failed))
		}
	}
}

// confirmRerun asks before replaying a step that has not declared itself safe.
//
// The warning names what the step already did, because "this may not be
// idempotent" is abstract and "a container is already running" is not.
func (p *panel) confirmRerun(in *bufio.Reader, e debug.Event) bool {
	fmt.Fprintf(os.Stderr, "\n  ⚠ %s is not marked replayable.\n", e.Name)
	if len(p.detail[e.N]) > 0 {
		fmt.Fprintf(os.Stderr, "    it already reported: %s\n", p.detail[e.N][len(p.detail[e.N])-1])
	}
	fmt.Fprintln(os.Stderr, "    running it again may repeat whatever it did.")
	fmt.Fprint(os.Stderr, "\n  rerun anyway? [y/N] ▸ ")

	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	// The affirmative set lives in kit/confirm, so every prompt in the
	// codebase accepts the same words. Not confirm.Yes itself: that owns its
	// own reader and refuses without a TTY, and this panel already holds the
	// reader and must stay drivable from a pipe.
	return confirm.IsAffirmative(line)
}

// keyHelp renders the accepted keys from keyActions.
// failureHint names the only two keys that do anything at a failed step.
//
// Built from keyActions rather than written out, because it appears wherever a
// refused keystroke is answered and a hardcoded "press r" would keep saying r
// after somebody rebound the key.
func failureHint() string {
	keys := make([]string, 0, len(keyActions))
	for _, ka := range keyActions {
		if ka.AfterFailure {
			keys = append(keys, "press "+ka.Key+" to "+ka.FailureLabel)
		}
	}
	return strings.Join(keys, ", or ")
}

// After a failure it lists only the keys that still mean something, so the
// prompt cannot offer one the parser will refuse.
func (p *panel) keyHelp(failed bool) string {
	parts := make([]string, 0, len(keyActions)+1)
	for _, ka := range keyActions {
		switch {
		case !failed:
			parts = append(parts, ka.Key+" "+ka.Label)
		case ka.AfterFailure:
			parts = append(parts, ka.Key+" "+ka.FailureLabel)
		}
	}
	parts = append(parts, keyState+" state")
	return strings.Join(parts, " · ")
}

// render draws the panel: the step list with a cursor, then the state.
//
// Redrawn in place by clearing the screen rather than by moving the cursor
// around, which keeps this readable and survives a resize. The pipeline's own
// terminal still shows the scrolling log, so nothing is lost by not keeping
// history here.
func (p *panel) render() {
	p.lastDraw = time.Now()
	p.frame++
	width := p.width()

	var b strings.Builder
	b.WriteString(clearScreen)

	title := p.pipelineName
	if p.project != "" {
		title = p.project + " · " + p.target
	}
	b.WriteString("\n  " + title + "\n\n")

	for _, s := range p.steps {
		b.WriteString("  " + p.line(s) + "\n")
		for _, d := range p.detail[s.Position] {
			b.WriteString(wrap(d, "        ", width))
		}
		if n := p.dropped[s.Position]; n > 0 {
			b.WriteString(fmt.Sprintf("          … %d earlier line(s), see the log\n", n))
		}
		for _, o := range p.output[s.Position] {
			b.WriteString(wrap(o, "          ", width))
		}
	}
	if p.showState && len(p.state) > 0 {
		b.WriteString("\n  state\n")
		for _, k := range sortedKeys(p.state) {
			b.WriteString(wrap(fmt.Sprintf("%-12s %s", k, p.state[k]), "    ", width))
		}
	}
	fmt.Fprint(os.Stderr, b.String())
}

// line renders one step row.
func (p *panel) line(s pipeline.PlanEntry) string {
	marker := " "
	switch p.status[s.Position] {
	case statusOK:
		marker = "✓"
	case statusFailed:
		marker = "✗"
	case statusRunning:
		marker = spinnerFrames[p.frame%len(spinnerFrames)]
	}
	row := fmt.Sprintf("%s %2d/%d  %-18s", marker, s.Position, s.Total, s.Name)
	switch {
	case p.took[s.Position] != "":
		row += "  " + p.took[s.Position]
	case p.status[s.Position] == statusRunning:
		// A running clock, because the number itself is the reassurance: a
		// build at 1m20s is working, the same panel frozen at 1m20s is not.
		row += "  " + elapsed(p.began[s.Position])
	}
	if p.status[s.Position] == statusFailed {
		row += "  " + statusFailed
	}
	return strings.TrimRight(row, " ")
}

// clearScreen resets the terminal between redraws.
//
// The two plain ANSI sequences every terminal has understood for decades ,
// erase display, cursor home. Hand-written rather than pulled from a library
// because cmd/lath ships without dependencies, and a panel that needed a
// dependency tree would land that cost on everyone who installs lath.
const clearScreen = "\033[2J\033[H"

// fallbackWidth is used when the terminal's size cannot be read, piped
// output, a recording, a platform without the ioctl.
const fallbackWidth = 100

// minWidth keeps wrapping sane in a very narrow window: below this, indenting
// leaves no room for content and every line becomes one character.
const minWidth = 40

// width reports how wide the panel may draw.
func (p *panel) width() int {
	if w := terminalWidth(os.Stderr); w >= minWidth {
		return w
	}
	return fallbackWidth
}

// wrap renders s at the given indent, folding rather than truncating.
//
// Truncating was the original behaviour and it hid the only line that
// mattered: a failing build reported "Cannot connect to the Docker daemon at
// uni…" and the rest, which names the socket, was gone. A wrapped line is
// untidy; a cut one loses the answer.
func wrap(s, indent string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	room := width - len(indent)
	if room < 1 {
		room = 1
	}
	var b strings.Builder
	for len(s) > room {
		// Broken at a space when there is one in the last quarter of the line,
		// so a long path or image digest is not split mid-token when it does
		// not have to be.
		cut := room
		if i := strings.LastIndex(s[:room], " "); i > room*3/4 {
			cut = i
		}
		b.WriteString(indent + s[:cut] + "\n")
		s = strings.TrimPrefix(s[cut:], " ")
	}
	b.WriteString(indent + s + "\n")
	return b.String()
}

// sortedKeys returns m's keys in a stable order, so a redraw does not shuffle
// the state pane.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// elapsed renders how long a running step has been going.
func elapsed(start time.Time) string {
	if start.IsZero() {
		return ""
	}
	d := time.Since(start).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// since renders how long ago t was, for the session picker.
func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.String()
}
