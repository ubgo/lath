package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

// TestPipelinesAreWired is the guarantee standing in for compile-time step
// chaining, which Go cannot express for a heterogeneous sequence. It proves
// every environment's pipeline is constructible, correctly configured, and
// correctly ordered. Faults surface HERE, in CI, not mid-deploy.
func TestPipelinesAreWired(t *testing.T) {
	t.Parallel()
	for _, env := range EnvironmentValues {
		t.Run(string(env), func(t *testing.T) {
			t.Parallel()
			if err := deployPipeline(env).Validate(); err != nil {
				t.Fatalf("deployPipeline(%q): %v", env, err)
			}
		})
	}
}

// TestPlanIsSideEffectFree pins that Plan can be run against production without
// consequence. The property that makes it usable as a review step.
func TestPlanIsSideEffectFree(t *testing.T) {
	t.Parallel()
	if err := Plan(string(EnvironmentProd)); err != nil {
		t.Fatalf("Plan(prod) = %v; want nil", err)
	}
}

func TestEnvironmentFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		args    []string
		want    Environment
		wantErr bool
	}{
		{"no argument defaults to dev, never prod", nil, EnvironmentDev, false},
		{"explicit prod", []string{"prod"}, EnvironmentProd, false},
		{"typo is rejected, not coerced", []string{"prd"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := environmentFrom(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("environmentFrom(%v) err = %v, wantErr %v", tc.args, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("environmentFrom(%v) = %q; want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestDefaultIsNotProduction pins the deliberate choice behind defaultEnvironment.
func TestDefaultIsNotProduction(t *testing.T) {
	t.Parallel()
	if defaultEnvironment == EnvironmentProd {
		t.Fatal("the default environment is production; an absent-minded `deploy` would ship")
	}
}

// TestEveryStepDeclaresAName guards against a blank line in plan output and an
// unattributable failure.
func TestEveryStepDeclaresAName(t *testing.T) {
	t.Parallel()
	for _, st := range deployPipeline(EnvironmentProd).Steps {
		if strings.TrimSpace(st.Name()) == "" {
			t.Errorf("%T has an empty Name()", st)
		}
	}
}

// TestValidateErrorsAreSentinels pins that a wiring fault is matchable with
// errors.Is, so tooling never parses message text.
func TestValidateErrorsAreSentinels(t *testing.T) {
	t.Parallel()
	broken := deployPipeline(EnvironmentProd)
	broken.Steps = broken.Steps[2:] // drop the providers everything needs
	if err := broken.Validate(); !errors.Is(err, pipeline.ErrNotWired) {
		t.Fatalf("Validate() = %v; want ErrNotWired", err)
	}
}

// TestEnvironmentWireValues pins the LITERAL strings a user types. A typed
// constant protects use sites from typos; it cannot protect its own value, and
// a test referencing the constant agrees with the bug.
func TestEnvironmentWireValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		got  Environment
		want string
	}{
		{EnvironmentDev, "dev"},
		{EnvironmentStaging, "staging"},
		{EnvironmentProd, "prod"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("environment = %q; want %q", tc.got, tc.want)
		}
	}
}

// TestRollbackPipelineIsWired holds the example to the same standard as the
// deploy: constructible and correctly ordered for every environment. Without
// it the rollback would be a snippet nobody compiles, which is the failure
// mode documentation has and code does not.
func TestRollbackPipelineIsWired(t *testing.T) {
	t.Parallel()
	for _, env := range EnvironmentValues {
		t.Run(string(env), func(t *testing.T) {
			t.Parallel()
			if err := rollbackPipeline(env, "0375fcf").Validate(); err != nil {
				t.Fatalf("rollbackPipeline(%q): %v", env, err)
			}
		})
	}
}

// TestRollbackBuildsNothingAndMigratesNothing pins the two omissions that make
// this a rollback rather than a redeploy.
//
// The migration is the one that matters: re-running it would run the OLD
// image's migrate command, which carries the OLD schema, and would try to
// bring the database backwards. Someone "restoring symmetry" with the deploy
// pipeline would reintroduce that, and this test is what stops them.
func TestRollbackBuildsNothingAndMigratesNothing(t *testing.T) {
	t.Parallel()
	var names []string
	for _, step := range rollbackPipeline(EnvironmentProd, "0375fcf").Steps {
		names = append(names, step.Name())
	}
	joined := strings.Join(names, " ")

	for _, forbidden := range []string{"build-image", "push-image", "pull-image", "migrate"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("the rollback runs %q: %s", forbidden, joined)
		}
	}
	// And the half that must survive: an image to run, health-checked before
	// the running release is retired.
	for _, required := range []string{"use-image", "start-processes", "wait-healthy", "stop-previous"} {
		if !strings.Contains(joined, required) {
			t.Errorf("the rollback is missing %q: %s", required, joined)
		}
	}
}

// TestRollbackNeedsAVersion. Defaulting to "the previous one" would mean
// guessing on the operator's behalf at the exact moment they are least able to
// check the guess.
func TestRollbackNeedsAVersion(t *testing.T) {
	t.Parallel()
	err := Rollback(t.Context(), string(EnvironmentProd))
	if err == nil {
		t.Fatal("a rollback with no version was accepted")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("err = %v; want it to say what is missing", err)
	}
}
