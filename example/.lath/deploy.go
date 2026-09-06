// Package main is this repository's deploy definition.
//
// Every EXPORTED function below is a command. Writing one is declaring it,
// there is no registration list, no switch, and no main(). lath discovers
// them by parsing this directory and generates the dispatcher.
//
// Supported shapes:
//
//	func Name()
//	func Name() error
//	func Name(ctx context.Context) error
//	func Name(args ...string) error
//	func Name(ctx context.Context, args ...string) error
//
// The first sentence of each doc comment becomes its help text in `lath -l`.
//
// `go build ./...` FAILS in this directory, and that is expected. This is
// package main with no func main: lath generates the dispatcher at build time
// from the functions below, compiles it alongside them, and deletes it again.
// Without that generated file the package has no entry point, so the linker
// reports "function main is undeclared in the main package".
//
// Everything else works normally, `go vet`, `go test`, `gofmt`, and the
// editor's language server all operate on this package unchanged, because a
// missing main is a link-time fault rather than a type error. Use `lath` to
// build it; use the ordinary Go tools for everything else.
//
// This is a nested module, deliberately outside the parent go.work, so its
// dependencies never enter the application's module graph. The editor still
// type-checks it fully. Gopls resolves from the module cache, and vendoring
// only duplicates what is already there.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/ssh"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/docker"
	"github.com/ubgo/lath/steps/git"
)

// Plan lists the steps for an environment without executing any of them.
//
// Safe to run against production: Validate is side-effect free, and no step is
// invoked. This is the review step before a real deploy.
func Plan(args ...string) error {
	env, err := environmentFrom(args)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	p := deployPipeline(env)
	if err := p.Validate(); err != nil {
		return fmt.Errorf("plan %s: %w", env, err)
	}
	fmt.Printf("pipeline %q: %d steps, target %s (nothing executed)\n",
		p.Name, len(p.Steps), env)
	for _, e := range p.Plan() {
		fmt.Printf("%2d/%d  %-18s", e.Position, e.Total, e.Name)
		if len(e.Requires) > 0 {
			fmt.Printf(" needs=%v", e.Requires)
		}
		if len(e.Provides) > 0 {
			fmt.Printf(" gives=%v", e.Provides)
		}
		fmt.Println()
	}
	return nil
}

// Deploy builds and ships the API to an environment.
func Deploy(ctx context.Context, args ...string) error {
	return runPipeline(ctx, args, pipeline.ModeExecute)
}

// DryRun walks the whole pipeline with every side effect suppressed.
//
// Unlike Plan, this executes each step's logic. So it catches faults that only
// appear once values are flowing, while changing nothing.
func DryRun(ctx context.Context, args ...string) error {
	return runPipeline(ctx, args, pipeline.ModeDryRun)
}

// Rollback re-runs a version that is already on the box, without building.
//
// The shape worth copying: a rollback is a TARGET a project writes, not a verb
// lath provides, because only this file can say what "back" means here and
// whether the schema comes with it. See rollbackPipeline for the four steps it
// deliberately leaves out.
//
// Usage: lath run rollback <env> <version>
func Rollback(ctx context.Context, args ...string) error {
	if len(args) < 2 {
		return fmt.Errorf("rollback: usage <env> <version>, e.g. rollback prod 0375fcf")
	}
	env, err := environmentFrom(args)
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	p := rollbackPipeline(env, args[1])
	if err := p.Validate(); err != nil {
		return fmt.Errorf("rollback %s: %w", env, err)
	}
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(os.Stdout))
	if err := p.Run(ctx, st); err != nil {
		return fmt.Errorf("rollback %s: %w", env, err)
	}
	return nil
}

// Environments prints the environments this definition knows about.
func Environments() {
	for _, e := range EnvironmentValues {
		fmt.Println(e)
	}
}

// runPipeline is unexported, so it is NOT a command, only exported functions
// become commands, which is how helpers stay out of the command list.
func runPipeline(ctx context.Context, args []string, mode pipeline.Mode) error {
	env, err := environmentFrom(args)
	if err != nil {
		return fmt.Errorf("%s: %w", mode, err)
	}
	st := pipeline.NewState(mode, pipeline.NewTextReporter(os.Stdout))
	if err := deployPipeline(env).Run(ctx, st); err != nil {
		return fmt.Errorf("%s %s: %w", mode, env, err)
	}
	fmt.Println("done")
	return nil
}

// environmentFrom parses the target environment from a command's arguments,
// defaulting to dev so an absent-minded `lath deploy` cannot touch prod.
func environmentFrom(args []string) (Environment, error) {
	env := defaultEnvironment
	if len(args) > 0 {
		env = Environment(args[0])
	}
	if !env.Valid() {
		return "", fmt.Errorf("unknown environment %q (want one of %v)", env, EnvironmentValues)
	}
	return env, nil
}

// Environment names a deploy target. A closed set: a typo would otherwise
// resolve to config nobody reviewed.
type Environment string

const (
	EnvironmentDev     Environment = "dev"
	EnvironmentStaging Environment = "staging"
	EnvironmentProd    Environment = "prod"
)

