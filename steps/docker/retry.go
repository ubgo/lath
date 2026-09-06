package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/supervise"
	"github.com/ubgo/lath/pipeline"
)

const (
	// DefaultRegistryAttempts is how many times a registry operation is tried.
	//
	// Registries fail transiently far more than anything else in a deploy ,
	// rate limits, token refreshes, brief 5xx, and giving up on the first one
	// discards a build that already succeeded.
	DefaultRegistryAttempts = 10
	// DefaultRegistryBackoff is the first wait between attempts; it doubles.
	DefaultRegistryBackoff = 4 * time.Second
	// DefaultRegistryMaxBackoff caps the growth.
	DefaultRegistryMaxBackoff = 30 * time.Second
)

// retryRegistry runs fn under this package's registry retry policy.
//
// Failures that retrying cannot fix stop immediately, see permanentIfHopeless.
// retrySpec is a caller's retry preferences. Zero fields take the package
// defaults, so a step that does not care passes an empty one.
type retrySpec struct {
	attempts int
	backoff  time.Duration
	max      time.Duration
}

func retryRegistry(ctx context.Context, s *pipeline.State, spec retrySpec, label string,
	fn func(context.Context) error) error {

	if spec.attempts <= 0 {
		spec.attempts = DefaultRegistryAttempts
	}
	if spec.backoff <= 0 {
		spec.backoff = DefaultRegistryBackoff
	}
	if spec.max <= 0 {
		spec.max = DefaultRegistryMaxBackoff
	}
	policy := supervise.Policy{
		Backoff:     spec.backoff,
		Max:         spec.max,
		MaxRestarts: spec.attempts,
		Out:         detailWriter{s: s},
	}
	wrapped := func(ctx context.Context) error { return permanentIfHopeless(fn(ctx)) }
	if err := supervise.Retry(ctx, policy, wrapped); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// permanentIfHopeless marks failures no amount of waiting will fix.
//
// Two kinds. A missing program or unreachable host, no wait installs docker.
// And a credential rejection, which looks identical to a rate limit from
// outside but turns an instant, obvious failure into a minute of noise ending
// in the same message.
func permanentIfHopeless(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"not found in $path", "no ssh client",
		"unauthorized", "authentication required", "denied", "forbidden",
		"invalid username or password",
	} {
		if strings.Contains(text, marker) {
			return supervise.Permanent(err)
		}
	}
	return err
}

// detailWriter routes a policy's retry notices into the pipeline's reporter, so
// a retrying step is visible in the same stream as everything else rather than
// on one nobody is watching.
type detailWriter struct{ s *pipeline.State }

func (d detailWriter) Write(b []byte) (int, error) {
	d.s.Detailf("%s", strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
