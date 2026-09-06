package debug_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/pipeline/debug"
)

// sock returns a short socket path inside a temp dir.
//
// Short on purpose: a unix socket path is capped at ~104 bytes on macOS, and
// t.TempDir() plus a long test name overruns it with an error that names
// neither cause.
func sock(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s.sock")
}

func TestListenAcceptAndRun(t *testing.T) {
	t.Parallel()
	path := sock(t)
	ln, err := debug.Listen(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if ln.Socket() != path {
		t.Errorf("Socket() = %q, want %q", ln.Socket(), path)
	}

	attached := make(chan *debug.Client, 1)
	go func() {
		c, err := debug.Dial(path)
		if err != nil {
			t.Error(err)
			close(attached)
			return
		}
		attached <- c
	}()

	srv, err := ln.Accept(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cli := <-attached
	if cli == nil {
		t.Fatal("client did not attach")
	}
	defer cli.Close()

	var log []string
	go func() {
		for {
			e, err := cli.Next()
			if err != nil {
				return
			}
			if e.Kind == debug.KindPaused {
				_ = cli.Send(pipeline.ActNext, e.N)
			}
			if e.Kind == debug.KindFinished {
				return
			}
		}
	}()

	st := pipeline.NewState(pipeline.ModeExecute, srv.Reporter(), pipeline.Debug(srv))
	if err := three(&log).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "one,two,three" {
		t.Errorf("order = %q", got)
	}
}

// TestNobodyAttaches holds the promise that a forgotten --debug fails rather
// than hanging a terminal forever.
func TestNobodyAttaches(t *testing.T) {
	t.Parallel()
	ln, err := debug.Listen(sock(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	started := time.Now()
	_, err = ln.Accept(context.Background(), 150*time.Millisecond)
	if !debug.IsNoClient(err) {
		t.Fatalf("err = %v, want ErrNoClient", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("waited %s; the deadline was not honoured", elapsed)
	}
}

// TestCancelWhileWaiting, Ctrl-C at the attach prompt must exit immediately,
// not sit out the timeout.
func TestCancelWhileWaiting(t *testing.T) {
	t.Parallel()
	ln, err := debug.Listen(sock(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	started := time.Now()
	_, err = ln.Accept(ctx, time.Hour)
	if !debug.IsNoClient(err) {
		t.Fatalf("err = %v, want ErrNoClient", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to name the cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("waited %s despite cancellation", elapsed)
	}
}

// TestOnlyOneClientAttaches. A second attacher must not get a say. There is
// no safe rule for whose keystroke wins on a deploy.
func TestOnlyOneClientAttaches(t *testing.T) {
	t.Parallel()
	path := sock(t)
	ln, err := debug.Listen(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, err := debug.Dial(path)
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	srv, err := ln.Accept(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// The listener is closed once a client is accepted, so a second dial has
	// nothing to connect to.
	if second, err := debug.Dial(path); err == nil {
		_ = second.Close()
		t.Error("a second client attached to a session already being debugged")
	}
}

// TestDialMissingSocketFails. A stale path must fail fast rather than hang,
// which is what makes a leftover socket a nuisance instead of a deadlock.
func TestDialMissingSocketFails(t *testing.T) {
	t.Parallel()
	if c, err := debug.Dial(filepath.Join(t.TempDir(), "absent.sock")); err == nil {
		_ = c.Close()
		t.Error("dialling a socket that does not exist succeeded")
	}
}

// TestListenClosesTheAdvertisement. The session registration must not outlive
// the socket, or a listing offers a session nobody serves.
func TestListenClosesTheAdvertisement(t *testing.T) {
	t.Parallel()
	closed := false
	ln, err := debug.Listen(sock(t), closerFunc(func() error { closed = true; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Error("closing the listener left the advertisement live")
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
