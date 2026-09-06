package docker_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/secret"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/docker"
	"github.com/ubgo/lath/steps/git"
)

// ── the honesty guarantee ────────────────────────────────────────────────

func TestNoStepSilentlySucceeds(t *testing.T) {
	t.Parallel()
	boom := errors.New("the runner is unreachable")

	for _, tc := range []struct {
		name string
		step pipeline.Step
	}{
		{"build-image", docker.Build{Repository: "r", Options: dockerkit.BuildOptions{Context: "."}, Runner: &runner.Fake{Fail: boom}}},
		// Attempts 1: these steps retry a transient registry failure, and a
		// test asserting the failure surfaces should not sit through the real
		// backoff schedule to see it.
		{"push-image", docker.Push{Attempts: 1, Runner: &runner.Fake{Fail: boom}}},
		{"pull-image", docker.Pull{Attempts: 1, Runner: &runner.Fake{Fail: boom}}},
		{"run-once", docker.RunOnce{Cmd: []string{"m"}, Runner: &runner.Fake{Fail: boom}}},
		{"start-processes", docker.Start{
			Processes: []docker.Process{{Name: "web"}}, Runner: &runner.Fake{Fail: boom}}},
		{"stop-previous", docker.StopPrevious{NamePrefix: "app", Runner: &runner.Fake{Fail: boom}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := deployState(pipeline.ModeExecute)
			// Preset for the steps that consume it, so a failure here is the
			// runner failing rather than a missing key.
			pipeline.Set(s, common.KeyContainers, []string{"app-web-a3f1c2d-0"})

			err := tc.step.Run(context.Background(), s)
			if err == nil {
				t.Fatalf("%s reported success while its runner was failing", tc.name)
			}
			if !errors.Is(err, boom) {
				t.Errorf("err = %v; want the underlying failure to stay reachable", err)
			}
		})
	}
}

func TestNonZeroExitIsAFailure(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "docker", Exit: 1, Stdout: ""}}}
	err := docker.Build{Repository: "r", Options: dockerkit.BuildOptions{Context: "."}, Runner: r}.
		Run(context.Background(), deployState(pipeline.ModeExecute))
	if err == nil {
		t.Fatal("a docker build exiting 1 was reported as success")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("err = %v; want the exit code named", err)
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		make func(r runner.Runner) pipeline.Step
	}{
		{"build-image", func(r runner.Runner) pipeline.Step {
			return docker.Build{Repository: "r", Options: dockerkit.BuildOptions{Context: "."}, Runner: r}
		}},
		{"push-image", func(r runner.Runner) pipeline.Step { return docker.Push{Runner: r} }},
		{"pull-image", func(r runner.Runner) pipeline.Step { return docker.Pull{Runner: r} }},
		{"migrate", func(r runner.Runner) pipeline.Step {
			return docker.RunOnce{Cmd: []string{"migrate"}, Runner: r}
		}},
		{"start-processes", func(r runner.Runner) pipeline.Step {
			return docker.Start{Processes: []docker.Process{{Name: "web"}}, Runner: r}
		}},
		{"wait-healthy", func(r runner.Runner) pipeline.Step { return docker.WaitHealthy{Runner: r} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &runner.Fake{}
			s := deployState(pipeline.ModeDryRun)
			pipeline.Set(s, common.KeyContainers, []string{"app-web-0"})

			if err := tc.make(r).Run(context.Background(), s); err != nil {
				t.Fatalf("dry run failed: %v", err)
			}
			if got := r.Commands(); len(got) != 0 {
				t.Errorf("a dry run executed %v", got)
			}
		})
	}
}

func TestDryRunStillProvidesItsKeys(t *testing.T) {
	t.Parallel()
	s := newState(pipeline.ModeDryRun, map[pipeline.Key]any{common.KeyCommit: "a3f1c2d"})

	if err := (docker.Build{Repository: "ghcr.io/acme/app", Options: dockerkit.BuildOptions{Context: "."}, Runner: &runner.Fake{}}).
		Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		t.Fatalf("KeyImage was not provided under dry run: %v", err)
	}
	if image != "ghcr.io/acme/app:a3f1c2d" {
		t.Errorf("image = %q", image)
	}
}

// ── the command lines ────────────────────────────────────────────────────

func TestBuildImageCommandLine(t *testing.T) {
	t.Parallel()
	t.Run("single platform uses plain build", func(t *testing.T) {
		t.Parallel()
		r := &runner.Fake{}
		if err := (docker.Build{Repository: "ghcr.io/acme/app", Runner: r,
			Options: dockerkit.BuildOptions{Context: ".", Dockerfile: "Dockerfile", Args: map[string]string{"VERSION": "1.2", "ARCH": "arm"}},
		}).Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
			t.Fatal(err)
		}
		got := r.Commands()[0]
		for _, want := range []string{
			"docker build", "-t ghcr.io/acme/app:a3f1c2d", "-f Dockerfile",
			"--build-arg ARCH=arm", "--build-arg VERSION=1.2",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in: %s", want, got)
			}
		}
		// Build args sorted, so two equivalent builds produce an identical
		// command line and a log diff means a real change.
		if strings.Index(got, "ARCH") > strings.Index(got, "VERSION") {
			t.Errorf("build args are not sorted: %s", got)
		}
		if strings.Contains(got, "buildx") {
			t.Errorf("plain build used buildx unnecessarily: %s", got)
		}
	})

	t.Run("multi platform requires buildx", func(t *testing.T) {
		t.Parallel()
		r := &runner.Fake{}
		if err := (docker.Build{Repository: "app", Runner: r,
			Options: dockerkit.BuildOptions{Context: ".", Platforms: []string{"linux/amd64", "linux/arm64"}},
		}).Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
			t.Fatal(err)
		}
		got := r.Commands()[0]
		// Plain `docker build` cannot do multi-platform and fails with a
		// message that does not say so.
		if !strings.Contains(got, "buildx build") {
			t.Errorf("multi-platform did not use buildx: %s", got)
		}
		if !strings.Contains(got, "--platform linux/amd64,linux/arm64") {
			t.Errorf("platforms missing: %s", got)
		}
	})
}

