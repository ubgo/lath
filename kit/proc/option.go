package proc

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// config is what the options build up. Unexported so the only way to construct
// one is through the options, which keeps every field's meaning in one place.
type config struct {
	dir     string
	env     []string
	envOnly bool
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	capture bool
	timeout time.Duration
}

// Option configures a run.
//
// Functional options rather than a struct because most calls set none of these
// and the ones that do set one or two. A config struct would put eight zero
// values at every call site.
type Option func(*config)

// Dir sets the working directory. Defaults to the caller's.
func Dir(path string) Option { return func(c *config) { c.dir = path } }

// Env ADDS variables to the parent environment.
//
// Env and EnvOnly are separate functions on purpose. "Does this add to or
// replace the environment?" is the question every such API leaves ambiguous,
// and answering it wrong yields a process with no PATH, which fails in a way
// that points nowhere near the cause.
//
// Composition: Env and EnvOnly may be given in any order and neither discards
// the other's variables. EnvOnly only decides whether the parent environment
// is inherited; Env only adds. An earlier version cleared that flag, so
// EnvOnly followed by Env silently handed the child the full parent
// environment. An isolation that looked applied and was not.
func Env(kv ...string) Option {
	return func(c *config) { c.env = append(c.env, kv...) }
}

// EnvOnly REPLACES the environment entirely. The process sees only these.
//
// Composition: Env and EnvOnly may be given in any order and neither discards
// the other's variables. EnvOnly only decides whether the parent environment
// is inherited; Env only adds. An earlier version cleared that flag, so
// EnvOnly followed by Env silently handed the child the full parent
// environment. An isolation that looked applied and was not.
func EnvOnly(kv ...string) Option {
	return func(c *config) { c.env = append(c.env, kv...); c.envOnly = true }
}

// Stdin supplies standard input.
func Stdin(r io.Reader) Option { return func(c *config) { c.stdin = r } }

// Out sends both stdout and stderr to w.
//
// Combined by default because that is what a task almost always wants: one
// stream, in the order things actually happened. Split them when the
// difference matters.
func Out(w io.Writer) Option { return func(c *config) { c.stdout, c.stderr = w, w } }

// Split sends stdout and stderr to different writers.
func Split(stdout, stderr io.Writer) Option {
	return func(c *config) { c.stdout, c.stderr = stdout, stderr }
}

// Capture fills Result.Stdout and Result.Stderr.
//
// Off by default: capturing means buffering everything in memory, which is
// wrong for a long-running process and invisible until something produces a
// gigabyte of logs.
func Capture() Option { return func(c *config) { c.capture = true } }

// Timeout kills the process after d and reports ErrTimeout.
func Timeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// build turns options into a config plus the buffers Capture needs.
func build(opts []Option) (config, *bytes.Buffer, *bytes.Buffer) {
	var c config
	for _, o := range opts {
		o(&c)
	}
	// os/exec copies stdout and stderr in SEPARATE goroutines. When both point
	// at one writer. Which is exactly what Out does, and what a caller
	// streaming a build wants. Those goroutines write to it concurrently.
	// Most writers are not safe for that: a strings.Builder detects it and a
	// bytes.Buffer silently corrupts.
	//
	// Serialised here rather than asking every caller to pass a locked writer,
	// because the sharing is created BY this package's own option and a caller
	// has no reason to expect it.
	if c.stdout != nil && c.stdout == c.stderr {
		// An *os.File is left alone, and that exemption is load-bearing:
		// os/exec connects an *os.File to the child by duplicating its
		// DESCRIPTOR, so no Go writer is involved and there is nothing to
		// race. The kernel serialises the child's own writes. Wrapping it
		// would replace that descriptor with a pipe, which is exactly what
		// makes a tool decide it is not talking to a terminal and drop to its
		// plain output. Wrapping here once cost docker its progress table.
		if _, isFile := c.stdout.(*os.File); !isFile {
			shared := &syncWriter{w: c.stdout}
			c.stdout, c.stderr = shared, shared
		}
	}
	if !c.capture {
		return c, nil, nil
	}
	outBuf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	// Capture composes with Out: a caller may want the output live AND kept.
	if c.stdout != nil {
		c.stdout = io.MultiWriter(c.stdout, outBuf)
	} else {
		c.stdout = outBuf
	}
	if c.stderr != nil {
		c.stderr = io.MultiWriter(c.stderr, errBuf)
	} else {
		c.stderr = errBuf
	}
	return c, outBuf, errBuf
}

// apply configures a command from a config.
func (c config) apply(cmd *exec.Cmd) {
	cmd.Dir = c.dir
	switch {
	case c.envOnly:
		cmd.Env = c.env
	case len(c.env) > 0:
		cmd.Env = append(os.Environ(), c.env...)
	}
	cmd.Stdin = c.stdin
	cmd.Stdout = c.stdout
	cmd.Stderr = c.stderr
}

// Writers resolves options to the streams a process's output should reach.
//
// Why it is exported: Run is not the only implementation of "run a command".
// A runner.Runner may execute over ssh, in a container, or not at all, and
// any of those has to honour Out and Split, or a caller that streams output
// gets silence from every runner except the local one. Without this the
// options are opaque outside this package, and a test double cannot even be
// written that behaves like the real thing.
//
// Either return may be nil, meaning "discard". They may also be the same
// writer, which is what Out produces and the common case.
func Writers(opts ...Option) (stdout, stderr io.Writer) {
	var c config
	for _, o := range opts {
		o(&c)
	}
	return c.stdout, c.stderr
}

// syncWriter serialises concurrent writes to one underlying writer.
//
// Deliberately not a general utility: it exists for the single case where this
// package points a process's two output streams at the same destination. See
// build.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// shellSafe are the characters a word may contain and still need no quoting.
//
// Deliberately conservative: anything outside this set gets quoted, so a
// rendered command is never subtly wrong. Over-quoting is ugly; under-quoting
// produces a line that looks right and does something else.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" +
	"@%+=:,./-_"

// CommandLine renders a command as a line a person can paste into a shell.
//
// Why it exists: printing a command as Go's %v gives `[build -t x .]`, which
// reads as the argv it is and cannot be run. The whole value of showing an
// operator what a step WOULD do is that they can then do it themselves ,
// check it, run it by hand, drop it into an issue, and a bracketed slice
// defeats every one of those.
//
// Single quotes rather than double: no expansion happens inside them, so a
// value containing $VAR or a backtick renders as itself.
func CommandLine(name string, args ...string) string {
	var b strings.Builder
	b.WriteString(shellWord(name))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(shellWord(a))
	}
	return b.String()
}

// shellWord quotes s when it needs it.
func shellWord(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune(shellSafe, r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
