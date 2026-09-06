// Command lath runs the exported functions of a repository's deploy
// definition as commands.
//
// The definition is ordinary Go in a nested module. Writing an exported
// function IS declaring a command. There is no registration list, no switch to
// maintain, and no main() for the author to write:
//
//	// Deploy ships the API to an environment.
//	func Deploy(ctx context.Context, args ...string) error { … }
//
//	$ lath deploy prod
//
// lath parses the directory, generates a dispatcher, compiles both with the
// Go toolchain, caches the result by content hash, and hands over. There is no
// DSL and no interpreter. The Go compiler does the type-checking.
//
// NOTE: the name is a placeholder, chosen to avoid colliding with the separate
// release tool called volt.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ubgo/lath/pipeline"
)

// version is overridden at release time via -ldflags.
var version = "dev"

// devEnginePath points a scaffolded definition at a local engine checkout.
//
// SET AT BUILD TIME, and only for local development builds:
//
//	go build -ldflags "-X main.devEnginePath=/path/to/lath" ...
//
// Empty in any released binary, which is what makes a released binary scaffold
// definitions that resolve the engine from the module proxy like any other
// dependency. The Taskfile's build task sets it to the repository root; nothing
// else does.
//
// It is a build-time value rather than something detected at run time on
// purpose. Inferring "am I a development build?" from the binary's own path, or
// from whether a proxy fetch happens to succeed, guesses at a question the
// person doing the build already knows the answer to, and guesses wrong in
// the cases that matter (a `go install` from a clone looks exactly like a
// released binary).
var devEnginePath = ""

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the real entry point, separated from main so it is testable:
// arguments in, exit code out.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}

	// The help flags fold to the verb. They are the only flags lath answers.
	if isHelpFlag(args[0]) {
		usage(os.Stderr)
		return exitOK
	}

	verb := Verb(strings.ToLower(args[0]))
	if !verb.Valid() {
		fmt.Fprintf(os.Stderr, "lath: unknown command %q\n", args[0])
		if s := closestVerb(args[0]); s != "" {
			fmt.Fprintf(os.Stderr, "  did you mean `lath %s`?\n", s)
		}
		// A bare word is very often a target typed without `run`, which is the
		// single most likely mistake given the command model.
		fmt.Fprintf(os.Stderr, "  to run a target from your definition: lath %s %s\n",
			VerbRun, args[0])
		fmt.Fprintln(os.Stderr)
		usage(os.Stderr)
		return exitUsage
	}

	rest := args[1:]

	// `lath <verb> --help` prints that verb's help.
	//
	// Checked at rest[0] ONLY, which is what keeps `lath run deploy --help`
	// working: there the flag belongs to the target, not to lath, and
	// intercepting it anywhere in the argument list would swallow a flag the
	// definition meant to receive.
	if len(rest) > 0 && isHelpFlag(rest[0]) {
		verbUsage(os.Stderr, verb)
		return exitOK
	}

	switch verb {
	case VerbHelp:
		usage(os.Stderr)
		return exitOK

	case VerbVersion:
		fmt.Printf("lath %s\n", version)
		return exitOK

	case VerbInit:
		// Takes no arguments. Where the engine resolves from is a BUILD-TIME
		// decision carried in devEnginePath, not something a caller picks per
		// invocation. See its declaration above.
		if len(rest) > 0 {
			fmt.Fprintf(os.Stderr,
				"lath: `%s` takes no arguments (got %q)\n"+
					"  where the engine resolves from is decided when lath is built.\n",
				VerbInit, rest[0])
			return exitUsage
		}
		return initDefinition(definitionDir)

	case VerbTUI:
		return runTUI(rest)

	case VerbCache:
		rest, asJSON := takeJSONFlag(rest)
		if asJSON {
			entries, err := listCache()
			if err != nil {
				fmt.Fprintln(os.Stderr, "lath:", err)
				return exitInternalErr
			}
			if err := printCacheJSON(entries); err != nil {
				fmt.Fprintln(os.Stderr, "lath:", err)
				return exitInternalErr
			}
			return exitOK
		}
		action := CacheActionList
		if len(rest) > 0 {
			action = CacheAction(strings.ToLower(rest[0]))
		}
		if !action.Valid() {
			fmt.Fprintf(os.Stderr,
				"lath: unknown cache action %q (want one of %v, or none to list)\n",
				rest[0], CacheActionValues)
			return exitUsage
		}
		return runCache(action)

	case VerbList:
		_, asJSON := takeJSONFlag(rest)
		targets, rejected, err := discover(definitionDir)
		if !asJSON {
			// Suppressed under --json: the warning is prose on stderr, and a
			// consumer gets the same information in the payload's `rejected`.
			warnRejected(rejected)
		}
		if err != nil {
			return reportDiscoveryError(err)
		}
		if asJSON {
			if err := printTargetsJSON(targets, rejected); err != nil {
				fmt.Fprintln(os.Stderr, "lath:", err)
				return exitInternalErr
			}
			return exitOK
		}
		listTargets(targets)
		return exitOK

	case VerbPlan, VerbDryRun:
		// Identical to run, with the engine told to describe or to rehearse.
		// Set here rather than parsed by the definition so it works on one
		// that never offered the choice, and inherited by the compiled
		// definition through the environment; see definitionEnv.
		mode := pipeline.ModeEnvPlan
		if verb == VerbDryRun {
			mode = pipeline.ModeEnvDryRun
		}
		if err := os.Setenv(pipeline.ModeEnvVar, mode); err != nil {
			fmt.Fprintln(os.Stderr, "lath:", err)
			return exitInternalErr
		}
		// Taken from anywhere in the arguments, unlike everything else after a
		// target, which is passed through untouched. The listing belongs to
		// lath rather than to the definition, so the flag that formats it does
		// too, and a reader who types it last should not be told they typed it
		// in the wrong place.
		var asJSON bool
		if rest, asJSON = takeJSONFlag(rest); asJSON {
			if err := os.Setenv(pipeline.PlanFormatEnvVar, pipeline.PlanFormatJSON); err != nil {
				fmt.Fprintln(os.Stderr, "lath:", err)
				return exitInternalErr
			}
		}
		if len(rest) == 0 {
			targets, rejected, err := discover(definitionDir)
			warnRejected(rejected)
			if err != nil {
				return reportDiscoveryError(err)
			}
			fmt.Fprintf(os.Stderr, "lath: %s needs a target\n\n", verb)
			listTargets(targets)
			return exitUsage
		}
		return runTarget(rest)

	case VerbRun:
		targets, rejected, err := discover(definitionDir)
		warnRejected(rejected)
		if err != nil {
			return reportDiscoveryError(err)
		}
		if len(rest) == 0 {
			fmt.Fprintf(os.Stderr, "lath: %s needs a target\n\n", VerbRun)
			listTargets(targets)
			return exitUsage
		}
		// A namespace with nothing after it lists that group. `lath run` alone
		// already lists everything; the same rule one level down, an
		// incomplete command shows what would complete it. Answered by the
		// runner, which already knows every target, so no compile is needed.
		group := strings.ToLower(rest[0])
		if hasNamespace(targets, group) && (len(rest) == 1 || isHelpFlag(rest[1])) {
			fmt.Fprintf(os.Stderr, "targets in %q:\n", group)
			listNamespace(targets, group, "  ")
			fmt.Fprintf(os.Stderr, "\nrun one with: lath %s %s <target> [args...]\n",
				VerbRun, group)
			if len(rest) == 1 {
				return exitUsage
			}
			return exitOK
		}
		return runTarget(rest)

	default:
		// Unreachable: Valid() gates every path here. Present so adding a Verb
		// without a case is a visible gap rather than a silent no-op.
		fmt.Fprintf(os.Stderr, "lath: %q is declared but not implemented\n", verb)
		return exitInternalErr
	}
}

