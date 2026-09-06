// Package confirm gates dangerous actions behind an operator's explicit answer.
//
// The mechanism that earns it a place is not the prompt, it is the terminal
// check. A prompt that blocks forever in CI is worse than no prompt: the job
// hangs until a global timeout with nothing in the log explaining why. Here
// that is a fast, legible failure instead.
package confirm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrDeclined reports that the operator said no.
var ErrDeclined = errors.New("confirm: declined")

// ErrNotInteractive reports that there was no terminal to ask on and no
// assumption was supplied.
var ErrNotInteractive = errors.New("confirm: no terminal to ask on")

// affirmatives are the answers accepted as yes. A closed set, defined once,
// rather than a scattered comparison.
var affirmatives = []string{"y", "yes"}

type config struct {
	in     io.Reader
	out    io.Writer
	assume *bool
	isTTY  func(io.Reader) bool
}

// Option configures a prompt.
type Option func(*config)

// In sets where the answer is read from. Defaults to os.Stdin.
func In(r io.Reader) Option { return func(c *config) { c.in = r } }

// Out sets where the prompt is written. Defaults to os.Stderr, not stdout, so
// a prompt never contaminates output a caller is piping somewhere.
func Out(w io.Writer) Option { return func(c *config) { c.out = w } }

// Assume answers without asking, how a caller passes on a decision already
// made, typically by a --yes or --apply flag.
func Assume(yes bool) Option { return func(c *config) { c.assume = &yes } }

// withTTYCheck overrides terminal detection. Unexported: it exists so this
// package's own tests can exercise the answer-parsing path, which is otherwise
// unreachable without a real terminal. Callers have Assume for the same effect.
func withTTYCheck(fn func(io.Reader) bool) Option { return func(c *config) { c.isTTY = fn } }

func build(opts []Option) config {
	c := config{in: os.Stdin, out: os.Stderr, isTTY: isTerminal}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// Yes asks a yes/no question, defaulting to no.
//
// Defaulting to no is the whole point: an empty answer, a stray newline from a
// pipe, or a reflexive Enter must never be the thing that authorises a
// destructive action.
func Yes(ctx context.Context, prompt string, opts ...Option) (bool, error) {
	c := build(opts)
	if c.assume != nil {
		return *c.assume, nil
	}
	if !c.isTTY(c.in) {
		return false, fmt.Errorf("confirm: %q: %w", prompt, ErrNotInteractive)
	}

	answer, err := ask(ctx, c, prompt+" [y/N]: ")
	if err != nil {
		return false, err
	}
	return IsAffirmative(answer), nil
}

// IsAffirmative reports whether an answer means yes.
//
// Exported so a caller that already HAS the answer, a panel with its own
// reader, a form, a protocol message, shares this package's set instead of
// hand-rolling `== "y" || == "yes"` somewhere else. That duplication is how a
// codebase ends up accepting different words in different prompts.
//
// Case and surrounding space are ignored. Anything unrecognised is no, which
// is the same default Yes applies: an empty answer or a stray newline from a
// pipe must never authorise a destructive action.
func IsAffirmative(answer string) bool {
	answer = strings.ToLower(strings.TrimSpace(answer))
	for _, a := range affirmatives {
		if answer == a {
			return true
		}
	}
	return false
}

// Phrase requires the operator to type an exact string.
//
// Used where a reflexive "y" is too cheap, dropping a database, deploying to
// production. Typing the environment's own name forces the operator to look at
// what they are about to affect.
//
// Assume(true) satisfies it, so a caller that already required the decision on
// the command line does not have to fake stdin.
func Phrase(ctx context.Context, prompt, required string, opts ...Option) error {
	c := build(opts)
	if c.assume != nil {
		if *c.assume {
			return nil
		}
		return fmt.Errorf("confirm: %w", ErrDeclined)
	}
	if !c.isTTY(c.in) {
		return fmt.Errorf("confirm: %q: %w", prompt, ErrNotInteractive)
	}

	answer, err := ask(ctx, c, fmt.Sprintf("%s\nType %q to continue: ", prompt, required))
	if err != nil {
		return err
	}
	// Compared exactly, with no trimming beyond the line ending and no case
	// folding: the point is that the operator typed precisely this.
	if strings.TrimRight(answer, "\r\n") != required {
		return fmt.Errorf("confirm: expected %q: %w", required, ErrDeclined)
	}
	return nil
}

// ask writes the prompt and reads one line, abandoning the read if ctx ends.
func ask(ctx context.Context, c config, prompt string) (string, error) {
	if _, err := io.WriteString(c.out, prompt); err != nil {
		return "", fmt.Errorf("confirm: %w", err)
	}

	type result struct {
		line string
		err  error
	}
	// Buffered so the goroutine can finish and exit even when nobody is left
	// to receive, otherwise a cancelled prompt leaks it for the process's life.
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(c.in).ReadString('\n')
		// EOF is an answer, not a failure. Ctrl-D at a prompt, or input that
		// simply ran out, means the operator did not confirm, and since both
		// callers default to no, returning whatever was typed produces exactly
		// that. Reporting a read error instead would surface a decline as a
		// broken terminal.
		if err != nil && !errors.Is(err, io.EOF) {
			ch <- result{err: err}
			return
		}
		ch <- result{line: line}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return "", fmt.Errorf("confirm: reading the answer: %w", r.err)
		}
		return r.line, nil
	}
}

// isTerminal reports whether r is an interactive terminal.
//
// Implemented by asking the OS for the file mode rather than pulling in a
// dependency: a character device is a terminal, a pipe or a regular file is
// not. Anything that is not an *os.File cannot be one.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
