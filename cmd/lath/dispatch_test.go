package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

// TestRunHelpAndVersion covers the verbs that need no definition directory.
func TestRunHelpAndVersion(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
		says string
	}{
		{"no arguments", nil, exitUsage, "usage"},
		{"help verb", []string{string(VerbHelp)}, exitOK, "usage"},
		{"help flag", []string{"--help"}, exitOK, "usage"},
		{"short help flag", []string{"-h"}, exitOK, "usage"},
		{"version", []string{string(VerbVersion)}, exitOK, "lath"},
		{"unknown verb", []string{"nonsense"}, exitUsage, "unknown command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			out := captureOutput(t, func() { code = run(tc.args) })
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
			if !strings.Contains(strings.ToLower(out), tc.says) {
				t.Errorf("output does not mention %q:\n%s", tc.says, out)
			}
		})
	}
}

// TestRunSuggestsOnATypo. The single most likely mistake is a target typed
// without `run`, and the second is a misspelled verb. Both deserve a pointer
// rather than a bare rejection.
func TestRunSuggestsOnATypo(t *testing.T) {
	out := captureOutput(t, func() { run([]string{"cach"}) })
	if !strings.Contains(out, string(VerbCache)) {
		t.Errorf("no suggestion for a near-miss verb:\n%s", out)
	}
	out = captureOutput(t, func() { run([]string{"deploy"}) })
	if !strings.Contains(out, string(VerbRun)+" deploy") {
		t.Errorf("a bare target name should suggest `lath run <target>`:\n%s", out)
	}
}

// TestRunVerbHelp covers `lath <verb> --help` for every verb, which must never
// be swallowed and must never be confused with a target's own --help.
func TestRunVerbHelp(t *testing.T) {
	for _, v := range VerbValues {
		var code int
		out := captureOutput(t, func() { code = run([]string{string(v), "--help"}) })
		if code != exitOK {
			t.Errorf("lath %s --help exited %d", v, code)
		}
		if !strings.Contains(out, "usage") {
			t.Errorf("lath %s --help printed no usage:\n%s", v, out)
		}
	}
}

// TestRunListWithoutADefinition. The message has to say what a definition IS
// and how to make one, because this is what a first-time user sees.
func TestRunListWithoutADefinition(t *testing.T) {
	t.Chdir(t.TempDir())
	var code int
	out := captureOutput(t, func() { code = run([]string{string(VerbList)}) })
	if code == exitOK {
		t.Error("listing targets with no definition reported success")
	}
	if !strings.Contains(out, definitionDir) || !strings.Contains(out, string(VerbInit)) {
		t.Errorf("message does not name the directory or how to create one:\n%s", out)
	}
}

// TestSessionKeySeparatesProjectsAndTargets is the naming rule behind debug
// sessions: without the project, two repositories collide; without the target,
// one project stepped through two pipelines at once collides.
func TestSessionKeySeparatesProjectsAndTargets(t *testing.T) {
	t.Parallel()
	a := sessionKey("/work/one", []string{"deploy", "local"})
	b := sessionKey("/work/two", []string{"deploy", "local"})
	c := sessionKey("/work/one", []string{"secrets", "push"})
	if a == b {
		t.Error("two projects running the same target share a key")
	}
	if a == c {
		t.Error("two targets in one project share a key")
	}
	if sessionKey("/work/one", []string{"deploy", "local"}) != a {
		t.Error("the key is not stable for identical input")
	}
}

// TestDebugPreflightRefusesWithoutATerminal is the promise that a deploy can
// never block on a keystroke nobody can send. Checked BEFORE anything is built.
func TestDebugPreflightRefusesWithoutATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	origIn, origOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = r, w
	defer func() { os.Stdin, os.Stdout = origIn, origOut }()

	err = debugPreflight()
	if err == nil {
		t.Fatal("accepted a debug session with no terminal on either stream")
	}
	if !strings.Contains(err.Error(), debugFlag) {
		t.Errorf("refusal does not name the flag: %v", err)
	}
}

// TestIsTerminal. A pipe is not a terminal, and a regular file is not either.
// Both are what CI, a redirect and a recording look like.
func TestIsTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) {
		t.Error("a pipe was reported as a terminal")
	}

	f, err := os.CreateTemp(t.TempDir(), "f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a regular file was reported as a terminal")
	}
}