// runTarget compiles the definition if needed and hands over to it.
//
// args is everything after `run`, passed through untouched, the definition's
// dispatcher owns them entirely, so a target may accept arguments that look
// like lath's own verbs without ambiguity.
func runTarget(args []string) int {
	// Taken before anything else so the preflight refusal happens before a
	// build, an advertisement, or any step: an operator must never discover
	// mid-deploy that nothing could have attached.
	args, debugging, attachWait, flagErr := takeDebugFlag(args)
	if flagErr != nil {
		fmt.Fprintln(os.Stderr, "lath:", flagErr)
		return exitUsage
	}
	if debugging {
		if err := debugPreflight(); err != nil {
			fmt.Fprintln(os.Stderr, "lath:", err)
			return exitUsage
		}
	}

	targets, rejected, err := discover(definitionDir)
	warnRejected(rejected)
	if err != nil {
		return reportDiscoveryError(err)
	}

	def, err := scan(definitionDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}

	// Looked up by hash SUFFIX rather than by rebuilding the filename.
	//
	// Two reasons. The readable prefix is decorative, so renaming a repository
	// must not orphan an entry that is still valid. And more importantly, the
	// name never has to be computed on the warm path, deriving it shells out
	// to git, and doing that on every invocation would tax every run to
	// produce something only a human reading the cache directory ever sees.
	binPath := cacheLookup(def.Hash)
	if binPath == "" {
		binPath = cacheEntryPath(def)
		fmt.Fprintf(os.Stderr, "lath: compiling %s (%d targets: %s) -> %s\n",
			def.Dir, len(targets), strings.Join(commandNames(targets), " "), def.Hash)

		generated, genErr := generate(def.Dir, targets)
		if genErr != nil {
			fmt.Fprintln(os.Stderr, "lath:", genErr)
			return exitInternalErr
		}
		// The dispatcher is build input, not an artefact to keep: an author
		// must never see generated code in their diff.
		//
		// NOT a defer. handOff calls syscall.Exec on unix, which REPLACES the
		// process image, deferred functions never run. Cleanup therefore
		// happens explicitly on every path out of this block.
		buildErr := build(def, binPath, os.Stderr, os.Stdout)
		if rmErr := removeGenerated(generated); rmErr != nil {
			fmt.Fprintln(os.Stderr, "lath:", rmErr)
		}
		if err := buildErr; err != nil {
			switch {
			case errors.Is(err, ErrCompile):
				// The compiler already printed the diagnostic; restating it
				// would push the useful line up-screen.
				return exitCompile
			case errors.Is(err, ErrToolchain):
				fmt.Fprintln(os.Stderr,
					"lath: the Go toolchain is required to build a definition")
				return exitInternalErr
			default:
				fmt.Fprintln(os.Stderr, "lath:", err)
				return exitInternalErr
			}
		}

		// Recorded after a successful build only. A failure here is reported
		// but never fatal: the manifest exists so a person can make sense of
		// the cache directory, and losing that must not stop a deploy.
		if err := writeManifest(binPath, def, targets); err != nil {
			fmt.Fprintln(os.Stderr, "lath: could not record cache manifest:", err)
		}
	}

	if debugging {
		root, rootErr := filepath.Abs(definitionDir)
		if rootErr != nil {
			fmt.Fprintln(os.Stderr, "lath:", rootErr)
			return exitInternalErr
		}
		// Advertised here, immediately before the hand-off, because
		// syscall.Exec keeps this PID: the session lath registers a moment
		// from now describes the definition that replaces it. Registering
		// earlier would leave a live-looking session behind if the build
		// failed.
		withSocket, sessErr := openDebugSession(args, filepath.Dir(root), attachWait)
		if sessErr != nil {
			fmt.Fprintln(os.Stderr, "lath:", sessErr)
			return exitInternalErr
		}
		args = withSocket
	}

	if err := handOff(binPath, args); err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	return exitOK // unreachable on unix, where handOff replaces the process
}