func TestImageIsTaggedByCommitNotByAMovingTag(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	s := newState(pipeline.ModeExecute, map[pipeline.Key]any{common.KeyCommit: "deadbee"})
	if err := (docker.Build{Repository: "app", Options: dockerkit.BuildOptions{Context: "."}, Runner: r}).
		Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	image, _ := pipeline.Get[string](s, common.KeyImage)
	// A moving tag makes "which image is running" unanswerable after the fact.
	if image != "app:deadbee" {
		t.Errorf("image = %q; want it tagged by commit", image)
	}
	if strings.Contains(r.Commands()[0], ":latest") {
		t.Errorf("a moving tag was applied: %s", r.Commands()[0])
	}
}

func TestStartProcessesCommandLine(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	s := deployState(pipeline.ModeExecute)
	err := docker.Start{
		Processes:  []docker.Process{{Name: "web", Port: 8080, Route: true, Scale: 2}},
		EnvFile:    "/srv/app/.env.prod",
		Network:    "appnet",
		NamePrefix: "app",
		Runner:     r,
	}.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	cmds := r.Commands()
	if len(cmds) != 2 {
		t.Fatalf("started %d containers; Scale 2 means 2", len(cmds))
	}
	for _, c := range cmds {
		for _, want := range []string{
			"docker run -d", "--restart unless-stopped",
			"--network appnet", "--env-file /srv/app/.env.prod",
			"ghcr.io/acme/app:a3f1c2d",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("missing %q in: %s", want, c)
			}
		}
		// Loopback only. Binding 0.0.0.0 exposes the container to the internet
		// regardless of the proxy in front of it.
		if !strings.Contains(c, "-p 127.0.0.1::8080") {
			t.Errorf("port not bound to loopback: %s", c)
		}
	}

	containers, err := pipeline.Get[[]string](s, common.KeyContainers)
	if err != nil {
		t.Fatal(err)
	}
	// Names carry the commit, so the previous release's containers can coexist
	// until traffic has moved and StopPrevious can tell them apart.
	for i, name := range containers {
		if !strings.HasPrefix(name, "app-web-a3f1c2d-") {
			t.Errorf("container %d is named %q", i, name)
		}
	}
	if containers[0] == containers[1] {
		t.Error("both replicas got the same container name")
	}
}

