package debug

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ubgo/lath/pipeline"
)

// Client is the debugger-side end of a session: it reads events and sends
// decisions.
//
// Deliberately UI-free. It knows the protocol and nothing about terminals, so
// the panel, a test, or a script can each drive a session through the same
// type, and so this package stays stdlib-only.
type Client struct {
	dec *json.Decoder
	enc *json.Encoder
	rc  io.Closer

	mu     sync.Mutex
	closed bool
}

// NewClient returns a Client speaking the protocol over rw.
func NewClient(rw io.ReadWriteCloser) *Client {
	return &Client{dec: json.NewDecoder(rw), enc: json.NewEncoder(rw), rc: rw}
}

// Next returns the next event, or io.EOF when the session ends.
//
// A blocking call rather than a channel: the caller is a render loop that must
// know precisely when the stream ended, and a closed channel cannot carry the
// reason it closed.
func (c *Client) Next() (Event, error) {
	var e Event
	if err := c.dec.Decode(&e); err != nil {
		return Event{}, err
	}
	if !e.Kind.Valid() {
		return Event{}, fmt.Errorf("debug: unknown event kind %q", e.Kind)
	}
	return e, nil
}

// Send answers the pause numbered ack with cmd.
//
// The ack is required rather than tracked internally so a caller cannot answer
// a pause it has not seen: passing the number it just received is the only way
// to be sure the answer matches what is on screen.
func (c *Client) Send(cmd pipeline.Action, ack int) error {
	if !cmd.Valid() {
		return fmt.Errorf("debug: %q is not one of %v", cmd, pipeline.ActionValues)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("debug: client is closed")
	}
	return encodeLine(c.enc, Control{Cmd: cmd, Ack: ack})
}

// Close ends the session. The pipeline sees the disconnect and aborts, which
// is the intended meaning of closing a debugger mid-run.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.rc.Close()
}
