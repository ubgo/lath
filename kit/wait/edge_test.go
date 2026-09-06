package wait_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/wait"
)

// TestNonPositiveIntervalDoesNotPanic pins the guard over time.NewTicker,
// which panics on a non-positive duration.
//
// A caller reaching this is not being perverse: Interval(budget/attempts)
// floors to zero as soon as attempts exceeds the budget in nanoseconds. A
// crash there would be a task runner taking down the build over arithmetic.
func TestNonPositiveIntervalDoesNotPanic(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -1, -time.Hour} {
		t.Run(d.String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			err := wait.Until(context.Background(), func(context.Context) (bool, error) {
				calls++
				return calls >= 2, nil
			}, wait.Interval(d), wait.Timeout(5*time.Second))
			if err != nil {
				t.Errorf("err = %v; want nil", err)
			}
		})
	}
}

// TestNonPositiveTimeoutStillAttemptsOnce pins that a zero timeout does not
// mean "never try". The condition is evaluated before any deadline check, so a
// condition already true succeeds.
func TestNonPositiveTimeoutStillAttemptsOnce(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -time.Second} {
		var calls atomic.Int32
		err := wait.Until(context.Background(), func(context.Context) (bool, error) {
			calls.Add(1)
			return true, nil
		}, wait.Timeout(d))
		if err != nil {
			t.Errorf("timeout %v: err = %v; want nil", d, err)
		}
		if calls.Load() != 1 {
			t.Errorf("timeout %v: condition ran %d times; want exactly 1", d, calls.Load())
		}
	}
}

// TestConditionTrueImmediately pins that a satisfied condition costs no
// interval. A wait that always sleeps first adds latency to every task.
func TestConditionTrueImmediately(t *testing.T) {
	t.Parallel()
	start := time.Now()
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		return true, nil
	}, wait.Interval(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s for a condition true on the first call", elapsed)
	}
}

// TestConditionErrorAbortsImmediately pins that an error stops the wait rather
// than being retried. Retrying a malformed request or a permission failure
// burns the whole timeout to reach the same answer.
func TestConditionErrorAbortsImmediately(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("bad configuration")
	var calls atomic.Int32
	start := time.Now()
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		calls.Add(1)
		return false, sentinel
	}, wait.Interval(10*time.Millisecond), wait.Timeout(5*time.Second))

	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want the condition's own error, unwrappable", err)
	}
	if calls.Load() != 1 {
		t.Errorf("condition ran %d times; want 1: an error must not be retried", calls.Load())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s; an error should abort at once", elapsed)
	}
}

func TestTimeoutReportsErrTimeout(t *testing.T) {
	t.Parallel()
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		return false, nil
	}, wait.Interval(10*time.Millisecond), wait.Timeout(300*time.Millisecond))
	if !errors.Is(err, wait.ErrTimeout) {
		t.Fatalf("err = %v; want ErrTimeout", err)
	}
	// The message must carry the timeout: "wait: timed out" alone leaves the
	// reader guessing whether the budget was 1s or 10m.
	if want := "300ms"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v; want it to mention %s", err, want)
	}
}

// TestCancellationIsDistinctFromTimeout pins that a cancelled parent reports
// context.Canceled, NOT ErrTimeout. Conflating them tells an operator their
// service was too slow when in fact the run was interrupted.
func TestCancellationIsDistinctFromTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()

	err := wait.Until(ctx, func(context.Context) (bool, error) {
		return false, nil
	}, wait.Interval(10*time.Millisecond), wait.Timeout(30*time.Second))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if errors.Is(err, wait.ErrTimeout) {
		t.Error("a cancelled wait reported ErrTimeout, blaming the wrong cause")
	}
}

func TestAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The condition is still evaluated once; if it is satisfied, that is the
	// answer. This pins that the call returns promptly either way.
	done := make(chan error, 1)
	go func() {
		done <- wait.Until(ctx, func(context.Context) (bool, error) { return false, nil })
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled context with a false condition returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Until hung on an already-cancelled context")
	}
}

// TestConditionReceivesTheDeadlineContext pins that the condition can observe
// the remaining budget, so an HTTP call inside it dies with the wait rather
// than outliving it.
func TestConditionReceivesTheDeadlineContext(t *testing.T) {
	t.Parallel()
	var gotDeadline atomic.Bool
	_ = wait.Until(context.Background(), func(ctx context.Context) (bool, error) {
		_, ok := ctx.Deadline()
		gotDeadline.Store(ok)
		return true, nil
	}, wait.Timeout(time.Second))
	if !gotDeadline.Load() {
		t.Error("the condition's context carries no deadline")
	}
}

