package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// captureOutput runs fn with os.Stdout and os.Stderr redirected, returning
// everything written to either.
//
// Several printers here write straight to the process streams rather than to
// an injected io.Writer, because they are the command's product rather than a
// library's. Testing them means swapping the streams, so these tests must NOT
// run in parallel with anything that prints.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	func() {
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			_ = w.Close()
		}()
		fn()
	}()

	out := <-done
	_ = r.Close()
	return out
}
