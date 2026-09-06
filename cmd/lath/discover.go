package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Signature is the shape of a target function. A closed set: the generated
// dispatcher must know exactly how to call each one, so anything outside this
// list is rejected at discovery with a message naming what IS supported ,
// rather than producing a generated file that fails to compile with an error
// pointing at code the author never wrote.
type Signature string

const (
	// SigPlain is `func Name()`.
	SigPlain Signature = "func()"
	// SigError is `func Name() error`.
	SigError Signature = "func() error"
	// SigCtxError is `func Name(context.Context) error`.
	SigCtxError Signature = "func(context.Context) error"
	// SigArgsError is `func Name(...string) error`.
	SigArgsError Signature = "func(...string) error"
	// SigCtxArgsError is `func Name(context.Context, ...string) error`.
	SigCtxArgsError Signature = "func(context.Context, ...string) error"
)

// errorTypeName is the identifier a dispatchable target must return.
//
// Matched by NAME because this reads the syntax tree, before any type
// checking: at this point "error" is a bare identifier, not the interface. A
// target returning a locally-shadowed type called error would satisfy this and
// then fail to compile in the generated dispatcher, an acceptable trade for
// not type-checking the whole package just to discover targets.
const errorTypeName = "error"

// SignatureValues is the canonical iteration order, used to build the error
// message shown when a function's shape is not supported.
var SignatureValues = []Signature{SigPlain, SigError, SigCtxError, SigArgsError, SigCtxArgsError}

// Statement renders the Go statement that invokes a target with this signature.
//
// Why this lives in Go rather than in the dispatcher template: a template
// branching on signature values would compare against string literals, so
// adding a Signature without adding a branch would silently generate a case
// that calls nothing. Here the switch is exhaustive by test (see
// TestEverySignatureRenders) and an unhandled value is a loud error, not a
// silently empty case body.
//
// The identifiers it emits, ctx and err, are declared by the dispatcher
// template; the two must agree, which is why they are named in one place here.
//
// argsExpr is the slice expression a variadic target receives. It is passed in
// rather than fixed because the offset differs by depth: a root target consumes
// os.Args[2:], a namespaced one os.Args[3:]. Using an expression instead of a
// declared variable also removes any possibility of an unused-variable error in
// the generated file.
func (s Signature) Statement(funcName, argsExpr string) (string, error) {
	switch s {
	case SigPlain:
		return funcName + "()", nil
	case SigError:
		return "err = " + funcName + "()", nil
	case SigCtxError:
		return "err = " + funcName + "(ctx)", nil
	case SigArgsError:
		return "err = " + funcName + "(" + argsExpr + "...)", nil
	case SigCtxArgsError:
		return "err = " + funcName + "(ctx, " + argsExpr + "...)", nil
	default:
		return "", fmt.Errorf("signature %q has no call form", s)
	}
}

// Identifiers from the Go language and standard library that discovery matches
// against. Named because they appear in a matcher where a typo produces a
// silent misclassification. A function quietly not becoming a command, rather
// than any error. Kept verbatim from upstream: these are Go's spellings, not
// ours to choose.
const (
	// goPkgContext is the package qualifier in `context.Context`.
	goPkgContext = "context"
	// goTypeContext is the type name in `context.Context`.
	goTypeContext = "Context"
	// goTypeString is the element type of the supported variadic parameter.
	goTypeString = "string"
	// goTestMainFunc is Go's test-harness entry point. Exported by name but
	// never a command, and it lives in a non-test file often enough to matter.
	goTestMainFunc = "TestMain"
	// goTestFileSuffix marks a test file.
	goTestFileSuffix = "_test.go"
)

// Target is one exported function discovered in the definition, exposed as a
// command.
type Target struct {
	// Func is the Go identifier, e.g. "Deploy".
	Func string
	// Namespace is the directory the target came from, or "" for a target in
	// the definition root. It is the first word the user types for a grouped
	// command: `lath run secrets push` has Namespace "secrets".
	Namespace string
	// Package is the Go package NAME of the namespace, which need not match
	// the directory: a directory called "my-scripts" cannot be `package
	// my-scripts`, since Go identifiers exclude dashes. Empty for a root
	// target. The generated import is aliased to the directory name so the two
	// can differ safely.
	Package string
	// Command is what the user types, "deploy", or "secrets push" for a
	// namespaced target. Matching is case-insensitive.
	Command string
	// Doc is the first line of the function's doc comment, shown in the list.
	Doc string
	// Sig is how the dispatcher must call it.
	Sig Signature
	// File is where it was found, for error messages.
	File string
}

