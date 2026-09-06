package supervise_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/supervise"
)

// fast is a policy with the timings compressed so tests finish quickly. The
// RATIOS are what matter and they are preserved.
var fast = supervise.Policy{
	Backoff:      10 * time.Millisecond,
	Max:          40 * time.Millisecond,
	HealthyAfter: 100 * time.Millisecond,
	Out:          io.Discard,
}

// TestLoopRetriesOnError is THE contract of this package. A supervisor that
// stops on the failure it exists to survive is worse than no supervisor ,
// it looks like it is working right up until the moment it is needed.
func TestLoopRetriesOnError(t *testing.T) {
	t.Parallel()
	var calls int
	p := fast
	p.MaxRestarts = 5

	err := supervise.Loop(context.Background(), p, func(context.Context) error {
		calls++
		return errors.New("always fails")
	})
	if !errors.Is(err, supervise.ErrTooManyRestarts) {
		t.Fatalf("Loop = %v; want ErrTooManyRestarts", err)
	}
	if calls != 5 {
		t.Errorf("fn ran %d times; want 5: an error must mean RETRY, not stop", calls)
	}
	// The underlying failure must survive, or a caller cannot say why it gave up.
	if err == nil || !errorContains(err, "always fails") {
		t.Errorf("err = %v; want it to wrap the last failure", err)
	}
}

func TestLoopStopsOnSuccess(t *testing.T) {
	t.Parallel()
	var calls int
	err := supervise.Loop(context.Background(), fast, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Loop = %v; want nil", err)
	}
	if calls != 3 {
		t.Errorf("fn ran %d times; want 3", calls)
	}
}

// TestLoopStopsOnCancellation pins that cancellation, and only cancellation ,
// ends the loop cleanly.
func TestLoopStopsOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var calls int

	err := supervise.Loop(ctx, fast, func(context.Context) error {
		calls++
		if calls == 3 {
			cancel()
		}
		return errors.New("failing")
	})
	if err != nil {
		t.Fatalf("Loop = %v; want nil: a supervisor asked to stop has succeeded", err)
	}
	if calls != 3 {
		t.Errorf("fn ran %d times after cancelling at 3", calls)
	}
}

// TestCancellationDuringBackoffIsPrompt pins that the sleep between attempts is
// interruptible. A plain time.Sleep would make Ctrl-C wait out the full
// backoff, which at the cap is eight seconds of an unresponsive process.
func TestCancellationDuringBackoffIsPrompt(t *testing.T) {
	t.Parallel()
	slow := supervise.Policy{
		Backoff: 5 * time.Second, Max: 5 * time.Second,
		HealthyAfter: time.Hour, Out: io.Discard,
	}
	ctx, cancel := context.WithCancel(context.Background())

	start := time.Now()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	err := supervise.Loop(ctx, slow, func(context.Context) error {
		return errors.New("fail immediately")
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Loop = %v; want nil", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s; the backoff sleep ignored cancellation", elapsed)
	}
}

