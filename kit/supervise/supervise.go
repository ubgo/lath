// Package supervise keeps something running.
//
// The contract that matters, and the one every naive implementation gets
// wrong: an error returned by the supervised function is a REASON TO RETRY,
// not a reason to stop. A supervisor that halts on the failure it exists to
// survive is worse than no supervisor, because it looks like it is working
// right up until the moment it is needed.
//
// Only context cancellation, or an explicit restart limit, ends the loop.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Defaults chosen for a development watchdog, which is the common case.
const (
	// DefaultBackoff is the delay after the first failure.
	DefaultBackoff = 1 * time.Second
	// DefaultMax caps the delay. Capped rather than growing without bound
	// because the failure being ridden out is usually a bad edit, and those
	// are fixed in seconds, waiting a minute to retry would sit on a dead
	// process long after the fix landed.
	DefaultMax = 8 * time.Second
	// DefaultHealthyAfter is how long a run must last to count as healthy.
	DefaultHealthyAfter = 15 * time.Second
	// DefaultMaxRestarts bounds a Policy that does not say otherwise.
	//
	// Bounded rather than unlimited because the safe default is the one that
	// STOPS: an unlimited loop is right for a dev watchdog and badly wrong for
	// a CI step, where it turns a failing build into a job that runs until the
	// runner is killed. A watchdog says Unlimited and means it.
	DefaultMaxRestarts = 10
	// backoffFactor is how fast the delay grows: 1s, 2s, 4s, 8s.
	backoffFactor = 2
)

// Unlimited disables the restart limit.
//
// Negative rather than zero so the zero value can mean "use the default", a
// Policy{} must be safe, and a caller that genuinely wants an endless loop
// should have to write the word.
const Unlimited = -1

// ErrTooManyRestarts means MaxRestarts was reached.
var ErrTooManyRestarts = errors.New("supervise: too many restarts")

// Policy governs restarts. The zero value is usable: every field falls back to
// its default, so a caller wanting ordinary watchdog behaviour writes Policy{}.
type Policy struct {
	// Backoff is the delay after the first failure.
	Backoff time.Duration
	// Max caps the delay.
	Max time.Duration
	// HealthyAfter is how long a run must last to reset the backoff.
	//
	// The distinction it draws: a process that ran for an hour then died had a
	// fresh problem and should restart at once; one that died on boot probably
	// still has the same problem, and hammering it helps nobody.
	HealthyAfter time.Duration
	// MaxRestarts stops the loop after this many attempts.
	//
	// Zero means DefaultMaxRestarts. Use Unlimited for a supervisor that must
	// never give up, a dev watchdog. Which then says so at the call site
	// rather than relying on a reader knowing what zero implies.
	MaxRestarts int
	// Out receives progress lines. Nil discards them.
	Out io.Writer
}

// withDefaults fills unset fields. Applied to a copy, so a caller's Policy is
// never mutated by having been used.
func (p Policy) withDefaults() Policy {
	if p.Backoff <= 0 {
		p.Backoff = DefaultBackoff
	}
	if p.Max <= 0 {
		p.Max = DefaultMax
	}
	if p.HealthyAfter <= 0 {
		p.HealthyAfter = DefaultHealthyAfter
	}
	// Any negative value means unlimited, not just the Unlimited constant. A
	// computed limit that goes negative should relax the bound, never collapse
	// it to "give up after the first attempt", which is what a bare equality
	// check against Unlimited did.
	switch {
	case p.MaxRestarts == 0:
		p.MaxRestarts = DefaultMaxRestarts
	case p.MaxRestarts < 0:
		p.MaxRestarts = Unlimited
	}
	if p.Out == nil {
		p.Out = io.Discard
	}
	return p
}

// Stats reports what a loop did.
type Stats struct {
	// Restarts is how many times fn was re-run after a failure.
	Restarts int
	// LastError is the final failure, if any.
	LastError error
	// TotalRuntime is the summed duration of every attempt, excluding backoff.
	TotalRuntime time.Duration
}

// Loop runs fn until the context is cancelled or MaxRestarts is reached.
//
// Returns nil on cancellation. A supervisor asked to stop has succeeded.
// Returns ErrTooManyRestarts, wrapping the last failure, when a limit was set
// and hit.
func Loop(ctx context.Context, p Policy, fn func(context.Context) error) error {
	_, err := LoopWithStats(ctx, p, fn)
	return err
}

// LoopWithStats is Loop, also reporting what happened.
func LoopWithStats(ctx context.Context, p Policy, fn func(context.Context) error) (Stats, error) {
	return runLoop(ctx, p, fn, false)
}

// runLoop is the shared engine behind Loop and Retry. The only difference
// between them is intent, so the arithmetic lives in one place.
//
// oneShot selects the vocabulary used in output and errors. It is not cosmetic:
// "the supervised process failed, restarting" and "the attempt failed, retrying"
// describe different situations to whoever reads the log, and a shared engine
// that reported both identically would make a one-shot operation look like a
// crash-looping service.
func runLoop(ctx context.Context, p Policy, fn func(context.Context) error, oneShot bool) (Stats, error) {
	noun, verb := "supervise", "restarting"
	if oneShot {
		noun, verb = "retry", "retrying"
	}
	p = p.withDefaults()
	var stats Stats
	backoff := p.Backoff

	for {
		if err := ctx.Err(); err != nil {
			return stats, nil
		}

		started := time.Now()
		err := fn(ctx)
		ran := time.Since(started)
		stats.TotalRuntime += ran

		// Checked immediately after fn returns: a cancelled context means the
		// caller asked to stop, and whatever fn returned was a consequence of
		// that rather than a failure to retry.
		if ctx.Err() != nil {
			return stats, nil
		}
		if err == nil {
			return stats, nil
		}
		stats.LastError = err

		// Honoured by BOTH Loop and Retry: a caller that has explicitly said
		// this failure is fatal is not second-guessed. See Permanent.
		if IsPermanent(err) {
			return stats, fmt.Errorf("%s: giving up after %d attempt(s), the failure is permanent: %w",
				noun, stats.Restarts+1, err)
		}

		// A run that lasted implies a working process that broke later, so the
		// next attempt should be immediate. A run that died at once probably
		// has the same problem still.
		if ran >= p.HealthyAfter {
			backoff = p.Backoff
		}

		if p.MaxRestarts != Unlimited && stats.Restarts >= p.MaxRestarts-1 {
			return stats, fmt.Errorf("%s: gave up after %d attempts: %w: %w",
				noun, stats.Restarts+1, ErrTooManyRestarts, err)
		}

		fmt.Fprintf(p.Out, "[%s] failed after %s: %v: %s in %s\n",
			noun, ran.Round(time.Millisecond), err, verb, backoff)

		// An interruptible sleep. A plain time.Sleep here would make Ctrl-C
		// wait out the full backoff, which at the cap is eight seconds of a
		// process that will not respond.
		select {
		case <-ctx.Done():
			return stats, nil
		case <-time.After(backoff):
		}

		stats.Restarts++
		if backoff < p.Max {
			backoff *= backoffFactor
		}
		if backoff > p.Max {
			backoff = p.Max
		}
	}
}
