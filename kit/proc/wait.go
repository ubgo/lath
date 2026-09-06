package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// watch arranges for the process to be stopped when ctx ends or timeout
// elapses, and for that reason to be recorded so Wait can report it.
//
// Runs in a goroutine that exits when the process does, released by
// stopTimer, so a short-lived process leaves nothing behind.
func (p *Process) watch(ctx context.Context, timeout time.Duration) {
	released := make(chan struct{})
	// Idempotent: Wait may be reached more than once (Stop calls it), and a
	// second close of a channel panics.
	var once sync.Once
	p.stopTimer = func() { once.Do(func() { close(released) }) }

	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		deadline = t.C
		// Stopped when the process finishes first, so a run far shorter than
		// its timeout does not hold a timer until it fires.
		go func() { <-released; t.Stop() }()
	}

	go func() {
		select {
		case <-released:
			return
		case <-ctx.Done():
			// SIGTERM, not SIGKILL: cancellation should let the process shut
			// down cleanly. A caller wanting it dead immediately can Stop with
			// a zero grace.
			_ = p.Signal(syscall.SIGTERM)
		case <-deadline:
			p.timedOut.Store(true)
			_ = p.Signal(syscall.SIGKILL)
		}
	}()
}

// Wait blocks until the process finishes and reports how it ended.
//
// Invariant: safe to call any number of times, from any goroutine. The first
// call reaps the process; later ones return the same Result and error from
// memory. Stop calls it internally, so a caller that waits and then stops ,
// or two goroutines racing to reap, is a supported shape, not a panic.
//
// A non-zero exit is reported in the Result, not as an error. An error means
// the process could not be reaped, or the timeout fired.
func (p *Process) Wait() (Result, error) {
	p.waitOnce.Do(func() { p.waitRes, p.waitErr = p.wait() })
	return p.waitRes, p.waitErr
}

// wait performs the single real reap. Called once, under waitOnce.
func (p *Process) wait() (Result, error) {
	waitErr := p.cmd.Wait()
	if p.stopTimer != nil {
		p.stopTimer()
	}

	r := resultFrom(waitErr, time.Since(p.started))
	if p.outBuf != nil {
		r.Stdout = p.outBuf.Bytes()
	}
	if p.errBuf != nil {
		r.Stderr = p.errBuf.Bytes()
	}

	if p.timedOut.Load() {
		return r, fmt.Errorf("proc: %s: %w", p.cmd.Path, ErrTimeout)
	}

	// A failure that is not an ExitError means the process was never reaped ,
	// an I/O error on a pipe, or a Wait that could not run. resultFrom marks
	// that with unknownExitCode, but returning nil alongside it would present
	// a genuine failure as a process that merely ended oddly.
	var ee *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &ee) {
		return r, fmt.Errorf("proc: waiting for %s: %w", p.cmd.Path, waitErr)
	}
	return r, nil
}
