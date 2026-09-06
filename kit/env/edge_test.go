package env_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/env"
)

// TestRequireTreatsBlankAsMissing pins that a variable set to whitespace is
// missing. It is the shape a shell produces from an unset lookup, VAR="$X"
// with X unset, and treating it as present hands an empty token to whatever
// asked for it, failing much later and somewhere else.
func TestRequireTreatsBlankAsMissing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		missing bool
	}{
		{"empty", "", true},
		{"a space", " ", true},
		{"a tab", "\t", true},
		{"a newline", "\n", true},
		{"mixed whitespace", " \t\n ", true},
		{"a real value", "x", false},
		// Padding is preserved, not trimmed: only the emptiness test trims.
		// A caller that wanted the padding gone can trim it themselves; a
		// caller whose secret legitimately ends in a space cannot get it back.
		{"a padded value", "  x  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LATH_EDGE_VAR", tc.value)
			got, err := env.Require("LATH_EDGE_VAR")
			if tc.missing {
				if !errors.Is(err, env.ErrMissing) {
					t.Errorf("Require(%q) = %q, %v; want ErrMissing", tc.value, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Require(%q) = %v", tc.value, err)
			}
			if got != tc.value {
				t.Errorf("Require returned %q; want the value verbatim, %q", got, tc.value)
			}
		})
	}
}

func TestRequireUnset(t *testing.T) {
	if _, err := env.Require("LATH_DEFINITELY_UNSET_9f2a"); !errors.Is(err, env.ErrMissing) {
		t.Errorf("err = %v; want ErrMissing", err)
	}
}

// TestRequireAllReportsEveryMissingName is the whole reason RequireAll exists.
// Reporting only the first turns configuring a deploy into one round trip per
// missing variable.
func TestRequireAllReportsEveryMissingName(t *testing.T) {
	t.Setenv("LATH_SET_A", "value")
	err := env.RequireAll("LATH_SET_A", "LATH_MISSING_B", "LATH_MISSING_C", "LATH_MISSING_D")
	if !errors.Is(err, env.ErrMissing) {
		t.Fatalf("err = %v; want ErrMissing", err)
	}
	for _, name := range []string{"LATH_MISSING_B", "LATH_MISSING_C", "LATH_MISSING_D"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "LATH_SET_A") {
		t.Errorf("the error names a variable that was set: %v", err)
	}
}

// TestRequireAllNoNames pins that requiring nothing succeeds, the natural
// result of a caller passing a slice that happened to be empty.
func TestRequireAllNoNames(t *testing.T) {
	if err := env.RequireAll(); err != nil {
		t.Errorf("RequireAll() = %v; want nil", err)
	}
	if err := env.RequireAll([]string{}...); err != nil {
		t.Errorf("RequireAll(empty slice) = %v; want nil", err)
	}
}

func TestRequireAllAllPresent(t *testing.T) {
	t.Setenv("LATH_P1", "a")
	t.Setenv("LATH_P2", "b")
	if err := env.RequireAll("LATH_P1", "LATH_P2"); err != nil {
		t.Errorf("err = %v; want nil", err)
	}
}

// TestGetFallsBackOnBlank pins that Get and Require agree on what "set" means.
// If they disagreed, a variable could pass Require and still yield the
// fallback, or vice versa.
func TestGetFallsBackOnBlank(t *testing.T) {
	for _, tc := range []struct {
		value, want string
	}{
		{"", "fallback"},
		{"   ", "fallback"},
		{"\t\n", "fallback"},
		{"real", "real"},
	} {
		t.Setenv("LATH_GET_VAR", tc.value)
		if got := env.Get("LATH_GET_VAR", "fallback"); got != tc.want {
			t.Errorf("Get with %q = %q; want %q", tc.value, got, tc.want)
		}
	}
}

func TestGetUnsetAndEmptyFallback(t *testing.T) {
	if got := env.Get("LATH_DEFINITELY_UNSET_9f2a", "d"); got != "d" {
		t.Errorf("Get = %q; want the fallback", got)
	}
	// An empty fallback is a legitimate choice and must be returned as-is.
	if got := env.Get("LATH_DEFINITELY_UNSET_9f2a", ""); got != "" {
		t.Errorf("Get = %q; want an empty string", got)
	}
}
