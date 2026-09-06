package main

import (
	"io"
	"os"
	"sync"
)

// liveWriter passes a launched run's output straight to this terminal until
// the panel takes over, then keeps only the log copy.
//
// Why both, in that order: a target may be an ordinary command with no
// pipeline in it at all, generating a file, tailing a dev server, and `lath
// tui` must behave like `lath run` for those, showing their output as it
// happens. But once a debug session opens, the panel owns the screen, and a
// child still writing to it would scribble over the frame being drawn.
//
// The switch is one-way. Nothing that has already been printed is taken back;
// the panel simply clears the screen when it starts.
type liveWriter struct {
	mu    sync.Mutex
	log   io.Writer
	live  io.Writer
	quiet bool
}

// newLiveWriter writes to the terminal and to log until Quiet is called.
func newLiveWriter(log io.Writer) *liveWriter {
	return &liveWriter{log: log, live: os.Stderr}
}

func (w *liveWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.quiet {
		// Errors on the terminal copy are ignored: losing a line of passthrough
		// must never fail the run that produced it.
		_, _ = w.live.Write(p)
	}
	return w.log.Write(p)
}

// Quiet stops the terminal copy. Called when the panel takes the screen.
func (w *liveWriter) Quiet() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.quiet = true
}
