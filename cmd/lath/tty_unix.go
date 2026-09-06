//go:build unix

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is a terminal.
//
// Hand-rolled rather than importing golang.org/x/term: cmd/lath ships with an
// empty require block, and one dependency for one ioctl is not a trade worth
// making. The ioctl is the same one every terminal library performs.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, err := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(),
		uintptr(ioctlReadTermios), uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return err == 0
}
