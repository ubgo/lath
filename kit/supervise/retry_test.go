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

func retryPolicy(max int) supervise.Policy {
	return supervise.Policy{Backoff: time.Millisecond, Max: 2 * time.Millisecond, MaxRestarts: max}
}

func TestRetrySucceedsAfterFailures(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	err := supervise.Retry(context.Background(), retryPolicy(supervise.Unlimited),
		func(context.Context) error {
			if calls.Add(1) < 4 {
				return errors.New("transient")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Errorf("fn ran %d times; want 4", calls.Load())
	}
}

// TestPermanentStopsImmediately is the whole reason Retry is distinct from
// Loop. A 400 or a missing binary produces ten identical failures otherwise,
// and delays the report by the entire backoff budget.
func TestPermanentStopsImmediately(t *testing.T) {
	t.Parallel()
	underlying := errors.New("malformed request")
	var calls atomic.Int32

	err := supervise.Retry(context.Background(), retryPolicy(10), func(context.Context) error {
		calls.Add(1)
		return supervise.Permanent(underlying)
	})

	if calls.Load() != 1 {
		t.Errorf("fn ran %d times; a permanent error must stop at the first", calls.Load())
	}
	if !errors.Is(err, underlying) {
		t.Errorf("err = %v; the underlying cause must stay reachable", err)
	}
	if errors.Is(err, supervise.ErrTooManyRestarts) {
		t.Error("a permanent failure was reported as exhausting the retry budget")
	}
	if !strings.Contains(err.Error(), "permanent") {
		t.Errorf("err = %v; want it to say why it stopped", err)
	}
}

// TestPermanentSurvivesWrapping pins that marking works through %w, so a
// caller can add context without losing the marking.
func TestPermanentSurvivesWrapping(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	inner := supervise.Permanent(errors.New("bad input"))

	_ = supervise.Retry(context.Background(), retryPolicy(5), func(context.Context) error {
		calls.Add(1)
		return fmt.Errorf("while calling the API: %w", inner)
	})
	if calls.Load() != 1 {
		t.Errorf("fn ran %d times; wrapping must not lose the marking", calls.Load())
	}
	if !supervise.IsPermanent(fmt.Errorf("outer: %w", inner)) {
		t.Error("IsPermanent did not see through a wrap")
	}
}

// TestLoopAlsoHonoursPermanent pins the decision that both functions obey it.
// A supervised process whose binary is missing fails identically forever.
func TestLoopAlsoHonoursPermanent(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	err := supervise.Loop(context.Background(),
		supervise.Policy{Backoff: time.Millisecond, Max: time.Millisecond, MaxRestarts: supervise.Unlimited},
		func(context.Context) error {
			calls.Add(1)
			return supervise.Permanent(errors.New("binary not found"))
		})
	if calls.Load() != 1 {
		t.Errorf("fn ran %d times; Loop must honour Permanent too", calls.Load())
	}
	if err == nil {
		t.Error("a permanent failure returned no error")
	}
}

// TestOrdinaryErrorsStillRetry pins that nothing is inferred, only an
// explicit marking stops the loop, so "an error means retry" stays true.
func TestOrdinaryErrorsStillRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	err := supervise.Retry(context.Background(), retryPolicy(4), func(context.Context) error {
		calls.Add(1)
		return errors.New("looks fatal but was never marked")
	})
	if calls.Load() != 4 {
		t.Errorf("fn ran %d times; want the full budget", calls.Load())
	}
	if !errors.Is(err, supervise.ErrTooManyRestarts) {
		t.Errorf("err = %v; want ErrTooManyRestarts", err)
	}
}

func TestPermanentNilIsNil(t *testing.T) {
	t.Parallel()
	if supervise.Permanent(nil) != nil {
		t.Error("Permanent(nil) must stay nil, or a success becomes a failure")
	}
	if supervise.IsPermanent(nil) {
		t.Error("IsPermanent(nil) reported true")
	}
}

// TestRetryVocabulary pins that a one-shot operation does not report itself as
// a restarting service.
func TestRetryVocabulary(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	p := retryPolicy(3)
	p.Out = &log
	_ = supervise.Retry(context.Background(), p, func(context.Context) error {
		return errors.New("nope")
	})
	if !strings.Contains(log.String(), "[retry]") || strings.Contains(log.String(), "restarting") {
		t.Errorf("Retry logged with the supervisor's vocabulary:\n%s", log.String())
	}
}

func TestRetryWithStats(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	stats, err := supervise.RetryWithStats(context.Background(), retryPolicy(supervise.Unlimited),
		func(context.Context) error {
			if attempts.Add(1) < 3 {
				return errors.New("transient")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// Three attempts means two retries between them.
	if stats.Restarts != 2 {
		t.Errorf("Restarts = %d; want 2 for three attempts", stats.Restarts)
	}
	if stats.LastError == nil {
		t.Error("LastError is nil; the failure that preceded success should be reported")
	}
}
