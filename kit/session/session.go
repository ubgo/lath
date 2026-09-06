// Package session advertises a running process's endpoint so another process
// can find and attach to it.
//
// The problem it solves: a long-running command wants to expose a socket, and
// something started later (a viewer, a debugger, an attach command) has to
// discover it without being told where to look. Doing that by hand means
// inventing a naming scheme, then discovering that a crashed run leaves its
// files behind and the next listing offers a socket nobody is serving.
//
// Liveness is the part worth reusing. A PID alone is not enough, because PIDs
// are recycled and an unrelated process inheriting the number would make a
// dead session look live forever; this delegates to kit/lock, which records
// the holder's process START TIME for exactly that reason.
//
// Nothing here knows what travels over the socket. The endpoint is a path and
// the description is a string map, so a pipeline debugger, a log tail and a
// profiler can all use it unchanged.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/lock"
)

const (
	// lockExt marks the file recording a session's holder.
	lockExt = ".lock"
	// sockExt marks a session's socket.
	sockExt = ".sock"
	// dirMode lets only this user reach the directory. Sessions carry a
	// control channel into a running process, so the directory is owner-only
	// rather than the usual 0755, filesystem permissions are the ONLY thing
	// scoping who may attach.
	dirMode = 0o700
)

// DirEnv names the environment variable overriding where sessions live.
//
// The one escape hatch, for a machine whose temp directory is shared between
// users or wiped mid-run. Callers should not need it.
const DirEnv = "LATH_SESSION_DIR"

// Meta describes a session to whoever is choosing one to attach to.
//
// A string map rather than a struct because this package must not know what
// kind of session it is holding: the process advertising it decides which
// fields are worth showing, and adding one must not require a change here.
type Meta map[string]string

// Session is one advertised endpoint.
type Session struct {
	// ID is the session's name, unique on this machine.
	ID string
	// Socket is the path to connect to.
	Socket string
	// Meta is whatever the advertising process described itself with.
	Meta Meta
	// PID is the advertising process.
	PID int
	// Since is when it was advertised.
	Since time.Time
}

// Dir returns the directory sessions are advertised in, creating it.
//
// Under the user's temp directory rather than a home-directory dotfile: a
// session is meaningless after a reboot, so it belongs somewhere the system
// already clears.
func Dir() (string, error) {
	dir := os.Getenv(DirEnv)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "lath-sessions")
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	// Enforced rather than assumed: MkdirAll leaves an EXISTING directory's
	// mode alone, and the process umask can widen a newly created one. Either
	// way the result would be a directory carrying control channels into
	// running deploys that other users on the machine can reach.
	//
	// A failure here is a refusal, not a warning: a session directory that
	// cannot be secured must not be used.
	if err := os.Chmod(dir, dirMode); err != nil {
		return "", fmt.Errorf("session: securing %s: %w", dir, err)
	}
	return dir, nil
}

// Advertised is a live advertisement. Close it when the session ends.
type Advertised struct {
	socket string
	lockf  string
	lk     *lock.Lock
}

// Socket is the path the advertising process must listen on.
func (a *Advertised) Socket() string { return a.socket }

// Close removes the advertisement. Safe to call twice.
//
// The socket file is removed too: a listener that exits without unlinking
// leaves a path that accepts connections from nobody, which is
// indistinguishable from a live session until something tries to use it.
func (a *Advertised) Close() error {
	if a == nil {
		return nil
	}
	var first error
	if a.lk != nil {
		if err := a.lk.Release(); err != nil {
			first = err
		}
		a.lk = nil
	}
	if err := os.Remove(a.socket); err != nil && !os.IsNotExist(err) && first == nil {
		first = err
	}
	return first
}

// Advertise reserves a session named by key and returns where to listen.
//
// key should identify the advertising work, a project path plus a target.
// It is hashed with the PID, so the SAME key advertised twice (one project run
// in two terminals) yields two distinct sessions rather than a collision, and
// two DIFFERENT projects running the same target never share a path.
//
// Invariant: on success the caller owns the returned socket path and MUST
// Close the result, which releases the advertisement and unlinks the socket.
func Advertise(ctx context.Context, key string, meta Meta) (*Advertised, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	id := ID(key, os.Getpid())

	blob, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("session: describing %s: %w", id, err)
	}

	lockPath := filepath.Join(dir, id+lockExt)
	// The lock is what makes liveness trustworthy AND what stops a second
	// process claiming this id. Note carries the description, so a listing
	// needs to read exactly one file per session.
	lk, err := lock.Acquire(ctx, lockPath, lock.Note(string(blob)))
	if err != nil {
		return nil, fmt.Errorf("session: advertising %s: %w", id, err)
	}

	sock := filepath.Join(dir, id+sockExt)
	// A leftover socket from a crashed run whose lock has just been reclaimed
	// would make Listen fail with "address already in use".
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		_ = lk.Release()
		return nil, fmt.Errorf("session: clearing %s: %w", sock, err)
	}
	return &Advertised{socket: sock, lockf: lockPath, lk: lk}, nil
}

// ID is the session name for a key and PID.
//
// Exported so a caller can predict the path it is about to own, and so tests
// can assert the collision properties rather than trusting them.
func ID(key string, pid int) string {
	return fmt.Sprintf("%s-%d", shortHash(key), pid)
}

// List returns the live sessions, sweeping dead ones.
//
// Sweeping here rather than in a separate cleanup command because a listing is
// the only moment anything is guaranteed to look: a session directory that is
// only ever appended to fills with sockets nobody serves, and the first
// symptom is a picker offering choices that hang.
func List() ([]Session, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	var out []Session
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, lockExt) {
			continue
		}
		id := strings.TrimSuffix(name, lockExt)
		lockPath := filepath.Join(dir, name)
		sock := filepath.Join(dir, id+sockExt)

		if !lock.Held(lockPath) {
			// The advertising process is gone. Its files are noise, and one
			// of them is a socket that would accept a connection and then
			// never answer.
			_ = os.Remove(lockPath)
			_ = os.Remove(sock)
			continue
		}
		info, err := lock.Read(lockPath)
		if err != nil {
			continue
		}
		meta := Meta{}
		if info.Note != "" {
			// A malformed note is not a reason to hide a live session; it just
			// has nothing to say about itself.
			_ = json.Unmarshal([]byte(info.Note), &meta)
		}
		out = append(out, Session{
			ID: id, Socket: sock, Meta: meta, PID: info.PID, Since: info.Since,
		})
	}
	// Newest first: the session someone is looking for is almost always the
	// one they just started.
	sort.Slice(out, func(i, j int) bool { return out[i].Since.After(out[j].Since) })
	return out, nil
}

// shortHash renders a stable, filesystem-safe digest of s.
//
// FNV-1a rather than a cryptographic hash: this names a temp file, collisions
// are not adversarial, and the PID in ID already separates same-key sessions.
func shortHash(s string) string {
	const (
		offset64 uint64 = 14695981039346656037
		prime64  uint64 = 1099511628211
	)
	h := offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return fmt.Sprintf("%08x", h&0xffffffff)
}
