// Package lock provides a single-instance guard backed by a file.
//
// The bug it exists to prevent: two supervisors each restarting a dev server
// that took a port with --force, so each killed the other's process forever.
// Nothing arbitrated who owned the resource. A lock is that arbiter.
package lock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ubgo/lath/kit/fsx"
)

const (
	// lockFilePerm is 0644 deliberately: a lock's contents are diagnostic, not
	// secret, and another user must be able to READ who holds it in order to
	// be told something useful.
	lockFilePerm = 0o644
	// pollInterval is how often a waiting Acquire retries. Short enough that a
	// released lock is picked up promptly, long enough not to spin.
	pollInterval = 100 * time.Millisecond
	// DefaultStaleAfter is how long a lock whose owner cannot be verified is
	// tolerated before it may be broken.
	DefaultStaleAfter = 2 * time.Hour
)

// ErrHeld reports that another live process holds the lock.
var ErrHeld = errors.New("lock: held by another process")

// Info describes a lock's holder. Written as JSON so a human can read it and a
// later version can add fields without breaking an older reader.
type Info struct {
	PID int `json:"pid"`
	// Started is the holder's process start time, and it is what makes the
	// liveness check trustworthy. A PID alone is not enough: PIDs are reused,
	// so an unrelated process inheriting the number would make a dead lock look
	// live forever.
	Started time.Time `json:"started"`
	// Since is when the lock was taken.
	Since time.Time `json:"since"`
	// Host distinguishes holders when a lock lives on a shared filesystem,
	// where a PID from another machine is meaningless.
	Host string `json:"host"`
	// Note is caller-supplied context, e.g. the command line.
	Note string `json:"note,omitempty"`
}

// String renders a holder for an operator.
func (i Info) String() string {
	s := fmt.Sprintf("pid %d on %s since %s", i.PID, i.Host, i.Since.Format(time.RFC3339))
	if i.Note != "" {
		s += " (" + i.Note + ")"
	}
	return s
}

// Lock is a held claim. Release must be called; a deferred Release is the
// intended shape.
type Lock struct {
	path     string
	info     Info
	released bool
}

// Path returns the lock file's location.
func (l *Lock) Path() string { return l.path }

// Info returns this holder's own record.
func (l *Lock) Info() Info { return l.info }

type config struct {
	wait       time.Duration
	staleAfter time.Duration
	note       string
}

// Option configures Acquire.
type Option func(*config)

// Wait blocks for up to d rather than failing immediately.
//
// Not the default: a lock that waits by default turns a forgotten holder into
// a CI job that hangs until the runner's global timeout, with no output saying
// why. Failing fast reports who holds it, at once.
func Wait(d time.Duration) Option { return func(c *config) { c.wait = d } }

// StaleAfter sets how old an unverifiable lock must be before it may be broken.
func StaleAfter(d time.Duration) Option { return func(c *config) { c.staleAfter = d } }

// Note attaches operator-facing context to the lock, shown to whoever is
// refused. Never put a credential here, the file is world-readable.
func Note(s string) Option { return func(c *config) { c.note = s } }

// Acquire takes the lock at path, creating parent directories as needed.
//
// Fails immediately with ErrHeld when another live process holds it, unless
// Wait was given. The context bounds everything: a cancelled ctx aborts a wait
// at once, so Ctrl-C is always honoured.
//
// A lock whose holder is gone, or whose PID has been reused by a process with
// a different start time, is reclaimed automatically. Without that, one crash
// wedges the tool until somebody finds and deletes a file they do not know about.
func Acquire(ctx context.Context, path string, opts ...Option) (*Lock, error) {
	c := config{staleAfter: DefaultStaleAfter}
	for _, o := range opts {
		o(&c)
	}

	deadline := time.Now().Add(c.wait)
	for {
		l, err := tryAcquire(path, c)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrHeld) || c.wait <= 0 || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// tryAcquire makes one attempt.
func tryAcquire(path string, c config) (*Lock, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("lock: %w", err)
		}
	}

	self, err := selfInfo(c.note)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(self)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	// O_EXCL is the actual mutual exclusion: the create either wins or reports
	// that a file is already there. Everything else is diagnosis.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, lockFilePerm)
	if err == nil {
		_, writeErr := f.Write(payload)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("lock: writing %s: %w", path, errors.Join(writeErr, closeErr))
		}
		return &Lock{path: path, info: self}, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("lock: %w", err)
	}

	holder, readErr := Read(path)
	if readErr != nil {
		// An unreadable or malformed lock file is treated as stale: it cannot
		// be attributed to anyone, and refusing forever on the strength of
		// corrupt bytes is worse than reclaiming it.
		return steal(path, payload, self)
	}
	if alive(holder) {
		return nil, fmt.Errorf("lock: %s: %w", holder, ErrHeld)
	}
	if c.staleAfter > 0 && time.Since(holder.Since) < c.staleAfter && holder.Host != self.Host {
		// Another machine's lock cannot have its liveness checked from here, so
		// it is honoured until it ages out.
		return nil, fmt.Errorf("lock: %s (another host): %w", holder, ErrHeld)
	}
	return steal(path, payload, self)
}

// steal replaces a lock whose holder is gone. Written atomically so a third
// process never observes a truncated file and concludes it is corrupt.
func steal(path string, payload []byte, self Info) (*Lock, error) {
	if err := fsx.WriteAtomic(path, payload, lockFilePerm); err != nil {
		return nil, err
	}
	return &Lock{path: path, info: self}, nil
}

// Release drops the lock. Safe to call more than once, so a deferred Release
// alongside an explicit one is not an error.
func (l *Lock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true

	// Only remove the file if it is still OURS. Between Acquire and Release the
	// lock may have been judged stale and taken by someone else; deleting it
	// then would strip a live holder of their claim.
	if current, err := Read(l.path); err == nil {
		if current.PID != l.info.PID || !current.Since.Equal(l.info.Since) {
			return nil
		}
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("lock: releasing %s: %w", l.path, err)
	}
	return nil
}

// Read reports who holds a lock without attempting to take it, for a message
// naming the holder rather than a bare failure.
func Read(path string) (Info, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Info{}, fmt.Errorf("lock: %w", err)
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		return Info{}, fmt.Errorf("lock: %s is not a valid lock file: %w", path, err)
	}
	return i, nil
}

// Held reports whether a live process currently holds the lock. Advisory only:
// the answer can be stale the instant it is returned, so it is for reporting,
// never for deciding whether to proceed. Use Acquire for that.
func Held(path string) bool {
	i, err := Read(path)
	return err == nil && alive(i)
}
