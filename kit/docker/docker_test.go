package docker_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/runner"
)

// TestBuildArgsSinglePlatform pins the plain-build command line.
func TestBuildArgsSinglePlatform(t *testing.T) {
	t.Parallel()
	got := strings.Join(docker.BuildOptions{
		Tag: "ghcr.io/acme/app:abc123", Context: ".", Dockerfile: "Dockerfile",
		Args: map[string]string{"VERSION": "1.2", "ARCH": "arm"},
	}.BuildArgs(), " ")

	for _, want := range []string{
		"build", "-t ghcr.io/acme/app:abc123", "-f Dockerfile",
		"--build-arg ARCH=arm", "--build-arg VERSION=1.2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	// Sorted, so two equivalent builds produce an identical command line and a
	// log diff means a real change.
	if strings.Index(got, "ARCH") > strings.Index(got, "VERSION") {
		t.Errorf("build args are not sorted: %s", got)
	}
	if strings.Contains(got, "buildx") {
		t.Errorf("a single-platform build used buildx: %s", got)
	}
}

// TestBuildArgsMultiPlatformNeedsBuildx pins that plain `docker build` is not
// used for multi-platform. It cannot do it, and fails with a message that
// does not say so.
func TestBuildArgsMultiPlatformNeedsBuildx(t *testing.T) {
	t.Parallel()
	got := strings.Join(docker.BuildOptions{
		Tag: "app:1", Context: ".", Platforms: []string{"linux/amd64", "linux/arm64"},
	}.BuildArgs(), " ")

	if !strings.HasPrefix(got, "buildx build") {
		t.Errorf("did not use buildx: %s", got)
	}
	if !strings.Contains(got, "--platform linux/amd64,linux/arm64") {
		t.Errorf("platforms missing: %s", got)
	}
}

func TestRunArgs(t *testing.T) {
	t.Parallel()
	t.Run("a detached service", func(t *testing.T) {
		t.Parallel()
		got := strings.Join(docker.RunOptions{
			Name: "app-web-0", Image: "app:1", Detach: true,
			Network: "appnet", EnvFile: "/srv/.env", Port: 8080,
			Volumes:     []string{"/srv/logs:/app/logs"},
			ExtraHosts:  []string{"host.docker.internal:host-gateway"},
			StopTimeout: 90 * time.Second,
		}.RunArgs(), " ")

		for _, want := range []string{
			"run -d", "--name app-web-0", "--restart unless-stopped",
			"--network appnet", "--env-file /srv/.env",
			"-v /srv/logs:/app/logs", "--add-host host.docker.internal:host-gateway",
			"--stop-timeout 90", "app:1",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in: %s", want, got)
			}
		}
		// Loopback only: 0.0.0.0 would expose the container to the internet
		// regardless of the proxy in front of it.
		if !strings.Contains(got, "-p 127.0.0.1::8080") {
			t.Errorf("port not bound to loopback: %s", got)
		}
	})

	t.Run("host networking cannot publish", func(t *testing.T) {
		t.Parallel()
		// docker rejects -p alongside --network host, so the flag must be
		// omitted rather than passed and refused.
		got := strings.Join(docker.RunOptions{
			Name: "x", Image: "app:1", Detach: true,
			Network: docker.HostNetwork, Port: 8080,
		}.RunArgs(), " ")
		if strings.Contains(got, "-p ") {
			t.Errorf("published a port with host networking: %s", got)
		}
	})

	t.Run("a one-off is removed and not restarted", func(t *testing.T) {
		t.Parallel()
		got := strings.Join(docker.RunOptions{
			Image: "app:1", Cmd: []string{"migrate"}, Remove: true,
		}.RunArgs(), " ")
		if !strings.Contains(got, "run --rm") {
			t.Errorf("one-off is not removed: %s", got)
		}
		// A restart policy on a command meant to finish would restart it
		// forever after it succeeds.
		if strings.Contains(got, "--restart") {
			t.Errorf("a one-off got a restart policy: %s", got)
		}
		if !strings.HasSuffix(got, "app:1 migrate") {
			t.Errorf("command not appended after the image: %s", got)
		}
	})
}

