package supervise_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/supervise"
)

// fastPolicy keeps backoff negligible so a test measures behaviour, not sleep.
func fastPolicy(maxRestarts int) supervise.Policy {
	return supervise.Policy{
		Backoff:      time.Millisecond,
		Max:          2 * time.Millisecond,
		HealthyAfter: time.Hour, // never "healthy", so backoff never resets
		MaxRestarts:  maxRestarts,
	}
}

// TestErrorMeansRetry pins the contract that makes this package a supervisor
// rather than a wrapper: an error from fn is a reason to run it AGAIN.
//
// It is the opposite of Go's usual convention, which is exactly why it is
// pinned. An implementation that returns on the first error looks correct in
// review and turns a dev watchdog into a one-shot.
func TestErrorMeansRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	err := supervise.Loop(context.Background(), fastPolicy(supervise.Unlimited),
		func(context.Context) error {
			if calls.Add(1) < 5 {
				return errors.New("still failing")
			}
			return nil // success ends the loop
		})
	if err != nil {
		t.Fatalf("err = %v; want nil once fn succeeds", err)
	}
	if calls.Load() != 5 {
		t.Errorf("fn ran %d times; want 5", calls.Load())
	}
}

// TestSuccessStopsImmediately pins that a clean return ends the loop rather
// than restarting a task that finished its job.
func TestSuccessStopsImmediately(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	stats, err := supervise.LoopWithStats(context.Background(), fastPolicy(supervise.Unlimited),
		func(context.Context) error {
			calls.Add(1)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("fn ran %d times; want 1", calls.Load())
	}
	if stats.Restarts != 0 {
		t.Errorf("Restarts = %d; want 0", stats.Restarts)
	}
}

// TestMaxRestartsCountsAttempts pins the exact number of runs for a given
// limit. An off-by-one here is invisible in normal use and doubles a CI job's
// worst case.
func TestMaxRestartsCountsAttempts(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{1, 2, 3, 10} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			sentinel := errors.New("always fails")
			stats, err := supervise.LoopWithStats(context.Background(), fastPolicy(limit),
				func(context.Context) error {
					calls.Add(1)
					return sentinel
				})
			if !errors.Is(err, supervise.ErrTooManyRestarts) {
				t.Fatalf("err = %v; want ErrTooManyRestarts", err)
			}
			// The last failure must still be reachable, it is the one that
			// says WHY the supervisor gave up.
			if !errors.Is(err, sentinel) {
				t.Errorf("err = %v; want the underlying failure to remain unwrappable", err)
			}
			if got := int(calls.Load()); got != limit {
				t.Errorf("fn ran %d times; MaxRestarts %d means %d attempts", got, limit, limit)
			}
			if stats.Restarts != limit-1 {
				t.Errorf("Restarts = %d; want %d", stats.Restarts, limit-1)
			}
			if !errors.Is(stats.LastError, sentinel) {
				t.Errorf("LastError = %v; want the failure", stats.LastError)
			}
		})
	}
}

// TestZeroMaxRestartsUsesTheDefault pins that the zero value is a bounded
// default, not "unlimited". A Policy{} left partly filled must not produce a
// job that never ends.
func TestZeroMaxRestartsUsesTheDefault(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	p := fastPolicy(0)
	err := supervise.Loop(context.Background(), p, func(context.Context) error {
		calls.Add(1)
		return errors.New("nope")
	})
	if !errors.Is(err, supervise.ErrTooManyRestarts) {
		t.Fatalf("err = %v; want the loop to be bounded", err)
	}
	if got := int(calls.Load()); got != supervise.DefaultMaxRestarts {
		t.Errorf("fn ran %d times; want DefaultMaxRestarts (%d)", got, supervise.DefaultMaxRestarts)
	}
}