// reportDiscoveryError maps a discovery failure to advice plus an exit code.
func reportDiscoveryError(err error) int {
	switch {
	case errors.Is(err, ErrNoDefinition):
		fmt.Fprintf(os.Stderr,
			"lath: no definition in ./%s\n"+
				"  a definition is a nested Go module with at least one exported function.\n"+
				"  run `lath %s` to create one.\n",
			definitionDir, VerbInit)
		return exitUsage
	case errors.Is(err, ErrNoTargets), errors.Is(err, ErrDuplicateTarget):
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitUsage
	default:
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
}

// closestVerb suggests the nearest runner verb to a mistyped one, or "" when
// nothing is close enough to be worth guessing at.
//
// A wrong suggestion is worse than none. It sends the reader after a command
// they did not want, so the threshold is deliberately tight.
func closestVerb(input string) string {
	best, bestDist := "", -1
	limit := len(input) / maxSuggestionErrorRatio
	if limit < 1 {
		limit = 1
	}
	for _, v := range VerbValues {
		d := editDistance(strings.ToLower(input), string(v))
		if d <= limit && (bestDist < 0 || d < bestDist) {
			best, bestDist = string(v), d
		}
	}
	return best
}

// editDistance is Levenshtein distance, used only to rank suggestions. Two
// rolling rows rather than a full matrix: the inputs are option names, so the
// allocation matters less than the code staying short enough to verify by eye.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// usage prints lath's own commands.
//
// Generated from VerbValues so the listing can never drift from the set the
// switch actually handles. A verb added without a description shows up in the
// test requiring every verb to be documented.
//
// Definition targets are deliberately NOT listed: naming them requires parsing
// a definition, and usage has to work in a repository that has none yet, which
// is exactly when someone needs to be told `init` exists.
func usage(w io.Writer) {
	fmt.Fprintf(w, "usage: lath <command> [args...]\n\n")
	for _, v := range VerbValues {
		fmt.Fprintf(w, "  %-10s %s\n", v, verbDoc(v))
	}
	fmt.Fprintf(w, "\n  %s %s [%s|%s]\n",
		"cache", "", CacheActionPrune, CacheActionClean)
	fmt.Fprintf(w,
		"\nTargets are the exported functions in ./%s. They run behind `%s`, so a\n"+
			"target may be named anything: including a word lath uses itself.\n",
		definitionDir, VerbRun)
}

// listTargets prints the discovered targets, grouped by namespace.
//
// Root targets first, then each namespace. Grouping is the whole point of
// namespaces from a reader's perspective: fifteen unrelated commands in one
// undifferentiated list has no structure to read.
func listTargets(targets []Target) {
	fmt.Fprintf(os.Stderr, "targets (from ./%s):\n", definitionDir)
	for _, t := range targets {
		if t.Namespace == "" {
			fmt.Fprintf(os.Stderr, "  %-18s %s\n", t.Command, describe(t))
		}
	}
	for _, ns := range namespacesOf(targets) {
		fmt.Fprintf(os.Stderr, "\n  %s\n", ns)
		listNamespace(targets, ns, "    ")
	}
	fmt.Fprintf(os.Stderr, "\nrun one with: lath %s <target> [args...]\n", VerbRun)
}

// listNamespace prints the targets inside one namespace.
func listNamespace(targets []Target, namespace, indent string) {
	for _, t := range targets {
		if t.Namespace != namespace {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s%-16s %s\n",
			indent, strings.TrimPrefix(t.Command, namespace+commandSeparator), describe(t))
	}
}

// namespacesOf returns the distinct namespaces present, in target order.
//
// Targets arrive sorted by command, so namespaces come out alphabetically and
// the listing is stable between runs.
func namespacesOf(targets []Target) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range targets {
		if t.Namespace == "" || seen[t.Namespace] {
			continue
		}
		seen[t.Namespace] = true
		out = append(out, t.Namespace)
	}
	return out
}