func TestStopUsesGraceNotForce(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{}
	if err := docker.On(f).Stop(context.Background(), "app-web-0", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	// `docker rm -f` SIGKILLs immediately and accepts no timeout, so draining
	// would be a comment rather than a behaviour.
	if !f.Ran("stop --time 30 app-web-0") {
		t.Errorf("did not stop with a grace period: %v", f.Commands())
	}
	if f.Ran("rm -f") {
		t.Errorf("used rm -f: %v", f.Commands())
	}
}

func TestInspectParsesState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out  string
		want docker.State
	}{
		{"true false 0 0 healthy", docker.State{Running: true, Health: docker.HealthHealthy}},
		{"true true 3 2 none", docker.State{Running: true, Restarting: true, RestartCount: 3, ExitCode: 2, Health: docker.HealthNone}},
		{"false false 0 1 none", docker.State{ExitCode: 1, Health: docker.HealthNone}},
	} {
		f := &runner.Fake{Reply: []runner.Scripted{{Match: "inspect", Stdout: tc.out}}}
		got, err := docker.On(f).Inspect(context.Background(), "c")
		if err != nil {
			t.Fatalf("%q: %v", tc.out, err)
		}
		if got != tc.want {
			t.Errorf("%q parsed as %+v; want %+v", tc.out, got, tc.want)
		}
	}
}

// TestLoginPassesThePasswordOnStdin pins that a credential never becomes an
// argument, where it would be visible in the process list to every user.
func TestLoginPassesThePasswordOnStdin(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{}
	if err := docker.On(f).Login(context.Background(), "ghcr.io", "actor", "ghp_supersecret"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.Commands(), " ")
	if !strings.Contains(joined, "--password-stdin") {
		t.Errorf("did not use --password-stdin: %s", joined)
	}
	if strings.Contains(joined, "ghp_supersecret") {
		t.Errorf("the password appeared in the command line: %s", joined)
	}
}

func TestNonZeroExitBecomesAnError(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "docker", Exit: 1, Stderr: "no such image"}}}
	err := docker.On(f).Pull(context.Background(), "app:1")
	if err == nil {
		t.Fatal("a failing pull reported success")
	}
	// Both the exit code and docker's own message must survive: one without
	// the other leaves the reader guessing.
	if !strings.Contains(err.Error(), "exit 1") || !strings.Contains(err.Error(), "no such image") {
		t.Errorf("err = %v", err)
	}
}

func TestUnstartableCommandIsAnError(t *testing.T) {
	t.Parallel()
	boom := errors.New("docker is not installed")
	err := docker.On(&runner.Fake{Fail: boom}).Push(context.Background(), "app:1")
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the underlying failure", err)
	}
}

func TestNamesFiltersByPrefix(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "ps", Stdout: "app-web-0\napp-web-1\n\n"}}}
	got, err := docker.On(f).Names(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %v; blank lines should be dropped", got)
	}
	if !f.Ran("--filter name=app") {
		t.Errorf("listed without a prefix filter: %v", f.Commands())
	}
}

func TestPruneReportsTheSummary(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "prune", Stdout: "Deleted Images:\nsha256:abc\n\nTotal reclaimed space: 1.2GB\n"},
	}}
	got, err := docker.On(f).Prune(context.Background(), docker.PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Total reclaimed space: 1.2GB" {
		t.Errorf("Prune = %q; want docker's summary line", got)
	}
}

func TestAuthFailureDistinguishesCredentialsFromTransient(t *testing.T) {
	t.Parallel()
	for _, permanent := range []string{
		"unauthorized: authentication required", "denied: permission denied", "invalid username or password",
	} {
		if !docker.AuthFailure([]byte(permanent)) {
			t.Errorf("%q was not recognised as a credential failure", permanent)
		}
	}
	for _, transient := range []string{"toomanyrequests: rate limit", "i/o timeout", "502 bad gateway"} {
		if docker.AuthFailure([]byte(transient)) {
			t.Errorf("%q was treated as a credential failure and would not be retried", transient)
		}
	}
}

// TestNilRunnerMeansLocal pins that the zero value is usable.
func TestNilRunnerMeansLocal(t *testing.T) {
	t.Parallel()
	if got := docker.On(nil).Where().Describe(); got != "locally" {
		t.Errorf("On(nil) runs %s; want locally", got)
	}
}

