package pipeline_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

// TestPlanModeListsAndRunsNothing is the whole feature: a reader asking what a
// target would do gets an answer without the author having written a plan
// target, and without anything happening.
func TestPlanModeListsAndRunsNothing(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	var log []string
	p := threeSteps(&log)

	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(&out),
		pipeline.PlanWriter(&out))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(log) != 0 {
		t.Errorf("plan mode ran %v", log)
	}
	for _, want := range []string{"one", "two", "three", "3 step(s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("listing omits %q:\n%s", want, out.String())
		}
	}
}

// TestPlanModeStillValidates. A plan that describes a pipeline which could
// never run is worse than an error: it is a confident wrong answer.
func TestPlanModeStillValidates(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		needs{name: "consumer", key: "never-produced"},
	}}
	err := p.Run(context.Background(), pipeline.NewState(pipeline.ModeExecute, nil))
	if err == nil {
		t.Fatal("an unsatisfiable pipeline was described as if it would work")
	}
}

// TestForcedDryRunOverridesTheDefinition. The author asked to execute; the
// runner said rehearse. The runner wins, which is what makes `lath dry-run`
// work on a definition that offers no such flag.
func TestForcedDryRunOverridesTheDefinition(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvDryRun)
	st := pipeline.NewState(pipeline.ModeExecute, nil)
	if !st.DryRun() {
		t.Error("an execute survived a forced dry run")
	}
}

// TestForcingOnlyEverMakesARunSafer. A runner flag that could turn someone's
// rehearsal into a real deploy would be a footgun with the safety filed off.
func TestForcingOnlyEverMakesARunSafer(t *testing.T) {
	for _, mode := range []string{pipeline.ModeEnvPlan, pipeline.ModeEnvDryRun, "", "nonsense"} {
		t.Setenv(pipeline.ModeEnvVar, mode)
		st := pipeline.NewState(pipeline.ModeDryRun, nil)
		if !st.DryRun() {
			t.Errorf("%s=%q turned a dry run into an execute", pipeline.ModeEnvVar, mode)
		}
	}
	// And an unrecognised value must not silently disarm anything.
	t.Setenv(pipeline.ModeEnvVar, "nonsense")
	if st := pipeline.NewState(pipeline.ModeExecute, nil); st.DryRun() {
		t.Error("an unrecognised mode suppressed a real run")
	}
	if pipeline.Planning() {
		t.Error("an unrecognised mode was treated as planning")
	}
}

// TestPlanningIsWhatADefinitionMayRead. Everything else about the mode is the
// runner's business; this is the one question a definition needs answered, so
// it can skip credential checks it would otherwise refuse to assemble without.
func TestPlanningIsWhatADefinitionMayRead(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	if !pipeline.Planning() {
		t.Error("Planning() is false in plan mode")
	}
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvDryRun)
	if pipeline.Planning() {
		t.Error("a dry run is not a plan: its steps still execute their logic")
	}
}

// needs is a step requiring a key nothing provides.
type needs struct {
	name string
	key  pipeline.Key
}

func (n needs) Name() string                             { return n.name }
func (n needs) Requires() []pipeline.Key                 { return []pipeline.Key{n.key} }
func (needs) Provides() []pipeline.Key                   { return nil }
func (needs) Run(context.Context, *pipeline.State) error { return nil }

// TestPlanShowsBothWhatAStepIsForAndWhatItWillDo. Two different questions: a
// name says what kind of thing happens, a summary says what it is for the way
// a CLI command's help does, and a detail says what will happen this time.
func TestPlanShowsBothWhatAStepIsForAndWhatItWillDo(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	const detail = "cloudflare: A api.example.com -> 203.0.113.7"
	const summary = "point this environment's hostname at the box"
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		pipeline.Func{
			Label:   "ensure-dns",
			Summary: summary,
			Detail:  detail,
			Do:      func(context.Context, *pipeline.State) error { return nil },
		},
	}}

	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.PlanWriter(&out))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{summary, detail} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("listing omits %q:\n%s", want, out.String())
		}
	}
	// And both reach Plan(), which is what a client, or a debugger's panel,
	// reads rather than the printed text.
	entry := p.Plan()[0]
	if entry.Summary != summary || entry.Detail != detail {
		t.Errorf("PlanEntry = %+v", entry)
	}
}

