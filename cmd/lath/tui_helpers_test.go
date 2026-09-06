package main

import (
	"net"
	"testing"

	"github.com/ubgo/lath/pipeline/debug"
)

// connectedPair returns a Server and Client over loopback TCP.
//
// Not net.Pipe: it is unbuffered, so a write blocks until read, which
// deadlocks any test that queues more than one message.
func connectedPair(t *testing.T) (*debug.Server, *debug.Client) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	dialed, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() { _ = dialed.Close(); _ = server.Close() })
	return debug.NewServer(server), debug.NewClient(dialed)
}