// TestEveryOptionsStructHasAnEscapeHatch pins the rule that keeps this package
// usable with flags it does not model.
//
// An exhaustive struct would need a field per docker flag and would still be
// missing the one somebody needs the day they need it. Without Extra, a caller
// abandons the package the first time it does not fit, losing its guarantees
// along with the gap.
func TestEveryOptionsStructHasAnEscapeHatch(t *testing.T) {
	t.Parallel()

	build := strings.Join(docker.BuildOptions{
		Tag: "app:1", Context: ".", Extra: []string{"--secret", "id=npm", "--cache-from", "type=gha"},
	}.BuildArgs(), " ")
	if !strings.Contains(build, "--secret id=npm --cache-from type=gha") {
		t.Errorf("build Extra not passed through: %s", build)
	}
	// Extra must precede the context, which docker takes as the sole
	// positional argument. Anything after it becomes a second one.
	if !strings.HasSuffix(build, " .") {
		t.Errorf("the build context is no longer last: %s", build)
	}

	run := strings.Join(docker.RunOptions{
		Name: "c", Image: "app:1", Cmd: []string{"serve"},
		Extra: []string{"--cap-add", "SYS_PTRACE", "--gpus", "all"},
	}.RunArgs(), " ")
	if !strings.Contains(run, "--cap-add SYS_PTRACE --gpus all") {
		t.Errorf("run Extra not passed through: %s", run)
	}
	// Everything after the image is the CONTAINER's argv, not docker's, so
	// Extra must land before it.
	if !strings.HasSuffix(run, "app:1 serve") {
		t.Errorf("the image and command are no longer last: %s", run)
	}

	prune := strings.Join(docker.PruneOptions{Extra: []string{"--volumes"}}.PruneArgs(), " ")
	if !strings.Contains(prune, "--volumes") {
		t.Errorf("prune Extra not passed through: %s", prune)
	}
}

// TestBuildOptionsCoverTheCommonFlags pins the typed options, which exist so
// the common cases do not have to reach for Extra.
func TestBuildOptionsCoverTheCommonFlags(t *testing.T) {
	t.Parallel()
	got := strings.Join(docker.BuildOptions{
		Tag: "app:1", Context: ".", Target: "runtime", NoCache: true, Pull: true,
		Labels: map[string]string{"org.opencontainers.image.revision": "abc", "team": "platform"},
	}.BuildArgs(), " ")

	for _, want := range []string{
		"--target runtime", "--no-cache", "--pull",
		"--label org.opencontainers.image.revision=abc", "--label team=platform",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	// Labels sorted, so two equivalent builds produce an identical command.
	if strings.Index(got, "image.revision") > strings.Index(got, "team=") {
		t.Errorf("labels are not sorted: %s", got)
	}
}

// TestRunOptionsEnvOverridesTheEnvFile pins the ordering that makes a
// per-container override possible: -e is applied after --env-file, so a caller
// can change one variable without rewriting the file.
func TestRunOptionsEnvOverridesTheEnvFile(t *testing.T) {
	t.Parallel()
	got := strings.Join(docker.RunOptions{
		Name: "c", Image: "app:1", EnvFile: "/srv/.env",
		Env: map[string]string{"LOG_LEVEL": "debug"},
	}.RunArgs(), " ")

	if strings.Index(got, "--env-file") > strings.Index(got, "-e LOG_LEVEL=debug") {
		t.Errorf("-e is applied before --env-file, so the file would win: %s", got)
	}
}

func TestRunOptionsResourceAndIdentityFlags(t *testing.T) {
	t.Parallel()
	got := strings.Join(docker.RunOptions{
		Name: "c", Image: "app:1",
		User: "1000:1000", Workdir: "/app", Memory: "512m", CPUs: "1.5",
		Ports: []string{"127.0.0.1:8080:80"},
	}.RunArgs(), " ")

	for _, want := range []string{
		"--user 1000:1000", "--workdir /app", "--memory 512m", "--cpus 1.5",
		"-p 127.0.0.1:8080:80",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

func TestPruneOptions(t *testing.T) {
	t.Parallel()
	got := strings.Join(docker.PruneOptions{
		Target: "volume", All: true, Filter: []string{"until=24h", "label!=keep"},
	}.PruneArgs(), " ")

	for _, want := range []string{"volume prune", "--all", "--filter until=24h", "--filter label!=keep"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	// -f is not optional: without it docker asks for confirmation on a stdin
	// nothing is attached to, and the command hangs rather than failing.
	if !strings.Contains(got, "-f") {
		t.Errorf("prune is not forced, so it would hang waiting for input: %s", got)
	}
	// The zero value must be the safe default: dangling images only.
	zero := strings.Join(docker.PruneOptions{}.PruneArgs(), " ")
	if zero != "image prune -f" {
		t.Errorf("the zero PruneOptions = %q; want dangling images only", zero)
	}
}

// TestWithOutputStreamsAndStillCaptures pins the property that makes a long
// build debuggable without giving up useful errors: output goes to the
// caller's writer AS IT HAPPENS, and is still captured so a failure can quote
// what the tool actually said.
func TestWithOutputStreamsAndStillCaptures(t *testing.T) {
	t.Parallel()
	var streamed strings.Builder
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "build", Stdout: "#1 DONE\n#2 DONE\n"}}}

	err := docker.On(f).WithOutput(&streamed).Build(context.Background(),
		docker.BuildOptions{Tag: "app:1", Context: "."})
	if err != nil {
		t.Fatal(err)
	}
	if got := streamed.String(); !strings.Contains(got, "#1 DONE") || !strings.Contains(got, "#2 DONE") {
		t.Errorf("streamed = %q, want the command's output", got)
	}
}

// TestWithoutOutputStaysSilent, streaming is opt-in, because a library that
// writes to a stream nobody asked for cannot be embedded.
func TestWithoutOutputStaysSilent(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{{Match: "build", Stdout: "noise\n"}}}
	if err := docker.On(f).Build(context.Background(),
		docker.BuildOptions{Tag: "app:1", Context: "."}); err != nil {
		t.Fatal(err)
	}
	// Nothing to assert but the absence of a panic and a clean run: the point
	// is that On() alone writes nowhere.
}

