package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEditingALocallyReplacedModuleChangesTheHash is the regression for a bug
// that wasted a real debugging session.
//
// A definition under development replaces its engine modules with local paths.
// The cache key covered only files INSIDE the definition, so editing the engine
// changed nothing the hash could see. Lath reused a stale binary and a fix
// that had definitely been made appeared not to work. That symptom is worse
// than a plain failure, because the evidence says the new code is running.
func TestEditingALocallyReplacedModuleChangesTheHash(t *testing.T) {
	root := t.TempDir()
	engine := filepath.Join(root, "engine")
	def := filepath.Join(root, ".lath")
	for _, d := range []string{engine, def} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(engine, "go.mod"), "module example.com/engine\n\ngo 1.24\n")
	write(t, filepath.Join(engine, "engine.go"), "package engine\n\nconst Answer = 1\n")
	write(t, filepath.Join(def, "go.mod"),
		"module deploydef\n\ngo 1.24\n\nrequire example.com/engine v0.0.0\n\nreplace example.com/engine => ../engine\n")
	write(t, filepath.Join(def, "main.go"), "package main\n\n// Deploy ships it.\nfunc Deploy() error { return nil }\n")

	before, err := scan(def)
	if err != nil {
		t.Fatal(err)
	}

	// Edit ONLY the replaced module. Nothing inside the definition changes.
	write(t, filepath.Join(engine, "engine.go"), "package engine\n\nconst Answer = 2\n")

	after, err := scan(def)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("editing a locally-replaced module did not change the hash: lath would run a stale binary")
	}
}

// TestTransitiveReplacesAreCovered pins that a replace reached through another
// replace counts too. The definition replaces steps; steps replaces kit; a
// change in kit must still invalidate the cache.
func TestTransitiveReplacesAreCovered(t *testing.T) {
	root := t.TempDir()
	kit := filepath.Join(root, "kit")
	steps := filepath.Join(root, "steps")
	def := filepath.Join(root, ".lath")
	for _, d := range []string{kit, steps, def} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(kit, "go.mod"), "module example.com/kit\n\ngo 1.24\n")
	write(t, filepath.Join(kit, "kit.go"), "package kit\n\nconst V = 1\n")
	write(t, filepath.Join(steps, "go.mod"),
		"module example.com/steps\n\ngo 1.24\n\nreplace example.com/kit => ../kit\n")
	write(t, filepath.Join(steps, "steps.go"), "package steps\n")
	write(t, filepath.Join(def, "go.mod"),
		"module deploydef\n\ngo 1.24\n\nreplace (\n\texample.com/steps => ../steps\n)\n")
	write(t, filepath.Join(def, "main.go"), "package main\n\n// Deploy ships it.\nfunc Deploy() error { return nil }\n")

	before, err := scan(def)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(kit, "kit.go"), "package kit\n\nconst V = 2\n")
	after, err := scan(def)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("a change two replaces deep did not change the hash")
	}
}

// TestTestFilesInAReplacedModuleDoNotBustTheCache pins the other direction:
// a test edit cannot affect the compiled dispatcher, so triggering a rebuild
// for one is pure waste.
func TestTestFilesInAReplacedModuleDoNotBustTheCache(t *testing.T) {
	root := t.TempDir()
	engine := filepath.Join(root, "engine")
	def := filepath.Join(root, ".lath")
	for _, d := range []string{engine, def} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(engine, "go.mod"), "module example.com/engine\n\ngo 1.24\n")
	write(t, filepath.Join(engine, "engine.go"), "package engine\n")
	write(t, filepath.Join(def, "go.mod"),
		"module deploydef\n\ngo 1.24\n\nreplace example.com/engine => ../engine\n")
	write(t, filepath.Join(def, "main.go"), "package main\n\n// Deploy ships it.\nfunc Deploy() error { return nil }\n")

	before, err := scan(def)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(engine, "engine_test.go"), "package engine\n\n// a new test\n")
	after, err := scan(def)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash != after.Hash {
		t.Error("adding a test to a replaced module forced a rebuild")
	}
}

// TestAModuleReplaceIsNotTreatedAsAPath pins that a replace pointing at
// another MODULE (not a directory) is left to go.sum, which the definition's
// own hash already covers.
func TestAModuleReplaceIsNotTreatedAsAPath(t *testing.T) {
	for _, line := range []string{
		"replace example.com/a => example.com/b v1.2.3",
		"replace example.com/a v1.0.0 => example.com/b v1.2.3",
	} {
		if _, ok := replaceTarget(line); ok {
			t.Errorf("%q was treated as a filesystem path", line)
		}
	}
	for _, line := range []string{
		"replace example.com/a => ../b",
		"example.com/a => ./b",
		"example.com/a v1.0.0 => ../../c",
	} {
		if _, ok := replaceTarget(line); !ok {
			t.Errorf("%q was not recognised as a filesystem path", line)
		}
	}
}

// TestNoReplacesCostsNothing pins that the normal, published case adds no
// digest at all.
func TestNoReplacesCostsNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module deploydef\n\ngo 1.24\n")
	got, err := localReplaceHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("localReplaceHash = %q; want empty when there are no local replaces", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
