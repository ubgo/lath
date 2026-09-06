package wait_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/wait"
)

// TestHTTPOptionsReachTheRequest covers the knobs that shape the probe.
//
// They matter because a health endpoint is rarely a bare GET on the default
// client: it may need a header to get past a proxy, a HEAD to avoid a body, or
// a client with a shorter timeout than the wait itself.
func TestHTTPOptionsReachTheRequest(t *testing.T) {
	t.Parallel()
	var (
		gotMethod string
		gotHeader string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotHeader = r.Method, r.Header.Get("X-Probe")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := wait.HTTPOK(context.Background(), srv.URL,
		wait.Method(http.MethodHead),
		wait.Header("X-Probe", "lath"),
		wait.Client(&http.Client{Timeout: 5 * time.Second}),
		wait.Timeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodHead {
		t.Errorf("method = %q, want HEAD", gotMethod)
	}
	if gotHeader != "lath" {
		t.Errorf("header = %q, want it forwarded", gotHeader)
	}
}

// TestAcceptDecidesWhatCountsAsReady. A service may signal readiness with
// something other than 2xx, and a probe that cannot express that forces the
// caller to hand-roll the whole wait.
func TestAcceptDecidesWhatCountsAsReady(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	// Without Accept, 418 is not ready and the wait times out.
	err := wait.HTTPOK(context.Background(), srv.URL, wait.Timeout(300*time.Millisecond))
	if err == nil {
		t.Error("418 was treated as ready by default")
	}

	// With it, the caller's rule decides.
	err = wait.HTTPOK(context.Background(), srv.URL, wait.Timeout(2*time.Second),
		wait.Accept(func(code int) bool { return code == http.StatusTeapot }))
	if err != nil {
		t.Errorf("Accept did not make 418 count as ready: %v", err)
	}
}