// TestFailureStillNamesWhatWentWrong. A failed build must not report only an
// exit code.
//
// NOTE this exercises the step's error path, not capture-while-streaming:
// runner.Fake returns its scripted output in the Result whether or not
// Capture was requested, so it cannot tell the two apart. That property is
// proc's, and is tested there, see TestCaptureComposesWithOut.
func TestFailureStillNamesWhatWentWrong(t *testing.T) {
	t.Parallel()
	var streamed strings.Builder
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "build", Stderr: "ERROR: failed to solve: no such file", Exit: 1},
	}}
	err := docker.On(f).WithOutput(&streamed).Build(context.Background(),
		docker.BuildOptions{Tag: "app:1", Context: "."})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error = %v, want it to quote what docker said", err)
	}
}

// TestAvailableMatchesThePath, reports whether the CLI this package shells
// out to exists. Local only: a remote answer needs a round trip, which callers
// make deliberately.
func TestAvailable(t *testing.T) {
	t.Parallel()
	_, lookErr := exec.LookPath(docker.Program)
	if got, want := docker.Available(), lookErr == nil; got != want {
		t.Errorf("Available() = %v, want %v", got, want)
	}
}

// TestWithTerminalReplacesWithOutput. The two are alternatives, not a pair:
// capturing is precisely what would reintroduce the pipe that WithTerminal
// exists to avoid.
func TestWithTerminalReplacesWithOutput(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var streamed strings.Builder
	fake := &runner.Fake{Reply: []runner.Scripted{{Match: "pull", Stdout: "pulled\n"}}}

	// WithTerminal after WithOutput must win, and must not also write to the
	// earlier writer.
	c := docker.On(fake).WithOutput(&streamed).WithTerminal(f)
	if err := c.Pull(context.Background(), "app:1"); err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "" {
		t.Errorf("output still went to the writer WithTerminal replaced: %q", streamed.String())
	}

	// And the reverse: WithOutput after WithTerminal restores capturing.
	var second strings.Builder
	c2 := docker.On(fake).WithTerminal(f).WithOutput(&second)
	if err := c2.Pull(context.Background(), "app:1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.String(), "pulled") {
		t.Errorf("WithOutput did not take effect after WithTerminal: %q", second.String())
	}
}