func TestRunOnceUsesRmAndRunsBeforeAnythingServes(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	if err := (docker.RunOnce{
		Cmd: []string{"/app/bin", "migrate"}, Network: "appnet",
		EnvFile: "/srv/.env", Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[0]
	// --rm, or one spent container accumulates per deploy forever.
	if !strings.Contains(got, "docker run --rm") {
		t.Errorf("the one-off container is not removed: %s", got)
	}
	for _, want := range []string{"--network appnet", "--env-file /srv/.env", "/app/bin migrate"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

// ── stop-previous, the step that can do real damage ──────────────────────

func TestStopPreviousOnlyStopsWhatThisReleaseReplaced(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{
		{Match: "docker ps", Stdout: "app-web-old111-0\napp-web-a3f1c2d-0\n"},
	}}
	s := deployState(pipeline.ModeExecute)
	pipeline.Set(s, common.KeyContainers, []string{"app-web-a3f1c2d-0"})

	if err := (docker.StopPrevious{NamePrefix: "app", Runner: r}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// SIGTERM with a grace period, THEN remove. `docker rm -f` would SIGKILL
	// immediately and silently ignore any timeout, so draining would be a
	// comment rather than a behaviour.
	if !r.Ran("docker stop --time 10 app-web-old111-0") {
		t.Errorf("the previous container was not gracefully stopped: %v", r.Commands())
	}
	if !r.Ran("docker rm app-web-old111-0") {
		t.Errorf("the previous container was not removed: %v", r.Commands())
	}
	if r.Ran("rm -f") {
		t.Errorf("used rm -f, which SIGKILLs and ignores the grace period: %v", r.Commands())
	}
	// The container this release just started must survive.
	for _, c := range r.Commands() {
		if (strings.Contains(c, "docker stop") || strings.Contains(c, "docker rm")) &&
			strings.Contains(c, "a3f1c2d") {
			t.Errorf("stop-previous targeted the CURRENT release: %s", c)
		}
	}
	// And the listing is scoped by prefix, so nothing else on the host is
	// even considered.
	if !r.Ran("--filter name=app") {
		t.Errorf("containers were listed without a prefix filter: %v", r.Commands())
	}
}

func TestStopPreviousDoesNothingOnAFirstDeploy(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "docker ps", Stdout: "app-web-a3f1c2d-0\n"}}}
	s := deployState(pipeline.ModeExecute)
	pipeline.Set(s, common.KeyContainers, []string{"app-web-a3f1c2d-0"})

	if err := (docker.StopPrevious{NamePrefix: "app", Runner: r}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if r.Ran("docker stop") || r.Ran("docker rm") {
		t.Errorf("stopped something on a first deploy: %v", r.Commands())
	}
}

func TestStopPreviousRequiresAPrefix(t *testing.T) {
	t.Parallel()
	// Without a prefix the step cannot tell its own containers from anything
	// else on the host, and stopping the wrong one is not fixed by retrying.
	if err := (docker.StopPrevious{}).Validate(); err == nil {
		t.Error("StopPrevious accepted an empty NamePrefix")
	}
}

// ── validation ───────────────────────────────────────────────────────────

func TestValidateRejectsBadConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		step pipeline.Validator
		want string
	}{
		{"build with no repository", docker.Build{Options: dockerkit.BuildOptions{Context: "."}}, "Repository"},
		{"build with no context", docker.Build{Repository: "r"}, "Options.Context"},
		{"run-once with no command", docker.RunOnce{}, "Cmd"},
		{"env with no name", common.ResolveEnv{}, "Environment"},
		{"no processes", docker.Start{}, "at least one"},
		{"process with no name", docker.Start{
			Processes: []docker.Process{{Cmd: []string{"x"}}}}, "Name"},
		{"routed process with no port", docker.Start{
			Processes: []docker.Process{{Name: "web", Route: true}}}, "no Port"},
		{"duplicate process names", docker.Start{
			Processes: []docker.Process{{Name: "web"}, {Name: "web"}}}, "twice"},
		{"stop with no prefix", docker.StopPrevious{}, "NamePrefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.step.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsGoodConfiguration(t *testing.T) {
	t.Parallel()
	for _, s := range []pipeline.Validator{
		docker.Build{Repository: "r", Options: dockerkit.BuildOptions{Context: "."}},
		docker.RunOnce{Cmd: []string{"m"}},
		common.ResolveEnv{Environment: "prod"},
		docker.Start{Processes: []docker.Process{{Name: "web", Port: 8080, Route: true}}},
		docker.StopPrevious{NamePrefix: "app"},
	} {
		if err := s.Validate(); err != nil {
			t.Errorf("%T rejected a valid configuration: %v", s, err)
		}
	}
}

func TestScaleZeroMeansOne(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	if err := (docker.Start{
		Processes: []docker.Process{{Name: "web"}}, NamePrefix: "app", Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	if len(r.Commands()) != 1 {
		t.Errorf("Scale 0 started %d containers; want 1", len(r.Commands()))
	}
}

// ── wiring ───────────────────────────────────────────────────────────────

func TestAFullPipelineValidates(t *testing.T) {
	t.Parallel()
	p := pipeline.Pipeline{Steps: []pipeline.Step{
		common.ResolveEnv{Environment: "prod"},
		git.ResolveCommit{},
		docker.Build{Repository: "ghcr.io/acme/app", Options: dockerkit.BuildOptions{Context: "."}},
		docker.Push{},
		docker.Pull{},
		docker.RunOnce{Cmd: []string{"migrate"}},
		docker.Start{Processes: []docker.Process{{Name: "web", Port: 8080, Route: true}}, NamePrefix: "app"},
		docker.WaitHealthy{Timeout: 30 * time.Second},
		docker.StopPrevious{NamePrefix: "app", Drain: time.Second},
	}}
	if err := p.Validate(); err != nil {
		t.Fatalf("the canonical deploy pipeline does not validate: %v", err)
	}
	if len(p.Plan()) != 9 {
		t.Errorf("Plan() has %d entries; want 9", len(p.Plan()))
	}
}

func TestWaitHealthyRejectsACrashLoopingContainer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		inspect string
		want    string
	}{
		// running, restarting, restartCount, exitCode
		{"restarting right now", "true true 2 2 none", "crash-looping"},
		{"has restarted at least once", "true false 1 1 none", "crash-looping"},
		{"already exited non-zero", "false false 0 1 none", "exited with code 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &runner.Fake{Reply: []runner.Scripted{{Match: "inspect", Stdout: tc.inspect}}}
			s := deployState(pipeline.ModeExecute)
			pipeline.Set(s, common.KeyContainers, []string{"app-web-0"})

			err := docker.WaitHealthy{Runner: r, Timeout: 3 * time.Second}.Run(context.Background(), s)
			if err == nil {
				t.Fatalf("a container reporting %q was called healthy", tc.inspect)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestWaitHealthyRequiresTheContainerToStayUp(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "inspect", Stdout: "true false 0 0 none"}}}
	s := deployState(pipeline.ModeExecute)
	pipeline.Set(s, common.KeyContainers, []string{"app-web-0"})

	start := time.Now()
	err := docker.WaitHealthy{Runner: r, Settle: 2 * time.Second, Timeout: 20 * time.Second}.
		Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("passed after %s; it must observe the container for the full settle window", elapsed)
	}
	// And it kept checking rather than asking once.
	if len(r.Commands()) < 2 {
		t.Errorf("inspected %d times; a settle window means repeated checks", len(r.Commands()))
	}
}

func TestWaitHealthyGivesUpFastOnAHopelessContainer(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "inspect", Stdout: "true true 3 2 none"}}}
	s := deployState(pipeline.ModeExecute)
	pipeline.Set(s, common.KeyContainers, []string{"app-web-0"})

	start := time.Now()
	if err := (docker.WaitHealthy{Runner: r, Timeout: 60 * time.Second}).Run(context.Background(), s); err == nil {
		t.Fatal("no error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s to report a crash loop; the answer was already known", elapsed)
	}
}

func TestWaitHealthyPrefersTheImagesOwnHealthcheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		inspect string
		wantErr string
	}{
		// running, restarting, restartCount, exitCode, health
		{"healthy", "true false 0 0 healthy", ""},
		{"unhealthy", "true false 0 0 unhealthy", "reports unhealthy"},
		// The case that matters: up, never restarted, and still not working.
		{"up but unhealthy", "true false 0 0 unhealthy", "reports unhealthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &runner.Fake{Reply: []runner.Scripted{{Match: "inspect", Stdout: tc.inspect}}}
			s := deployState(pipeline.ModeExecute)
			pipeline.Set(s, common.KeyContainers, []string{"app-web-0"})

			err := docker.WaitHealthy{Runner: r, Timeout: 5 * time.Second}.Run(context.Background(), s)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v; want success", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("a container reporting %q was called healthy", tc.inspect)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("err = %v; want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestWaitHealthyPassesWithoutSettlingWhenTheImageSaysHealthy(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{{Match: "inspect", Stdout: "true false 0 0 healthy"}}}
	s := deployState(pipeline.ModeExecute)
	pipeline.Set(s, common.KeyContainers, []string{"app-web-0"})

	start := time.Now()
	if err := (docker.WaitHealthy{Runner: r, Settle: 30 * time.Second, Timeout: time.Minute}).
		Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; a declared healthcheck should not wait out the settle window", elapsed)
	}
}