// TestAStepNeedNotDescribeItself. Describer is optional: the great majority of
// steps are fully explained by their name plus the keys they read and write.
func TestAStepNeedNotDescribeItself(t *testing.T) {
	t.Parallel()
	var log []string
	p := threeSteps(&log)
	for _, e := range p.Plan() {
		if e.Detail != "" || e.Summary != "" {
			t.Errorf("%s invented documentation: %+v", e.Name, e)
		}
	}
}

// TestPlanJSONCarriesTheSameAnswerAsTheListing. A script and a reader must be
// looking at one answer, not two renderings that can drift.
func TestPlanJSONCarriesTheSameAnswerAsTheListing(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	t.Setenv(pipeline.PlanFormatEnvVar, pipeline.PlanFormatJSON)
	p := pipeline.Pipeline{Name: "deploy-prod", Steps: []pipeline.Step{
		pipeline.Func{
			Label: "ensure-dns", Summary: "point the hostname at the box",
			Detail: "cloudflare: A api.example.com -> 203.0.113.7",
			Do:     func(context.Context, *pipeline.State) error { return nil },
		},
	}}

	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.PlanWriter(&out))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Pipeline string               `json:"pipeline"`
		Steps    []pipeline.PlanEntry `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if got.Pipeline != "deploy-prod" {
		t.Errorf("pipeline = %q", got.Pipeline)
	}
	want := p.Plan()
	if len(got.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(got.Steps), len(want))
	}
	if !reflect.DeepEqual(got.Steps[0], want[0]) {
		t.Errorf("JSON step = %+v, Plan() step = %+v", got.Steps[0], want[0])
	}
}

// TestPlanJSONIsTheWholeOfStdout is what makes `| jq` work: a stray line of
// human text turns a machine format into a parse error.
func TestPlanJSONIsTheWholeOfStdout(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	t.Setenv(pipeline.PlanFormatEnvVar, pipeline.PlanFormatJSON)
	var log []string
	p := threeSteps(&log)

	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.PlanWriter(&out))
	if err := p.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("output does not start with JSON:\n%s", out.String())
	}
	var any map[string]any
	if err := json.Unmarshal([]byte(out.String()), &any); err != nil {
		t.Errorf("output is not parseable as one document: %v", err)
	}
}

// TestAnUnknownFormatStaysReadable. A typo in the format must not produce
// silence, or a caller sees an empty plan and concludes there are no steps.
func TestAnUnknownFormatStaysReadable(t *testing.T) {
	t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
	t.Setenv(pipeline.PlanFormatEnvVar, "yaml-please")
	var log []string
	var out strings.Builder
	st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.PlanWriter(&out))
	if err := threeSteps(&log).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "one") {
		t.Errorf("an unknown format printed nothing useful:\n%s", out.String())
	}
}

// TestPlanJSONGoesToStdoutByDefault pins the half of the routing the other
// tests cannot see, because they pass an explicit writer: JSON must land where
// a pipe reads, and the human listing must not.
//
// Worth the awkwardness of swapping os.Stdout: "it goes to stdout" was
// otherwise a claim in a comment, and the one that breaks `| jq` when wrong.
func TestPlanJSONGoesToStdoutByDefault(t *testing.T) {
	capture := func(format string) (stdout, stderr string) {
		t.Helper()
		outR, outW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		realOut, realErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = outW, errW
		defer func() { os.Stdout, os.Stderr = realOut, realErr }()

		t.Setenv(pipeline.ModeEnvVar, pipeline.ModeEnvPlan)
		t.Setenv(pipeline.PlanFormatEnvVar, format)
		var log []string
		if err := threeSteps(&log).Run(context.Background(),
			pipeline.NewState(pipeline.ModeExecute, nil)); err != nil {
			t.Fatal(err)
		}

		_ = outW.Close()
		_ = errW.Close()
		o, _ := io.ReadAll(outR)
		e, _ := io.ReadAll(errR)
		return string(o), string(e)
	}

	stdout, stderr := capture(pipeline.PlanFormatJSON)
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("JSON did not go to stdout; stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(stderr, "{") {
		t.Errorf("JSON also went to stderr: %q", stderr)
	}

	stdout, stderr = capture(pipeline.PlanFormatText)
	if !strings.Contains(stderr, "one") {
		t.Errorf("the human listing did not go to stderr: %q", stderr)
	}
	if strings.Contains(stdout, "one") {
		t.Errorf("the human listing went to stdout, where it would mix with a definition's output: %q", stdout)
	}
}
