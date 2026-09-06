//go:build !windows

package proc

import "syscall"

// errProcessGone is the platform's "no such process". Aliased so the
// portable code above can compare against it without naming a syscall
// constant that does not exist everywhere.
var errProcessGone = syscall.ESRCH

// killPID sends sig to a process.
func killPID(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
