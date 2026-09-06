package debug

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ubgo/lath/pipeline"
)

// ErrNoClient means no debugger attached before the deadline, or the one that
// had attached went away. Callers branch on it to distinguish "nobody was
// watching" from a pipeline failure.
var ErrNoClient = errors.New("debug: no client attached")

// Server is the pipeline-side end of a debug session: a pipeline.Debugger
// that emits events to a connection and reads decisions back.
//
// Invariant: Pause blocks until the client answers or the connection fails.
// It never invents an answer. A debugger that has lost its operator returns
// an error so the run aborts, because losing supervision is not consent to
// proceed.
type Server struct {
	enc *json.Encoder
	dec *json.Decoder
	rc  io.Closer

	mu sync.Mutex
}

// NewServer returns a Server speaking the protocol over rw.
//
// Takes an io.ReadWriteCloser rather than a net.Conn so the whole protocol is
// testable over net.Pipe with no socket, no filesystem and no terminal, which
// is most of why the transport is JSON over a stream rather than a callback.
func NewServer(rw io.ReadWriteCloser) *Server {
	return &Server{enc: json.NewEncoder(rw), dec: json.NewDecoder(rw), rc: rw}
}

// Reporter returns a pipeline.Reporter that emits this session's events.
func (s *Server) Reporter() pipeline.Reporter { return NewReporter(s.send) }

// send writes one event. Serialized because a Reporter call can arrive from a
// step while Pause is writing, and interleaved JSON is unparseable.
func (s *Server) send(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return encodeLine(s.enc, e)
}

// Plan implements pipeline.Debugger.
func (s *Server) Plan(name string, steps []pipeline.PlanEntry) error {
	return s.send(Event{Kind: KindPlan, Pipeline: name, Steps: steps})
}

// Pause implements pipeline.Debugger. It announces the pause and blocks until
// the client answers it.
//
// Control messages naming a different pause are discarded rather than obeyed ,
// see Control.Ack for the failure that guards against. Unknown actions are
// discarded too: the alternative is treating them as "keep going", which is
// the one outcome nobody asked for.
func (s *Server) Pause(p pipeline.Pause) (pipeline.Action, error) {
	if err := s.send(Event{
		Kind:       KindPaused,
		N:          p.Position,
		Total:      p.Total,
		Name:       p.Step.Name(),
		Next:       p.Next,
		State:      p.State,
		Replayable: p.Replayable,
		Err:        errString(p.Err),
	}); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoClient, err)
	}

	for {
		var c Control
		if err := s.dec.Decode(&c); err != nil {
			// EOF is the ordinary shape of a client closing its terminal.
			// Reported as ErrNoClient either way, so the run aborts rather
			// than continuing unattended.
			return "", fmt.Errorf("%w: %w", ErrNoClient, err)
		}
		if c.Ack != p.Position {
			// Stale: queued keystrokes, a reconnecting client replaying, or
			// an operator typing ahead over nc.
			continue
		}
		if !c.Cmd.Valid() {
			continue
		}
		return c.Cmd, nil
	}
}

// Finish implements pipeline.Debugger.
func (s *Server) Finish(f pipeline.Finished) error {
	return s.send(Event{Kind: KindFinished, Err: errString(f.Err), State: f.State})
}

// Close releases the connection.
func (s *Server) Close() error { return s.rc.Close() }