// EnvironmentValues is the canonical iteration order, drives argument
// validation, the environments command, and the wiring test's coverage.
var EnvironmentValues = []Environment{EnvironmentDev, EnvironmentStaging, EnvironmentProd}

// Valid reports whether e is a declared environment.
func (e Environment) Valid() bool {
	for _, v := range EnvironmentValues {
		if e == v {
			return true
		}
	}
	return false
}

// defaultEnvironment is used when no environment argument is given.
const defaultEnvironment = EnvironmentDev

const (
	// healthTimeout bounds the wait for new containers to report healthy.
	// Sized for the slowest observed cold start plus margin; too short turns a
	// slow boot into a failed deploy.
	healthTimeout = 60 * time.Second

	// drainPeriod is how long the previous version keeps serving in-flight
	// work after traffic has moved away from it.
	drainPeriod = 30 * time.Second

	// appName prefixes every container this definition manages, which is what
	// lets StopPrevious tell our containers apart from anything else running
	// on the box.
	appName = "acme"

	// imageRepo is the image name without a tag. Named because two pipelines
	// use it now: the deploy tags it with a commit, and the rollback names a
	// tag it already produced. A literal in both is a rollback that silently
	// points at a different repository after a rename.
	imageRepo = "acme"

	// appNetwork is the docker network the app shares with its database.
	appNetwork = "acmenet"

	// apiPort is the port the api process listens on inside its container.
	apiPort = 2315

	// drainWindow is how long the previous release keeps serving after the new
	// one is healthy, so in-flight requests finish instead of being cut off.
	drainWindow = 10 * time.Second

	// proxyAdminAPI is the admin endpoint of the reverse proxy ALREADY running
	// on the host. We edit its upstreams rather than running a proxy of our
	// own, because the host serves unrelated applications on :80/:443.
	proxyAdminAPI = "http://localhost:2019"
)

// rollbackPipeline re-deploys an image that already exists on the target.
//
// It is the deploy with four things missing, and the omissions are the design:
//
//   - no Build, Push or Pull. A rebuild produces a NEW artifact from whatever
//     the build inputs resolve to today, which is not the thing that was
//     running. The image is already on the box, which is also why MustExist
//     checks THERE rather than in the registry.
//   - no migration. Re-running it would run the OLD image's migrate command,
//     which carries the OLD schema, and would try to bring the database
//     backwards. An image rollback is a code rollback: it works while
//     migrations are additive, and stops working the moment one renames or
//     drops something the old binary still reads. A project whose migrations
//     are not additive has to say so, here, and ship expand-then-contract.
//   - no traffic switch, when the proxy points at a stable name every release
//     answers to. This example switches by container name, so it keeps that
//     step; a definition using an alias would drop it too.
//
// What it keeps is what makes a deploy safe: start alongside, health-check,
// then drain and stop the previous one.
func rollbackPipeline(env Environment, version string) pipeline.Pipeline {
	var box runner.Runner = runner.Local{}
	if env != EnvironmentDev {
		box = runner.Remote{Host: ssh.Host{Addr: string(env) + ".example.com", User: "deploy"}}
	}
	return pipeline.Pipeline{
		Name: "rollback-api",
		Steps: []pipeline.Step{
			common.ResolveEnv{Environment: string(env)},
			// KeyCommit is SET from the target version rather than read from
			// git: container names are built from it, so after a rollback
			// `docker ps` names the version actually running. Resolving it
			// from the working tree would label containers with a commit whose
			// code is nowhere inside them.
			pipeline.Func{
				Label:      "rollback-target",
				Summary:    "name the version to go back to, without consulting git",
				Detail:     version,
				Gives:      []pipeline.Key{common.KeyCommit},
				Idempotent: true,
				Do: func(_ context.Context, s *pipeline.State) error {
					pipeline.Set(s, common.KeyCommit, version)
					return nil
				},
			},
			docker.UseImage{
				Repository: imageRepo,
				TagFrom:    common.KeyCommit,
				// Worth the round trip: without it a mistyped or pruned tag
				// fails several steps later as a registry error that never
				// says what could have been used instead.
				MustExist: true,
				Runner:    box,
			},
			docker.Start{Runner: box, Network: appNetwork, NamePrefix: appName, Processes: []docker.Process{
				{Name: "api", Cmd: []string{"acme", "api"}, Port: apiPort, Route: true},
			}},
			docker.WaitHealthy{Timeout: healthTimeout, Runner: box},
			docker.StopPrevious{NamePrefix: appName, Drain: drainWindow, Runner: box},
		},
	}
}

// deployPipeline builds the pipeline for env.
//
// Ordering invariants, which Validate cannot infer:
//   - The migration RunOnce runs BEFORE StartProcesses so the schema is ready
//     when processes boot, and exactly once rather than once per container.
//   - WaitHealthy runs BEFORE the traffic switch so traffic never moves to a
//     version that has not proven itself.
//   - StopPrevious runs LAST so in-flight requests finish on the old version.

