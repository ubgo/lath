package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// writeDef creates a definition directory with the given files and returns it.
func writeDef(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestScan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		files     map[string]string
		wantFiles int
		wantErr   error
	}{
		{
			name:    "missing directory",
			files:   nil,
			wantErr: ErrNoDefinition,
		},
		{
			name:    "directory with no Go files",
			files:   map[string]string{"README.md": "hi"},
			wantErr: ErrNoDefinition,
		},
		{
			name: "every .go file plus go.mod and go.sum counts",
			files: map[string]string{
				"pipeline.go": "package main",
				"steps.go":    "package main",
				goModFile:     "module x",
				goSumFile:     "",
				"notes.md":    "ignored",
			},
			wantFiles: 4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "absent")
			if tc.files != nil {
				dir = writeDef(t, tc.files)
			}
			def, err := scan(dir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("scan() = %v; want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(def.Files) != tc.wantFiles {
				t.Errorf("hashed %v (%d files); want %d", def.Files, len(def.Files), tc.wantFiles)
			}
			if len(def.Hash) != hashPrefixLen {
				t.Errorf("hash %q is %d chars; want %d", def.Hash, len(def.Hash), hashPrefixLen)
			}
		})
	}
}

// TestScanHashIsStableAndContentSensitive covers the two properties the cache
// depends on: identical content must reuse the cached binary, and ANY change
// must miss. A hash that is merely "usually right" silently ships stale code.
func TestScanHashIsStableAndContentSensitive(t *testing.T) {
	t.Parallel()
	base := map[string]string{"a.go": "package main", "b.go": "// b", goModFile: "module x"}

	first, err := scan(writeDef(t, base))
	if err != nil {
		t.Fatal(err)
	}
	same, err := scan(writeDef(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != same.Hash {
		t.Errorf("identical content hashed differently: %s vs %s", first.Hash, same.Hash)
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"edit a file", func(m map[string]string) { m["a.go"] = "package main // changed" }},
		{"add a file", func(m map[string]string) { m["c.go"] = "// c" }},
		{"remove a file", func(m map[string]string) { delete(m, "b.go") }},
		{"bump a dependency", func(m map[string]string) { m[goModFile] = "module x\nrequire y v1.0.0" }},
		// Moving bytes between files leaves the concatenation identical. The
		// name+length framing in the hash is what catches it.
		{"move bytes between files", func(m map[string]string) {
			m["a.go"] = "package main// b"
			m["b.go"] = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mutated := make(map[string]string, len(base))
			for k, v := range base {
				mutated[k] = v
			}
			tc.mutate(mutated)
			got, err := scan(writeDef(t, mutated))
			if err != nil {
				t.Fatal(err)
			}
			if got.Hash == first.Hash {
				t.Errorf("hash unchanged after %q; a stale binary would be reused", tc.name)
			}
		})
	}
}

func TestVerbValid(t *testing.T) {
	t.Parallel()
	for _, v := range VerbValues {
		if !v.Valid() {
			t.Errorf("%q is in VerbValues but Valid() is false", v)
		}
	}
	if Verb("nope").Valid() {
		t.Error("an undeclared verb reported valid")
	}
}

// TestVerbWireValues pins the LITERAL words a user types. A typed constant
// protects use sites from typos; it cannot protect its own value, and a test
// referencing the constant would agree with a wrong one.
func TestVerbWireValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		got  Verb
		want string
	}{
		{VerbRun, "run"},
		{VerbInit, "init"},
		{VerbList, "list"},
		{VerbCache, "cache"},
		{VerbVersion, "version"},
		{VerbHelp, "help"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("verb = %q; want %q", tc.got, tc.want)
		}
	}
	for _, tc := range []struct{ got, want string }{
		{FlagHelp, "-h"}, {FlagHelpLong, "--help"},
	} {
		if tc.got != tc.want {
			t.Errorf("flag = %q; want %q", tc.got, tc.want)
		}
	}
}

func TestCacheActionValid(t *testing.T) {
	t.Parallel()
	if !CacheActionList.Valid() {
		t.Error("the default (list) action must be valid")
	}
	for _, a := range CacheActionValues {
		if !a.Valid() {
			t.Errorf("%q is in CacheActionValues but Valid() is false", a)
		}
	}
	if CacheAction("nuke").Valid() {
		t.Error("an undeclared action reported valid")
	}
}

