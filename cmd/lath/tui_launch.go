package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/session"
	"github.com/ubgo/lath/pipeline/debug"
)

// launchLogName is where a launched run's own terminal output goes.
//
// Redirected rather than shown, because the panel already receives every step
// boundary and detail line over the protocol, see pipeline.ReporterSource ,
// and letting the child also write to this terminal would scribble over the
// panel it is being rendered into. Kept rather than discarded so a crash
// before the first event is still diagnosable.
const launchLogName = "run.log"

// launchAndAttach picks a target, starts it, and steps through it here.
//
// This is `lath tui` with nothing already waiting: the whole session lives in
// one terminal. lath advertises the session itself and passes the socket
// straight through, rather than handing the child a --debug flag, the child's
// stdout is a pipe, so the terminal check that flag performs would refuse a
// session this process is about to drive from a terminal it certainly has.
func launchAndAttach(args []string) int {
	targets, rejected, err := discover(definitionDir)
	warnRejected(rejected)
	if err != nil {
		return reportDiscoveryError(err)
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "lath: no targets in ./%s\n", definitionDir)
		return exitUsage
	}

	command, err := chooseTarget(targets, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitUsage
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	root, err := filepath.Abs(definitionDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}

	// Advertised by THIS process, which outlives the child and is the one an
	// operator would go looking for. The child needs no advertisement of its
	// own; it is handed the socket.
	adv, err := session.Advertise(context.Background(), sessionKey(filepath.Dir(root), command),
		session.Meta{
			"project": filepath.Base(filepath.Dir(root)),
			"dir":     filepath.Dir(root),
			"target":  strings.Join(command, " "),
		})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	defer adv.Close()

	logPath := filepath.Join(filepath.Dir(adv.Socket()), launchLogName)
	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	defer logFile.Close()

	childArgs := append([]string{string(VerbRun)}, command...)
	childArgs = append(childArgs,
		socketFlag+"="+adv.Socket(),
		waitFlag+"="+debug.DefaultAttachTimeout.String())

	// The child is given THIS terminal, not a pipe, whenever there is one.
	//
	// That is what lets docker render its own live progress table instead of a
	// flat log: os/exec passes an *os.File to the child by descriptor, and any
	// wrapper turns it into a pipe. The panel and the child never collide
	// because they take turns. The panel draws only when the run is paused,
	// and a running step is the only thing executing then.
	//
	// Without a terminal, piped, recorded, redirected, the child gets the
	// tee instead, so output is still visible and still logged.
	var out io.Writer = newLiveWriter(logFile)
	tty := isTerminal(os.Stderr)
	if tty {
		out = os.Stderr
		childArgs = append(childArgs, ttyFlag+"=1")
	}
	child := exec.Command(self, childArgs...)
	child.Stdout, child.Stderr = out, out
	child.Stdin = os.Stdin
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	// Reaped in the background so the waits below can tell a child that is
	// still compiling from one that has already exited.
	//
	// A closed channel rather than a value on one: the exit is observed from
	// more than one place, first while waiting for a session, then again when
	// reporting the status, and a one-shot receive deadlocks the second
	// reader. Closing broadcasts; the error is read afterwards, which the
	// close happens-before makes safe.
	run := &launched{cmd: child, done: make(chan struct{})}
	go run.reap()

	fmt.Fprintf(os.Stderr, "lath: %s\n\n", strings.Join(command, " "))

	cli, err := dialWhenReady(adv.Socket(), run.done)
	if err != nil {
		// The socket never opened, which for a launched run means the child
		// died before reaching it. Its output has already been shown live.
		return run.status()
	}
	defer cli.Close()

	// The first event is what proves there is a pipeline to step. Until it
	// arrives this is an ordinary run, and it may never arrive: plenty of
	// targets do useful work without building a Pipeline, and one of them
	// being picked here must not look like a failure.
	first, err := awaitFirstEvent(cli, run.done)
	if err != nil {
		return run.status()
	}
	if lw, ok := out.(*liveWriter); ok {
		lw.Quiet()
	}

	return drivePanel(cli, first, tty, session.Session{Meta: session.Meta{
		"project": filepath.Base(filepath.Dir(root)),
		"target":  strings.Join(command, " "),
	}})
}

// launched is a started run, observable from several places.
type launched struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// reap waits for the run and broadcasts that it ended.
func (l *launched) reap() {
	l.err = l.cmd.Wait()
	close(l.done)
}

