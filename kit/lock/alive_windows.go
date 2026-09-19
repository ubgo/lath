//go:build windows

package lock

import (
	"os"
	"time"
)

// processExists is best-effort on Windows: os.FindProcess does not report
// liveness there, so a lock is effectively governed by StaleAfter rather than
// by liveness. Documented rather than silently weaker.
func processExists(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}

// processStart is unknowable here without spawning anything, so it does not
// try: the zero time is the supported "unverifiable" answer alive falls back
// on, and LivenessVerifiable is false by construction.
//
// It used to shell out to ps like the unix build. A ps IS usually on a
// Windows PATH (Git for Windows ships MSYS's), it is slow to spawn, and it
// cannot report the start time of an arbitrary Windows process, so every
// Acquire paid for up to three useless process spawns, each capped at two
// seconds. On a loaded CI runner that took a fail-fast refusal past a second,
// which is exactly what fail-fast promises not to do (2026-09-19).
func processStart(int) time.Time { return time.Time{} }

// isZombie is always false: zombies are a unix concept, and an unreadable
// answer means "not a zombie" everywhere, so this can only ever behave as the
// unix build does when its ps is unavailable.
func isZombie(int) bool { return false }
