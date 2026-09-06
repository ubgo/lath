package env_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/env"
)

func TestRequire(t *testing.T) {
	t.Setenv("LATH_TEST_SET", "value")
	t.Setenv("LATH_TEST_EMPTY", "   ")

	if v, err := env.Require("LATH_TEST_SET"); err != nil || v != "value" {
		t.Errorf("Require(set) = %q, %v", v, err)
	}
	// An empty value counts as missing: a variable set to "" is almost never
	// intended, and treating it as present pushes the failure downstream.
	if _, err := env.Require("LATH_TEST_EMPTY"); !errors.Is(err, env.ErrMissing) {
		t.Errorf("Require(empty) = %v; want ErrMissing", err)
	}
	if _, err := env.Require("LATH_TEST_UNSET_XYZ"); !errors.Is(err, env.ErrMissing) {
		t.Errorf("Require(unset) = %v; want ErrMissing", err)
	}
	// The error must NAME the variable, which is the entire point over os.Getenv.
	_, err := env.Require("LATH_TEST_UNSET_XYZ")
	if !strings.Contains(err.Error(), "LATH_TEST_UNSET_XYZ") {
		t.Errorf("error %q does not name the variable", err)
	}
}

// TestRequireAllReportsEveryMissing pins that a caller fixing configuration
// gets the whole list in one run, rather than one restart at a time.
func TestRequireAllReportsEveryMissing(t *testing.T) {
	t.Setenv("LATH_TEST_PRESENT", "x")

	err := env.RequireAll("LATH_TEST_PRESENT", "LATH_MISSING_A", "LATH_MISSING_B")
	if !errors.Is(err, env.ErrMissing) {
		t.Fatalf("err = %v; want ErrMissing", err)
	}
	for _, name := range []string{"LATH_MISSING_A", "LATH_MISSING_B"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s: only the first was reported", err, name)
		}
	}
	if strings.Contains(err.Error(), "LATH_TEST_PRESENT") {
		t.Errorf("error %q names a variable that IS set", err)
	}

	if err := env.RequireAll("LATH_TEST_PRESENT"); err != nil {
		t.Errorf("RequireAll with everything set = %v", err)
	}
}

func TestGet(t *testing.T) {
	t.Setenv("LATH_TEST_GET", "actual")
	t.Setenv("LATH_TEST_BLANK", "  ")

	if got := env.Get("LATH_TEST_GET", "fallback"); got != "actual" {
		t.Errorf("Get(set) = %q", got)
	}
	if got := env.Get("LATH_TEST_UNSET_XYZ", "fallback"); got != "fallback" {
		t.Errorf("Get(unset) = %q", got)
	}
	if got := env.Get("LATH_TEST_BLANK", "fallback"); got != "fallback" {
		t.Errorf("Get(blank) = %q; a whitespace-only value must use the fallback", got)
	}
}