// TestOptionsLayerMostSpecificWins pins the merge that makes a real deploy
// expressible: set something once for every container, override it for one.
//
// Workers want a memory limit and an extra capability the web process does not,
// and without layering a caller would have to repeat every shared setting on
// every process.
func TestOptionsLayerMostSpecificWins(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := docker.Start{
		Runner:     r,
		NamePrefix: "app",
		Options: dockerkit.RunOptions{
			Memory: "256m",
			Env:    map[string]string{"LOG_LEVEL": "info", "REGION": "eu"},
			Labels: map[string]string{"team": "platform"},
			Extra:  []string{"--dns", "1.1.1.1"},
		},
		Processes: []docker.Process{
			{Name: "web"},
			{Name: "worker", Options: dockerkit.RunOptions{
				Memory: "2g",
				Env:    map[string]string{"LOG_LEVEL": "debug"},
				Extra:  []string{"--cap-add", "SYS_NICE"},
			}},
		},
	}.Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}

	cmds := r.Commands()
	web, worker := cmds[0], cmds[1]

	// Shared settings reach both.
	for i, c := range cmds {
		for _, want := range []string{"--label team=platform", "-e REGION=eu", "--dns 1.1.1.1"} {
			if !strings.Contains(c, want) {
				t.Errorf("container %d missing shared %q: %s", i, want, c)
			}
		}
	}
	// The process overrides what it names, and only that.
	if !strings.Contains(web, "--memory 256m") {
		t.Errorf("web lost the shared memory limit: %s", web)
	}
	if !strings.Contains(worker, "--memory 2g") || strings.Contains(worker, "--memory 256m") {
		t.Errorf("worker did not override the memory limit: %s", worker)
	}
	if !strings.Contains(web, "-e LOG_LEVEL=info") {
		t.Errorf("web lost the shared log level: %s", web)
	}
	if !strings.Contains(worker, "-e LOG_LEVEL=debug") || strings.Contains(worker, "-e LOG_LEVEL=info") {
		t.Errorf("worker did not override the log level: %s", worker)
	}
	// Extra accumulates rather than replacing, a process adds a capability
	// without discarding the shared DNS.
	if !strings.Contains(worker, "--cap-add SYS_NICE") {
		t.Errorf("worker's own Extra was dropped: %s", worker)
	}
}

// TestStepOwnedFieldsCannotBeOverridden pins that the settings derived from
// pipeline state win, so a caller cannot silently produce a container named or
// imaged differently from what the pipeline recorded.
func TestStepOwnedFieldsCannotBeOverridden(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := docker.Start{
		Runner: r, NamePrefix: "app", Network: "appnet",
		Options:   dockerkit.RunOptions{Name: "hijacked", Image: "evil:latest", Network: "hostile"},
		Processes: []docker.Process{{Name: "web"}},
	}.Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[0]
	if strings.Contains(got, "hijacked") || strings.Contains(got, "evil:latest") || strings.Contains(got, "hostile") {
		t.Errorf("a caller overrode a step-owned field: %s", got)
	}
	if !strings.Contains(got, "--name app-web-a3f1c2d-0") || !strings.Contains(got, "ghcr.io/acme/app:a3f1c2d") {
		t.Errorf("the pipeline's own values were not used: %s", got)
	}
}

// TestBuildRefusesACallerSuppliedTag pins that the tag stays derived from the
// commit. Silently overwriting it would produce an image tagged something the
// caller never asked for.
func TestBuildRefusesACallerSuppliedTag(t *testing.T) {
	t.Parallel()
	err := docker.Build{
		Repository: "app",
		Options:    dockerkit.BuildOptions{Context: ".", Tag: "mine:latest"},
	}.Validate()
	if err == nil {
		t.Fatal("a caller-supplied Tag was accepted")
	}
	if !strings.Contains(err.Error(), "derived") {
		t.Errorf("err = %v; want it to say why", err)
	}
}

