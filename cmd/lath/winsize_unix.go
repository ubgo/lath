//go:build unix

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// winsize mirrors struct winsize from <sys/ioctl.h>.
type winsize struct {
	rows, cols, xpixel, ypixel uint16
}

// terminalWidth reports the terminal's column count, or 0 when unknown.
//
// Why it matters here: the panel used to truncate every line at a fixed width,
// which is fine for a container name and catastrophic for the one line that
// says why a build failed. The operator sees "Cannot connect to the Docker
// daemon at uni…" and has to go find the log to learn the rest.
func terminalWidth(f *os.File) int {
	var ws winsize
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		uintptr(ioctlGetWinsize), uintptr(unsafe.Pointer(&ws)))
	if err != 0 || ws.cols == 0 {
		return 0
	}
	return int(ws.cols)
}
