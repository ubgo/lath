package debug

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// DefaultAttachTimeout bounds how long a run waits for a debugger.
//
// Two minutes, not the thirty seconds this originally shipped with. Thirty was
// chosen by reasoning about how long it takes to switch terminals; the first
// person to actually use it missed the window, because the real sequence is
// read the message, find the other terminal, and type, often while the run
// was still compiling and not being watched at all. The failure was silent
// from the operator's side: by the time they attached, the run had given up.
//
// It is still bounded rather than infinite. A run that waited forever would be
// indistinguishable from a stuck deploy, which is the confusion a debugger
// exists to remove. CI never reaches this at all, a session without a
// terminal is refused before any step runs.
const DefaultAttachTimeout = 2 * time.Minute

// Listener is the socket half of a session, before a client has attached.
//
// Split from Serve so a caller can print the attach instructions with the real
// path in them BEFORE blocking. The operator needs to read the hint while the
// run is waiting, not after.
type Listener struct {
	ln     net.Listener
	socket string
	closer io.Closer
}

// Socket is the path a client must dial.
func (l *Listener) Socket() string { return l.socket }

// Close releases the socket and the advertisement.
func (l *Listener) Close() error {
	var first error
	if l.ln != nil {
		if err := l.ln.Close(); err != nil {
			first = err
		}
		l.ln = nil
	}
	if l.closer != nil {
		if err := l.closer.Close(); err != nil && first == nil {
			first = err
		}
		l.closer = nil
	}
	return first
}

// Listen opens a unix socket at path, taking ownership of it.
//
// A unix socket rather than TCP, deliberately: this is a control channel into
// a running deploy, and a unix socket is scoped by filesystem permissions
// with no port to be reached across a network. Exposing it over TCP would need
// authentication designed rather than bolted on.
//
// advertisement, if non-nil, is closed with the listener, normally the
// session registration that produced path.
func Listen(path string, advertisement io.Closer) (*Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("debug: listening on %s: %w", path, err)
	}
	return &Listener{ln: ln, socket: path, closer: advertisement}, nil
}

// Accept waits for a client and returns the Server speaking to it.
//
// Invariant: exactly one client per session. A second connection would need a
// rule for whose keystroke wins, and there is no answer to that which is safe
// on a deploy, so the listener is closed as soon as one client is accepted.
//
// Returns ErrNoClient if timeout elapses or ctx is cancelled, so a caller can
// tell "nobody was watching" from a real failure.
func (l *Listener) Accept(ctx context.Context, timeout time.Duration) (*Server, error) {
	if timeout <= 0 {
		timeout = DefaultAttachTimeout
	}
	if d, ok := l.ln.(interface{ SetDeadline(time.Time) error }); ok {
		if err := d.SetDeadline(time.Now().Add(timeout)); err != nil {
			return nil, fmt.Errorf("debug: %w", err)
		}
	}

	// Unblocks Accept when the caller's context is cancelled, Ctrl-C while
	// waiting must exit, not sit until the deadline.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.ln.Close()
		case <-stop:
		}
	}()

	conn, err := l.ln.Accept()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoClient, ctxErr)
		}
		return nil, fmt.Errorf("%w after %s: %w", ErrNoClient, timeout, err)
	}
	// One client only: stop listening, but keep the socket path owned so the
	// advertisement stays truthful until the run ends.
	_ = l.ln.Close()
	l.ln = nil
	return NewServer(conn), nil
}

// Dial attaches to a session's socket as a client.
func Dial(socket string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		// A socket file whose server is gone fails here rather than hanging,
		// which is why a stale one is a nuisance and not a deadlock.
		return nil, fmt.Errorf("debug: attaching to %s: %w", socket, err)
	}
	return NewClient(conn), nil
}

// IsNoClient reports whether err means nobody was watching.
func IsNoClient(err error) bool { return errors.Is(err, ErrNoClient) }