// TestEveryVerbIsDocumented pins that the help listing cannot contain a blank
// line. A verb added without a description.
func TestEveryVerbIsDocumented(t *testing.T) {
	t.Parallel()
	for _, v := range VerbValues {
		if strings.TrimSpace(verbDoc(v)) == "" {
			t.Errorf("verb %q has no description", v)
		}
	}
}

// TestTargetsMayShadowVerbs is the property the `run` namespace buys: because
// targets live behind it, a definition may export functions named after lath's
// own commands. Under the rejected design, bare targets, lath's commands
// behind a prefix. Every one of these would have been a fatal name clash.
func TestTargetsMayShadowVerbs(t *testing.T) {
	t.Parallel()
	// Only the verbs a Go function could be named after. A hyphenated verb
	// like dry-run cannot collide with a target at all, because a target is a
	// Go identifier, so the property holds for it trivially and there is
	// nothing to generate.
	var shadowable []Verb
	for _, v := range VerbValues {
		if isGoIdentifier(string(v)) {
			shadowable = append(shadowable, v)
		}
	}
	src := "package main\n"
	for _, v := range shadowable {
		name := strings.ToUpper(string(v)[:1]) + string(v)[1:]
		src += "// " + name + " is a target sharing a name with a lath verb.\n"
		src += "func " + name + "() error { return nil }\n"
	}
	dir := writeDef(t, map[string]string{goModFile: "module d", "a.go": src})

	targets, _, err := discover(dir)
	if err != nil {
		t.Fatalf("a definition naming targets after lath verbs was rejected: %v", err)
	}
	if len(targets) != len(shadowable) {
		t.Fatalf("discovered %d targets; want %d", len(targets), len(shadowable))
	}
	// And the dispatcher generated for them must still compile.
	path, err := generate(dir, targets)
	if err != nil {
		t.Fatal(err)
	}
	src2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, src2, 0); err != nil {
		t.Fatalf("dispatcher for verb-named targets does not parse: %v", err)
	}
}

// TestVerbsWorkWithoutADefinition pins that lath's own commands work in a
// repository that has none. Which is when help and init are most needed.
func TestVerbsWorkWithoutADefinition(t *testing.T) {
	for _, args := range [][]string{
		{string(VerbVersion)},
		{string(VerbHelp)},
		{FlagHelp},
		{FlagHelpLong},
	} {
		if got := run(args); got != exitOK {
			t.Errorf("run(%v) = %d; want %d", args, got, exitOK)
		}
	}
}

// --- discovery ---

func TestDiscover(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{
		goModFile: "module d",
		"a.go": `package main
import "context"

// Deploy ships it. Second sentence is dropped.
func Deploy(ctx context.Context, args ...string) error { return nil }

// Plan lists things.
func Plan(args ...string) error { return nil }

// Bare takes and returns nothing.
func Bare() {}

// Erroring returns only an error.
func Erroring() error { return nil }

// CtxOnly takes a context.
func CtxOnly(ctx context.Context) error { return nil }

func unexported() error { return nil }

type T struct{}
// Method is a method, not a command.
func (T) Method() error { return nil }
`,
	})
	got, _, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]Signature{
		"bare":     SigPlain,
		"ctxonly":  SigCtxError,
		"deploy":   SigCtxArgsError,
		"erroring": SigError,
		"plan":     SigArgsError,
	}
	if len(got) != len(want) {
		t.Fatalf("discovered %d targets, want %d: %+v", len(got), len(want), got)
	}
	for _, tgt := range got {
		wantSig, ok := want[tgt.Command]
		if !ok {
			t.Errorf("unexpected target %q", tgt.Command)
			continue
		}
		if tgt.Sig != wantSig {
			t.Errorf("%s: signature = %q, want %q", tgt.Command, tgt.Sig, wantSig)
		}
	}

	// Sorted, so listings and generated code are byte-stable across runs.
	for i := 1; i < len(got); i++ {
		if got[i-1].Command > got[i].Command {
			t.Errorf("targets not sorted: %q before %q", got[i-1].Command, got[i].Command)
		}
	}

	// The doc line drops the leading identifier and stops at the first period.
	for _, tgt := range got {
		if tgt.Command == "deploy" && tgt.Doc != "Deploy ships it" {
			t.Errorf("doc = %q; want the first sentence only", tgt.Doc)
		}
	}
}

