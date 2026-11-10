package argv_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/argv"
)

var known = []string{"--apply", "--allow-prod", "--prebuilt", "--tag="}

// TestAMisspeltFlagIsAnError is the reason the package exists: read with
// slices.Contains, "--prebuit" is simply absent, and the deploy takes the path
// the flag was meant to avoid without a word.
func TestAMisspeltFlagIsAnError(t *testing.T) {
	t.Parallel()
	_, err := argv.Parse([]string{"prod", "--prebuit"}, known...)
	if !errors.Is(err, argv.ErrUnknownFlag) {
		t.Fatalf("err = %v; want ErrUnknownFlag", err)
	}
	// The message names the probable intent and every accepted flag, so the
	// user can fix the command without reading the target's source.
	for _, want := range []string{`"--prebuit"`, `did you mean "--prebuilt"`, "--apply, --allow-prod, --prebuilt, --tag"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// TestAFarOffFlagGetsNoSuggestion pins the distance bound: a suggestion for a
// flag the user was not thinking of sends them the wrong way.
func TestAFarOffFlagGetsNoSuggestion(t *testing.T) {
	t.Parallel()
	_, err := argv.Parse([]string{"--verbose"}, known...)
	if !errors.Is(err, argv.ErrUnknownFlag) {
		t.Fatalf("err = %v; want ErrUnknownFlag", err)
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("suggested a flag for an unrelated one: %v", err)
	}
}

// TestPositionalsAndFlagsAreSeparated pins the split and its order: the
// environment a target reads first must be the first positional, wherever
// the flags sit.
func TestPositionalsAndFlagsAreSeparated(t *testing.T) {
	t.Parallel()
	a, err := argv.Parse([]string{"--apply", "prod", "-", "--tag=abc1234", "extra"}, known...)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.Positional(), []string{"prod", "-", "extra"}; !slices.Equal(got, want) {
		t.Errorf("Positional = %v, want %v (a bare - is stdin, not a flag)", got, want)
	}
	if !a.Has("--apply") || a.Has("--prebuilt") {
		t.Errorf("Has: --apply=%v --prebuilt=%v", a.Has("--apply"), a.Has("--prebuilt"))
	}
	if v, ok := a.Value("--tag"); !ok || v != "abc1234" {
		t.Errorf("Value(--tag) = %q, %v", v, ok)
	}

}

// TestEverythingAfterDoubleDashIsPositional pins the escape for a positional
// that starts with a dash, which would otherwise be refused as a flag.
func TestEverythingAfterDoubleDashIsPositional(t *testing.T) {
	t.Parallel()
	a, err := argv.Parse([]string{"--apply", "--", "--not-a-flag", "x"}, known...)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.Positional(), []string{"--not-a-flag", "x"}; !slices.Equal(got, want) {
		t.Errorf("Positional = %v, want %v", got, want)
	}
}

// TestNoKnownFlagsRefusesEveryFlag pins the case of a target that takes none:
// any flag is a mistake, and the message says none are accepted rather than
// trailing off after a colon.
func TestNoKnownFlagsRefusesEveryFlag(t *testing.T) {
	t.Parallel()
	_, err := argv.Parse([]string{"--apply"})
	if !errors.Is(err, argv.ErrUnknownFlag) || !strings.Contains(err.Error(), "accepted: none") {
		t.Errorf("err = %v", err)
	}
}

// TestValuesAreDeclaredAndEnforced pins the two misreadings a schema-free
// parser makes: a value given as the next argument becomes a positional (and
// the environment), and a value on a switch is ignored (so --apply=false
// applies).
func TestValuesAreDeclaredAndEnforced(t *testing.T) {
	t.Parallel()
	for args, want := range map[string]string{
		"--tag abc1234 prod": "--tag takes a value",
		"--apply=false":      "--apply takes no value",
	} {
		_, err := argv.Parse(strings.Fields(args), known...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%s) err = %v; want %q", args, err, want)
		}
	}
}

// TestAskingAboutAnUndeclaredFlagPanics pins that the target's own typo is
// caught: Has("--prebuit") answering false is the silent bug moved one file
// over.
func TestAskingAboutAnUndeclaredFlagPanics(t *testing.T) {
	t.Parallel()
	a, err := argv.Parse(nil, known...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Error("Has on an undeclared flag did not panic")
		}
	}()
	a.Has("--prebuit")
}

// TestAMalformedKnownFlagIsTheTargetsBug pins that a known entry which could
// never match is reported, not silently ignored: "apply" without dashes would
// make --apply unusable and look like the user's fault.
func TestAMalformedKnownFlagIsTheTargetsBug(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"apply", "-", "--tag=x", "-="} {
		if _, err := argv.Parse(nil, bad); err == nil || errors.Is(err, argv.ErrUnknownFlag) {
			t.Errorf("Parse(known %q) err = %v; want a configuration error", bad, err)
		}
	}
}

// TestPositionalIsACopy pins that a caller editing the result cannot change
// what a second call returns.
func TestPositionalIsACopy(t *testing.T) {
	t.Parallel()
	a, err := argv.Parse([]string{"prod"})
	if err != nil {
		t.Fatal(err)
	}
	a.Positional()[0] = "staging"
	if a.Positional()[0] != "prod" {
		t.Error("Positional shares its backing array with the caller")
	}
}
