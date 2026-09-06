//go:build !unix

package main

import "os"

// terminalWidth is unavailable outside unix; 0 means "unknown", and callers
// fall back to a fixed width.
func terminalWidth(*os.File) int { return 0 }