// discover parses every non-test .go file in dir and returns the exported
// functions usable as commands.
//
// Why AST parsing rather than requiring the author to register commands: a
// registration list is a second place to update, and the failure mode when it
// drifts is a command that silently does not exist. Reading the source means
// writing the function IS declaring the command, there is nothing to keep in
// sync.
//
// Invariant: returns targets sorted by command name, so listings and generated
// code are byte-stable across runs.
//
// Exported functions whose shape cannot be dispatched are returned in rejected
// rather than dropped. Reporting them is not cosmetic: an author who mistypes
// a signature, `args []string` instead of `args ...string`, otherwise sees
// their command simply not exist, with no indication the file was even read.
func discover(dir string) (targets []Target, rejected []string, err error) {
	// Checked before parsing so a missing directory produces the actionable
	// "no definition here" message rather than parser.ParseDir's raw
	// "open .lath: no such file or directory". discover runs BEFORE scan, so
	// without this the friendly message in reportDiscoveryError is unreachable.
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("discover %s: %w", dir, ErrNoDefinition)
	}

	root, rootRejected, err := discoverPackage(dir, "")
	if err != nil {
		return nil, nil, err
	}
	targets = append(targets, root...)
	rejected = append(rejected, rootRejected...)

	namespaces, err := namespaceDirs(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, ns := range namespaces {
		nsTargets, nsRejected, err := discoverPackage(filepath.Join(dir, ns), ns)
		if err != nil {
			return nil, nil, err
		}
		rejected = append(rejected, nsRejected...)
		// A directory with no usable targets is NOT a namespace: importing it
		// would leave an unused import and the generated dispatcher would not
		// compile. Its rejected functions are returned to the caller, which
		// warns. A whole namespace vanishing in silence is the worst case,
		// because there is nothing left to hint the directory was read.
		if len(nsTargets) == 0 {
			continue
		}
		targets = append(targets, nsTargets...)
	}

	if len(targets) == 0 {
		msg := fmt.Sprintf("discover %s: no exported functions to run", dir)
		if len(rejected) > 0 {
			msg += "\nfound, but not usable:\n" + strings.Join(rejected, "\n") +
				"\nsupported shapes:\n  " + joinSignatures()
		}
		return nil, rejected, fmt.Errorf("%s: %w", msg, ErrNoTargets)
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].Command < targets[j].Command })

	// Two exported functions differing only in case map to one command, and
	// the second would silently shadow the first.
	for i := 1; i < len(targets); i++ {
		if targets[i].Command == targets[i-1].Command {
			return nil, rejected, fmt.Errorf("discover %s: %s (%s) and %s (%s) both map to command %q: %w",
				dir, targets[i-1].Func, targets[i-1].File,
				targets[i].Func, targets[i].File, targets[i].Command, ErrDuplicateTarget)
		}
	}

	// A namespace and a root target claiming the same word is rejected rather
	// than resolved by precedence: a precedence rule means one of the two is
	// silently unreachable, which is the failure the `run` namespace exists to
	// prevent.
	rootNames := map[string]Target{}
	for _, t := range targets {
		if t.Namespace == "" {
			rootNames[t.Command] = t
		}
	}
	for _, t := range targets {
		if t.Namespace == "" {
			continue
		}
		if clash, ok := rootNames[t.Namespace]; ok {
			return nil, rejected, fmt.Errorf(
				"discover %s: namespace %q and target %s (%s) both claim the command %q: "+
					"rename one: %w",
				dir, t.Namespace, clash.Func, clash.File, t.Namespace, ErrNamespaceClash)
		}
	}

	return targets, rejected, nil
}