// TestBuildPassesEveryLibraryOptionThrough is the point of embedding the
// options struct: an option added to the library needs no change here.
func TestBuildPassesEveryLibraryOptionThrough(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := docker.Build{
		Repository: "app", Runner: r,
		Options: dockerkit.BuildOptions{
			Context: ".", Target: "runtime", NoCache: true,
			Labels: map[string]string{"rev": "abc"},
			Extra:  []string{"--secret", "id=npm"},
		},
	}.Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[0]
	for _, want := range []string{"--target runtime", "--no-cache", "--label rev=abc", "--secret id=npm"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

// TestBuildArgsFromStateReachTheCommandLine is the test that would have caught
// the real bug: acme_api's images shipped with unstamped binaries because the
// build args were never passed, and nothing failed to say so.
func TestBuildArgsFromStateReachTheCommandLine(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{}
	b := docker.Build{
		Repository: "ghcr.io/acme/app",
		Options: dockerkit.BuildOptions{
			Context: ".",
			Args:    map[string]string{"BUILD_TIME": "2026-08-26T00:00:00Z"},
		},
		ArgsFromState: map[string]pipeline.Key{
			"COMMIT":      common.KeyCommit,
			"ENVIRONMENT": common.KeyEnv,
		},
		Runner: f,
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := b.Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--build-arg COMMIT=a3f1c2d",
		"--build-arg ENVIRONMENT=prod",
		"--build-arg BUILD_TIME=2026-08-26T00:00:00Z",
	} {
		if !f.Ran(want) {
			t.Errorf("missing %q in: %v", want, f.Commands())
		}
	}
}

// TestBuildArgsFromStateJoinRequires proves the bindings are data, not a
// callback: the pipeline can see which keys a build needs and refuse a
// mis-ordered pipeline before anything is built.
func TestBuildArgsFromStateJoinRequires(t *testing.T) {
	t.Parallel()
	b := docker.Build{
		Repository:    "ghcr.io/acme/app",
		Options:       dockerkit.BuildOptions{Context: "."},
		ArgsFromState: map[string]pipeline.Key{"ENVIRONMENT": common.KeyEnv},
	}
	var sawEnv bool
	for _, k := range b.Requires() {
		if k == common.KeyEnv {
			sawEnv = true
		}
	}
	if !sawEnv {
		t.Errorf("Requires = %v, missing %q", b.Requires(), common.KeyEnv)
	}
}

// TestBuildArgsFromStateOverrideOptions pins the documented precedence: a
// value resolved from state is the more specific one.
func TestBuildArgsFromStateOverrideOptions(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{}
	b := docker.Build{
		Repository: "ghcr.io/acme/app",
		Options: dockerkit.BuildOptions{
			Context: ".",
			Args:    map[string]string{"COMMIT": "stale"},
		},
		ArgsFromState: map[string]pipeline.Key{"COMMIT": common.KeyCommit},
		Runner:        f,
	}
	if err := b.Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("--build-arg COMMIT=a3f1c2d") {
		t.Errorf("state did not win: %v", f.Commands())
	}
	if f.Ran("COMMIT=stale") {
		t.Errorf("stale Options value survived: %v", f.Commands())
	}
}

// TestBuildDoesNotMutateCallerOptions holds the copy invariant: running the
// same pipeline value twice must not accumulate args from the first run.
func TestBuildDoesNotMutateCallerOptions(t *testing.T) {
	t.Parallel()
	opts := dockerkit.BuildOptions{Context: "."}
	b := docker.Build{
		Repository:    "ghcr.io/acme/app",
		Options:       opts,
		ArgsFromState: map[string]pipeline.Key{"COMMIT": common.KeyCommit},
		Runner:        &runner.Fake{},
	}
	if err := b.Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	if len(b.Options.Args) != 0 {
		t.Errorf("caller's Options.Args was mutated: %v", b.Options.Args)
	}
}

// TestBuildRejectsEmptyStateKey catches the binding that would resolve to
// nothing at run time. A config error deserves a config-time refusal.
func TestBuildRejectsEmptyStateKey(t *testing.T) {
	t.Parallel()
	b := docker.Build{
		Repository:    "ghcr.io/acme/app",
		Options:       dockerkit.BuildOptions{Context: "."},
		ArgsFromState: map[string]pipeline.Key{"COMMIT": ""},
	}
	if err := b.Validate(); err == nil {
		t.Error("Validate accepted a binding with no state key")
	}
}

// TestBuildRequiresDedupes covers two build args reading one key, a version
// and a branch are commonly the same value.
func TestBuildRequiresDedupes(t *testing.T) {
	t.Parallel()
	b := docker.Build{
		Repository: "ghcr.io/acme/app",
		Options:    dockerkit.BuildOptions{Context: "."},
		ArgsFromState: map[string]pipeline.Key{
			"VERSION": common.KeyBranch,
			"BRANCH":  common.KeyBranch,
			"COMMIT":  common.KeyCommit,
		},
	}
	seen := map[pipeline.Key]int{}
	for _, k := range b.Requires() {
		seen[k]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("key %q appears %d times in Requires", k, n)
		}
	}
}

// TestDryRunShowsTheFullRunArgv pins that a dry run prints a copy-pasteable
// command for the step whose command is hardest to guess.
//
// docker run carries the mounts, the env file, the network, the restart policy
// and the stop timeout. A dry run that summarised it as "would start X" could
// not be used to check any of that, nor to reproduce the step by hand, which
// is most of what a dry run is for.
func TestDryRunShowsTheFullRunArgv(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeDryRun, pipeline.NewTextReporter(&out))
	pipeline.Set(st, common.KeyCommit, "a3f1c2d")
	pipeline.Set(st, common.KeyEnv, "prod")
	pipeline.Set(st, common.KeyImage, "ghcr.io/acme/app:a3f1c2d")

	start := docker.Start{
		NamePrefix: "app-prod",
		Network:    "host",
		EnvFile:    "/srv/app/.env.prod",
		Volumes:    []string{"/srv/app/logs:/app/logs"},
		Processes:  []docker.Process{{Name: "api", StopTimeout: 90 * time.Second}},
	}
	if err := start.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{
		"would run locally: docker run",
		"--env-file /srv/app/.env.prod",
		"-v /srv/app/logs:/app/logs",
		"--network host",
		"--stop-timeout 90",
		"ghcr.io/acme/app:a3f1c2d",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dry run output is missing %q:\n%s", want, got)
		}
	}
}