// TestRemoveAndInspectField cover the two container queries a deploy makes
// outside the Start/Stop path.
func TestRemoveAndInspectField(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{Reply: []runner.Scripted{
		{Match: "inspect", Stdout: "running\n"},
	}}
	c := docker.On(f)

	got, err := c.InspectField(context.Background(), "app-1", "{{.State.Status}}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "running" {
		t.Errorf("InspectField = %q, want the trimmed value", got)
	}
	if !f.Ran("{{.State.Status}}") {
		t.Errorf("the format did not reach docker: %v", f.Commands())
	}

	if err := c.Remove(context.Background(), "app-1"); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("rm") {
		t.Errorf("Remove did not run docker rm: %v", f.Commands())
	}
}

// TestRunPassesOptionsThrough, Run is the raw escape hatch for a container
// this package's helpers do not model.
func TestClientRun(t *testing.T) {
	t.Parallel()
	f := &runner.Fake{}
	err := docker.On(f).Run(context.Background(), docker.RunOptions{
		Name: "one-shot", Image: "app:1", Cmd: []string{"migrate"}, Remove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--name one-shot", "--rm", "app:1", "migrate"} {
		if !f.Ran(want) {
			t.Errorf("missing %q in: %v", want, f.Commands())
		}
	}
}

// fakeDocker puts a stub `docker` on PATH so a Client can be exercised against
// a REAL process rather than runner.Fake.
//
// The distinction matters here and nowhere else: runner.Fake returns its
// scripted output in the Result whether or not proc.Capture was requested, so
// it cannot tell a captured command from an uncaptured one. That is exactly
// the property the terminal-mode tests below are about, so they need a real
// child process, whose output really does go nowhere when it is not captured.
//
// No t.Parallel in any test using this: t.Setenv forbids it.
func fakeDocker(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, docker.Program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// tempTerminal stands in for the operator's terminal. A regular file is not a
// TTY, but every code path this package has cares only that it is an *os.File,
// which is what os/exec passes to the child by descriptor instead of wrapping
// in a pipe.
func tempTerminal(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestTerminalModeStillQuotesFailures is the regression guard for a deploy
// that died with nothing to act on:
//
//	docker run acme-local-api-0f3747e-0 locally: exit 125 after 55ms:
//
// The message after the colon was empty because the whole Client was in
// terminal mode, so docker run was not captured and everything it said went to
// a screen the panel then repainted over. Only the progress-drawing
// subcommands need a terminal; the rest must stay captured so their errors say
// what happened.
func TestTerminalModeStillQuotesFailures(t *testing.T) {
	fakeDocker(t, `[ "$1" = run ] && { echo "docker: invalid mount config" >&2; exit 125; }; exit 0`)

	err := docker.On(nil).WithTerminal(tempTerminal(t)).Run(context.Background(),
		docker.RunOptions{Name: "api-0", Image: "app:1"})
	if err == nil {
		t.Fatal("expected the stub's exit 125 to be an error")
	}
	if !strings.Contains(err.Error(), "invalid mount config") {
		t.Errorf("error = %v, want it to quote what docker said", err)
	}
}

// TestTerminalModeStillParsesOutput. The same bug in its quieter form: inspect
// and ps are read from captured stdout, and an uncaptured Result is empty, so
// in terminal mode a running container reported as missing, with no error to
// notice.
func TestTerminalModeStillParsesOutput(t *testing.T) {
	fakeDocker(t, `[ "$1" = inspect ] && { echo running; exit 0; }; exit 1`)

	got, err := docker.On(nil).WithTerminal(tempTerminal(t)).
		InspectField(context.Background(), "api-0", "{{.State.Status}}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "running" {
		t.Errorf("InspectField = %q, want the stub's output; terminal mode swallowed it", got)
	}
}

// TestTerminalModeGivesProgressCommandsTheTerminal is the other half: build,
// push and pull must keep the raw *os.File, because handing BuildKit a pipe
// silently downgrades its live table to a flat log. Asserted by the file
// having received the output directly.
func TestTerminalModeGivesProgressCommandsTheTerminal(t *testing.T) {
	fakeDocker(t, `echo "#1 [internal] load build definition"; exit 0`)
	tty := tempTerminal(t)

	if err := docker.On(nil).WithTerminal(tty).Build(context.Background(),
		docker.BuildOptions{Tag: "app:1", Context: "."}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(tty.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "load build definition") {
		t.Errorf("the build's output did not reach the terminal: %q", b)
	}
}

// TestTerminalModeEchoesCapturedCommandsToo. Capturing a command must not make
// it silent: the terminal is still where the operator is looking, and a
// `docker run` that prints nothing at all is the same debugging hole in a
// different place.
func TestTerminalModeEchoesCapturedCommandsToo(t *testing.T) {
	fakeDocker(t, `echo "0f3747e0"; exit 0`)
	tty := tempTerminal(t)

	if err := docker.On(nil).WithTerminal(tty).Run(context.Background(),
		docker.RunOptions{Name: "api-0", Image: "app:1"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(tty.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "0f3747e0") {
		t.Errorf("a captured command wrote nothing to the terminal: %q", b)
	}
}

// TestAliasesNeedAUserDefinedNetwork. Docker refuses the whole run with
// "network-scoped aliases are only supported for user-defined networks", so an
// alias on the default bridge is not a no-op, it is a failed deploy. Dropping
// it is what lets a pipeline written for a real stack rehearse on a laptop.
func TestAliasesNeedAUserDefinedNetwork(t *testing.T) {
	t.Parallel()
	base := docker.RunOptions{Image: "app:1", Name: "app-0", NetworkAliases: []string{"app"}}

	for _, tc := range []struct {
		network string
		want    bool
	}{
		{"", false},                 // default bridge: docker refuses
		{docker.HostNetwork, false}, // no DNS of its own to add a name to
		{"prod_stack", true},        // user-defined: the alias works
	} {
		opts := base
		opts.Network = tc.network
		got := strings.Contains(strings.Join(opts.RunArgs(), " "), "--network-alias")
		if got != tc.want {
			t.Errorf("network %q: alias emitted = %v, want %v", tc.network, got, tc.want)
		}
	}
}

// TestImagesListsWhatCouldRunWithoutPulling. For a deploy that is "what could
// I roll back to": an image already on the machine will start whatever the
// registry currently thinks.
func TestImagesListsWhatCouldRunWithoutPulling(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{
		{Match: "image ls", Stdout: "0375fcf\n7e4e84d\n<none>\n\n1d905d4\n"},
	}}

	got, err := docker.On(r).Images(context.Background(), "ghcr.io/acme/app")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0375fcf", "7e4e84d", "1d905d4"}
	if len(got) != len(want) {
		t.Fatalf("Images = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Images[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Untagged images cannot be run by name, so offering them as candidates
	// would be offering something that does not work.
	for _, tag := range got {
		if tag == "<none>" {
			t.Error("an untagged image was offered as a candidate")
		}
	}
}

// TestTagOfSurvivesARegistryPort is the trap this pair exists to close. A
// registry host may carry a port, so a reference can hold two colons;
// splitting from the left reports the port number as the running version,
// which is a wrong answer that looks plausible enough to act on.
func TestTagOfSurvivesARegistryPort(t *testing.T) {
	t.Parallel()
	for reference, want := range map[string]string{
		"ghcr.io/acme/app:0375fcf":       "0375fcf",
		"registry:5000/acme/app:a3f1c2d": "a3f1c2d",
		// Untagged, both shapes: no colon at all, and a colon that belongs to
		// a port. Both mean "docker will read this as :latest", which a deploy
		// must never mistake for a version it can roll back to.
		"acme/app":               "",
		"registry:5000/acme/app": "",
	} {
		if got := docker.TagOf(reference); got != want {
			t.Errorf("TagOf(%q) = %q, want %q", reference, got, want)
		}
	}
}

// TestReferenceAndTagOfAreInverses. The two are used at opposite ends of a
// deploy, one names the image to run and the other reads back what IS
// running, so a disagreement between them shows up as a rollback that reports
// success while running something else.
func TestReferenceAndTagOfAreInverses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ repository, tag string }{
		{"ghcr.io/acme/app", "0375fcf"},
		{"registry:5000/acme/app", "a3f1c2d"},
		{"app", "latest"},
	} {
		ref := docker.Reference(tc.repository, tc.tag)
		if got := docker.TagOf(ref); got != tc.tag {
			t.Errorf("TagOf(Reference(%q, %q)) = %q, want %q", tc.repository, tc.tag, got, tc.tag)
		}
	}
}

// TestReferenceWithoutATagIsTheBareRepository. Docker reads that as :latest,
// which is a real thing to want and never what a deploy wants: appending a
// bare separator would instead produce "repo:", which docker rejects.
func TestReferenceWithoutATagIsTheBareRepository(t *testing.T) {
	t.Parallel()
	const repository = "ghcr.io/acme/app"
	if got := docker.Reference(repository, ""); got != repository {
		t.Errorf("Reference(%q, \"\") = %q, want the repository unchanged", repository, got)
	}
}

// TestRemoveImageClassifiesDockersRefusals.
//
// A retention sweep walks a list and removes what it no longer needs. Two of
// the three outcomes are not faults: an image already gone is the sweep's goal
// reached early, and an image a container holds is the safety property that
// stops the sweep deleting what is serving. A caller can only continue past
// them if it can TELL them apart, and matching on docker's prose at every call
// site is how one of them silently becomes fatal.
func TestRemoveImageClassifiesDockersRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		stderr string
		want   error
	}{
		{
			"already gone",
			"Error: No such image: ghcr.io/acme/app:0375fcf",
			docker.ErrNoSuchImage,
		},
		{
			"held by a container",
			`Error response from daemon: conflict: unable to remove repository reference ` +
				`"ghcr.io/acme/app:eaea317" (must force) - container 9f2 is being used by it`,
			docker.ErrImageInUse,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &runner.Fake{Reply: []runner.Scripted{
				{Match: "image rm", Exit: 1, Stderr: tc.stderr},
			}}

			err := docker.On(r).RemoveImage(context.Background(), "ghcr.io/acme/app:0375fcf")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v; want it to classify as %v so a sweep can continue", err, tc.want)
			}
			// The reference has to survive into the message: a sweep reports
			// what it skipped, and "no such image" alone names nothing.
			if !strings.Contains(err.Error(), "0375fcf") {
				t.Errorf("err = %v; want the reference named", err)
			}
		})
	}
}

// TestRemoveImageReportsAnUnrecognisedFailureAsItself. A disk error, a dead
// daemon or an unreachable host must not be flattened into one of the two
// benign outcomes, or a sweep would skip past a machine that is broken.
func TestRemoveImageReportsAnUnrecognisedFailureAsItself(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{
		{Match: "image rm", Exit: 1, Stderr: "Cannot connect to the Docker daemon"},
	}}

	err := docker.On(r).RemoveImage(context.Background(), "ghcr.io/acme/app:0375fcf")
	if err == nil {
		t.Fatal("a dead daemon was reported as a successful removal")
	}
	for _, benign := range []error{docker.ErrNoSuchImage, docker.ErrImageInUse} {
		if errors.Is(err, benign) {
			t.Errorf("an unrecognised failure was classified as %v", benign)
		}
	}
}

// TestRemoveImageNeverForces is the invariant that makes a retention sweep
// safe to run unattended: docker's refusal to delete an image a container
// holds is the only thing standing between the sweep and the release currently
// serving.
func TestRemoveImageNeverForces(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}

	if err := docker.On(r).RemoveImage(context.Background(), "ghcr.io/acme/app:0375fcf"); err != nil {
		t.Fatal(err)
	}
	issued := strings.Join(r.Commands(), " ")
	for _, forced := range []string{"--force", " -f"} {
		if strings.Contains(issued, forced) {
			t.Errorf("the removal forces: %s", issued)
		}
	}
	if !strings.Contains(issued, "image rm ghcr.io/acme/app:0375fcf") {
		t.Errorf("unexpected command: %s", issued)
	}
}

// TestRemoveImageEscapeHatch. A caller that genuinely wants --force owns the
// consequence, and must not have to leave the package to get it.
func TestRemoveImageEscapeHatch(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}

	if err := docker.On(r).RemoveImage(context.Background(), "app:old", "--force"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Commands(), " "), "--force") {
		t.Errorf("the flag never reached docker: %v", r.Commands())
	}
}
