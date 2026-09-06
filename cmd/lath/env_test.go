package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/pipeline"
)

// TestDefinitionEnvStatesTheProjectRoot pins the contract between the runner
// and every definition: the child is told which directory CONTAINS .lath,
// because it cannot work that out for itself. A definition is a nested module,
// so its own upward search for a go.mod stops at .lath.
func TestDefinitionEnvStatesTheProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, definitionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	env, err := definitionEnv()
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, pipeline.RootEnvVar+"="); ok {
			got = after
		}
	}
	if got == "" {
		t.Fatalf("%s is not in the definition's environment", pipeline.RootEnvVar)
	}
	// The ROOT, not the definition directory: pointing a definition at itself
	// is the exact mistake this exists to prevent.
	if filepath.Base(got) == definitionDir {
		t.Errorf("%s = %q, which is the definition, not the project", pipeline.RootEnvVar, got)
	}
	want, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Errorf("%s = %q, want %q", pipeline.RootEnvVar, got, want)
	}
}

// TestDefinitionEnvKeepsTheCallersEnvironment. A deploy reads credentials and
// PATH from the environment it was started in; replacing rather than extending
// it would break every definition in a way that looks like a missing secret.
func TestDefinitionEnvKeepsTheCallersEnvironment(t *testing.T) {
	t.Setenv("LATH_TEST_CANARY", "present")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, definitionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	env, err := definitionEnv()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, kv := range env {
		if kv == "LATH_TEST_CANARY=present" {
			found = true
		}
	}
	if !found {
		t.Error("the caller's environment did not survive")
	}
}
