package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Run starts a program, waits for it, and reports how it ended.
//
// A non-zero exit is a Result, not an error. An error means the program never
// ran. It was not found, the working directory did not exist, or the context
// ended, or Timeout elapsed. Callers that treat "ran and failed" the same as
// "could not run" have collapsed a distinction they will need later.
//
// Invariant: when err is nil, Result describes a process that actually
// finished, and Result.OK reports whether it succeeded.
func Run(ctx context.Context, name string, args []string, opts ...Option) (Result, error) {
	p, err := Start(ctx, name, args, opts...)
	if err != nil {
		return Result{}, err
	}
	return p.Wait()
}

// Output runs a program and returns its trimmed standard output.
//
// Here a non-zero exit IS an error, the opposite of Run. The asymmetry is
// deliberate: a caller asking for output has no use for the failure case, and
// making it check twice, once for err, once for Result.OK, is the kind of
// ceremony that gets skipped and then bites.
//
// The error names the program and includes captured stderr, because "exit 1"
// with no context is the least useful error a tool can produce.
func Output(ctx context.Context, name string, args []string, opts ...Option) (string, error) {
	r, err := Run(ctx, name, args, append(opts, Capture())...)
	if err != nil {
		return "", err
	}
	if !r.OK() {
		msg := strings.TrimSpace(string(r.Stderr))
		if msg == "" {
			msg = "no stderr"
		}
		return "", fmt.Errorf("proc: %s %s: %s: %s", name, strings.Join(args, " "), r, msg)
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// Process is a running program.
type Process struct {
	cmd     *exec.Cmd
	started time.Time
	outBuf  *bytes.Buffer
	errBuf  *bytes.Buffer
	// timedOut records that the deadline, rather than the caller, ended it ,
	// so Wait can report ErrTimeout instead of a bare signalled Result.
	//
	// Atomic because it is written by the watchdog goroutine and read by Wait
	// on the caller's. A plain bool here is a data race the race detector
	// finds immediately, and a corrupted read in production would report a
	// timeout that never happened.
	timedOut atomic.Bool
	// stopTimer releases the timeout goroutine when the process ends first.
	// Idempotent, see watch.
	stopTimer func()
	// waitOnce guards the single permitted call to cmd.Wait, and waitRes and
	// waitErr hold its outcome so later calls can be answered from memory.
	//
	// Why: os/exec allows exactly one Wait, and Stop calls it internally. A
	// caller that waits and then stops. A supervisor with a deferred cleanup,
	// the ordinary shape, would otherwise hit the second call. Once.Do also
	// establishes the happens-before that makes reading the two fields safe.
	waitOnce sync.Once
	waitRes  Result
	waitErr  error
}

// Start launches a program without waiting for it.
//
// For anything long-lived: a dev server, a supervised worker. The caller owns
// the returned Process and must Wait or Stop it.
func Start(ctx context.Context, name string, args []string, opts ...Option) (*Process, error) {
	c, outBuf, errBuf := build(opts)

	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("proc: %s: %w", name, ErrNotFound)
	}

	// Deliberately exec.Command, not exec.CommandContext.
	//
	// CommandContext kills with SIGKILL and no grace period. A dev server or a
	// deploy step needs the chance to shut down cleanly, so cancellation is
	// handled explicitly in Wait, which sends SIGTERM first.
	cmd := exec.Command(name, args...)
	c.apply(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proc: starting %s: %w", name, err)
	}

	p := &Process{cmd: cmd, started: time.Now(), outBuf: outBuf, errBuf: errBuf}
	p.watch(ctx, c.timeout)
	return p, nil
}

// PID reports the process id.
func (p *Process) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Signal sends sig to the process.
//
// On a platform without signals the process is terminated instead; see
// deliver, and SignalsSupported, which says whether the polite phase of
// Stop means anything here.
func (p *Process) Signal(sig syscall.Signal) error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := deliver(p.cmd.Process, sig); err != nil {
		// Already gone is the normal case during shutdown; reporting it would
		// add noise to every clean exit.
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("proc: signalling %d: %w", p.PID(), err)
	}
	return nil
}

// Stop asks the process to exit, then kills it if it will not.
//
// SIGTERM first, because a process that handles it can flush, close, and
// release its port. SIGKILL only after grace, because a process that ignores
// SIGTERM will otherwise hang the caller forever.
func (p *Process) Stop(ctx context.Context, grace time.Duration) error {
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { _, _ = p.Wait(); close(done) }()

	select {
	case <-done:
		return nil
	case <-time.After(grace):
		if err := p.Signal(syscall.SIGKILL); err != nil {
			return err
		}
		<-done
		return nil
	case <-ctx.Done():
		_ = p.Signal(syscall.SIGKILL)
		<-done
		return ctx.Err()
	}
}