// TestDiscoverRejectsUnsupportedShapes proves an un-callable function is
// reported rather than silently omitted. A command that quietly does not
// exist is the worst outcome.
func TestDiscoverRejectsUnsupportedShapes(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{
		goModFile: "module d",
		"a.go": `package main
// Weird returns two values.
func Weird() (int, error) { return 0, nil }
// AlsoWeird takes an int.
func AlsoWeird(n int) error { return nil }
`,
	})
	_, _, err := discover(dir)
	if !errors.Is(err, ErrNoTargets) {
		t.Fatalf("discover() = %v; want ErrNoTargets", err)
	}
	for _, want := range []string{"Weird", "AlsoWeird", "unsupported signature"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q missing %q", err, want)
		}
	}
}

// TestDiscoverRejectsCaseCollisions guards a silent shadowing: two exported
// functions differing only in case map to one command.
func TestDiscoverRejectsCaseCollisions(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{
		goModFile: "module d",
		"a.go": `package main
// Deploy one.
func Deploy() error { return nil }
// DEPLOY two.
func DEPLOY() error { return nil }
`,
	})
	if _, _, err := discover(dir); !errors.Is(err, ErrDuplicateTarget) {
		t.Fatalf("discover() = %v; want ErrDuplicateTarget", err)
	}
}

// TestDiscoverIgnoresTestsAndGenerated pins the two exclusions the scan relies
// on: a Test* function is not a command, and reading back our own generated
// dispatcher would be circular.
func TestDiscoverIgnoresTestsAndGenerated(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{
		goModFile:         "module d",
		"a.go":            "package main\n// Real is a command.\nfunc Real() error { return nil }\n",
		"a_test.go":       "package main\nimport \"testing\"\nfunc TestThing(t *testing.T) {}\n",
		generatedFileName: generatedHeader + "\npackage main\n// Injected must not be discovered.\nfunc Injected() error { return nil }\n",
	})
	got, _, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Command != "real" {
		t.Fatalf("discovered %+v; want only \"real\"", got)
	}
}

// TestGeneratedFileExcludedFromHash pins the fix for a real bug: the dispatcher
// is derived FROM the hashed files, so including it made the hash depend on its
// own output and every run missed the cache.
func TestGeneratedFileExcludedFromHash(t *testing.T) {
	t.Parallel()
	base := map[string]string{goModFile: "module d", "a.go": "package main\nfunc Real() error { return nil }\n"}
	withGen := map[string]string{}
	for k, v := range base {
		withGen[k] = v
	}
	withGen[generatedFileName] = generatedHeader + "\npackage main\nfunc main() {}\n"

	a, err := scan(writeDef(t, base))
	if err != nil {
		t.Fatal(err)
	}
	b, err := scan(writeDef(t, withGen))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Errorf("a stale generated file changed the hash (%s vs %s); every run would miss the cache",
			a.Hash, b.Hash)
	}
}

// TestRemoveGeneratedRefusesForeignFiles pins that a hand-written file sharing
// the name is never destroyed.
func TestRemoveGeneratedRefusesForeignFiles(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{generatedFileName: "package main // written by a human\n"})
	path := filepath.Join(dir, generatedFileName)
	if err := removeGenerated(path); err == nil {
		t.Fatal("removeGenerated deleted a file it did not write")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the file was removed despite the error")
	}
}

func TestGenerateProducesCompilableDispatcher(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{goModFile: "module d"})
	targets := []Target{
		{Func: "Bare", Command: "bare", Sig: SigPlain, Doc: "does nothing"},
		{Func: "Deploy", Command: "deploy", Sig: SigCtxArgsError, Doc: "ships it"},
	}
	path, err := generate(dir, targets)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{generatedHeader, `case "bare":`, "Bare()", `case "deploy":`, "Deploy(ctx, os.Args[2:]...)"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated dispatcher missing %q", want)
		}
	}
	// It must parse as Go; a template mistake would otherwise surface as a
	// compile error in code the author never wrote.
	if _, err := parser.ParseFile(token.NewFileSet(), path, src, 0); err != nil {
		t.Fatalf("generated dispatcher does not parse: %v", err)
	}
}

