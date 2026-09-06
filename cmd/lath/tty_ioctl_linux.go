//go:build linux

package main

import "syscall"

// ioctlReadTermios is the request that reads a terminal's attributes. Linux
// spells it TCGETS; the BSDs, including macOS, spell it TIOCGETA.
const ioctlReadTermios = syscall.TCGETS

// ioctlGetWinsize is the request that reports a terminal's size.
const ioctlGetWinsize = syscall.TIOCGWINSZ
