//go:build !unix && !windows

package main

import "os"

// isTerminal is unimplemented on platforms that are neither unix nor windows,
// and answers "yes" so a debug session is refused only where the check is
// trustworthy. Both of those have a real implementation; see tty_unix.go and
// tty_windows.go.
//
// The asymmetry is deliberate: wrongly refusing a session on a platform whose
// terminals this cannot inspect would make --debug simply unavailable there,
// while wrongly allowing one costs an attach timeout and a clear error.
func isTerminal(*os.File) bool { return true }
