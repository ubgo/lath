//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "syscall"

// ioctlReadTermios is the request that reads a terminal's attributes. See the
// linux file for why this differs per platform.
const ioctlReadTermios = syscall.TIOCGETA

// ioctlGetWinsize is the request that reports a terminal's size.
const ioctlGetWinsize = syscall.TIOCGWINSZ