// TestEverySignatureRenders is the exhaustiveness check that a Go switch cannot
// give for a string-based enum: every declared Signature must have a call form.
// Adding one to SignatureValues without adding a case fails here rather than
// generating a switch case with an empty body.
func TestEverySignatureRenders(t *testing.T) {
	t.Parallel()
	for _, sig := range SignatureValues {
		stmt, err := sig.Statement("Target", "os.Args[2:]")
		if err != nil {
			t.Errorf("signature %q has no call form: %v", sig, err)
			continue
		}
		if !strings.Contains(stmt, "Target(") {
			t.Errorf("signature %q rendered %q, which does not call the function", sig, stmt)
		}
		// Anything returning an error must assign it, or the dispatcher would
		// swallow every failure and exit 0 on a failed deploy.
		if sig != SigPlain && !strings.HasPrefix(stmt, "err = ") {
			t.Errorf("signature %q rendered %q; an error-returning target must assign err", sig, stmt)
		}
	}
	if _, err := Signature("func(int) bool").Statement("X", "os.Args[2:]"); err == nil {
		t.Error("an undeclared signature produced a call form")
	}
}

// TestDiscoverMissingDirectory pins a real bug: discover runs BEFORE scan, so
// when it returned a raw os error for a missing directory, the actionable
// "no definition here" message in reportDiscoveryError was unreachable and the
// user saw "open .deploy: no such file or directory" with an internal-error
// exit code.
func TestDiscoverMissingDirectory(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	if _, _, err := discover(missing); !errors.Is(err, ErrNoDefinition) {
		t.Fatalf("discover(missing) = %v; want ErrNoDefinition", err)
	}

	// A file where a directory belongs is the same class of mistake.
	notDir := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := discover(notDir); !errors.Is(err, ErrNoDefinition) {
		t.Fatalf("discover(file) = %v; want ErrNoDefinition", err)
	}
}

// TestDuplicateTargetErrorNamesBothFiles pins that the message is actionable:
// knowing WHICH files hold the colliding functions is the difference between a
// one-second fix and a grep.
func TestDuplicateTargetErrorNamesBothFiles(t *testing.T) {
	t.Parallel()
	dir := writeDef(t, map[string]string{
		goModFile: "module d",
		"one.go":  "package main\n// Deploy one.\nfunc Deploy() error { return nil }\n",
		"two.go":  "package main\n// DEPLOY two.\nfunc DEPLOY() error { return nil }\n",
	})
	_, _, err := discover(dir)
	if !errors.Is(err, ErrDuplicateTarget) {
		t.Fatalf("discover() = %v; want ErrDuplicateTarget", err)
	}
	for _, want := range []string{"one.go", "two.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
		}
	}
}