// TestUsesPipelineReadsGoMod, whether debugger code is generated is decided
// by the definition's require list, because that is what answers "can code
// importing pipeline compile here".
func TestUsesPipelineReadsGoMod(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, mod string
		want      bool
	}{
		{"requires it", "module d\n\ngo 1.24\n\nrequire github.com/ubgo/lath/pipeline v0.0.0\n", true},
		{"grouped require", "module d\n\ngo 1.24\n\nrequire (\n\tgithub.com/ubgo/lath/pipeline v0.0.0\n)\n", true},
		{"does not", "module d\n\ngo 1.24\n", false},
		// A longer path that merely starts the same way is a PACKAGE, never a
		// module, and must not match.
		{"a package of it", "module d\n\ngo 1.24\n\nrequire github.com/ubgo/lath/pipeline/debug v0.0.0\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.mod), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := usesPipeline(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("usesPipeline = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUsesPipelineNoGoMod. A directory with no go.mod is not a definition,
// and must be reported as "no" rather than as an error.
func TestUsesPipelineNoGoMod(t *testing.T) {
	t.Parallel()
	got, err := usesPipeline(t.TempDir())
	if err != nil || got {
		t.Errorf("usesPipeline = %v, %v; want false, nil", got, err)
	}
}

// TestRunDispatchesEveryVerb walks the dispatch itself. handOff is the one
// path this cannot reach: it calls syscall.Exec, which REPLACES the process ,
// there is no "after" to assert in. `lath run` is therefore exercised through
// its components (discover, generate, build, cacheLookup) rather than here.
func TestRunDispatchesEveryVerb(t *testing.T) {
	isolateCache(t)
	t.Chdir(t.TempDir())

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"cache list on an empty cache", []string{string(VerbCache)}, exitOK},
		{"cache prune on an empty cache", []string{string(VerbCache), string(CacheActionPrune)}, exitOK},
		{"cache clean on an empty cache", []string{string(VerbCache), string(CacheActionClean)}, exitOK},
		{"cache with an unknown action", []string{string(VerbCache), "nonsense"}, exitUsage},
		{"list with no definition", []string{string(VerbList)}, exitUsage},
		{"run with no target named", []string{string(VerbRun)}, exitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			captureOutput(t, func() { code = run(tc.args) })
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestRunInitThenList is the first-run path end to end, through the dispatch:
// scaffold a definition, then list what it exposes.
func TestRunInitThenList(t *testing.T) {
	needsGo(t)
	useLocalEngine(t)
	isolateCache(t)
	t.Chdir(t.TempDir())

	var code int
	captureOutput(t, func() { code = run([]string{string(VerbInit)}) })
	if code != exitOK {
		t.Fatalf("init exited %d", code)
	}

	var out string
	out = captureOutput(t, func() { code = run([]string{string(VerbList)}) })
	if code != exitOK {
		t.Errorf("list exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "targets") {
		t.Errorf("list printed no targets after init:\n%s", out)
	}

	// A second init must refuse rather than overwrite real deploy logic.
	captureOutput(t, func() { code = run([]string{string(VerbInit)}) })
	if code != exitUsage {
		t.Errorf("a second init exited %d, want a refusal", code)
	}
}

// TestPlanAndDryRunSetTheModeForTheDefinition. The mode is inherited through
// the ENVIRONMENT, because the compiled definition is a separate process that
// lath hands over to. If the variable were not set here, `lath plan` would
// perform the deploy it was asked to describe, which is the most expensive
// possible failure of this command.
func TestPlanAndDryRunSetTheModeForTheDefinition(t *testing.T) {
	isolateCache(t)
	t.Chdir(t.TempDir())

	for _, tc := range []struct {
		verb Verb
		want string
	}{
		{VerbPlan, pipeline.ModeEnvPlan},
		{VerbDryRun, pipeline.ModeEnvDryRun},
	} {
		t.Run(string(tc.verb), func(t *testing.T) {
			// Restored by the framework: run() sets the real process
			// environment, and leaking it would put every later test in the
			// package into plan mode.
			t.Setenv(pipeline.ModeEnvVar, "")
			t.Setenv(pipeline.PlanFormatEnvVar, "")

			var code int
			// No target: the argument check happens AFTER the mode is set, so
			// this exercises the setting without needing a definition to
			// compile.
			captureOutput(t, func() { code = run([]string{string(tc.verb)}) })
			if code != exitUsage {
				t.Errorf("exit = %d, want a usage error when no target is named", code)
			}
			if got := os.Getenv(pipeline.ModeEnvVar); got != tc.want {
				t.Errorf("%s = %q, want %q", pipeline.ModeEnvVar, got, tc.want)
			}
			if got := os.Getenv(pipeline.PlanFormatEnvVar); got == pipeline.PlanFormatJSON {
				t.Error("the JSON format was selected without --json")
			}
		})
	}
}

// TestPlanJSONFlagIsTakenFromAnywhere. The flag formats lath's own listing, so
// lath owns it wherever it appears; a reader who types it last must not be
// told they typed it in the wrong place.
func TestPlanJSONFlagIsTakenFromAnywhere(t *testing.T) {
	isolateCache(t)
	t.Chdir(t.TempDir())
	t.Setenv(pipeline.ModeEnvVar, "")
	t.Setenv(pipeline.PlanFormatEnvVar, "")

	captureOutput(t, func() { run([]string{string(VerbPlan), jsonFlag}) })
	if got := os.Getenv(pipeline.PlanFormatEnvVar); got != pipeline.PlanFormatJSON {
		t.Errorf("%s = %q, want %q", pipeline.PlanFormatEnvVar, got, pipeline.PlanFormatJSON)
	}
}

// TestInitRefusesArguments. Where the engine resolves from is decided when
// lath is BUILT, so an argument here is a caller expecting a choice they do
// not have; accepting and ignoring it would scaffold a definition pointing
// somewhere they did not ask for.
func TestInitRefusesArguments(t *testing.T) {
	isolateCache(t)
	t.Chdir(t.TempDir())

	var code int
	out := captureOutput(t, func() { code = run([]string{string(VerbInit), "./somewhere"}) })
	if code != exitUsage {
		t.Errorf("exit = %d, want a usage error", code)
	}
	if !strings.Contains(out, "takes no arguments") {
		t.Errorf("output does not say why:\n%s", out)
	}
	// Nothing may be scaffolded by a refused command.
	if _, err := os.Stat(definitionDir); !os.IsNotExist(err) {
		t.Error("a refused init created a definition anyway")
	}
}

// TestCacheJSONIsMachineReadable. The flag exists for consumption by other
// programs, so an empty cache must still be valid JSON rather than prose
// saying there is nothing to show.
func TestCacheJSONIsMachineReadable(t *testing.T) {
	isolateCache(t)
	t.Chdir(t.TempDir())

	var code int
	out := captureOutput(t, func() { code = run([]string{string(VerbCache), jsonFlag}) })
	if code != exitOK {
		t.Fatalf("exit = %d:\n%s", code, out)
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Errorf("--json emitted something that is not JSON: %v\n%s", err, out)
	}
}

// TestListJSONWithoutADefinition. The failure has to arrive as an exit code
// and prose on stderr; emitting an empty JSON payload would tell a consumer
// there are no targets when in fact there is no definition.
func TestListJSONWithoutADefinition(t *testing.T) {
	isolateCache(t)
	t.Chdir(t.TempDir())

	var code int
	out := captureOutput(t, func() { code = run([]string{string(VerbList), jsonFlag}) })
	if code == exitOK {
		t.Errorf("listing an absent definition succeeded:\n%s", out)
	}
	if strings.TrimSpace(out) == "{}" {
		t.Error("an empty payload was emitted for a missing definition")
	}
}

// TestNamespaceWithNothingAfterItLists. An incomplete command shows what would
// complete it, one level down from `lath run` alone. The exit code differs by
// intent: bare is a usage error, --help is what the user asked for.
func TestNamespaceWithNothingAfterItLists(t *testing.T) {
	needsGo(t)
	useLocalEngine(t)
	isolateCache(t)
	t.Chdir(t.TempDir())

	var code int
	captureOutput(t, func() { code = run([]string{string(VerbInit)}) })
	if code != exitOK {
		t.Fatalf("init exited %d", code)
	}
	targets, _, err := discover(definitionDir)
	if err != nil {
		t.Fatal(err)
	}
	group := ""
	for _, tgt := range targets {
		if tgt.Namespace != "" {
			group = tgt.Namespace
			break
		}
	}
	if group == "" {
		t.Skip("the scaffold has no namespaced target to list")
	}

	out := captureOutput(t, func() { code = run([]string{string(VerbRun), group}) })
	if code != exitUsage {
		t.Errorf("a bare namespace exited %d, want a usage error", code)
	}
	if !strings.Contains(out, group) {
		t.Errorf("the listing does not name the namespace:\n%s", out)
	}

	out = captureOutput(t, func() { code = run([]string{string(VerbRun), group, "--help"}) })
	if code != exitOK {
		t.Errorf("an explicit --help exited %d, want success:\n%s", code, out)
	}
}

// TestVersionFallsBackToTheModuleVersion.
//
// `go install …@v0.1.0` cannot pass ldflags, so every installed copy reported
// itself as "dev" — and a tool that cannot say which version it is makes every
// bug report guesswork. Under `go test` the binary has no module version
// either, so what this pins is the CONTRACT: an ldflags-set version always
// wins, and the fallback never reports something meaningless.
func TestVersionFallsBackToTheModuleVersion(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = "v9.9.9"
	if got := buildVersion(); got != "v9.9.9" {
		t.Errorf("buildVersion() = %q; an explicit ldflags version must win", got)
	}

	version = "dev"
	if got := buildVersion(); got == "" || got == "(devel)" {
		t.Errorf("buildVersion() = %q; the fallback must never report an empty "+
			"or meaningless version", got)
	}
}
