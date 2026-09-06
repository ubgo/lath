//go:build windows

package main

import (
	"os"
	"syscall"
)

// isTerminal reports whether f is a console.
//
// GetConsoleMode succeeds only for a real console handle: a pipe, a file or a
// redirected stream fails it. That is exactly the question `--debug` asks, and
// it is answerable here — the previous !unix stub answered "yes" always, which
// meant a debug session could be started against a pipe nobody could type
// into, and the failure arrived two minutes later as an attach timeout.
//
// Hand-rolled rather than importing golang.org/x/sys/windows: the runner ships
// with no external dependencies, and syscall carries this call already.
func isTerminal(f *os.File) bool {
	var mode uint32
	err := syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode)
	return err == nil
}
