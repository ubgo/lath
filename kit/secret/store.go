package secret

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Store is somewhere credentials live.
//
// Implementations know a vendor. A GitHub Environment, a Vault path, a
// 1Password vault, an AWS parameter prefix, and none of them belong in this
// package. Sync is written against this interface so the orchestration is
// shared and the vendor knowledge is a thin adapter beneath it.
//
// Invariant for implementers: Set must be safe to call for a key that already
// exists, because Sync writes rather than diffing by default (see Comparer).
type Store interface {
	// Set writes a credential, creating or replacing it.
	Set(ctx context.Context, key string, v Value) error
	// List returns the names the store currently holds. Values are not
	// returned: most stores cannot produce them, and Sync does not need them.
	List(ctx context.Context) ([]string, error)
	// Delete removes a credential. Deleting an absent key must succeed, so
	// that pruning is idempotent.
	Delete(ctx context.Context, key string) error
	// Reserved reports names this store refuses, letting the store state its
	// own rules rather than every caller hardcoding them.
	Reserved(key string) bool
}

// Comparer is an optional upgrade for a Store that can tell whether a stored
// credential already matches.
//
// Optional rather than part of Store because most secret stores are write-only
// names can be listed but values never read back, and requiring every
// implementation to answer a question it cannot would force them all to lie.
// Sync type-asserts for it: a store that has the capability declares it, and
// one that does not needs no configuration and is unaffected.
type Comparer interface {
	Same(ctx context.Context, key string, v Value) (bool, error)
}

// Provisioner is an optional upgrade for a Store whose destination must exist
// before credentials can be written to it, a GitHub Environment, a Vault
// mount, an AWS parameter prefix.
//
// Optional for the same reason Comparer is: plenty of stores have nothing to
// provision, and requiring them to implement a no-op would be noise.
//
// Invariant: Ensure must be idempotent. It is called before a sync that opted
// in, and a destination that already exists is the common case.
type Provisioner interface {
	Ensure(ctx context.Context) error
}

// EventKind classifies what happened to one credential during a Sync.
type EventKind string

const (
	EventSet         EventKind = "set"
	EventUnchanged   EventKind = "unchanged"
	EventSkipped     EventKind = "skipped"
	EventDeleted     EventKind = "deleted"
	EventFailed      EventKind = "failed"
	EventProvisioned EventKind = "provisioned"
)

// EventKindValues is the canonical list, so a caller switching on a kind can
// verify it covers every case from one place.
var EventKindValues = []EventKind{
	EventSet, EventUnchanged, EventSkipped, EventDeleted, EventFailed, EventProvisioned,
}

// Event describes one credential's outcome, redacted.
//
// Invariant: carries no Value and no plaintext. An Event is designed to be
// printed, so it must not be able to disclose anything.
type Event struct {
	Kind  EventKind
	Key   string
	Bytes int    // length of the value involved, or 0
	Err   error  // set only when Kind is EventFailed
	Note  string // why, for skips
}

// String renders an Event for progress output.
func (e Event) String() string {
	switch e.Kind {
	case EventFailed:
		return fmt.Sprintf("failed %s: %v", e.Key, e.Err)
	case EventSkipped:
		if e.Note != "" {
			return fmt.Sprintf("skipped %s (%s)", e.Key, e.Note)
		}
		return "skipped " + e.Key
	case EventDeleted:
		return "deleted " + e.Key
	case EventProvisioned:
		if e.Note != "" {
			return "would ensure the destination exists (" + e.Note + ")"
		}
		return "ensured the destination exists"
	case EventUnchanged:
		return fmt.Sprintf("unchanged %s (%d bytes)", e.Key, e.Bytes)
	default:
		return fmt.Sprintf("set %s (%d bytes)", e.Key, e.Bytes)
	}
}

// Report is the outcome of a Sync.
//
// Invariant: every key in the desired Set appears in exactly one of Set,
// Unchanged, Skipped or Failed. That completeness is what makes a partial
// failure diagnosable. A half-published credential set with an unclear
// boundary is the worst outcome, so the Report must say exactly where it got to.
type Report struct {
	Set       []string
	Unchanged []string
	Skipped   []string
	Deleted   []string
	Failed    map[string]error
	DryRun    bool
	// Provisioned records that the destination was created or verified. Only
	// ever true when Provision was requested.
	Provisioned bool
}

// Accounted reports how many of the desired keys the Report explains. Used by
// the package's own tests to enforce the completeness invariant.
func (r Report) Accounted() int {
	return len(r.Set) + len(r.Unchanged) + len(r.Skipped) + len(r.Failed)
}

// FailedKeys returns the failed names in sorted order, so output is stable.
func (r Report) FailedKeys() []string {
	out := make([]string, 0, len(r.Failed))
	for k := range r.Failed {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String summarises a Report in one line.
func (r Report) String() string {
	parts := make([]string, 0, 5)
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(len(r.Set), "set")
	add(len(r.Unchanged), "unchanged")
	add(len(r.Skipped), "skipped")
	add(len(r.Deleted), "deleted")
	add(len(r.Failed), "failed")
	if len(parts) == 0 {
		parts = append(parts, "nothing to do")
	}
	prefix := ""
	if r.DryRun {
		prefix = "dry run: would have "
	}
	return prefix + strings.Join(parts, ", ")
}
