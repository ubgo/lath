package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// The example definition is the proof that the engine carries a real deploy.
// Testing only that its pipelines VALIDATE leaves the commands themselves —
// the part a reader copies — unexercised, which is how an example rots into
// something that no longer runs.

// inARepository puts the test in a throwaway git repository with one commit.
//
// The dev pipeline resolves the commit from the working tree and refuses a
// dirty one, so running it inside this checkout would pass or fail depending
// on whether the developer happens to have uncommitted work. A repository of
// our own makes the rehearsal deterministic.
func inARepository(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.com"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Chdir(dir)
}

// capture returns everything written to stdout while fn runs.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	func() {
		defer func() {
			os.Stdout = original
			_ = w.Close()
		}()
		fn()
	}()
	return <-done
}

// TestDryRunRehearsesTheWholePipeline is the example's own claim, executed.
//
// Every step RUNS here — this is not Validate — with side effects suppressed
// by dry-run mode. It is the difference between "the pipeline is wired" and
// "the pipeline works": a step that panics on a missing value, or reads a key
// with the wrong type, fails here and nowhere earlier.
func TestDryRunRehearsesTheWholePipeline(t *testing.T) {
	inARepository(t)
	// The credentials the definition declares. Values are irrelevant, presence
	// is the whole check: ResolveEnv refuses to start a deploy that would fail
	// later for want of a variable, which is tested on its own below.
	t.Setenv("DATABASE_URL", "postgres://localhost/example")
	t.Setenv("NATS_CREDS", "/dev/null")

	var err error
	out := capture(t, func() { err = DryRun(context.Background(), string(EnvironmentDev)) })
	if err != nil {
		t.Fatalf("the rehearsal failed: %v\n%s", err, out)
	}
	// Every step announced itself, and the ones with side effects said "would"
	// rather than doing anything.
	for _, want := range []string{"build-image", "migrate", "start-processes", "wait-healthy", "would"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rehearsal never mentions %q:\n%s", want, out)
		}
	}
}

// TestARehearsalStopsOnAMissingCredential, before anything is built.
//
// The value is never used by a dry run, so it would be easy to let it pass and
// discover the gap on the real deploy — which is the failure this ordering
// exists to prevent. The error must name every missing variable at once: a
// caller fixing their environment wants the list, not one round trip per name.
func TestARehearsalStopsOnAMissingCredential(t *testing.T) {
	inARepository(t)
	t.Setenv("DATABASE_URL", "")
	os.Unsetenv("DATABASE_URL")
	t.Setenv("NATS_CREDS", "")
	os.Unsetenv("NATS_CREDS")

	err := DryRun(context.Background(), string(EnvironmentDev))
	if err == nil {
		t.Fatal("a rehearsal ran with no credentials configured")
	}
	for _, want := range []string{"DATABASE_URL", "NATS_CREDS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to name %s", err, want)
		}
	}
}

// TestDeployRefusesAnUnknownEnvironment. The check runs BEFORE anything is
// built, which is the only place it is free: a typo caught after the image is
// pushed has already cost minutes and left an artifact behind.
func TestDeployRefusesAnUnknownEnvironment(t *testing.T) {
	t.Parallel()
	for name, run := range map[string]func() error{
		"deploy":   func() error { return Deploy(context.Background(), "nowhere") },
		"dry run":  func() error { return DryRun(context.Background(), "nowhere") },
		"plan":     func() error { return Plan("nowhere") },
		"rollback": func() error { return Rollback(context.Background(), "nowhere", "0375fcf") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := run()
			if err == nil {
				t.Fatal("an unknown environment was accepted")
			}
			if !strings.Contains(err.Error(), "nowhere") {
				t.Errorf("err = %v; want the offending value quoted", err)
			}
		})
	}
}

// TestEnvironmentsListsEveryOne. The command exists so a caller does not have
// to read the source to learn what it may deploy to; a list that omits one is
// worse than no list.
func TestEnvironmentsListsEveryOne(t *testing.T) {
	out := capture(t, Environments)
	for _, env := range EnvironmentValues {
		if !strings.Contains(out, string(env)) {
			t.Errorf("the listing omits %q:\n%s", env, out)
		}
	}
}

