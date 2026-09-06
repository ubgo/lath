package download_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/ubgo/lath/kit/download"
)

const payload = "#!/bin/sh\necho a fetched binary\n"

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func serve(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchesAndVerifies(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) })
	dst := filepath.Join(t.TempDir(), "tool")

	if err := download.ToFile(context.Background(), url, dst, download.SHA256(digestOf(payload))); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("content = %q", got)
	}
}

// TestChecksumMismatchWritesNothing is the guarantee that matters: an
// unverified artifact must never reach the destination path, where a later
// step would find it and assume it is good.
func TestChecksumMismatchWritesNothing(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "TAMPERED PAYLOAD") })
	dst := filepath.Join(t.TempDir(), "tool")

	err := download.ToFile(context.Background(), url, dst, download.SHA256(digestOf(payload)))
	if !errors.Is(err, download.ErrChecksumMismatch) {
		t.Fatalf("err = %v; want ErrChecksumMismatch", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("an unverified download was written to the destination")
	}
	// Both digests must appear, or diagnosing a mismatch means recomputing by hand.
	if !strings.Contains(err.Error(), digestOf(payload)) {
		t.Errorf("err = %v; want it to name the expected digest", err)
	}
}

// TestChecksumIsCaseAndSpaceInsensitive pins that a digest pasted from a
// release page, often uppercase, often with stray whitespace, still works.
func TestChecksumIsCaseAndSpaceInsensitive(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) })
	for _, form := range []string{
		digestOf(payload),
		strings.ToUpper(digestOf(payload)),
		"  " + digestOf(payload) + "\n",
	} {
		dst := filepath.Join(t.TempDir(), "tool")
		if err := download.ToFile(context.Background(), url, dst, download.SHA256(form)); err != nil {
			t.Errorf("digest %q rejected: %v", form, err)
		}
	}
}

func TestNoChecksumStillWorks(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) })
	dst := filepath.Join(t.TempDir(), "tool")
	if err := download.ToFile(context.Background(), url, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("nothing was written: %v", err)
	}
}

func TestNonSuccessStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusNotFound, http.StatusUnauthorized,
		http.StatusInternalServerError, http.StatusServiceUnavailable,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			url := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			dst := filepath.Join(t.TempDir(), "tool")

			err := download.ToFile(context.Background(), url, dst)
			if !errors.Is(err, download.ErrStatus) {
				t.Fatalf("err = %v; want ErrStatus", err)
			}
			if _, statErr := os.Stat(dst); statErr == nil {
				t.Error("a failed request still wrote a file")
			}
			// The status must appear, or a 404 and a 500 are indistinguishable.
			if !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Errorf("err = %v; want it to name the status", err)
			}
		})
	}
}

func TestHeadersAreSent(t *testing.T) {
	t.Parallel()
	var gotAuth, gotAccept atomic.Value
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotAccept.Store(r.Header.Get("Accept"))
		fmt.Fprint(w, payload)
	})
	dst := filepath.Join(t.TempDir(), "tool")
	if err := download.ToFile(context.Background(), url, dst,
		download.Header("Authorization", "Bearer tok"),
		download.Header("Accept", "application/octet-stream")); err != nil {
		t.Fatal(err)
	}
	if gotAuth.Load() != "Bearer tok" || gotAccept.Load() != "application/octet-stream" {
		t.Errorf("headers = %v, %v", gotAuth.Load(), gotAccept.Load())
	}
}

func TestRedirectsAreFollowedButBounded(t *testing.T) {
	t.Parallel()
	t.Run("a normal redirect is followed", func(t *testing.T) {
		t.Parallel()
		var final string
		mux := http.NewServeMux()
		mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) })
		mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, final+"/final", http.StatusFound)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		final = srv.URL

		dst := filepath.Join(t.TempDir(), "tool")
		if err := download.ToFile(context.Background(), srv.URL+"/start", dst,
			download.SHA256(digestOf(payload))); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an infinite redirect loop is stopped", func(t *testing.T) {
		t.Parallel()
		var self string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, self, http.StatusFound)
		}))
		defer srv.Close()
		self = srv.URL

		done := make(chan error, 1)
		go func() {
			done <- download.ToFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "x"))
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Error("a redirect loop completed successfully")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a redirect loop was not bounded")
		}
	})
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
		fmt.Fprint(w, payload)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	start := time.Now()
	err := download.ToFile(ctx, url, filepath.Join(t.TempDir(), "tool"))
	if err == nil {
		t.Fatal("a cancelled download reported success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; cancellation must not wait out the transfer", elapsed)
	}
}

