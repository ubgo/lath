package main

import (
	"strings"
	"testing"
)

// TestRejectedFunctionsAreReportedAlongsideUsableOnes pins the regression that
// a mistyped signature vanishes without a word.
//
// Found in real use: a namespace's three targets all declared `args []string`
// instead of `args ...string`. Every one was rejected, the namespace was
// dropped for having no usable targets, and `lath list` showed the definition's
// OTHER targets as though nothing were missing. The rejection message existed
// but was only reachable when the definition had no usable targets at all ,
// exactly the case where the author already knows something is wrong.
func TestRejectedFunctionsAreReportedAlongsideUsableOnes(t *testing.T) {
	dir := writeDef(t, map[string]string{
		"main.go": `package main

// Good is dispatchable.
func Good() error { return nil }

// BadSlice takes a slice where a variadic is required.
func BadSlice(args []string) error { return nil }

// BadReturn returns something other than error.
func BadReturn() string { return "" }
`})

	targets, rejected, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Func != "Good" {
		t.Fatalf("targets = %+v; want only Good", targets)
	}
	// The point of the test: usable targets exist, and the rejects are STILL
	// reported.
	if len(rejected) != 2 {
		t.Fatalf("rejected = %v; want both unusable functions", rejected)
	}
	joined := strings.Join(rejected, "\n")
	for _, name := range []string{"BadSlice", "BadReturn"} {
		if !strings.Contains(joined, name) {
			t.Errorf("rejected does not name %s: %v", name, rejected)
		}
	}
	if strings.Contains(joined, "Good") {
		t.Errorf("a usable target was reported as rejected: %v", rejected)
	}
}

// TestRejectedInAnEmptyNamespaceIsReported pins the harder half: a namespace
// whose functions are ALL unusable is dropped entirely, so without this its
// directory leaves no trace in any output.
func TestRejectedInAnEmptyNamespaceIsReported(t *testing.T) {
	dir := writeNS(t,
		map[string]string{"main.go": "package main\n\n// Good is dispatchable.\nfunc Good() error { return nil }\n"},
		map[string]map[string]string{
			"secrets": {"s.go": "package secrets\n\n// Push has an undispatchable shape.\nfunc Push(args []string) error { return nil }\n"},
		})

	targets, rejected, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v; want only the root target", targets)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0], "Push") {
		t.Errorf("rejected = %v; want the dropped namespace's function named", rejected)
	}
}

// TestWarnRejectedIsQuietWhenNothingWasRejected pins that the clean case adds
// no noise. A warning that fires on every run stops being read.
func TestWarnRejectedIsQuietWhenNothingWasRejected(t *testing.T) {
	dir := writeDef(t, map[string]string{
		"main.go": "package main\n\n// Good is dispatchable.\nfunc Good() error { return nil }\n",
	})
	_, rejected, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Errorf("rejected = %v; want none", rejected)
	}
}
