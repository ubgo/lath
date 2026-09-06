package supervise

import (
	"context"
	"errors"
)

// permanent marks an error as not worth retrying.
type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks an error as fatal, so Retry and Loop stop instead of trying
// again.
//
// A 400, a malformed request, a missing binary: attempting it ten more times
// produces ten identical failures and delays the report by the whole backoff
// budget.
//
// Nothing is inferred. An error is retried unless the CALLER wrapped it, which
// is what keeps "an error means retry" true as a rule while still allowing an
// exception the caller chooses deliberately.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err: err}
}

// IsPermanent reports whether err was marked by Permanent anywhere in its
// chain, so wrapping a permanent error with %w preserves the marking.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// Retry runs fn until it succeeds, the context ends, the policy's limit is
// reached, or fn returns an error marked Permanent.
//
// The difference from Loop is intent, and it shows up in exactly one place.
// Loop supervises something expected to run forever, so any return is a
// failure to retry. Retry drives an operation expected to finish. Both share
// this package's backoff so the two cannot drift apart.
func Retry(ctx context.Context, p Policy, fn func(context.Context) error) error {
	_, err := RetryWithStats(ctx, p, fn)
	return err
}

// RetryWithStats is Retry, reporting how many attempts it took.
func RetryWithStats(ctx context.Context, p Policy, fn func(context.Context) error) (Stats, error) {
	return runLoop(ctx, p, fn, true)
}