func TestTimeoutBoundsTheWholeTransfer(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		// Headers arrive at once, the body never finishes: the failure mode a
		// connection-only timeout misses entirely.
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(10 * time.Second)
	})
	start := time.Now()
	err := download.ToFile(context.Background(), url, filepath.Join(t.TempDir(), "tool"),
		download.Timeout(500*time.Millisecond))
	if err == nil {
		t.Fatal("a stalled body was not timed out")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; the timeout must cover the body, not just the connection", elapsed)
	}
}

func TestUnreachableHost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // bind, record, close: nothing listens now

	dst := filepath.Join(t.TempDir(), "tool")
	if err := download.ToFile(context.Background(), url, dst); err == nil {
		t.Fatal("downloading from a closed port succeeded")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("a failed download left a file behind")
	}
}

// TestClientOptionReplacesTheTransport. The reason the option exists: a caller
// with its own proxy, instrumentation or certificate pool must be able to
// supply it, and the package must then use THAT client rather than quietly
// building its own alongside it.
func TestClientOptionReplacesTheTransport(t *testing.T) {
	t.Parallel()
	var used atomic.Bool
	custom := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used.Store(true)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(payload)),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})}
	dst := filepath.Join(t.TempDir(), "tool")

	err := download.ToFile(context.Background(), "http://example.invalid/tool", dst,
		download.Client(custom), download.SHA256(digestOf(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if !used.Load() {
		t.Error("the supplied client was ignored; the package built its own")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != payload {
		t.Errorf("dst = %q, %v; want the body the supplied client returned", got, err)
	}
}

// TestABodyThatDiesMidStreamWritesNothing. A truncated transfer that left a
// partial file at dst would be the worst outcome here: a binary that exists,
// is the right name, and is half a binary. The checksum cannot save a caller
// who did not supply one, so the write itself must not happen.
func TestABodyThatDiesMidStreamWritesNothing(t *testing.T) {
	t.Parallel()
	custom := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(failingBody()),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})}
	dst := filepath.Join(t.TempDir(), "tool")

	err := download.ToFile(context.Background(), "http://example.invalid/tool", dst,
		download.Client(custom))
	if err == nil {
		t.Fatal("a transfer that failed halfway reported success")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("err = %v; want the read named, not the request", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Error("a partial file was left at the destination")
	}
}

// TestATimeoutOfZeroFallsBackToTheDefault. Zero is what a config struct holds
// when nobody set it, and honouring it literally would mean a context that is
// already expired: every download would fail, and the caller who never asked
// for a timeout would be the one it happened to.
func TestATimeoutOfZeroFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	})
	dst := filepath.Join(t.TempDir(), "tool")

	if err := download.ToFile(context.Background(), url, dst, download.Timeout(0)); err != nil {
		t.Fatalf("a zero timeout was taken literally: %v", err)
	}
}

// roundTripFunc adapts a function to http.RoundTripper, so a test can answer a
// request without a listening socket.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// failingBody delivers some bytes and then fails, the shape a dropped
// connection takes after the headers have already arrived.
func failingBody() io.Reader {
	return io.MultiReader(
		strings.NewReader("half a bin"),
		iotest.ErrReader(errors.New("connection reset by peer")),
	)
}

// TestTheDefaultClientDoesNotShareTheGlobalPool.
//
// The package promises not to depend on process-global HTTP settings. A fresh
// http.Client with a nil Transport keeps that promise in letter and breaks it
// in fact: it uses http.DefaultTransport, so the connection pool is shared
// with every other component in the process, and anything calling
// CloseIdleConnections on it — which httptest.Server.Close does on every
// close — can break an unrelated download that is mid-flight.
//
// Asserted by making the shared transport hostile: closing its idle
// connections repeatedly while a download runs. With a private transport the
// download is untouched. With the global one this is flaky, which is exactly
// how it was found.
func TestTheDefaultClientDoesNotShareTheGlobalPool(t *testing.T) {
	t.Parallel()
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	})
	dst := filepath.Join(t.TempDir(), "tool")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			http.DefaultTransport.(*http.Transport).CloseIdleConnections()
			time.Sleep(time.Millisecond)
		}
	}()
	t.Cleanup(func() { <-done })

	for attempt := 1; attempt <= 5; attempt++ {
		if err := download.ToFile(context.Background(), url, dst, download.SHA256(digestOf(payload))); err != nil {
			t.Fatalf("attempt %d was broken by an unrelated component closing idle connections: %v",
				attempt, err)
		}
	}
}
