package common

import (
	"context"
	"fmt"

	"github.com/ubgo/lath/kit/env"
	"github.com/ubgo/lath/pipeline"
)

// ResolveEnv publishes the environment name and verifies the credentials that
// environment requires are present.
//
// Vendor-neutral, which is why it lives here rather than beside the docker or
// git adapters: an environment name and a set of required credentials mean the
// same thing whatever the deploy target is.
//
// Invariant: Secrets holds secret NAMES, never values. A definition file is
// committed to git; a value here would commit a credential.
type ResolveEnv struct {
	// Environment is the target name, e.g. "prod".
	Environment string
	// Secrets are the names of the credentials this environment requires,
	// checked against the process environment.
	//
	// Checked HERE, before an image is built, rather than at container start.
	// A deploy that fails on a missing credential after pushing an image has
	// already spent minutes and left an artifact behind.
	Secrets []string
}

func (ResolveEnv) Name() string { return "resolve-env" }

// Docs explains the step in a plan. See pipeline.Documented.
func (e ResolveEnv) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "record the target environment and check its credentials are present",
		Detail:  e.Environment,
	}
}
func (ResolveEnv) Requires() []pipeline.Key { return nil }
func (ResolveEnv) Provides() []pipeline.Key { return []pipeline.Key{KeyEnv} }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Reads configuration and sets state; touches nothing.
func (ResolveEnv) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (e ResolveEnv) Validate() error {
	if e.Environment == "" {
		return fmt.Errorf("Environment is required")
	}
	return nil
}

// Run verifies the required credentials and publishes the environment name.
//
// The check runs under dry run too: it reads nothing and changes nothing, and
// a rehearsal that skipped it would report a deploy as viable when the first
// real attempt would fail.
func (e ResolveEnv) Run(_ context.Context, s *pipeline.State) error {
	if len(e.Secrets) > 0 {
		if err := env.RequireAll(e.Secrets...); err != nil {
			return fmt.Errorf("resolve-env: %s needs credentials that are not set: %w",
				e.Environment, err)
		}
	}
	pipeline.Set(s, KeyEnv, e.Environment)
	s.Detailf("env=%s, %d required credential(s) present", e.Environment, len(e.Secrets))
	return nil
}
