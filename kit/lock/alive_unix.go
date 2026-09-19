//go:build !windows

package lock

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"time"

	"github.com/ubgo/lath/kit/proc"
)

// startLookupTimeout bounds the ps call. A liveness check that hangs would
// make Acquire hang, which is the failure the fail-fast default exists to
// avoid, so it is capped hard.
const startLookupTimeout = 2 * time.Second

// psStartLayouts are the formats `ps -o lstart=` produces. Two are listed
// because the day-of-month is space-padded on some systems and zero-padded on
// others, and guessing wrong must not be fatal, see processStart.
var psStartLayouts = []string{
	"Mon Jan  2 15:04:05 2006",
	"Mon Jan 2 15:04:05 2006",
}

// processExists reports whether a PID is live.
//
// Signal 0 delivers nothing and only performs the permission and existence
// checks. EPERM means the process exists but belongs to another user, still
// alive, and still holding the lock.
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processStart reports when a process began, or the zero time when that cannot
// be determined on this platform.
//
// Shells out to ps rather than reading /proc, because /proc does not exist on
// macOS and this needs one implementation. The zero return is a supported
// answer, not a failure: callers treat it as "unverifiable" and fall back.
func processStart(pid int) time.Time {
	if !proc.Exists("ps") {
		return time.Time{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), startLookupTimeout)
	defer cancel()

	out, err := proc.Output(ctx, "ps", []string{"-o", "lstart=", "-p", itoa(pid)})
	if err != nil {
		return time.Time{}
	}
	out = strings.TrimSpace(out)
	for _, layout := range psStartLayouts {
		if t, parseErr := time.ParseInLocation(layout, out, time.Local); parseErr == nil {
			return t
		}
	}
	return time.Time{}
}

// zombieState is the process state ps reports for an exited, unreaped process.
// The letter is the first character of the STAT column on every unix ps.
const zombieState = 'Z'

// isZombie reports whether pid has exited but not yet been reaped.
//
// Uses ps for the same reason processStart does: /proc does not exist on
// macOS, and one implementation is worth more than two. An unreadable answer
// means "not a zombie", so a platform where this cannot be determined behaves
// exactly as it did before. The check can only ever reclaim a lock, never
// refuse to.
func isZombie(pid int) bool {
	if !proc.Exists("ps") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), startLookupTimeout)
	defer cancel()

	out, err := proc.Output(ctx, "ps", []string{"-o", "state=", "-p", itoa(pid)})
	if err != nil {
		return false
	}
	state := strings.TrimSpace(out)
	return state != "" && state[0] == zombieState
}