// TestLoginValidate covers the configuration check. Every field is required
// because a login missing any of them fails at the registry with a message
// about credentials rather than about the pipeline.
func TestLoginValidate(t *testing.T) {
	t.Parallel()
	full := docker.Login{Registry: "ghcr.io", Username: "u", Password: secret.New("p")}
	if err := full.Validate(); err != nil {
		t.Errorf("a complete Login was rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		l    docker.Login
	}{
		{"no registry", docker.Login{Username: "u", Password: secret.New("p")}},
		{"no username", docker.Login{Registry: "ghcr.io", Password: secret.New("p")}},
		{"no password", docker.Login{Registry: "ghcr.io", Username: "u"}},
	} {
		if err := tc.l.Validate(); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// TestLoginRunNeverLogsThePassword is the containment check on the one step
// that handles a credential directly.
func TestLoginRunNeverLogsThePassword(t *testing.T) {
	t.Parallel()
	const password = "ghp_averysecrettoken"
	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&out))
	f := &runner.Fake{}

	l := docker.Login{Registry: "ghcr.io", Username: "actor", Password: secret.New(password), Runner: f}
	if err := l.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), password) {
		t.Errorf("the password reached the run's output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ghcr.io") {
		t.Errorf("the registry is not named, so a failure could not be placed:\n%s", out.String())
	}
	// It must still have logged in. The credential reaches docker, just not
	// the log.
	if !f.Ran("login") {
		t.Errorf("no login was attempted: %v", f.Commands())
	}
}

func TestLoginDryRunDoesNotAuthenticate(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{}
	st := pipeline.NewState(pipeline.ModeDryRun, nil)
	l := docker.Login{Registry: "ghcr.io", Username: "u", Password: secret.New("p"), Runner: f}
	if err := l.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(f.Commands()) != 0 {
		t.Errorf("a dry run authenticated: %v", f.Commands())
	}
}

// TestPruneRun covers the last step in a deploy, including its dry run, which
// must print a command a person could paste rather than a summary.
func TestPruneRun(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "prune", Stdout: "Total reclaimed space: 1.2GB\n"},
	}}
	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&out))

	p := docker.Prune{Runner: f, Options: dockerkit.PruneOptions{Filter: []string{"until=24h"}}}
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1.2GB") {
		t.Errorf("prune did not report what it reclaimed:\n%s", out.String())
	}
	if !f.Ran("until=24h") {
		t.Errorf("the filter did not reach docker: %v", f.Commands())
	}
}

func TestPruneDryRunPrintsAPasteableCommand(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeDryRun, pipeline.NewTextReporter(&out))
	f := &runner.Fake{}

	p := docker.Prune{Runner: f, Options: dockerkit.PruneOptions{Filter: []string{"until=24h"}}}
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(f.Commands()) != 0 {
		t.Errorf("a dry run pruned: %v", f.Commands())
	}
	got := out.String()
	if !strings.Contains(got, "docker image prune") || !strings.Contains(got, "until=24h") {
		t.Errorf("dry run did not print the command:\n%s", got)
	}
	if strings.Contains(got, "[") {
		t.Errorf("the command was printed as a Go slice rather than a shell line:\n%s", got)
	}
}

// TestRunOnceLabelsItselfForThePlan. A pipeline may hold several one-off
// commands, a migration, a seed, a cache warm, and a plan that lists "run-once"
// three times says nothing about which is which. The default names the
// mechanism rather than the commonest use, because a plan reading "migrate"
// for a cache warm is a plan that lies.
func TestRunOnceLabelsItselfForThePlan(t *testing.T) {
	t.Parallel()
	if got := (docker.RunOnce{Cmd: []string{"x"}}).Name(); got != "run-once" {
		t.Errorf("unlabelled RunOnce is called %q, want the mechanism's name", got)
	}
	if got := (docker.RunOnce{Label: "migrate", Cmd: []string{"x"}}).Name(); got != "migrate" {
		t.Errorf("Name() = %q, want the caller's label", got)
	}
}

// TestPruneNamesWhatItActuallyPrunes. The step reported "prune-images"
// whatever it was given, so a plan pruning volumes said it was pruning images.
// The label is derived from the target for exactly that reason.
func TestPruneNamesWhatItActuallyPrunes(t *testing.T) {
	t.Parallel()
	if got := (docker.Prune{}).Name(); got != "prune-"+dockerkit.DefaultPruneTarget {
		t.Errorf("the zero Prune is called %q, want the default target", got)
	}
	for _, target := range dockerkit.PruneTargets {
		step := docker.Prune{Options: dockerkit.PruneOptions{Target: target}}
		if got, want := step.Name(), "prune-"+target; got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
	}
}

// TestStartPublishesOnlyRoutedContainersAsRouted is what makes Process.Route
// mean something. It declared an intent that nothing acted on until this: the
// flag's only effect was to demand a Port, while SwitchTraffic handed the
// proxy every container the deploy started, workers included.
func TestStartPublishesOnlyRoutedContainersAsRouted(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	st := deployState(pipeline.ModeExecute)
	err := (docker.Start{
		Processes: []docker.Process{
			{Name: "web", Port: 8080, Route: true},
			{Name: "worker"}, // dials out; a proxy must never route to it
		},
		NamePrefix: "app", Runner: r,
	}).Run(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	all, err := pipeline.Get[[]string](st, common.KeyContainers)
	if err != nil {
		t.Fatal(err)
	}
	routed, err := pipeline.Get[[]string](st, common.KeyRouted)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("started %v, want both processes", all)
	}
	if len(routed) != 1 || !strings.Contains(routed[0], "web") {
		t.Errorf("routed = %v, want only the web container", routed)
	}
}

// TestStartProvidesRoutedEvenWhenEmpty. A key a step Provides must exist
// afterwards, or a later step's Requires is a lie and the wiring check that
// Validate performs proves nothing.
func TestStartProvidesRoutedEvenWhenEmpty(t *testing.T) {
	t.Parallel()
	st := deployState(pipeline.ModeExecute)
	if err := (docker.Start{
		Processes: []docker.Process{{Name: "worker"}}, Runner: &runner.Fake{},
	}).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	routed, err := pipeline.Get[[]string](st, common.KeyRouted)
	if err != nil {
		t.Fatalf("KeyRouted was not provided: %v", err)
	}
	if len(routed) != 0 {
		t.Errorf("routed = %v, want none", routed)
	}
}