// status blocks until the run ends and returns its exit code.
//
// Its output has already been shown live, so there is nothing left to print:
// the only thing to convey is whether it worked.
func (l *launched) status() int {
	<-l.done
	if l.err == nil {
		return exitOK
	}
	var ee *exec.ExitError
	if errors.As(l.err, &ee) {
		return ee.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "lath:", l.err)
	return exitInternalErr
}

// awaitFirstEvent returns the session's first event, or an error if the run
// ended without opening one.
func awaitFirstEvent(cli *debug.Client, done <-chan struct{}) (debug.Event, error) {
	type result struct {
		e   debug.Event
		err error
	}
	got := make(chan result, 1)
	go func() {
		e, err := cli.Next()
		got <- result{e, err}
	}()

	select {
	case r := <-got:
		return r.e, r.err
	case <-done:
		// The child finished. Give an already-queued first event a moment to
		// land. A pipeline short enough to complete before this select runs
		// still deserves its panel.
		select {
		case r := <-got:
			if r.err == nil {
				return r.e, nil
			}
		case <-time.After(dialInterval):
		}
		return debug.Event{}, errNoPipeline
	}
}

// errNoPipeline means the run did its work without building a Pipeline, so
// there was nothing to step through. Not a failure.
var errNoPipeline = errors.New("no pipeline")

// chooseTarget resolves which target to run.
//
// Given arguments, they ARE the command, `lath tui deploy local --apply` runs
// that, with no prompt. Given none, it asks.
func chooseTarget(targets []Target, args []string) ([]string, error) {
	if len(args) > 0 {
		return args, nil
	}

	commands := commandNames(targets)
	fmt.Fprint(os.Stderr, "\n  targets:\n\n")
	for i, c := range commands {
		fmt.Fprintf(os.Stderr, "    %2d) %s\n", i+1, c)
	}
	fmt.Fprintf(os.Stderr, "\n  run [1-%d]: ", len(commands))

	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading a choice: %w", err)
	}
	choice := strings.TrimSpace(line)
	n := 0
	if _, err := fmt.Sscanf(choice, "%d", &n); err != nil || n < 1 || n > len(commands) {
		return nil, fmt.Errorf("pick a number between 1 and %d", len(commands))
	}

	// Asked for separately because most targets need an environment, and a
	// picker that could only run argument-less targets would be useless for
	// exactly the pipelines worth stepping through.
	fmt.Fprint(os.Stderr, "  arguments (e.g. prod --apply, or blank): ")
	rest, err := in.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading arguments: %w", err)
	}
	return append(strings.Fields(commands[n-1]), strings.Fields(rest)...), nil
}

// dialInterval is how often dialWhenReady retries. Short enough that attaching
// feels immediate once the child is up, long enough not to spin.
const dialInterval = 150 * time.Millisecond

// dialWhenReady retries until the child opens its socket, or stops.
//
// The child has to compile the definition first, which on a cold cache is
// minutes, so the socket cannot be assumed present. But waiting on a clock
// alone is wrong in a way that shows up immediately in practice: a target that
// never builds a pipeline. A plan, a lint, anything that only prints, opens
// no session and never will, and the operator would watch a blank terminal for
// the whole timeout before being told nothing happened.
//
// So the child's exit is the other end of the wait. Whichever comes first
// answers.
func dialWhenReady(socket string, done <-chan struct{}) (*debug.Client, error) {
	deadline := time.Now().Add(debug.DefaultAttachTimeout)
	for {
		if cli, err := debug.Dial(socket); err == nil {
			return cli, nil
		}
		select {
		case <-done:
			// One last attempt: a very short run can open and close the socket
			// between the dial above and the process exiting.
			if cli, dialErr := debug.Dial(socket); dialErr == nil {
				return cli, nil
			}
			return nil, errNoPipeline
		case <-time.After(dialInterval):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the run did not open a debug session within %s",
				debug.DefaultAttachTimeout)
		}
	}
}

// logTailLines is how much of a failed run's output to show inline. Enough to
// carry the error that stopped it, short enough not to bury the message
// explaining what happened.
const logTailLines = 12

// tailFile returns the last n lines of a file, indented, or empty.
func tailFile(path string, n int) string {
	blob, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(blob), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "      " + strings.Join(lines, "\n      ")
}