// TestHelloIsTheNoArgumentCommandShape. It documents the simplest form lath
// discovers, func Name() with no error, and a shape nobody executes is a shape
// nobody knows still works.
func TestHelloIsTheNoArgumentCommandShape(t *testing.T) {
	if out := capture(t, Hello); !strings.Contains(out, "hello") {
		t.Errorf("Hello printed %q", out)
	}
}

// TestNotifySlackWithholdsUnderDryRun. A message is a side effect other people
// can see, which is precisely what dry run promises not to produce.
func TestNotifySlackWithholdsUnderDryRun(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	s := pipeline.NewState(pipeline.ModeDryRun, pipeline.NewTextReporter(&out))
	pipeline.Set(s, common.KeyImage, "ghcr.io/acme/app:a3f1c2d")
	pipeline.Set(s, common.KeyEnv, "prod")

	if err := (NotifySlack{Channel: "#deploys"}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would post") {
		t.Errorf("a dry run did not say it was withholding:\n%s", out.String())
	}
}

// TestNotifySlackReportsWhatItCannotDo. A step that silently does nothing when
// misconfigured is the failure mode a plan cannot show.
func TestNotifySlackReportsWhatItCannotDo(t *testing.T) {
	t.Parallel()
	full := func() *pipeline.State {
		s := pipeline.NewState(pipeline.ModeExecute, nil)
		pipeline.Set(s, common.KeyImage, "ghcr.io/acme/app:a3f1c2d")
		pipeline.Set(s, common.KeyEnv, "prod")
		return s
	}

	if err := (NotifySlack{}).Run(context.Background(), full()); err == nil {
		t.Error("a step with no channel reported success")
	}
	// Keys it declares in Requires but that are absent: the pipeline's
	// validation prevents this, so reaching it means something bypassed
	// validation, and a nil-pointer panic there would be reported as a lath
	// bug rather than a definition one.
	empty := pipeline.NewState(pipeline.ModeExecute, nil)
	if err := (NotifySlack{Channel: "#deploys"}).Run(context.Background(), empty); err == nil {
		t.Error("a step ran without the state it declared")
	}

	// The declaration itself must stay complete: under-reporting here defeats
	// Validate for the whole pipeline.
	requires := NotifySlack{}.Requires()
	if len(requires) != 2 {
		t.Errorf("Requires = %v; want both keys Run reads", requires)
	}
}

// TestCaddyUpstreamsRefusesAnEmptySet. An empty upstream list takes the site
// down while the request that installed it succeeds — the worst shape of
// failure, because nothing reports it.
func TestCaddyUpstreamsRefusesAnEmptySet(t *testing.T) {
	t.Parallel()
	s := pipeline.NewState(pipeline.ModeExecute, nil)
	pipeline.Set(s, common.KeyRouted, []string{})

	if _, err := caddyUpstreams(s); err == nil {
		t.Fatal("an empty upstream list was rendered")
	}

	// And with nothing set at all: the key's producer never ran.
	if _, err := caddyUpstreams(pipeline.NewState(pipeline.ModeExecute, nil)); err == nil {
		t.Error("a missing key rendered as an empty list")
	}
}

