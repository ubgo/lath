// Package env reads environment variables with errors worth reading.
//
// The value is entirely in the failure case: os.Getenv returns "" for a
// variable that is unset, misspelled, or empty, and that empty string travels
// until something far away fails for a reason that names nothing.
package env

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrMissing means a required variable is unset or empty.
var ErrMissing = errors.New("env: required variable not set")

// Require returns a variable's value, or an error naming it.
//
// An empty value counts as missing. A variable set to "" is almost never
// intended, and treating it as present pushes the failure downstream.
func Require(name string) (string, error) {
	v := os.Getenv(name)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("env: %s: %w", name, ErrMissing)
	}
	return v, nil
}

// RequireAll checks several variables and reports EVERY missing one.
//
// Not the first: a caller fixing configuration wants the whole list in one
// run, rather than discovering them one restart at a time.
func RequireAll(names ...string) error {
	var missing []string
	for _, n := range names {
		if _, err := Require(n); err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("env: %s: %w", strings.Join(missing, ", "), ErrMissing)
}

// Get returns a variable's value, or fallback when it is unset or empty.
func Get(name, fallback string) string {
	if v := os.Getenv(name); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
