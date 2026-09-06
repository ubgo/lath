package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/secret"
	"github.com/ubgo/lath/pipeline"
)

// Login authenticates the runner to a container registry.
//
// Retried, because this is the flakiest step in a deploy and its failures are
// usually transient, but a credential rejection stops at once; see
// permanentIfHopeless.
type Login struct {
	// Registry is the host, e.g. "ghcr.io".
	Registry string
	// Username is the account to log in as.
	Username string
	// Password is the token. Passed on stdin, never as an argument.
	Password secret.Value
	// Attempts defaults to DefaultRegistryAttempts.
	Attempts int
	// Backoff and MaxBackoff override the retry schedule. Zero means the
	// package defaults.
	//
	// Settable because a registry behind a strict rate limit needs a longer
	// wait, and one on the same network needs almost none.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// Runner is where the login happens. Nil means this machine.
	Runner runner.Runner
}

func (Login) Name() string { return "registry-login" }

// Docs explains the step in a plan. See pipeline.Documented.
func (l Login) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "authenticate to the image registry",
		Detail:  fmt.Sprintf("%s as %s, %s", l.Registry, l.Username, runner.OrLocal(l.Runner).Describe()),
	}
}
func (Login) Requires() []pipeline.Key { return nil }
func (Login) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Storing the same credential twice leaves one credential.
func (Login) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (l Login) Validate() error {
	if l.Registry == "" {
		return fmt.Errorf("Registry is required")
	}
	if l.Username == "" {
		return fmt.Errorf("Username is required")
	}
	if l.Password.IsZero() {
		return fmt.Errorf("Password is required")
	}
	return nil
}

func (l Login) Run(ctx context.Context, s *pipeline.State) error {
	client, done := newClient(l.Runner, s)
	defer done()
	if s.DryRun() {
		s.Detailf("would log in to %s as %s %s", l.Registry, l.Username, client.Where().Describe())
		return nil
	}
	s.Detailf("logging in to %s as %s %s", l.Registry, l.Username, client.Where().Describe())
	return retryRegistry(ctx, s, retrySpec{attempts: l.Attempts, backoff: l.Backoff, max: l.MaxBackoff},
		"registry-login", func(ctx context.Context) error {
			return client.Login(ctx, l.Registry, l.Username, l.Password.Reveal())
		})
}