// TestReplaceClearsTheNameFirst covers redeploying the SAME commit, which is
// what a rehearsal does over and over: container names carry the commit, so
// the second run collides with the first and docker refuses with exit 125,
// "container name is already in use". Replace stops and removes the holder
// first, because that holder is this very release.
func TestReplaceClearsTheNameFirst(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := (docker.Start{
		Processes:  []docker.Process{{Name: "app", StopTimeout: 30 * time.Second}},
		NamePrefix: "app", Replace: true, Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	cmds := r.Commands()
	if len(cmds) != 3 {
		t.Fatalf("want stop, rm, run; got %d command(s): %v", len(cmds), cmds)
	}
	// Stopped before removed: `docker rm` refuses a running container, and
	// forcing it would kill work the process was told it had time to finish.
	if !strings.Contains(cmds[0], "docker stop") || !strings.Contains(cmds[0], "--time 30") {
		t.Errorf("first command = %q, want a graceful stop", cmds[0])
	}
	if !strings.Contains(cmds[1], "docker rm") {
		t.Errorf("second command = %q, want the removal", cmds[1])
	}
	if !strings.Contains(cmds[2], "docker run") {
		t.Errorf("third command = %q, want the start", cmds[2])
	}
}

// TestWithoutReplaceNothingIsRemoved is the default, and it is the safe
// reading: "that name is taken" normally means something is running that this
// step knows nothing about.
func TestWithoutReplaceNothingIsRemoved(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := (docker.Start{
		Processes: []docker.Process{{Name: "app"}}, NamePrefix: "app", Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Commands() {
		if strings.Contains(c, "docker rm") || strings.Contains(c, "docker stop") {
			t.Errorf("the default removed something: %q", c)
		}
	}
}

// TestReplaceIsSuppressedByADryRun. A rehearsal that removed the running
// container would be a rehearsal with an outage in it.
func TestReplaceIsSuppressedByADryRun(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := (docker.Start{
		Processes: []docker.Process{{Name: "app"}}, NamePrefix: "app", Replace: true, Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeDryRun))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.Commands()); n != 0 {
		t.Errorf("a dry run issued %d command(s): %v", n, r.Commands())
	}
}

// TestReplaceIsQuietWhenThereIsNothingToReplace. The stop and the remove are
// speculative: on the happy path there is no such container, and docker says
// so on stderr. Streaming that into the deploy log prints "Error response from
// daemon: No such container" twice on a run where nothing is wrong, which
// teaches whoever reads the log to skip errors.
func TestReplaceIsQuietWhenThereIsNothingToReplace(t *testing.T) {
	t.Parallel()
	var streamed strings.Builder
	// Matched on the full subcommand, not on "stop": `docker run` carries
	// --restart unless-stopped, which contains it, and the reply meant for the
	// cleanup would answer the start instead.
	r := &runner.Fake{Reply: []runner.Scripted{
		{Match: "docker stop", Stderr: "Error response from daemon: No such container", Exit: 1},
		{Match: "docker rm", Stderr: "Error response from daemon: No such container", Exit: 1},
	}}
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&streamed))
	pipeline.Set(st, common.KeyImage, "ghcr.io/acme/app:a3f1c2d")
	pipeline.Set(st, common.KeyEnv, "prod")

	err := (docker.Start{
		Processes: []docker.Process{{Name: "app"}}, NamePrefix: "app", Replace: true, Runner: r,
	}).Run(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(streamed.String(), "No such container") {
		t.Errorf("the speculative cleanup narrated its failure:\n%s", streamed.String())
	}
}

// TestAliasGivesAReleaseAStableName. A container named after its commit cannot
// be addressed by anything written in advance. An alias is what lets a proxy
// configuration, or another service, name this release once and keep working
// across deploys, which is what makes that configuration version-controllable.
func TestAliasGivesAReleaseAStableName(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	err := (docker.Start{
		Processes:  []docker.Process{{Name: "app", Alias: "acme-stag", Port: 2310, Route: true}},
		NamePrefix: "acme-stag", Network: "prod_stack", Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[0]
	if !strings.Contains(got, "--network-alias acme-stag") {
		t.Errorf("no alias in: %s", got)
	}
	// The container keeps its commit-carrying name; the alias is additional.
	if !strings.Contains(got, "--name acme-stag-app-") {
		t.Errorf("the alias replaced the container name: %s", got)
	}
}

// TestNoAliasByDefault. Two containers sharing a name is a real cost, paid
// only when asked for.
func TestNoAliasByDefault(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	if err := (docker.Start{
		Processes: []docker.Process{{Name: "app"}}, NamePrefix: "app", Runner: r,
	}).Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Commands()[0], "network-alias") {
		t.Errorf("an alias appeared unasked: %s", r.Commands()[0])
	}
}

// ── the plan contract ────────────────────────────────────────────────────

// TestEveryStepIsNamedAndDocumented.
//
// `lath plan` is the command an operator reads before letting something touch
// production, and it is assembled entirely from these four methods. A step
// with no Docs renders as a blank line, which is worse than absent: the reader
// counts the steps, sees one they cannot identify, and either stops trusting
// the listing or approves something they did not read.
//
// Table-driven over every step this package exports, so a new step that
// forgets its Docs fails here rather than in front of whoever is deploying.
func TestEveryStepIsNamedAndDocumented(t *testing.T) {
	t.Parallel()
	for _, step := range []pipeline.Step{
		docker.Build{Repository: "ghcr.io/acme/app", Options: dockerkit.BuildOptions{Context: "."}},
		docker.UseImage{Repository: "ghcr.io/acme/app", Tag: "0375fcf"},
		docker.Push{},
		docker.Pull{},
		docker.Login{Registry: "ghcr.io", Username: "u", Password: secret.New(secretMarker)},
		docker.RunOnce{Label: "migrate", Cmd: []string{"app", "migrate"}},
		docker.Start{NamePrefix: "app", Processes: []docker.Process{{Name: "web"}}},
		docker.WaitHealthy{},
		docker.StopPrevious{NamePrefix: "app"},
		docker.Prune{},
	} {
		t.Run(step.Name(), func(t *testing.T) {
			t.Parallel()
			if step.Name() == "" {
				t.Fatalf("%T has no name; it renders as a blank line in every plan", step)
			}
			documented, ok := step.(pipeline.Documented)
			if !ok {
				t.Fatalf("%T is not Documented; a plan cannot say what it does", step)
			}
			docs := documented.Docs()
			if strings.TrimSpace(docs.Summary) == "" {
				t.Errorf("%T has an empty summary", step)
			}
			// A summary is help text, not a sentence: it sits in a column
			// beside the step name and a period there reads as a typo.
			if strings.HasSuffix(docs.Summary, ".") {
				t.Errorf("%T's summary ends in a period: %q", step, docs.Summary)
			}
			// Plans are pasted into chat and issues. A credential reaching
			// either is a leak, and the values these steps hold are the ones
			// worth leaking.
			if strings.Contains(docs.Detail, secretMarker) {
				t.Errorf("%T's detail carries a credential: %q", step, docs.Detail)
			}
		})
	}
}

// secretMarker is a value no legitimate detail line would contain, used to
// prove a credential passed through a step does not reach plan output.
const secretMarker = "s3cr3t-should-never-print"

// TestARetryNoticeReachesTheReporter. supervise writes its "failed after …,
// retrying" notices to a writer; routing them anywhere but the pipeline's
// reporter means a step that is quietly retrying for a minute is
// indistinguishable from a step that has hung.
func TestARetryNoticeReachesTheReporter(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&out))
	pipeline.Set(st, common.KeyImage, "ghcr.io/acme/app:a3f1c2d")
	// A failure that is not hopeless, so the policy retries rather than giving
	// up at once: a name-resolution blip is the textbook transient.
	r := &runner.Fake{Fail: errors.New("temporary failure in name resolution")}

	err := docker.Push{
		Attempts: 2, Backoff: time.Millisecond, MaxBackoff: time.Millisecond, Runner: r,
	}.Run(context.Background(), st)
	if err == nil {
		t.Fatal("a push against a failing runner reported success")
	}
	if !strings.Contains(out.String(), "retrying") {
		t.Errorf("the retry was invisible in the pipeline's own output:\n%s", out.String())
	}
}

// TestLoginKeepsItsPasswordOutOfEveryString. The credential is the one value
// in this package that must never be rendered, and Docs, Name and the error
// path are the three places a step turns itself into text.
func TestLoginKeepsItsPasswordOutOfEveryString(t *testing.T) {
	t.Parallel()
	step := docker.Login{
		Registry: "ghcr.io", Username: "deploy", Password: secret.New(secretMarker),
	}
	docs := step.Docs()

	rendered := step.Name() + " " + docs.Summary + " " + docs.Detail + " " + fmt.Sprint(step)
	if strings.Contains(rendered, secretMarker) {
		t.Errorf("the password appears in the step's own rendering:\n%s", rendered)
	}
}

// TestARehearsalDoesNotRequireDocker.
//
// stop-previous reads the host to name the containers a real run would retire,
// and that read happens under dry run too, because it is most of what makes
// the rehearsal worth reading. But a rehearsal must not REQUIRE the machine to
// answer: checking that a definition is wired correctly is not the same as
// being ready to deploy, and a laptop without docker, or an unreachable host,
// is exactly when a dry run is most useful.
//
// Found by running the gate in a container with no docker binary: every other
// step rehearsed cleanly and this one aborted the whole pipeline.
func TestARehearsalDoesNotRequireDocker(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	st := pipeline.NewState(pipeline.ModeDryRun, pipeline.NewTextReporter(&out))
	pipeline.Set(st, common.KeyContainers, []string{"app-web-a3f1c2d-0"})
	missing := errors.New("proc: docker: program not found")

	step := docker.StopPrevious{NamePrefix: "app", Runner: &runner.Fake{Fail: missing}}
	if err := step.Run(context.Background(), st); err != nil {
		t.Fatalf("a rehearsal failed because the host could not be asked: %v", err)
	}

	said := out.String()
	// Reported, never swallowed: a reader comparing this rehearsal against a
	// real run has to know which part could not be answered.
	if !strings.Contains(said, "cannot list containers") {
		t.Errorf("the gap was hidden:\n%s", said)
	}
	if !strings.Contains(said, "app") {
		t.Errorf("the rehearsal does not say what a real run would retire:\n%s", said)
	}
}

// TestARealRunStillFailsWhenTheHostCannotBeAsked. The tolerance above is for
// rehearsals only: proceeding through a real deploy without knowing which
// containers exist would leave the previous release running alongside the new
// one, both answering to the same alias.
func TestARealRunStillFailsWhenTheHostCannotBeAsked(t *testing.T) {
	t.Parallel()
	st := deployState(pipeline.ModeExecute)
	pipeline.Set(st, common.KeyContainers, []string{"app-web-a3f1c2d-0"})
	missing := errors.New("proc: docker: program not found")

	step := docker.StopPrevious{NamePrefix: "app", Runner: &runner.Fake{Fail: missing}}
	err := step.Run(context.Background(), st)
	if err == nil {
		t.Fatal("a real run continued without being able to see the host")
	}
	if !errors.Is(err, missing) {
		t.Errorf("err = %v; want the underlying failure to stay reachable", err)
	}
}