// TestAnyNegativeMeansUnlimited pins that a computed limit going negative
// relaxes the bound rather than collapsing it.
//
// The regression: the check compared against Unlimited by equality, so -1 was
// unlimited and -2 gave up after a single attempt, the exact inverse of what
// a negative number reads as.
func TestAnyNegativeMeansUnlimited(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, -2, -100} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			const target = 6
			err := supervise.Loop(context.Background(), fastPolicy(limit),
				func(context.Context) error {
					if calls.Add(1) < target {
						return errors.New("still failing")
					}
					return nil
				})
			if err != nil {
				t.Fatalf("MaxRestarts %d: err = %v; want it to keep retrying", limit, err)
			}
			if calls.Load() != target {
				t.Errorf("MaxRestarts %d: fn ran %d times; want %d", limit, calls.Load(), target)
			}
		})
	}
}

// TestUnlimitedSurvivesManyFailures is the dev-watchdog case: a server that
// crashes far more often than any bounded limit would tolerate.
func TestUnlimitedSurvivesManyFailures(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	const target = supervise.DefaultMaxRestarts * 4
	err := supervise.Loop(context.Background(), fastPolicy(supervise.Unlimited),
		func(context.Context) error {
			if calls.Add(1) < target {
				return errors.New("crashed")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("err = %v; an unlimited supervisor must not give up", err)
	}
	if calls.Load() != target {
		t.Errorf("fn ran %d times; want %d", calls.Load(), target)
	}
}

// TestCancellationIsACleanStop pins that cancelling reports no error. Ctrl-C
// is the operator saying stop; surfacing it as a failure makes every clean
// shutdown exit non-zero.
func TestCancellationIsACleanStop(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"before the first run", "while running", "during backoff"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			var calls atomic.Int32

			fn := func(ctx context.Context) error {
				calls.Add(1)
				switch when {
				case "while running":
					cancel()
					<-ctx.Done()
					return ctx.Err()
				case "during backoff":
					go func() { time.Sleep(20 * time.Millisecond); cancel() }()
					return errors.New("failed")
				}
				return errors.New("failed")
			}
			if when == "before the first run" {
				cancel()
			}

			p := supervise.Policy{
				Backoff: 50 * time.Millisecond, Max: 50 * time.Millisecond,
				MaxRestarts: supervise.Unlimited,
			}
			if err := supervise.Loop(ctx, p, fn); err != nil {
				t.Errorf("err = %v; cancellation is a clean stop", err)
			}
			if when == "before the first run" && calls.Load() != 0 {
				t.Errorf("fn ran %d times after the context was already cancelled", calls.Load())
			}
		})
	}
}

// TestBackoffGrowsAndIsCapped pins that delays increase but never exceed Max.
// Unbounded growth turns a watchdog into one that takes hours to notice a
// service came back.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	t.Parallel()
	// The SIGNAL here is the difference between one backoff and the next, and
	// it has to be large compared to the noise in measuring a sleep. At 20ms
	// base the growth step was also 20ms, which is the same size as the
	// overhead a loaded or emulated machine adds: under QEMU this measured
	// 43ms then 42ms — the delays had grown, and the measurement could not
	// see it. Five times larger costs about a second and makes the signal
	// unmistakable.
	const base, max = 100 * time.Millisecond, 300 * time.Millisecond
	var gaps []time.Duration
	last := time.Now()
	var calls int

	p := supervise.Policy{Backoff: base, Max: max, HealthyAfter: time.Hour, MaxRestarts: 6}
	_ = supervise.Loop(context.Background(), p, func(context.Context) error {
		now := time.Now()
		if calls > 0 {
			gaps = append(gaps, now.Sub(last))
		}
		last, calls = now, calls+1
		return errors.New("fail")
	})

	if len(gaps) < 4 {
		t.Fatalf("recorded %d gaps; want at least 4", len(gaps))
	}
	// Growth: the second wait must exceed the first.
	if gaps[1] <= gaps[0] {
		t.Errorf("backoff did not grow: %v then %v", gaps[0], gaps[1])
	}
	// The cap. Slack is proportional to Max rather than a fixed number of
	// milliseconds, because the overhead of a sleep scales with how slow the
	// machine is; the assertion is "capped", not "capped to the millisecond",
	// and doubling still separates it from uncapped growth, which would reach
	// 1.6s by the sixth attempt.
	for i, g := range gaps {
		if g > 2*max {
			t.Errorf("gap %d was %v; Max is %v", i, g, max)
		}
	}
}