// TestHealthyRunResetsBackoff pins the distinction the policy exists to draw:
// a process that ran a while then died had a fresh problem and should restart
// at once; one that dies on boot probably still has the same problem.
func TestHealthyRunResetsBackoff(t *testing.T) {
	t.Parallel()
	// Durations chosen for MARGIN, not speed. The assertion below has to tell
	// a reset backoff from an un-reset one, and every gap it measures also
	// carries the callback's own runtime plus scheduler noise, which under
	// `-race` on a loaded machine has been observed at ~27ms. Doubling the
	// original values pushes the two outcomes 120ms apart, which no plausible
	// noise can bridge; the test costs ~0.4s and stops lying.
	p := supervise.Policy{
		Backoff:      40 * time.Millisecond,
		Max:          400 * time.Millisecond,
		HealthyAfter: 120 * time.Millisecond,
		MaxRestarts:  4,
		Out:          io.Discard,
	}

	var calls int
	var gaps []time.Duration
	last := time.Now()

	_ = supervise.Loop(context.Background(), p, func(context.Context) error {
		calls++
		gaps = append(gaps, time.Since(last))
		// Run 2 lasts long enough to count as healthy.
		if calls == 2 {
			time.Sleep(160 * time.Millisecond)
		}
		last = time.Now()
		return errors.New("fail")
	})

	if calls != 4 {
		t.Fatalf("fn ran %d times; want 4", calls)
	}
	// The gap after the healthy run must be the BASE backoff (40ms), not the
	// doubled-twice one it would have reached without the reset (160ms). The
	// bound sits between them rather than close to either: this test asks
	// which of two outcomes happened, and pinning the timing precisely would
	// only make it fail on a busy machine while telling nobody anything new.
	const resetCeiling = 100 * time.Millisecond
	if gaps[3] > resetCeiling {
		t.Errorf("gap after a healthy run was %s, over the %s ceiling; the backoff did not reset",
			gaps[3], resetCeiling)
	}
}

func TestStats(t *testing.T) {
	t.Parallel()
	p := fast
	p.MaxRestarts = 3
	stats, err := supervise.LoopWithStats(context.Background(), p, func(context.Context) error {
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected ErrTooManyRestarts")
	}
	if stats.Restarts != 2 {
		t.Errorf("Restarts = %d; want 2 (3 attempts, 2 retries)", stats.Restarts)
	}
	if stats.LastError == nil {
		t.Error("LastError is nil")
	}
}

// TestZeroPolicyIsUsable pins that Policy{} works, a caller wanting ordinary
// watchdog behaviour should not have to know four durations.
func TestZeroPolicyIsUsable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := supervise.Loop(ctx, supervise.Policy{}, func(context.Context) error {
		return errors.New("fail")
	}); err != nil {
		t.Fatalf("Loop with a zero Policy = %v; want nil", err)
	}
}

func errorContains(err error, s string) bool {
	return err != nil && len(err.Error()) > 0 && contains(err.Error(), s)
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// TestDefaultMaxRestartsIsBounded pins that a Policy{} STOPS.
//
// The unsafe default is unlimited: right for a dev watchdog, badly wrong for a
// CI step, where it turns a failing build into a job that runs until the runner
// is killed. A caller wanting an endless loop should have to write the word.
func TestDefaultMaxRestartsIsBounded(t *testing.T) {
	t.Parallel()
	var calls int
	p := supervise.Policy{Backoff: time.Millisecond, Max: time.Millisecond, Out: io.Discard}

	err := supervise.Loop(context.Background(), p, func(context.Context) error {
		calls++
		return errors.New("always fails")
	})
	if !errors.Is(err, supervise.ErrTooManyRestarts) {
		t.Fatalf("Loop with a zero MaxRestarts = %v; want it to give up", err)
	}
	if calls != supervise.DefaultMaxRestarts {
		t.Errorf("fn ran %d times; want DefaultMaxRestarts (%d)",
			calls, supervise.DefaultMaxRestarts)
	}
}

// TestUnlimitedNeverGivesUp pins the watchdog case: only cancellation ends it.
func TestUnlimitedNeverGivesUp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var calls int

	p := supervise.Policy{
		Backoff: time.Millisecond, Max: time.Millisecond,
		MaxRestarts: supervise.Unlimited, Out: io.Discard,
	}
	err := supervise.Loop(ctx, p, func(context.Context) error {
		calls++
		// Far beyond the bounded default, proof the limit is really off.
		if calls > supervise.DefaultMaxRestarts*3 {
			cancel()
		}
		return errors.New("always fails")
	})
	if err != nil {
		t.Fatalf("Loop = %v; want nil", err)
	}
	if calls <= supervise.DefaultMaxRestarts {
		t.Errorf("fn ran %d times; Unlimited did not disable the limit", calls)
	}
}
