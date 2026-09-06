package common_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/secret"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// TestKeyValuesCoversEveryKey pins the canonical list against reality, so a key
// added without registering it fails here rather than silently escaping
// documentation and tooling.
func TestKeyValuesCoversEveryKey(t *testing.T) {
	t.Parallel()
	declared := map[pipeline.Key]bool{}
	for _, k := range common.KeyValues {
		declared[k] = true
	}
	for _, k := range []pipeline.Key{
		common.KeyCommit, common.KeyEnv, common.KeyImage, common.KeyContainers,
		common.KeyRouted,
	} {
		if !declared[k] {
			t.Errorf("%s is not in KeyValues", k)
		}
	}
}

// ── ResolveEnv ───────────────────────────────────────────────────────────

// TestResolveEnvFailsEarlyOnAMissingCredential pins why the check lives here:
// a deploy that discovers a missing credential after pushing an image has
// already spent minutes and left an artifact behind.
func TestResolveEnvFailsEarlyOnAMissingCredential(t *testing.T) {
	t.Setenv("LATH_STEPS_PRESENT", "value")

	err := common.ResolveEnv{
		Environment: "prod",
		Secrets:     []string{"LATH_STEPS_PRESENT", "LATH_STEPS_ABSENT_A", "LATH_STEPS_ABSENT_B"},
	}.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))

	if err == nil {
		t.Fatal("a missing credential was accepted")
	}
	// Every missing name, not just the first: otherwise configuring a deploy is
	// one round trip per absent variable.
	for _, want := range []string{"LATH_STEPS_ABSENT_A", "LATH_STEPS_ABSENT_B", "prod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to mention %q", err, want)
		}
	}
}

// TestResolveEnvChecksUnderDryRunToo pins that a rehearsal cannot report a
// deploy as viable when the first real attempt would fail on configuration.
func TestResolveEnvChecksUnderDryRunToo(t *testing.T) {
	err := common.ResolveEnv{
		Environment: "prod", Secrets: []string{"LATH_STEPS_DEFINITELY_ABSENT"},
	}.Run(context.Background(), pipeline.NewState(pipeline.ModeDryRun, nil))
	if err == nil {
		t.Error("a dry run skipped the credential check")
	}
}

func TestResolveEnvProvidesTheName(t *testing.T) {
	t.Parallel()
	s := pipeline.NewState(pipeline.ModeExecute, nil)
	if err := (common.ResolveEnv{Environment: "staging"}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.Get[string](s, common.KeyEnv)
	if err != nil || got != "staging" {
		t.Errorf("KeyEnv = %q, %v", got, err)
	}
	if err := (common.ResolveEnv{}).Validate(); err == nil {
		t.Error("an empty Environment was accepted")
	}
}

// ── PutFile ──────────────────────────────────────────────────────────────

// TestPutFileNeverLogsTheContent pins that a step routinely carrying
// credentials reports a byte count and nothing else.
func TestPutFileNeverLogsTheContent(t *testing.T) {
	t.Parallel()
	const credential = "ghp_must_not_be_logged"
	var log strings.Builder
	s := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&log))

	f := &runner.Fake{}
	if err := (common.PutFile{
		Path: "/srv/.env", Secret: secret.New(credential), Runner: f,
	}).Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), credential) {
		t.Errorf("the credential was logged: %s", log.String())
	}
	if !strings.Contains(log.String(), "bytes") {
		t.Errorf("no byte count reported: %s", log.String())
	}
}

// TestPutFileSecretImpliesOwnerOnly pins that a credential cannot be written
// world-readable by forgetting the Sensitive flag.
func TestPutFileSecretImpliesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "creds")

	if err := (common.PutFile{Path: path, Secret: secret.New("token")}).
		Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v; a Secret must imply owner-only", perm)
	}
}

func TestPutFileDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "never")
	if err := (common.PutFile{Path: path, Content: "x"}).
		Run(context.Background(), pipeline.NewState(pipeline.ModeDryRun, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("a dry run created the file")
	}
}

func TestPutFileValidate(t *testing.T) {
	t.Parallel()
	if err := (common.PutFile{}).Validate(); err == nil {
		t.Error("an empty Path was accepted")
	}
	if err := (common.PutFile{Path: "/x"}).Validate(); err != nil {
		t.Errorf("a valid configuration was rejected: %v", err)
	}
}

// ── EnsureDir ────────────────────────────────────────────────────────────

func TestEnsureDirCreates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	paths := []string{filepath.Join(root, "logs"), filepath.Join(root, "a", "b")}

	if err := (common.EnsureDir{Paths: paths}).
		Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil)); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Errorf("%s not created: %v", p, err)
		}
	}
	if err := (common.EnsureDir{}).Validate(); err == nil {
		t.Error("an empty Paths was accepted")
	}
}

func TestEnsureDirDryRunCreatesNothing(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "never")
	if err := (common.EnsureDir{Paths: []string{p}}).
		Run(context.Background(), pipeline.NewState(pipeline.ModeDryRun, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Error("a dry run created the directory")
	}
}

// ── failures surface ─────────────────────────────────────────────────────

// TestStepsFailWhenTheirRunnerDoes pins the honesty guarantee for this
// package's own steps: a step that cannot reach its runner must FAIL, not
// report success.
func TestStepsFailWhenTheirRunnerDoes(t *testing.T) {
	t.Parallel()
	boom := errors.New("the runner is unreachable")

	for _, tc := range []struct {
		name string
		step pipeline.Step
	}{
		{"put-file", common.PutFile{Path: "/x", Content: "y", Runner: &runner.Fake{Fail: boom}}},
		{"ensure-dirs", common.EnsureDir{Paths: []string{"/x"}, Runner: &runner.Fake{Fail: boom}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.step.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
			if err == nil {
				t.Fatalf("%s reported success while its runner was failing", tc.name)
			}
			if !errors.Is(err, boom) {
				t.Errorf("err = %v; want the cause to stay unwrappable", err)
			}
		})
	}
}

// TestPutFileOwnerComposesWithEveryModeSource is why PutFile resolves the mode
// itself instead of branching on which remotefs call to make: an earlier shape
// would have dropped Owner on the Sensitive/Secret paths, which are exactly
// the files that need it. Each case asserts the mode still holds.
func TestPutFileOwnerComposesWithEveryModeSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	t.Parallel()
	cases := []struct {
		name string
		file common.PutFile
		want os.FileMode
	}{
		{"secret", common.PutFile{Secret: secret.New("token")}, 0o600},
		{"sensitive", common.PutFile{Content: "x", Sensitive: true}, 0o600},
		{"plain", common.PutFile{Content: "x"}, 0o644},
		{"explicit mode", common.PutFile{Content: "x", Mode: 0o640}, 0o640},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := tc.file
			f.Path = filepath.Join(t.TempDir(), "file")
			// An owner this user cannot grant: the write must still succeed,
			// which is the best-effort contract remotefs documents.
			f.Owner = "0:0"
			if err := f.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil)); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(f.Path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != tc.want {
				t.Errorf("mode = %04o, want %04o", perm, tc.want)
			}
		})
	}
}