// TestStatsAccumulateRuntime pins that TotalRuntime measures the supervised
// work, and excludes the backoff spent between attempts.
func TestStatsAccumulateRuntime(t *testing.T) {
	t.Parallel()
	// The backoff is an order of magnitude longer than the work on purpose.
	// This test asks WHICH of two outcomes happened — runtime alone, or
	// runtime plus the waiting — and the two have to be far enough apart that
	// no amount of slowness can blur them. An earlier version used 30ms of
	// work against 40ms of backoff, which put the outcomes 80ms apart and
	// failed under emulation on a foreign architecture, where each sleep
	// overshoots enough to swallow the gap.
	const runFor = 20 * time.Millisecond
	const backoff = 200 * time.Millisecond
	const restarts = 3
	p := supervise.Policy{
		Backoff: backoff, Max: backoff,
		HealthyAfter: time.Hour, MaxRestarts: restarts,
	}

	start := time.Now()
	stats, _ := supervise.LoopWithStats(context.Background(), p, func(context.Context) error {
		time.Sleep(runFor)
		return errors.New("fail")
	})
	wall := time.Since(start)

	if stats.TotalRuntime < restarts*runFor {
		t.Errorf("TotalRuntime = %v; want at least %v", stats.TotalRuntime, restarts*runFor)
	}
	// Measured against the WALL CLOCK of this very run rather than against a
	// fixed number, so the assertion calibrates itself to whatever machine it
	// is on: a slow one inflates both figures together. Wall time contains the
	// work plus two backoffs; if the waiting were counted as runtime the two
	// would be nearly equal, so requiring that runtime falls short of wall
	// time by at least half the waiting is the property, stated directly.
	waited := (restarts - 1) * backoff
	if ceiling := wall - waited/2; stats.TotalRuntime > ceiling {
		t.Errorf("TotalRuntime = %v against %v of wall clock; backoff appears to be counted as runtime",
			stats.TotalRuntime, wall)
	}
}

// TestOutReportsEachRetry pins that a supervisor is not silent. A dev server
// restarting with no output looks like a hang.
func TestOutReportsEachRetry(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	p := fastPolicy(3)
	p.Out = &log
	_ = supervise.Loop(context.Background(), p, func(context.Context) error {
		return errors.New("the boom happened")
	})
	// Two restarts between three attempts. Loop says "restarting", not
	// "retrying": it supervises a process, and calling that a retry would make
	// a crash-looping service read like a one-shot operation.
	if got := strings.Count(log.String(), "restarting"); got != 2 {
		t.Errorf("logged %d restarts; want 2. Log:\n%s", got, log.String())
	}
	if !strings.Contains(log.String(), "the boom happened") {
		t.Errorf("the log omits the failure reason:\n%s", log.String())
	}
}

// TestNilOutIsSafe pins that the zero Policy does not panic on its first
// failure. The most likely way to meet this package for the first time.
func TestNilOutIsSafe(t *testing.T) {
	t.Parallel()
	err := supervise.Loop(context.Background(), supervise.Policy{
		Backoff: time.Millisecond, Max: time.Millisecond, MaxRestarts: 2,
	}, func(context.Context) error {
		return errors.New("fail")
	})
	if !errors.Is(err, supervise.ErrTooManyRestarts) {
		t.Errorf("err = %v", err)
	}
}

// TestFnPanicIsNotSwallowed pins that a panic propagates rather than being
// converted into a retry. Restarting a function that panicked on a nil map
// would loop forever on a bug that needs a human.
func TestFnPanicIsNotSwallowed(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("a panic inside fn did not reach the caller")
		}
	}()
	_ = supervise.Loop(context.Background(), fastPolicy(3), func(context.Context) error {
		panic("boom")
	})
}