func deployPipeline(env Environment) pipeline.Pipeline {
	// box is where the deploy half runs. THIS is the line that makes the whole
	// pipeline rehearsable: swap it for runner.Local{} and every remote step
	// executes against local docker instead, with nothing else changed.
	//
	// A definition can key this off the environment, so `dev` rehearses on the
	// laptop and `prod` reaches the real host.
	var box runner.Runner = runner.Local{}
	if env != EnvironmentDev {
		box = runner.Remote{Host: ssh.Host{Addr: string(env) + ".example.com", User: "deploy"}}
	}
	return pipeline.Pipeline{
		Name: "deploy-api",
		Steps: []pipeline.Step{
			git.ResolveCommit{},
			common.ResolveEnv{
				Environment: string(env),
				Secrets:     []string{"DATABASE_URL", "NATS_CREDS"},
			},
			// Options carries everything docker build accepts, so a new library
			// flag needs no change to this step or to this file.
			docker.Build{
				Repository: imageRepo,
				Options: dockerkit.BuildOptions{
					Dockerfile: ".docker/Dockerfile.prod",
					Context:    ".",
				},
			},
			// Push here, pull on the box. Splitting them means a registry
			// failure is reported as a registry failure rather than as a
			// container that would not start.
			docker.Push{},
			docker.Pull{Runner: box},
			// Labelled, because RunOnce is named for the mechanism and a plan
			// listing "run-once" says nothing about which one-off this is.
			docker.RunOnce{
				Label:   "migrate",
				Cmd:     []string{"acme", "migrate"},
				Network: appNetwork,
				Runner:  box,
			},
			docker.Start{Runner: box, Network: appNetwork, NamePrefix: appName, Processes: []docker.Process{
				{Name: "api", Cmd: []string{"acme", "api"}, Port: 2315, Route: true},
				{Name: "wbhrcvr", Cmd: []string{"acme", "wbhrcvr"}, Port: 2316, Route: true},
				// Queue consumers: no Port, no Route, they dial out, so no
				// proxy upstream changes when they are replaced.
				{Name: "worker", Cmd: []string{"acme", "worker"}, Scale: 2},
				{Name: "cron", Cmd: []string{"cron"}},
			}},
			docker.WaitHealthy{Timeout: healthTimeout, Runner: box},
			docker.StopPrevious{NamePrefix: appName, Drain: drainWindow, Runner: box},
			// (StopPrevious runs last, see the ordering invariants above.)

			// A step defined by THIS project, in a sibling file, the shape for
			// something reused or worth naming.
			NotifySlack{Channel: "#deploys"},

			// The INLINE form, for a one-off that does not deserve a type.
			// Needs is not optional bookkeeping: Validate uses it, so a closure
			// reading a key it did not declare defeats the wiring check for the
			// whole pipeline.
			pipeline.Func{
				Label: "record-deploy",
				Needs: []pipeline.Key{common.KeyImage},
				Do: func(_ context.Context, s *pipeline.State) error {
					image, err := pipeline.Get[string](s, common.KeyImage)
					if err != nil {
						return fmt.Errorf("record-deploy: %w", err)
					}
					if s.DryRun() {
						s.Detailf("would append %s to the deploy log", image)
						return nil
					}
					s.Detailf("appended %s to the deploy log", image)
					return nil
				},
			},

			// A traffic switch is an HTTP call with a body we build, which is
			// why there is no SwitchTraffic step: the mechanism is
			// common.Request and the proxy-specific half lives here, where the
			// name of the proxy already is.
			common.Request{
				Label:  "switch-traffic",
				URL:    proxyAdminAPI + "/config/apps/http/servers/srv0/routes/0/handle/0/upstreams",
				Method: http.MethodPatch,
				Reads:  []pipeline.Key{common.KeyRouted},
				Body:   caddyUpstreams,
			},
		},
	}
}

// caddyUpstreams renders the ROUTED containers as Caddy's upstreams array.
//
// Lives in the definition, not in the steps package, because it is the one
// piece of this deploy that knows which proxy is in front, swapping Caddy for
// Traefik changes this function and nothing else.
//
// Reads common.KeyRouted rather than KeyContainers: the workers dial out, and
// a proxy given their names would route public requests to something with no
// listener. Refusing an empty set is policy too, and it belongs here for the
// same reason: an empty upstream list takes the site down while the request
// itself succeeds.
func caddyUpstreams(s *pipeline.State) ([]byte, error) {
	containers, err := pipeline.Get[[]string](s, common.KeyRouted)
	if err != nil {
		return nil, err
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("no routed containers: mark the process that takes inbound traffic with Route")
	}
	type upstream struct {
		Dial string `json:"dial"`
	}
	out := make([]upstream, 0, len(containers))
	for _, name := range containers {
		// Container name as hostname: they share a docker network, so docker's
		// embedded DNS resolves it without any published port.
		out = append(out, upstream{Dial: fmt.Sprintf("%s:%d", name, apiPort)})
	}
	return json.Marshal(out)
}
