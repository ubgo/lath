package secret

import (
	"context"
	"errors"
	"fmt"
)

// ErrSyncIncomplete reports that at least one credential could not be written.
// The Report names each one; this sentinel exists so a caller can branch
// without inspecting the map.
var ErrSyncIncomplete = errors.New("secret: some credentials were not written")

// ErrCannotProvision reports that Provision was requested of a Store that does
// not implement Provisioner. An explicit failure rather than a silent no-op:
// the caller asked for the destination to be created, and quietly not doing so
// would surface later as a confusing "not found".
var ErrCannotProvision = errors.New("secret: this store cannot provision its destination")

type syncConfig struct {
	dryRun    bool
	prune     bool
	provision bool
	progress  func(Event)
}

// SyncOption configures Sync.
type SyncOption func(*syncConfig)

// DryRun reports what would happen and changes nothing.
func DryRun() SyncOption { return func(c *syncConfig) { c.dryRun = true } }

// Prune deletes credentials the store holds that are absent from the desired
// Set.
//
// Off by default: a Set assembled from a partial source would otherwise delete
// everything it did not happen to include, and a deleted credential is not
// recoverable from the store that held it.
func Prune() SyncOption { return func(c *syncConfig) { c.prune = true } }

// Progress reports each credential's outcome as it happens, so a long sync is
// not silent. Events carry no plaintext.
func Progress(fn func(Event)) SyncOption { return func(c *syncConfig) { c.progress = fn } }

// Provision creates the destination first, for a Store that implements
// Provisioner.
//
// Off by default, and deliberately NOT automatic. The destination is usually
// named by a positional argument, so a mistyped name would otherwise silently
// provision infrastructure instead of failing, and the failure is the useful
// outcome, because it tells the operator they typed the wrong thing. Creating
// something is a decision, so it has to be asked for.
func Provision() SyncOption { return func(c *syncConfig) { c.provision = true } }

// Sync reconciles a desired Set against a Store.
//
// Writes what is missing or changed, skips what the store reserves, and with
// Prune removes what is no longer wanted.
//
// Invariant: a failure on one credential does NOT stop the run. Every entry is
// attempted and every failure recorded, because aborting midway leaves a
// half-published set whose boundary the operator cannot determine. The returned
// error wraps ErrSyncIncomplete when anything failed; the Report says what.
//
// Change detection: by default every desired credential is written, because
// most stores cannot report whether a value already matches. A Store that can
// may implement Comparer, and Sync will then skip the ones already correct. No
// local state is kept under any circumstance. A digest cached on one machine
// would make the same command behave differently in CI, silently.
func Sync(ctx context.Context, store Store, desired *Set, opts ...SyncOption) (Report, error) {
	var c syncConfig
	for _, o := range opts {
		o(&c)
	}
	if desired == nil {
		desired = NewSet()
	}

	report := Report{Failed: make(map[string]error), DryRun: c.dryRun}
	emit := func(e Event) {
		if c.progress != nil {
			c.progress(e)
		}
	}

	// Before anything is written, and never during a dry run: a dry run must
	// stay entirely read-only, so it reports the intent instead.
	if c.provision {
		p, ok := store.(Provisioner)
		switch {
		case !ok:
			return report, fmt.Errorf("secret: %w", ErrCannotProvision)
		case c.dryRun:
			emit(Event{Kind: EventProvisioned, Note: "dry run"})
			report.Provisioned = true
		default:
			if err := p.Ensure(ctx); err != nil {
				return report, fmt.Errorf("secret: ensuring the destination exists: %w", err)
			}
			emit(Event{Kind: EventProvisioned})
			report.Provisioned = true
		}
	}

	for _, key := range desired.Keys() {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		value, _ := desired.Get(key)

		if store.Reserved(key) {
			report.Skipped = append(report.Skipped, key)
			emit(Event{Kind: EventSkipped, Key: key, Note: "reserved by the store"})
			continue
		}

		if same, checked := alreadyCorrect(ctx, store, key, value); checked && same {
			report.Unchanged = append(report.Unchanged, key)
			emit(Event{Kind: EventUnchanged, Key: key, Bytes: value.Len()})
			continue
		}

		if c.dryRun {
			report.Set = append(report.Set, key)
			emit(Event{Kind: EventSet, Key: key, Bytes: value.Len()})
			continue
		}

		if err := store.Set(ctx, key, value); err != nil {
			report.Failed[key] = err
			emit(Event{Kind: EventFailed, Key: key, Err: err})
			continue
		}
		report.Set = append(report.Set, key)
		emit(Event{Kind: EventSet, Key: key, Bytes: value.Len()})
	}

	if c.prune {
		if err := prune(ctx, store, desired, &report, emit, c.dryRun); err != nil {
			return report, err
		}
	}

	if len(report.Failed) > 0 {
		return report, fmt.Errorf("secret: %d of %d failed: %w",
			len(report.Failed), desired.Len(), ErrSyncIncomplete)
	}
	return report, nil
}

// alreadyCorrect asks a Comparer-capable store whether a value already matches.
// The second result reports whether the question could be asked at all, so a
// comparison error is not mistaken for "differs".
func alreadyCorrect(ctx context.Context, store Store, key string, v Value) (same, checked bool) {
	cmp, ok := store.(Comparer)
	if !ok {
		return false, false
	}
	same, err := cmp.Same(ctx, key, v)
	if err != nil {
		// A store that cannot answer is treated as "unknown", and the value is
		// written. Writing a correct value again is harmless; skipping a stale
		// one is not.
		return false, false
	}
	return same, true
}

// prune removes stored credentials absent from the desired Set.
func prune(ctx context.Context, store Store, desired *Set, report *Report,
	emit func(Event), dryRun bool) error {

	live, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("secret: listing for prune: %w", err)
	}
	for _, key := range live {
		if desired.Has(key) || store.Reserved(key) {
			continue
		}
		if dryRun {
			report.Deleted = append(report.Deleted, key)
			emit(Event{Kind: EventDeleted, Key: key})
			continue
		}
		if err := store.Delete(ctx, key); err != nil {
			report.Failed[key] = err
			emit(Event{Kind: EventFailed, Key: key, Err: err})
			continue
		}
		report.Deleted = append(report.Deleted, key)
		emit(Event{Kind: EventDeleted, Key: key})
	}
	return nil
}
