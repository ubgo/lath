package runner

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/ubgo/lath/kit/proc"
)

// Fake records every command and replies from a script.
//
// Exported, and deliberately not in a _test file: docker, git and steps all
// need it, and three copies of the same fake is how they drift apart. It is
// what lets every command line be asserted with no docker, no git and no
// network. Which is where the flags review misses actually get checked.
type Fake struct {
	mu sync.Mutex
	// Reply matches on a substring of the joined command line; first match
	// wins, so a specific rule can precede a general one.
	Reply []Scripted
	// Fail makes every Run report that the command could not be started.
	Fail error
	got  [][]string
}

// Scripted is one canned response.
type Scripted struct {
	Match  string
	Stdout string
	Stderr string
	Exit   int
}

// Run records the invocation and returns the scripted reply.
func (f *Fake) Run(_ context.Context, name string, args []string, opts ...proc.Option) (proc.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, append([]string{name}, args...))
	if f.Fail != nil {
		return proc.Result{}, f.Fail
	}
	line := name + " " + strings.Join(args, " ")
	for _, s := range f.Reply {
		if !strings.Contains(line, s.Match) {
			continue
		}
		// Scripted output is WRITTEN as well as returned, because a real
		// runner honours proc.Out and a fake that did not could not be used
		// to test anything that streams. Silence here would look exactly like
		// a streaming bug in the code under test.
		writeTo(s.Stdout, s.Stderr, opts...)
		return proc.Result{ExitCode: s.Exit, Stdout: []byte(s.Stdout), Stderr: []byte(s.Stderr)}, nil
	}
	return proc.Result{}, nil
}

// writeTo sends scripted output to whichever streams the options name.
func writeTo(stdout, stderr string, opts ...proc.Option) {
	o, e := proc.Writers(opts...)
	if o != nil && stdout != "" {
		// Errors ignored: a test double must not fail a run because a test's
		// buffer misbehaved.
		_, _ = io.WriteString(o, stdout)
	}
	if e != nil && stderr != "" {
		_, _ = io.WriteString(e, stderr)
	}
}

// Describe names the fake.
func (f *Fake) Describe() string { return "on the fake runner" }

// Commands returns each recorded invocation as one space-joined string.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.got))
	for _, c := range f.got {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// Ran reports whether any recorded command contains substr.
func (f *Fake) Ran(substr string) bool {
	for _, c := range f.Commands() {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}
