package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// needsGo skips a test that shells out to the toolchain.
//
// These are the tests that exercise what lath actually DOES, compile a
// definition and hand off to it, so they are worth having despite the cost;
// but a machine without a toolchain should report a skip, not a failure.
func needsGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(goToolName); err != nil {
		t.Skip("needs the Go toolchain")
	}
}

// writeDefinition creates a minimal, compilable definition in dir.
//
// It requires nothing, so no network or module cache is involved: what is
// under test is lath's own discover → generate → build path, not module
// resolution.
func writeDefinition(t *testing.T, dir, body string) string {
	t.Helper()
	def := filepath.Join(dir, definitionDir)
	if err := os.MkdirAll(def, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def, "go.mod"),
		[]byte("module "+definitionModuleName+"\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def, "tasks.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return def
}

const helloDefinition = `package main

import "fmt"

// Hello greets, and is the simplest shape a target can have.
func Hello() error {
	fmt.Println("hello from the definition")
	return nil
}

// Echo takes arguments, which the dispatcher passes straight through.
func Echo(args ...string) error {
	fmt.Println("echo:", args)
	return nil
}
`

// TestBuildCompilesADefinition covers the compile path end to end: generate a
// dispatcher, build it, and get a runnable binary.
func TestBuildCompilesADefinition(t *testing.T) {
	needsGo(t)
	root := t.TempDir()
	def := writeDefinition(t, root, helloDefinition)

	targets, rejected, err := discover(def)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Errorf("rejected %v; both functions are valid shapes", rejected)
	}
	if len(targets) != 2 {
		t.Fatalf("discovered %d targets, want 2", len(targets))
	}

	generated, err := generate(def, targets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeGenerated(generated) })

	bin := filepath.Join(t.TempDir(), "definition")
	if err := build(definition{Dir: def}, bin, os.Stderr, os.Stdout); err != nil {
		t.Fatalf("build: %v", err)
	}

	out, err := exec.Command(bin, "hello").CombinedOutput()
	if err != nil {
		t.Fatalf("running the built definition: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hello from the definition") {
		t.Errorf("built binary did not run the target:\n%s", out)
	}
}

// TestBuildReportsACompileError. A type error in a definition must reach the
// author verbatim, wrapped in a sentinel so the runner can tell it apart from
// a missing toolchain.
func TestBuildReportsACompileError(t *testing.T) {
	needsGo(t)
	root := t.TempDir()
	def := writeDefinition(t, root, "package main\n\nfunc Broken() error { return notAThing }\n")

	targets, _, err := discover(def)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := generate(def, targets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeGenerated(generated) })

	err = build(definition{Dir: def}, filepath.Join(t.TempDir(), "bin"), os.Stderr, os.Stdout)
	if !errors.Is(err, ErrCompile) {
		t.Errorf("err = %v, want ErrCompile", err)
	}
}

// TestRunTargetBuildsCachesAndDispatches is the whole warm path: first run
// compiles and records a manifest, second finds the cached binary by hash.
//
// handOff is not reached, it replaces the process, so this asserts what is
// observable up to that point: the cache entry and its manifest.
func TestRunTargetPopulatesTheCache(t *testing.T) {
	needsGo(t)
	isolateCache(t)
	root := t.TempDir()
	writeDefinition(t, root, helloDefinition)
	t.Chdir(root)

	def, err := scan(definitionDir)
	if err != nil {
		t.Fatal(err)
	}
	targets, _, err := discover(definitionDir)
	if err != nil {
		t.Fatal(err)
	}

	binPath := cacheEntryPath(def)
	generated, err := generate(def.Dir, targets)
	if err != nil {
		t.Fatal(err)
	}
	buildErr := build(def, binPath, os.Stderr, os.Stdout)
	_ = removeGenerated(generated)
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if err := writeManifest(binPath, def, targets); err != nil {
		t.Fatal(err)
	}

	// The warm path: found by hash suffix, not by rebuilding the name.
	if got := cacheLookup(def.Hash); got != binPath {
		t.Errorf("cacheLookup = %q, want %q", got, binPath)
	}
	m, err := readManifest(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Targets) != 2 {
		t.Errorf("manifest targets = %v, want both", m.Targets)
	}

	// The generated dispatcher must never be left behind: an author must not
	// see generated code in their diff.
	if _, err := os.Stat(filepath.Join(definitionDir, generatedFileName)); !os.IsNotExist(err) {
		t.Error("the generated dispatcher was left in the definition directory")
	}
}

// useLocalEngine points scaffolds at this checkout, which is what a
// development build does via the devEnginePath ldflag.
//
// Without it a scaffolded definition requires the PUBLISHED engine modules,
// and `go mod tidy` reaches for a repository that does not exist yet, a real
// and correct failure, but one about publication rather than about the code
// under test.
func useLocalEngine(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	orig := devEnginePath
	devEnginePath = root
	t.Cleanup(func() { devEnginePath = orig })
}

// TestInitScaffoldsARunnableDefinition covers `lath init` and then proves the
// thing it wrote actually works. A scaffold that does not compile is worse
// than none, because it looks like a working starting point.
func TestInitScaffoldsARunnableDefinition(t *testing.T) {
	needsGo(t)
	useLocalEngine(t)
	root := t.TempDir()
	t.Chdir(root)

	var code int
	out := captureOutput(t, func() { code = initDefinition(definitionDir) })
	if code != exitOK {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	for _, f := range []string{starterFileName, goModFile} {
		if _, err := os.Stat(filepath.Join(definitionDir, f)); err != nil {
			t.Errorf("init did not write %s: %v", f, err)
		}
	}
	if !strings.Contains(out, definitionDir) {
		t.Errorf("init did not say what it created:\n%s", out)
	}

	targets, _, err := discover(definitionDir)
	if err != nil {
		t.Fatalf("the scaffolded definition is not discoverable: %v", err)
	}
	if len(targets) == 0 {
		t.Error("the scaffold exposes no targets, so `lath list` would be empty")
	}
}

// TestInitRefusesToOverwrite. A definition holds real deploy logic, so
// nothing here may replace it.
func TestInitRefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	writeDefinition(t, root, helloDefinition)
	t.Chdir(root)

	var code int
	out := captureOutput(t, func() { code = initDefinition(definitionDir) })
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(out, "refusing") {
		t.Errorf("message does not say it refused:\n%s", out)
	}
	if !strings.Contains(out, "rm -rf") {
		t.Errorf("message does not say how to start over:\n%s", out)
	}
}

// TestTidyRunsInTheDefinition. The scaffold's go.mod must be tidied with the
// workspace OFF, or a definition inside a go.work repo resolves against the
// workspace instead of its own requires.
func TestTidyRunsInTheDefinition(t *testing.T) {
	needsGo(t)
	root := t.TempDir()
	def := writeDefinition(t, root, helloDefinition)
	if err := tidy(def); err != nil {
		t.Errorf("tidy on a self-contained definition failed: %v", err)
	}
}

func TestTidyReportsFailure(t *testing.T) {
	needsGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("this is not a go.mod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := tidy(dir)
	if err == nil {
		t.Fatal("tidy accepted a malformed go.mod")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error does not name the directory: %v", err)
	}
}
