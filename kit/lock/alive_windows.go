//go:build windows

package lock

import "os"

// processExists is best-effort on Windows: os.FindProcess does not report
// liveness there, so a lock is effectively governed by StaleAfter rather than
// by liveness. Documented rather than silently weaker.
func processExists(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