// TestGeneratedDispatcherCompilesForEverySignatureMix is the matrix that a
// single example definition cannot cover.
//
// Go rejects an unused variable and an unused import, so a dispatcher that
// unconditionally declares ctx and imports context fails to compile for any
// definition whose targets all omit it. A definition of only `func Name()
// error` targets, for instance. Every combination is exercised here, and each
// generated file must parse.
//
// There is deliberately no equivalent assertion for args: variadic targets
// receive os.Args[N:] as an inline expression rather than a declared variable,
// so that failure mode cannot arise at all.
func TestGeneratedDispatcherCompilesForEverySignatureMix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		sigs     []Signature
		wantCtx  bool
		wantArgs bool
	}{
		{"plain only", []Signature{SigPlain}, false, false},
		{"error only", []Signature{SigError}, false, false},
		{"ctx only", []Signature{SigCtxError}, true, false},
		{"args only", []Signature{SigArgsError}, false, true},
		{"ctx+args", []Signature{SigCtxArgsError}, true, true},
		{"plain and ctx", []Signature{SigPlain, SigCtxError}, true, false},
		{"plain and args", []Signature{SigPlain, SigArgsError}, false, true},
		{"every shape", SignatureValues, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			targets := make([]Target, 0, len(tc.sigs))
			for i, s := range tc.sigs {
				targets = append(targets, Target{
					Func:    fmt.Sprintf("Target%d", i),
					Command: fmt.Sprintf("target%d", i),
					Sig:     s,
					Doc:     "does a thing",
				})
			}
			dir := writeDef(t, map[string]string{goModFile: "module d"})
			path, err := generate(dir, targets)
			if err != nil {
				t.Fatal(err)
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Must parse: a template mistake would otherwise surface as a
			// compile error in code the author never wrote.
			if _, err := parser.ParseFile(token.NewFileSet(), path, src, 0); err != nil {
				t.Fatalf("generated dispatcher does not parse: %v\n%s", err, src)
			}
			text := string(src)
			// The context handed to a target must be SIGNAL-AWARE, not a bare
			// Background. A target receiving one that never cancels would have
			// to install its own handler to be interruptible at all, and a
			// task runner whose Ctrl-C does nothing is worse than useless,
			// because it looks like it works.
			if got := strings.Contains(text, "signal.NotifyContext("); got != tc.wantCtx {
				t.Errorf("uses signal.NotifyContext = %v, want %v", got, tc.wantCtx)
			}
			if strings.Contains(text, "ctx := context.Background()") {
				t.Error("hands targets a plain context.Background(): Ctrl-C would do nothing")
			}
			// Each of these imports must appear exactly when ctx is declared;
			// an unused import does not compile.
			for _, imp := range []string{`"context"`, `"os/signal"`, `"syscall"`} {
				if got := strings.Contains(text, imp); got != tc.wantCtx {
					t.Errorf("imports %s = %v, want %v", imp, got, tc.wantCtx)
				}
			}
			// Variadic targets receive the slice inline; nothing is declared.
			if strings.Contains(text, "args :=") {
				t.Errorf("dispatcher declares an args variable; it must pass "+
					"os.Args[N:] inline so no unused-variable case exists:\n%s", text)
			}
			if got := strings.Contains(text, "os.Args[2:]..."); got != tc.wantArgs {
				t.Errorf("passes os.Args[2:] = %v, want %v", got, tc.wantArgs)
			}
		})
	}
}

// --- scaffolding ---

func TestScaffold(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".lath")

	if err := scaffold(dir, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{goModFile, starterFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("scaffold did not write %s", name)
		}
	}

	// The starter must be discoverable as-is, or the first thing a new user
	// does after -init fails.
	targets, _, err := discover(dir)
	if err != nil {
		t.Fatalf("a scaffolded definition is not discoverable: %v", err)
	}
	if len(targets) < 2 {
		t.Errorf("scaffold produced %d targets; want plan and deploy", len(targets))
	}
}

// TestScaffoldRefusesToOverwrite pins that re-running the command cannot
// destroy real deploy logic.
func TestScaffoldRefusesToOverwrite(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), ".lath")
	if err := scaffold(dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := scaffold(dir, ""); !errors.Is(err, ErrAlreadyInitialised) {
		t.Fatalf("second scaffold = %v; want ErrAlreadyInitialised", err)
	}
}