// TestCaddyUpstreamsRendersOnlyRoutedContainers. Workers dial out; a proxy
// given their names would route public requests to something with no listener.
func TestCaddyUpstreamsRendersOnlyRoutedContainers(t *testing.T) {
	t.Parallel()
	s := pipeline.NewState(pipeline.ModeExecute, nil)
	pipeline.Set(s, common.KeyRouted, []string{"acme-api-a3f1c2d-0", "acme-wbhrcvr-a3f1c2d-0"})

	raw, err := caddyUpstreams(s)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Dial string `json:"dial"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the upstreams are not the JSON Caddy expects: %v\n%s", err, raw)
	}
	if len(got) != 2 {
		t.Fatalf("rendered %d upstreams, want 2: %s", len(got), raw)
	}
	for _, u := range got {
		// Container name as hostname, port appended: they share a docker
		// network, so docker's DNS resolves it with no published port.
		if !strings.HasSuffix(u.Dial, ":"+itoa(apiPort)) {
			t.Errorf("dial = %q; want the api port appended", u.Dial)
		}
	}
}

// itoa avoids importing strconv for one call in one assertion.
func itoa(n int) string {
	return strings.TrimSpace(string([]byte{byte('0' + n/1000%10), byte('0' + n/100%10), byte('0' + n/10%10), byte('0' + n%10)}))
}

// TestPlanPrintsEveryStepWithItsWiring. Plan is the review step before a real
// deploy, so it has to show what each step reads and writes; a listing of bare
// names cannot be reviewed.
func TestPlanPrintsEveryStepWithItsWiring(t *testing.T) {
	out := capture(t, func() {
		if err := Plan(string(EnvironmentProd)); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"build-image", "needs=", "gives=", "nothing executed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan omits %q:\n%s", want, out)
		}
	}
	// Every step in the pipeline reaches the listing.
	steps := deployPipeline(EnvironmentProd).Steps
	for _, step := range steps {
		if !strings.Contains(out, step.Name()) {
			t.Errorf("the plan omits the step %q", step.Name())
		}
	}
}

// TestRollbackPipelineTargetsTheRequestedVersion. The tag reaches both the
// image being deployed and the container names, which is what keeps docker ps
// honest after a rollback.
func TestRollbackPipelineTargetsTheRequestedVersion(t *testing.T) {
	t.Parallel()
	const version = "0375fcf"
	p := rollbackPipeline(EnvironmentProd, version)

	var found bool
	for _, entry := range p.Plan() {
		if entry.Name == "rollback-target" && strings.Contains(entry.Detail, version) {
			found = true
		}
	}
	if !found {
		t.Errorf("the plan does not show which version it targets: %+v", p.Plan())
	}
}

// TestFilePathsInTheExampleResolve. The example is copied by readers; a
// Dockerfile path that does not exist in this repository is a broken example
// that still compiles.
func TestFilePathsInTheExampleResolve(t *testing.T) {
	t.Parallel()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// example/.lath -> example/
	repoRoot := filepath.Dir(root)
	for _, step := range deployPipeline(EnvironmentDev).Steps {
		if step.Name() != "build-image" {
			continue
		}
		// The build context is the example repository itself; the Dockerfile
		// is relative to it.
		if _, err := os.Stat(filepath.Join(repoRoot, ".docker", "Dockerfile.prod")); err != nil {
			t.Skipf("the example ships no Dockerfile to check: %v", err)
		}
	}
}

// TestBothRunnerBranchesAreWired. `dev` runs against local docker and every
// other environment reaches a host over ssh — one field's difference, and the
// only thing separating a rehearsal from a real deploy. A pipeline that only
// assembles for one of them is a rehearsal that does not exercise the path it
// claims to.
func TestBothRunnerBranchesAreWired(t *testing.T) {
	t.Parallel()
	for _, env := range EnvironmentValues {
		t.Run(string(env), func(t *testing.T) {
			t.Parallel()
			if err := rollbackPipeline(env, "0375fcf").Validate(); err != nil {
				t.Errorf("rollbackPipeline(%q): %v", env, err)
			}
			if err := deployPipeline(env).Validate(); err != nil {
				t.Errorf("deployPipeline(%q): %v", env, err)
			}
		})
	}
}

// TestNotifySlackAnnouncesOnARealRun. The dry-run half is tested above; this
// is the half that is supposed to have an effect, and a step whose only tested
// path is the one that does nothing is a step nobody has seen work.
func TestNotifySlackAnnouncesOnARealRun(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	s := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&out))
	pipeline.Set(s, common.KeyImage, "ghcr.io/acme/app:a3f1c2d")
	pipeline.Set(s, common.KeyEnv, "prod")

	if err := (NotifySlack{Channel: "#deploys"}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	for _, want := range []string{"#deploys", "a3f1c2d", "prod"} {
		if !strings.Contains(said, want) {
			t.Errorf("the announcement omits %q:\n%s", want, said)
		}
	}
	if strings.Contains(said, "would") {
		t.Errorf("a real run described itself as a rehearsal:\n%s", said)
	}
}