// hasNamespace reports whether name is a namespace among targets.
func hasNamespace(targets []Target, name string) bool {
	for _, t := range targets {
		if t.Namespace == name {
			return true
		}
	}
	return false
}

// isHelpFlag reports whether s is one of the help flags.
//
// Centralised because help must be answerable at every level, top level, after
// any verb, and after a namespace. Scattering the comparison is how one level
// ends up not answering it.
func isHelpFlag(s string) bool {
	return s == FlagHelp || s == FlagHelpLong
}

// verbUsage prints detailed help for one verb.
//
// A switch rather than a map so adding a Verb without help is a visible gap;
// the fallback prints the one-line description rather than nothing, so a new
// verb is never completely silent.
func verbUsage(w io.Writer, v Verb) {
	switch v {
	case VerbPlan:
		fmt.Fprintf(w, "usage: lath %s <target> [args...]\n\n", VerbPlan)
		fmt.Fprintf(w,
			"Lists the steps the target's pipeline would run, and runs none of them.\n\n"+
				"Works on any definition, including one whose author never wrote a plan\n"+
				"target: the runner sets %s=%s and the engine describes itself instead\n"+
				"of executing. Validation still runs, so a mis-wired pipeline is\n"+
				"reported rather than described.\n\n"+
				"Code in a target that runs BEFORE it builds its pipeline still runs.\n"+
				"Every pipeline is forced to a dry run as well, so those steps suppress\n"+
				"their side effects, but a target that writes a file by hand before\n"+
				"assembling anything will still write it.\n\n"+
				"  %s   the same steps as JSON on stdout, for a script\n\n"+
				"Unlike every other argument after a target, %s is taken by lath\n"+
				"wherever it appears: the listing is lath's output, so the flag that\n"+
				"formats it is lath's too.\n",
			pipeline.ModeEnvVar, pipeline.ModeEnvPlan, jsonFlag, jsonFlag)

	case VerbDryRun:
		fmt.Fprintf(w, "usage: lath %s <target> [args...]\n\n", VerbDryRun)
		fmt.Fprintf(w,
			"Runs the target with every pipeline forced into a dry run: each step's\n"+
				"logic executes and reports what it WOULD do, without the side effect.\n\n"+
				"Overrides whatever mode the definition chose, and only ever in the\n"+
				"safer direction: this can turn a real deploy into a rehearsal, never\n"+
				"the reverse.\n")

	case VerbRun:
		fmt.Fprintf(w, "usage: lath %s <target> [args...]\n\n", VerbRun)
		fmt.Fprintf(w,
			"Runs a target from ./%s. Everything after the target name is passed to\n"+
				"it untouched, so a target may take any arguments it likes: including\n"+
				"ones that look like lath's own commands.\n\n"+
				"Targets in a subdirectory are grouped:  lath %s secrets push\n"+
				"A group with nothing after it lists what is inside it.\n\n"+
				"  lath %s              list every target\n",
			definitionDir, VerbRun, VerbList)
	case VerbTUI:
		fmt.Fprintf(w, "usage: lath %s [session]\n\n", VerbTUI)
		fmt.Fprintf(w,
			"Steps through a pipeline one step at a time, pausing after each until\n"+
				"you say to go on.\n\n"+
				"  lath %s                      pick a target and step through it\n"+
				"  lath %s deploy local --apply run that, stepping\n"+
				"  lath %s <session>            attach to a session already waiting\n\n"+
				"With nothing waiting it starts the run itself, here, in this terminal;\n"+
				"the run's own output goes to a log so it cannot scribble over the panel.\n"+
				"With one session waiting it attaches to it; with several it asks which.\n\n"+
				"A run started elsewhere with `lath %s <target> %s` is picked up the\n"+
				"same way: useful when it must run somewhere this terminal is not.\n\n"+
				"Keys at each pause: n next, r rerun, c continue, q quit, s state.\n\n"+
				"Rerunning REPLAYS a step, it does not undo one. A step that has not\n"+
				"declared itself safe to replay asks first.\n\n",
			VerbTUI, VerbTUI, VerbTUI, VerbRun, debugFlag)
	case VerbCache:
		fmt.Fprintf(w, "usage: lath %s [%s|%s]\n\n", VerbCache, CacheActionPrune, CacheActionClean)
		fmt.Fprintf(w,
			"  (no action)   list cached pipelines, with the project each belongs to\n"+
				"  %-13s remove entries whose project directory no longer exists\n"+
				"  %-13s remove every entry\n\n"+
				"Compiled pipelines are cached per definition-content hash. Prune only\n"+
				"touches orphans: an entry whose project still exists may be the binary\n"+
				"that project runs, and removing it would silently cost a rebuild.\n",
			CacheActionPrune, CacheActionClean)
	case VerbInit:
		fmt.Fprintf(w, "usage: lath %s\n\n", VerbInit)
		fmt.Fprintf(w,
			"Scaffolds ./%s with a working starter definition and its go.mod.\n\n"+
				"Takes no arguments. Refuses if the directory already exists, because a\n"+
				"definition holds real deploy logic.\n",
			definitionDir)
	case VerbList:
		fmt.Fprintf(w, "usage: lath %s\n\n", VerbList)
		fmt.Fprintf(w,
			"Lists the exported functions in ./%s, grouped by subdirectory, with the\n"+
				"first sentence of each doc comment. Run one with `lath %s <target>`.\n",
			definitionDir, VerbRun)
	case VerbVersion:
		fmt.Fprintf(w, "usage: lath %s\n\nPrints the lath version.\n", VerbVersion)
	case VerbHelp:
		// The writer, not os.Stderr: verbUsage's whole contract is that it
		// writes where it is told, and a case that ignores that is invisible
		// until something tries to capture the output.
		usage(w)
	default:
		fmt.Fprintf(w, "usage: lath %s\n\n%s\n", v, verbDoc(v))
	}
}

