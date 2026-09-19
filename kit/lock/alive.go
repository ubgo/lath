package lock

import (
	"os"
	"time"
)

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

// LivenessVerifiable reports whether this machine can tell a live lock holder
// from a process that merely inherited its PID.
//
// The distinction needs a process START TIME, which this package reads with
// ps. Where that is unavailable — Windows, and any system whose ps does not
// report one — liveness degrades to "a process with that PID exists", so a
// recycled PID keeps a dead lock alive until StaleAfter ages it out.
//
// Exported because it changes what a CALLER should do, not just what a test
// should assert: a tool relying on locks to prevent concurrent deploys wants
// to know whether it is relying on liveness or on a timeout, and the answer is
// a property of the machine rather than of the lock.
//
// Probes with this process, whose start time is by definition knowable if
// anything's is.
func LivenessVerifiable() bool { return !processStart(os.Getpid()).IsZero() }

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
