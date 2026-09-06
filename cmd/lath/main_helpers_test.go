package main

import (
	"strings"
	"testing"
)

// TestEditDistance pins the ranking function behind command suggestions.
//
// Not a general-purpose Levenshtein: it exists only to decide which verb a
// typo most resembles, so what matters is that it is symmetric, zero for
// identical input, and counts each single-character edit once.
func TestEditDistance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"run", "run", 0},
		{"", "", 0},
		{"", "run", 3},
		{"run", "", 3},
		{"rnu", "run", 2}, // two substitutions, not a transposition
		{"cach", "cache", 1},
		{"lst", "list", 1},
		{"xyz", "run", 3},
	}
	for _, tc := range cases {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		// Symmetry: a suggestion must not depend on argument order.
		if f, r := editDistance(tc.a, tc.b), editDistance(tc.b, tc.a); f != r {
			t.Errorf("editDistance is asymmetric for %q/%q: %d vs %d", tc.a, tc.b, f, r)
		}
	}
}

// TestClosestVerb covers the "did you mean" path. The threshold matters more
// than the ranking: suggesting a wildly different verb is worse than
// suggesting nothing, because it sends the reader off to try it.
func TestClosestVerb(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"rn", string(VerbRun)},
		{"lst", string(VerbList)},
		{"cach", string(VerbCache)},
		{"tui", string(VerbTUI)},
		// Nothing close enough, silence beats a misleading suggestion.
		{"deploy", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := closestVerb(tc.in); got != tc.want {
			t.Errorf("closestVerb(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestClosestVerbSuggestsOnlyRealVerbs. A suggestion the dispatch would
// reject is worse than none.
func TestClosestVerbSuggestsOnlyRealVerbs(t *testing.T) {
	t.Parallel()
	for _, typo := range []string{"rn", "lst", "cach", "vrsion", "hlp", "int", "tu"} {
		if s := closestVerb(typo); s != "" && !Verb(s).Valid() {
			t.Errorf("closestVerb(%q) suggested %q, which is not a verb", typo, s)
		}
	}
}

func TestCommandNames(t *testing.T) {
	t.Parallel()
	got := commandNames([]Target{
		{Command: "deploy"},
		{Command: "secrets push", Namespace: "secrets"},
	})
	if strings.Join(got, "|") != "deploy|secrets push" {
		t.Errorf("commandNames = %v", got)
	}
	if n := commandNames(nil); len(n) != 0 {
		t.Errorf("commandNames(nil) = %v, want empty", n)
	}
}

// TestVerbValuesCoverTheDispatch is the drift guard between the help listing
// and the switch: a verb that exists but is not in VerbValues never appears in
// help, and one listed but unhandled prints help and exits usage.
func TestVerbValuesCoverTheDispatch(t *testing.T) {
	t.Parallel()
	seen := map[Verb]bool{}
	for _, v := range VerbValues {
		if !v.Valid() {
			t.Errorf("%q is in VerbValues but Valid() is false", v)
		}
		if seen[v] {
			t.Errorf("%q appears twice in VerbValues", v)
		}
		seen[v] = true
		if verbDoc(v) == "" {
			t.Errorf("%q has no one-line summary, so it renders blank in help", v)
		}
	}
	if Verb("nonsense").Valid() {
		t.Error("an undeclared verb validated")
	}
}