// namespaceDirs lists the subdirectories of dir that may hold targets.
//
// Rejects anything nested deeper than maxNamespaceDepth by name rather than
// ignoring it: a directory placed two levels down was placed there on purpose,
// and silently exposing none of its functions is the worst outcome.
func namespaceDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Dot- and underscore-prefixed directories are invisible to the Go
		// tool itself, so they cannot hold a buildable package.
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if name == internalDirName {
			continue
		}
		nested, err := os.ReadDir(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("discover %s/%s: %w", dir, name, err)
		}
		for _, n := range nested {
			if n.IsDir() && n.Name() != internalDirName &&
				!strings.HasPrefix(n.Name(), ".") && !strings.HasPrefix(n.Name(), "_") {
				return nil, fmt.Errorf(
					"discover %s: %s/%s is nested %d levels deep; targets may live at most "+
						"%d level below the definition root: %w",
					dir, name, n.Name(), maxNamespaceDepth+1, maxNamespaceDepth, ErrTooDeep)
			}
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// discoverPackage parses one directory and returns its targets.
//
// namespace is "" for the definition root. The returned rejected slice
// describes exported functions whose shape the dispatcher cannot call, always
// reported, never silently skipped, because a command that quietly does not
// exist is the worst available outcome.
func discoverPackage(dir, namespace string) (targets []Target, rejected []string, err error) {
	fset := token.NewFileSet()
	pkgs, parseErr := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		// Test files are excluded: a Test* function is not a command, and an
		// exported helper in a _test.go file is not reachable from the built
		// binary anyway. The generated dispatcher is excluded because it is
		// derived from this scan, reading it back would be circular.
		return !strings.HasSuffix(fi.Name(), goTestFileSuffix) && fi.Name() != generatedFileName
	}, parser.ParseComments)
	if parseErr != nil {
		return nil, nil, fmt.Errorf("discover %s: %w", dir, parseErr)
	}

	for pkgName, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil { // methods are not commands
					continue
				}
				name := fn.Name.Name
				if !ast.IsExported(name) || name == goTestMainFunc {
					continue
				}
				label := filepath.Base(path)
				if namespace != "" {
					label = filepath.Join(namespace, label)
				}
				sig, ok := classify(fn.Type)
				if !ok {
					rejected = append(rejected, fmt.Sprintf(
						"  %s (%s): unsupported signature", name, label))
					continue
				}
				command := strings.ToLower(name)
				if namespace != "" {
					command = namespace + commandSeparator + command
				}
				targets = append(targets, Target{
					Func:      name,
					Namespace: namespace,
					Package:   packageNameFor(namespace, pkgName),
					Command:   command,
					Doc:       firstDocLine(fn.Doc),
					Sig:       sig,
					File:      label,
				})
			}
		}
	}
	return targets, rejected, nil
}

// packageNameFor returns the Go package name to qualify calls with, or "" for
// a root target.
func packageNameFor(namespace, pkgName string) string {
	if namespace == "" {
		return ""
	}
	return pkgName
}

// classify maps a function's declared type to a Signature, reporting false for
// any shape the dispatcher cannot call.
func classify(ft *ast.FuncType) (Signature, bool) {
	// Results: none, or exactly one `error`.
	returnsError := false
	if ft.Results != nil {
		if len(ft.Results.List) != 1 {
			return "", false
		}
		if id, ok := ft.Results.List[0].Type.(*ast.Ident); !ok || id.Name != errorTypeName {
			return "", false
		}
		returnsError = true
	}

	params := []ast.Expr{}
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			// One entry per NAME, so `func(a, b string)` counts as two.
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				params = append(params, f.Type)
			}
		}
	}

	switch len(params) {
	case 0:
		if returnsError {
			return SigError, true
		}
		return SigPlain, true
	case 1:
		if !returnsError {
			return "", false
		}
		if isContext(params[0]) {
			return SigCtxError, true
		}
		if isVariadicString(params[0]) {
			return SigArgsError, true
		}
	case 2:
		if returnsError && isContext(params[0]) && isVariadicString(params[1]) {
			return SigCtxArgsError, true
		}
	}
	return "", false
}

// isContext reports whether e is context.Context. Matched by selector text
// rather than by resolved type because discovery deliberately does not type-
// check: a definition that does not compile must fail at `go build` with the
// compiler's own message, not with a confusing parse-time complaint.
func isContext(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == goPkgContext && sel.Sel.Name == goTypeContext
}

// isVariadicString reports whether e is `...string`.
func isVariadicString(e ast.Expr) bool {
	ell, ok := e.(*ast.Ellipsis)
	if !ok {
		return false
	}
	id, ok := ell.Elt.(*ast.Ident)
	return ok && id.Name == goTypeString
}

// firstDocLine extracts the leading sentence of a doc comment for the listing.
// Go doc convention starts with the identifier, which is redundant in a list
// keyed by that identifier, so it is stripped.
func firstDocLine(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	text := strings.TrimSpace(doc.Text())
	if text == "" {
		return ""
	}
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if i := strings.IndexByte(line, '.'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

func joinSignatures() string {
	out := make([]string, 0, len(SignatureValues))
	for _, s := range SignatureValues {
		out = append(out, string(s))
	}
	return strings.Join(out, "\n  ")
}

// warnRejected reports exported functions that were found but cannot be run.
//
// Written to stderr rather than returned as an error: the other targets are
// perfectly usable, so this must not stop the run. But it must not be silent
// either. A mistyped signature otherwise presents as a command that never
// existed, and the author's next move is to doubt the file was read at all.
func warnRejected(rejected []string) {
	if len(rejected) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "lath: %d exported function(s) found but not usable as commands:\n%s\n"+
		"supported shapes:\n  %s\n\n",
		len(rejected), strings.Join(rejected, "\n"), joinSignatures())
}