// TestPollsRepeatedly pins that the wait actually retries rather than
// answering once and giving up.
func TestPollsRepeatedly(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		return calls.Add(1) >= 4, nil
	}, wait.Interval(10*time.Millisecond), wait.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Errorf("condition ran %d times; want 4", calls.Load())
	}
}

// ── HTTPOK ───────────────────────────────────────────────────────────────

// TestHTTPOKStatusRange pins exactly which statuses count as ready. The
// interesting ones are the boundaries and the redirect: a 3xx means the
// service answered but is not serving this path, and treating it as ready
// starts a deploy against a server that is not up.
func TestHTTPOKStatusRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		ready  bool
	}{
		{http.StatusOK, true},
		{http.StatusCreated, true},
		{http.StatusNoContent, true},
		{299, true},
		{http.StatusMultipleChoices, false}, // 300, just past the range
		{http.StatusMovedPermanently, false},
		{http.StatusUnauthorized, false},
		{http.StatusNotFound, false},
		{http.StatusServiceUnavailable, false},
		// The lower boundary is not testable over real HTTP: a 1xx is an
		// informational status, which Go sends as an interim response followed
		// by a real one, so the client never sees it as final.
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Written directly so a 3xx is not followed by the client.
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			err := wait.HTTPOK(context.Background(), srv.URL,
				wait.Interval(20*time.Millisecond), wait.Timeout(400*time.Millisecond))
			if tc.ready && err != nil {
				t.Errorf("status %d: err = %v; want ready", tc.status, err)
			}
			if !tc.ready && !errors.Is(err, wait.ErrTimeout) {
				t.Errorf("status %d: err = %v; want ErrTimeout", tc.status, err)
			}
		})
	}
}

// TestHTTPOKRetriesUntilTheServerIsUp is the primitive's real job: a service
// that refuses connections and then starts answering.
func TestHTTPOKRetriesUntilTheServerIsUp(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := wait.HTTPOK(context.Background(), srv.URL,
		wait.Interval(20*time.Millisecond), wait.Timeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if hits.Load() < 3 {
		t.Errorf("server saw %d requests; want at least 3", hits.Load())
	}
}

// TestHTTPOKConnectionRefusedIsRetried pins that an unreachable server is a
// retry, not an abort. It is the normal state of a service still booting.
func TestHTTPOKConnectionRefusedIsRetried(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: bind, record, close.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	start := time.Now()
	err := wait.HTTPOK(context.Background(), url,
		wait.Interval(20*time.Millisecond), wait.Timeout(400*time.Millisecond))
	if !errors.Is(err, wait.ErrTimeout) {
		t.Errorf("err = %v; want ErrTimeout after retrying", err)
	}
	// It must have spent the budget retrying rather than failing at once.
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("gave up after %s; a refused connection must be retried", elapsed)
	}
}

// TestHTTPOKMalformedURLAbortsImmediately pins the opposite: a URL that can
// never work is a caller bug, not a service that is slow to start.
func TestHTTPOKMalformedURLAbortsImmediately(t *testing.T) {
	t.Parallel()
	start := time.Now()
	err := wait.HTTPOK(context.Background(), "://not a url",
		wait.Interval(20*time.Millisecond), wait.Timeout(10*time.Second))
	if err == nil {
		t.Fatal("a malformed URL produced no error")
	}
	if errors.Is(err, wait.ErrTimeout) {
		t.Error("a malformed URL was reported as a timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s; want an immediate abort", elapsed)
	}
}

// TestHTTPOKDrainsBodies pins that response bodies are read and closed.
// Leaking them exhausts the connection pool, so a long wait starts failing for
// a reason unrelated to the service being watched.
func TestHTTPOKDrainsBodies(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 30 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "not ready yet, with a body worth draining")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := wait.HTTPOK(context.Background(), srv.URL,
		wait.Interval(5*time.Millisecond), wait.Timeout(10*time.Second)); err != nil {
		t.Fatalf("err = %v after %d requests", err, hits.Load())
	}
}

func TestHTTPOKCancellation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	err := wait.HTTPOK(ctx, srv.URL, wait.Interval(10*time.Millisecond), wait.Timeout(30*time.Second))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
}