// verbDoc describes a verb. A switch rather than a map so adding a Verb without
// describing it is a visible gap rather than a blank line.
func verbDoc(v Verb) string {
	switch v {
	case VerbRun:
		return "run a target from the definition"
	case VerbPlan:
		return "list the steps a target would run, without running them"
	case VerbDryRun:
		return "run a target with every step's side effects suppressed"
	case VerbInit:
		return "scaffold a definition in ./" + definitionDir
	case VerbList:
		return "list the targets this definition exposes"
	case VerbTUI:
		return "step through a pipeline one step at a time"
	case VerbCache:
		return "inspect or prune the compiled-pipeline cache"
	case VerbVersion:
		return "print the lath version"
	case VerbHelp:
		return "print this help"
	default:
		return ""
	}
}

// describe returns a target's help text, substituting a placeholder when the
// author wrote no doc comment.
func describe(t Target) string {
	if strings.TrimSpace(t.Doc) == "" {
		return undocumentedTarget
	}
	return t.Doc
}

func commandNames(targets []Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Command)
	}
	return out
}

// initDefinition scaffolds a definition and reports what to do next.
//
// Where the engine resolves from is decided entirely by devEnginePath, a
// build-time value. There is no runtime option: a development build points
// definitions at its own checkout, a released build lets them resolve from the
// module proxy, and a caller has no say in either. One way to do it.
//
// Returns an exit code rather than an error because it is a terminal command:
// the guidance printed here is more useful than any error a caller could
// re-render.
func initDefinition(dir string) int {
	enginePath := devEnginePath

	if err := scaffold(dir, enginePath); err != nil {
		if errors.Is(err, ErrAlreadyInitialised) {
			fmt.Fprintf(os.Stderr,
				"lath: ./%s already exists: refusing to overwrite a definition\n"+
					"  it holds real deploy logic, so nothing here will replace it.\n"+
					"  to start over: rm -rf ./%s && lath %s\n",
				dir, dir, VerbInit)
			return exitUsage
		}
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}

	fmt.Printf("created ./%s\n", dir)
	fmt.Printf("  %-16s the definition: every exported function is a command\n", starterFileName)
	fmt.Printf("  %-16s module %s\n", goModFile, definitionModuleName)
	if enginePath != "" {
		// Echoed so absolute paths in a generated file are never unexplained.
		fmt.Printf("  %-16s -> %s\n", "replace", enginePath)
	}

	if err := tidy(dir); err != nil {
		fmt.Fprintf(os.Stderr, "\nlath: could not resolve the engine modules:\n  %v\n", err)
		if enginePath == "" {
			fmt.Fprintf(os.Stderr,
				"\n  This binary has no engine checkout baked in, so the definition\n"+
					"  resolves the engine from the module proxy. If you are developing\n"+
					"  lath itself, build a development binary from your checkout with\n"+
					"  `task build`, then re-run this.\n")
		} else {
			fmt.Fprintf(os.Stderr,
				"\n  Using the checkout at %s.\n"+
					"  Check it contains %v, then run:\n"+
					"    cd %s && GOWORK=off go mod tidy\n",
				enginePath, engineModules, dir)
		}
		return exitUsage
	}

	fmt.Printf("  %-16s dependencies resolved\n", goSumFile)
	fmt.Printf("\nnext: lath %s\n", VerbList)
	return exitOK
}
