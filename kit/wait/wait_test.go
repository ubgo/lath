package wait_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/wait"
)

func TestUntilSucceeds(t *testing.T) {
	t.Parallel()
	var calls int
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		calls++
		return calls >= 3, nil
	}, wait.Interval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("cond ran %d times; want 3", calls)
	}
}

// TestUntilChecksBeforeSleeping pins that an already-satisfied condition
// returns at once rather than paying an interval for nothing.
func TestUntilChecksBeforeSleeping(t *testing.T) {
	t.Parallel()
	start := time.Now()
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		return true, nil
	}, wait.Interval(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s; the first check happened after a sleep", elapsed)
	}
}

func TestUntilTimeout(t *testing.T) {
	t.Parallel()
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		return false, nil
	}, wait.Interval(5*time.Millisecond), wait.Timeout(50*time.Millisecond))
	if !errors.Is(err, wait.ErrTimeout) {
		t.Fatalf("err = %v; want ErrTimeout", err)
	}
}

// TestUntilAbortsOnConditionError pins that an error from cond is fatal, not
// retried. A condition that cannot be EVALUATED differs from one that is not
// yet TRUE, and retrying the former just delays the report by the timeout.
func TestUntilAbortsOnConditionError(t *testing.T) {
	t.Parallel()
	var calls int
	boom := errors.New("cannot evaluate")
	err := wait.Until(context.Background(), func(context.Context) (bool, error) {
		calls++
		return false, boom
	}, wait.Interval(5*time.Millisecond), wait.Timeout(5*time.Second))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the condition's own error", err)
	}
	if calls != 1 {
		t.Errorf("cond ran %d times; an error must abort immediately", calls)
	}
}

// TestUntilHonoursCancellationMidInterval pins that cancelling does not have to
// wait out the current sleep.
func TestUntilHonoursCancellationMidInterval(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	start := time.Now()
	err := wait.Until(ctx, func(context.Context) (bool, error) { return false, nil },
		wait.Interval(5*time.Second), wait.Timeout(time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s; cancellation waited out the interval", elapsed)
	}
}

func TestHTTPOK(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fails twice, then succeeds. The shape of a server still booting.
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := wait.HTTPOK(context.Background(), srv.URL,
		wait.Interval(5*time.Millisecond), wait.Timeout(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got < 3 {
		t.Errorf("server was hit %d times; want at least 3", got)
	}
}

func TestHTTPOKTimesOutOnRefusedConnection(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: connection refused is exactly what waiting is
	// for, so it must be retried rather than treated as fatal.
	err := wait.HTTPOK(context.Background(), "http://127.0.0.1:1",
		wait.Interval(5*time.Millisecond), wait.Timeout(60*time.Millisecond))
	if !errors.Is(err, wait.ErrTimeout) {
		t.Fatalf("err = %v; want ErrTimeout", err)
	}
}

// TestHTTPOKRejectsMalformedURLImmediately pins that a URL which can never
// become valid aborts instead of being retried for the whole timeout.
func TestHTTPOKRejectsMalformedURLImmediately(t *testing.T) {
	t.Parallel()
	start := time.Now()
	err := wait.HTTPOK(context.Background(), "://not a url",
		wait.Interval(time.Second), wait.Timeout(10*time.Second))
	if err == nil {
		t.Fatal("a malformed URL produced no error")
	}
	if errors.Is(err, wait.ErrTimeout) {
		t.Error("a malformed URL was retried until timeout instead of aborting")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s", elapsed)
	}
}