func TestParentGoDirective(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		gomod string
		want  string
	}{
		{"copied from the parent", "module x\n\ngo 1.23\n", "1.23"},
		{"patch versions kept", "module x\n\ngo 1.24.2\n", "1.24.2"},
		{"malformed go.mod falls back", "module x\n", fallbackGoDirective},
		// A definition may live in a repository of any language, so an absent
		// go.mod is normal rather than an error.
		{"no go.mod at all falls back", "", fallbackGoDirective},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.gomod != "" {
				if err := os.WriteFile(filepath.Join(root, goModFile), []byte(tc.gomod), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := parentGoDirective(root); got != tc.want {
				t.Errorf("parentGoDirective = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestRenderGoModModes pins the two shapes a scaffolded go.mod takes, chosen by
// devEnginePath alone, getting either wrong makes `-init` fail on first use.
//
//   - PUBLISHED (no engine path): no require, no replace. `go mod tidy` reads
//     the starter's imports and resolves real versions from the proxy. Writing
//     `v0.0.0` here would pin a version that does not exist.
//   - LOCAL CHECKOUT: require + replace. The version is a placeholder because a
//     replace overrides it entirely.
func TestRenderGoModModes(t *testing.T) {
	t.Parallel()

	published := renderGoMod("1.24", "")
	for _, forbidden := range []string{"require", "replace", "v0.0.0"} {
		if strings.Contains(published, forbidden) {
			t.Errorf("published-mode go.mod contains %q:\n%s", forbidden, published)
		}
	}
	if !strings.Contains(published, "module "+definitionModuleName) {
		t.Errorf("published-mode go.mod has no module line:\n%s", published)
	}

	local := renderGoMod("1.24", "/src/lath")
	for _, want := range []string{"require (", "replace ("} {
		if !strings.Contains(local, want) {
			t.Errorf("local-mode go.mod missing %q:\n%s", want, local)
		}
	}
	for _, m := range engineModules {
		// Every engine module must appear in BOTH blocks, or the replace
		// silently covers only part of what is required.
		if strings.Count(local, m) < 2 {
			t.Errorf("%s appears in only one of require/replace:\n%s", m, local)
		}
		// Each replace points at the module's OWN directory, which is the last
		// element of its import path. A replace aimed at the repository root
		// would resolve nothing. The root is a workspace, not a module.
		want := m + " => /src/lath/" + path.Base(m)
		if !strings.Contains(local, want) {
			t.Errorf("local-mode go.mod missing %q:\n%s", want, local)
		}
	}
	// The old broken layout kept modules under modules/. A replace pointing
	// there resolves nothing and is unpublishable.
	if strings.Contains(local, "/src/lath/modules") {
		t.Errorf("the replace points into a modules/ subdirectory:\n%s", local)
	}
}

// TestScaffoldedDefinitionIsRunnable is the end-to-end guarantee for `-init`:
// what it writes must discover, generate, and parse without any edit.
func TestScaffoldedDefinitionIsRunnable(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), ".lath")
	if err := scaffold(dir, "/src/lath"); err != nil {
		t.Fatal(err)
	}
	targets, _, err := discover(dir)
	if err != nil {
		t.Fatalf("scaffolded definition is not discoverable: %v", err)
	}
	path, err := generate(dir, targets)
	if err != nil {
		t.Fatalf("cannot generate a dispatcher for the scaffold: %v", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, src, 0); err != nil {
		t.Fatalf("scaffold produced a dispatcher that does not parse: %v\n%s", err, src)
	}
}

// TestDevEnginePathIsEmptyByDefault pins that the source default is the RELEASE
// behaviour. A development build opts in via -ldflags; nothing opts out. If the
// default were ever a path, every released binary would scaffold definitions
// carrying absolute paths from the machine that built it.
func TestDevEnginePathIsEmptyByDefault(t *testing.T) {
	t.Parallel()
	if devEnginePath != "" {
		t.Fatalf("devEnginePath = %q in source; it must be empty and set only "+
			"by a development build's -ldflags", devEnginePath)
	}
}

// --- help ---

// TestEveryVerbHasRealHelp is the structural version of "every command must
// have help": it fails if a verb falls through to the generic fallback rather
// than getting text of its own.
//
// The check is that the output says more than the one-line description the
// fallback would print. A verb with no case in verbUsage produces exactly
// that, and would otherwise pass a naive non-empty assertion.
func TestEveryVerbHasRealHelp(t *testing.T) {
	t.Parallel()
	for _, v := range VerbValues {
		t.Run(string(v), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			verbUsage(&buf, v)
			out := buf.String()

			if !strings.Contains(out, "usage: lath") {
				t.Errorf("help for %q has no usage line:\n%s", v, out)
			}
			// The fallback is "usage: lath <v>\n\n<one line>\n", four lines at
			// most. Real help explains what the verb does and its arguments.
			if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines < 3 {
				t.Errorf("help for %q is only %d lines: it looks like the fallback:\n%s",
					v, lines, out)
			}
		})
	}
}

// TestHelpFlagsAnswerAtEveryLevel pins the fix for a real gap: help was
// answered at the top level only, so `lath cache --help` fell through to that
// verb's argument parsing and reported an unknown cache action.
func TestHelpFlagsAnswerAtEveryLevel(t *testing.T) {
	for _, flag := range []string{FlagHelp, FlagHelpLong} {
		for _, v := range VerbValues {
			args := []string{string(v), flag}
			if got := run(args); got != exitOK {
				t.Errorf("run(%v) = %d; want %d: help must answer after every verb",
					args, got, exitOK)
			}
		}
		if got := run([]string{flag}); got != exitOK {
			t.Errorf("run([%q]) = %d; want %d", flag, got, exitOK)
		}
	}
}

// TestHelpFlagIsNotStolenFromTargets pins the boundary: after `run <target>`,
// a help flag belongs to the TARGET, not to lath. Intercepting it anywhere in
// the argument list would swallow a flag the definition meant to receive.
func TestHelpFlagIsNotStolenFromTargets(t *testing.T) {
	t.Parallel()
	// Position 0 after the verb is lath's; anything later is passed through.
	if !isHelpFlag(FlagHelp) || !isHelpFlag(FlagHelpLong) {
		t.Fatal("isHelpFlag does not recognise its own flags")
	}
	if isHelpFlag("help") {
		t.Error("the bare word `help` is a verb, not a flag: treating it as a flag " +
			"would make `lath run help` unreachable")
	}
}

// --- namespaces ---

// writeNS creates a definition with root files and namespace subdirectories.
func writeNS(t *testing.T, root map[string]string, namespaces map[string]map[string]string) string {
	t.Helper()
	dir := writeDef(t, root)
	for ns, files := range namespaces {
		nsDir := filepath.Join(dir, ns)
		if err := os.MkdirAll(nsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(nsDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// TestNamespacesAllowDuplicateFunctionNames is the property namespaces exist
// for: separate Go packages mean `Push` in two of them is legal, and both
// remain reachable. In one flat package the second would not compile.
func TestNamespacesAllowDuplicateFunctionNames(t *testing.T) {
	t.Parallel()
	dir := writeNS(t,
		map[string]string{goModFile: "module d", "a.go": "package main\n// Deploy ships it.\nfunc Deploy() error { return nil }\n"},
		map[string]map[string]string{
			"secrets": {"p.go": "package secrets\n// Push writes secrets.\nfunc Push() error { return nil }\n"},
			"db":      {"p.go": "package db\n// Push writes rows.\nfunc Push() error { return nil }\n"},
		})
	targets, _, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"deploy": "", "secrets push": "secrets", "db push": "db"}
	if len(targets) != len(want) {
		t.Fatalf("discovered %d targets, want %d: %+v", len(targets), len(want), targets)
	}
	for _, tgt := range targets {
		ns, ok := want[tgt.Command]
		if !ok {
			t.Errorf("unexpected command %q", tgt.Command)
			continue
		}
		if tgt.Namespace != ns {
			t.Errorf("%q: namespace = %q, want %q", tgt.Command, tgt.Namespace, ns)
		}
	}
}

func TestNamespaceConstraints(t *testing.T) {
	t.Parallel()
	rootDeploy := map[string]string{
		goModFile: "module d",
		"a.go":    "package main\n// Deploy ships it.\nfunc Deploy() error { return nil }\n",
	}
	for _, tc := range []struct {
		name       string
		root       map[string]string
		namespaces map[string]map[string]string
		nested     string // a directory to create two levels deep
		wantErr    error
		wantNS     []string // namespaces that must appear
		absentNS   []string // namespaces that must NOT appear
	}{
		{
			name: "internal is not a namespace, and does not break discovery",
			root: rootDeploy,
			namespaces: map[string]map[string]string{
				"internal": {"h.go": "package internal\n// Helper helps.\nfunc Helper() error { return nil }\n"},
			},
			absentNS: []string{"internal"},
		},
		{
			name: "a namespace clashing with a root target is rejected",
			root: rootDeploy,
			namespaces: map[string]map[string]string{
				"deploy": {"x.go": "package deploy\n// X does a thing.\nfunc X() error { return nil }\n"},
			},
			wantErr: ErrNamespaceClash,
		},
		{
			name:       "a namespace with no usable targets is skipped, not imported",
			root:       rootDeploy,
			namespaces: map[string]map[string]string{"empty": {"e.go": "package empty\n// Bad has a bad shape.\nfunc Bad() (int, error) { return 0, nil }\n"}},
			absentNS:   []string{"empty"},
		},
		{
			name: "a directory name that is not a valid Go identifier still works",
			root: rootDeploy,
			namespaces: map[string]map[string]string{
				"my-scripts": {"s.go": "package myscripts\n// Seed seeds.\nfunc Seed() error { return nil }\n"},
			},
			wantNS: []string{"my-scripts"},
		},
		{
			name:       "two levels deep is rejected",
			root:       rootDeploy,
			namespaces: map[string]map[string]string{"secrets": {"p.go": "package secrets\n// Push pushes.\nfunc Push() error { return nil }\n"}},
			nested:     "secrets/aws",
			wantErr:    ErrTooDeep,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := writeNS(t, tc.root, tc.namespaces)
			if tc.nested != "" {
				if err := os.MkdirAll(filepath.Join(dir, tc.nested), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			targets, _, err := discover(dir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("discover() = %v; want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			present := map[string]bool{}
			for _, tgt := range targets {
				if tgt.Namespace != "" {
					present[tgt.Namespace] = true
				}
			}
			for _, ns := range tc.wantNS {
				if !present[ns] {
					t.Errorf("namespace %q missing from %+v", ns, targets)
				}
			}
			for _, ns := range tc.absentNS {
				if present[ns] {
					t.Errorf("namespace %q should not be a namespace", ns)
				}
			}
		})
	}
}

// TestNamespaceHashCoversSubdirectories pins the failure that would be silent:
// a hash ignoring namespace files would not change when secrets/push.go was
// edited, and lath would run a stale binary indefinitely.
func TestNamespaceHashCoversSubdirectories(t *testing.T) {
	t.Parallel()
	build := func(push string) string {
		dir := writeNS(t,
			map[string]string{goModFile: "module d", "a.go": "package main\n// Deploy ships it.\nfunc Deploy() error { return nil }\n"},
			map[string]map[string]string{"secrets": {"p.go": push}})
		def, err := scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		return def.Hash
	}
	before := build("package secrets\n// Push pushes.\nfunc Push() error { return nil }\n")
	after := build("package secrets\n// Push pushes.\nfunc Push() error { return nil } // edited\n")
	if before == after {
		t.Fatal("editing a namespace file did not change the hash: lath would run a stale binary")
	}
}

// TestNamespacedDispatcherCompiles covers the generated file for every mix of
// root and namespaced targets. An import that is emitted but unused, or a
// namespace rendered without its import, fails to compile, with an error
// pointing at code the author never wrote.
func TestNamespacedDispatcherCompiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		root       map[string]string
		namespaces map[string]map[string]string
	}{
		{
			name: "root only",
			root: map[string]string{goModFile: "module d", "a.go": "package main\n// A does a.\nfunc A() error { return nil }\n"},
		},
		{
			name:       "namespace only",
			root:       map[string]string{goModFile: "module d"},
			namespaces: map[string]map[string]string{"ns": {"a.go": "package ns\n// A does a.\nfunc A() error { return nil }\n"}},
		},
		{
			name: "root and two namespaces, every signature shape",
			root: map[string]string{goModFile: "module d", "a.go": "package main\nimport \"context\"\n// A does a.\nfunc A() {}\n// B does b.\nfunc B(ctx context.Context, args ...string) error { return nil }\n"},
			namespaces: map[string]map[string]string{
				"one": {"a.go": "package one\nimport \"context\"\n// C does c.\nfunc C(ctx context.Context) error { return nil }\n"},
				"two": {"a.go": "package two\n// D does d.\nfunc D(args ...string) error { return nil }\n"},
			},
		},
		{
			name:       "no target takes a context",
			root:       map[string]string{goModFile: "module d", "a.go": "package main\n// A does a.\nfunc A() error { return nil }\n"},
			namespaces: map[string]map[string]string{"ns": {"a.go": "package ns\n// B does b.\nfunc B() error { return nil }\n"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := writeNS(t, tc.root, tc.namespaces)
			targets, _, err := discover(dir)
			if err != nil {
				t.Fatal(err)
			}
			path, err := generate(dir, targets)
			if err != nil {
				t.Fatal(err)
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.ParseFile(token.NewFileSet(), path, src, 0); err != nil {
				t.Fatalf("dispatcher does not parse: %v\n%s", err, src)
			}
			text := string(src)
			// An import must appear exactly when its namespace has targets.
			for _, ns := range namespacesOf(targets) {
				want := `"d/` + ns + `"`
				if !strings.Contains(text, want) {
					t.Errorf("namespace %q has targets but no import %s:\n%s", ns, want, text)
				}
			}
			for ns := range tc.namespaces {
				if !hasNamespace(targets, ns) && strings.Contains(text, `"d/`+ns+`"`) {
					t.Errorf("namespace %q has no targets but was imported", ns)
				}
			}
		})
	}
}

// isGoIdentifier reports whether name could be a Go function name.
func isGoIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || unicode.IsLetter(r):
		case i > 0 && unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}
