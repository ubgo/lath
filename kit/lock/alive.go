package lock

import (
	"context"
	"os"
	"strings"
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

// selfInfo describes this process as a lock holder.
func selfInfo(note string) (Info, error) {
	host, err := os.Hostname()
	if err != nil {
		// A hostname is diagnostic, not load-bearing. Failing the lock because
		// the machine will not name itself would be absurd.
		host = "unknown"
	}
	pid := os.Getpid()
	return Info{
		PID:     pid,
		Started: processStart(pid),
		Since:   time.Now(),
		Host:    host,
		Note:    note,
	}, nil
}

// alive reports whether the recorded holder is still running.
//
// Two questions, because one is not enough. Does a process with that PID exist?
// And is it the SAME process, PIDs are recycled, so an unrelated program that
// inherited the number would otherwise keep a dead lock alive forever.
//
// Degradation is deliberate and always toward caution: when the start time
// cannot be established, either now or when the lock was taken, existence
// alone decides. That risks honouring a lock slightly too long, which is
// recoverable; the opposite risks breaking a live one, which is not.
func alive(i Info) bool {
	if i.PID <= 0 {
		return false
	}
	if !processExists(i.PID) {
		return false
	}
	if isZombie(i.PID) {
		// A process that has exited but has not been reaped still answers
		// signal 0, so the existence check above says yes. It holds nothing,
		// serves nothing, and its socket accepts connections that are never
		// answered, treating it as a live holder makes a finished run look
		// like one still in progress, forever.
		return false
	}
	if i.Started.IsZero() {
		return true
	}
	current := processStart(i.PID)
	if current.IsZero() {
		return true
	}
	// Second granularity: ps reports no finer, and two processes reusing a PID
	// within the same second is not a case worth optimising for.
	return current.Truncate(time.Second).Equal(i.Started.Truncate(time.Second))
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

// itoa avoids importing strconv for one call in a file that is otherwise about
// process identity.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
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
