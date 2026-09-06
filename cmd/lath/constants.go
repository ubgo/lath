package main

import "io/fs"

// Rule: NO BARE STRINGS for closed sets. Every literal below appears at more
// than one use site or defines part of this tool's contract with a repository.

// Verb is a command lath itself handles.
//
// Every verb lives at the top level, and a definition's targets live behind
// VerbRun. That split is the whole command model: there is exactly one rule ,
// targets go after `run`, and consequently no reserved words at all. A
// definition may export a function named Init, List, or anything else, because
// its commands never occupy the same namespace as lath's.
//
// The rejected alternative was the reverse: bare targets with lath's own
// commands hidden behind a prefix. It keeps the common path four characters
// shorter, at the cost of a reserved word, a fatal error when a definition
// collides with it, and a prefix that means nothing until explained.
type Verb string

const (
	// VerbRun executes a target from the definition. Everything after it is
	// passed through untouched, so a target may take any arguments it likes,
	// including ones that look like lath's own verbs.
	VerbRun Verb = "run"
	// VerbInit scaffolds a definition directory.
	VerbInit Verb = "init"
	// VerbList prints the commands a definition exposes.
	VerbList Verb = "list"
	// VerbCache manages the compiled-pipeline cache.
	VerbCache Verb = "cache"
	// VerbTUI attaches to a run started with --debug and steps through it.
	VerbTUI Verb = "tui"
	// VerbPlan lists the steps a target would run, and runs none of them.
	//
	// Works on ANY definition, including one whose author never wrote a plan
	// target: the runner forces the mode, the engine lists instead of
	// executing. See pipeline.ModeEnvVar.
	VerbPlan Verb = "plan"
	// VerbDryRun runs a target with every step's side effects suppressed,
	// whatever mode the definition asked for.
	VerbDryRun Verb = "dry-run"
	// VerbVersion prints build information.
	VerbVersion Verb = "version"
	// VerbHelp prints usage.
	VerbHelp Verb = "help"
)

// VerbValues is the canonical iteration order, drives the help listing, so a
// verb cannot appear in the switch without appearing in the help, or vice
// versa.
var VerbValues = []Verb{VerbRun, VerbPlan, VerbDryRun, VerbList, VerbTUI, VerbInit, VerbCache, VerbVersion, VerbHelp}

// Valid reports whether v is a declared verb.
func (v Verb) Valid() bool {
	for _, x := range VerbValues {
		if v == x {
			return true
		}
	}
	return false
}

// CacheAction selects what `cache` does. The empty value lists.
type CacheAction string

const (
	// CacheActionList is the default: show what is cached.
	CacheActionList CacheAction = ""
	// CacheActionPrune removes entries whose project no longer exists.
	CacheActionPrune CacheAction = "prune"
	// CacheActionClean removes every entry.
	CacheActionClean CacheAction = "clean"
)

// CacheActionValues is the canonical iteration order, excluding the default.
var CacheActionValues = []CacheAction{CacheActionPrune, CacheActionClean}

// Valid reports whether a is a declared action.
func (a CacheAction) Valid() bool {
	if a == CacheActionList {
		return true
	}
	for _, x := range CacheActionValues {
		if a == x {
			return true
		}
	}
	return false
}

// Help flags.
//
// The only flags lath answers, because every command-line tool is expected to.
// `lath help` is the real spelling; these exist for muscle memory alone.
const (
	FlagHelp     = "-h"
	FlagHelpLong = "--help"
)

// Namespace layout.

// internalDirName is Go's reserved directory. A definition using
// .lath/internal/<pkg> for helpers is doing the ordinary Go thing, and those
// helpers are not commands, so it is never treated as a namespace, while
// remaining importable by the definition as usual.
const internalDirName = "internal"

// commandSeparator joins a namespace to its target in a command name. A space,
// because that is how the user types it: `lath run secrets push`.
const commandSeparator = " "

// namespaceAliasPrefix prefixes generated import aliases so they can never
// collide with an identifier the author wrote in the definition root.
const namespaceAliasPrefix = "ns_"

// maxNamespaceDepth is how many directory levels below the definition root may
// hold targets. One.
//
// `lath run secrets push` is the readable limit; arbitrary depth would make the
// dispatcher, the listing and the help output all grow a tree walk to serve a
// command line nobody enjoys typing. A directory nested deeper is reported by
// name rather than silently flattened or ignored.
const maxNamespaceDepth = 1

// definitionDir is the directory a project keeps its pipeline definition in.
//
// It is a nested Go module deliberately excluded from the parent go.work, so
// `go work vendor` and `go build ./...` at the repository root never reach it.
// That exclusion is what keeps the definition's dependencies out of the
// application's module graph while still giving the editor full type checking ,
// gopls resolves types from the module cache, which vendoring only duplicates.
const definitionDir = ".lath"

// Files whose contents determine whether a rebuild is needed. go.mod and go.sum
// are included because a dependency bump changes the compiled result even when
// no .go file did.
const (
	goFileSuffix = ".go"
	goModFile    = "go.mod"
	goSumFile    = "go.sum"
)

// hashPrefixLen is how many hex characters of the SHA-256 name the cache entry.
// Twelve: long enough that an accidental collision across one machine's cache
// is not a practical concern, short enough to read in a log line and compare by
// eye between two runs.
const hashPrefixLen = 12

// cacheDirPerm is the mode for the cache directory. Owner-writable only, the
// cache holds executables built from repository content, so a world-writable
// directory would let another local user swap in a different binary.
const cacheDirPerm fs.FileMode = 0o700

// cacheSubdir namespaces this tool's entries inside the user cache directory.
const cacheSubdir = "lath"

// fallbackCacheName names a cache entry when neither a repository nor a
// containing directory yields a usable name.
const fallbackCacheName = "pipeline"

// maxCacheNameLen caps the human-readable half of a cache entry's filename.
// Long enough to stay recognisable, short enough that the full name, name,
// separator and hash, is comfortable in a terminal listing.
const maxCacheNameLen = 24

// cacheNameSeparator joins the readable name to the content hash. A single
// character that cannot appear in a sanitised name, so splitting on the LAST
// occurrence always recovers the hash.
const cacheNameSeparator = "-"

// maxSuggestionErrorRatio bounds how wrong a mistyped option may be before a
// suggestion is withheld: at most len(input)/N characters may differ. A wrong
// suggestion is worse than none, because it sends the reader after a command
// they did not want.
const maxSuggestionErrorRatio = 3

// cacheManifestSuffix marks the sidecar describing a cache entry. Without it a
// cache directory is a pile of anonymous binaries with no way to tell which
// project any of them came from.
const cacheManifestSuffix = ".json"

// undocumentedTarget is shown in listings for a target whose function has no
// doc comment. A blank column reads as a rendering fault; naming the gap tells
// the author what to add.
const undocumentedTarget = "(no description: add a doc comment)"

// Scaffolding constants, the shape `-init` writes.

// definitionModuleName is the module path given to a new definition.
//
// A fixed, unpublished name rather than one derived from the repository: the
// definition module is never fetched by anything, so its path only has to be
// valid and stable. Deriving it from a directory name would make the same
// definition change identity when a repository is renamed.
const definitionModuleName = "deploydef"

// starterFileName is the file `-init` writes the example commands into.
const starterFileName = "deploy.go"

// scaffoldFilePerm is the mode for scaffolded source files. Ordinary source:
// owner-writable, world-readable.
const scaffoldFilePerm = 0o644

// definitionDirPerm is the mode for a newly created definition directory.
const definitionDirPerm = 0o755

// fallbackGoDirective is the `go` line used when the repository has no go.mod
// to copy one from. A definition can live in a repository of any language, so
// there is not always a parent version to match.
const fallbackGoDirective = "1.24"

// engineModules are the modules a scaffolded definition requires, listed once
// so the generated go.mod and the starter file's imports cannot disagree.
//
// Each is published separately so they can version independently, and each
// sits at the directory its import path names, a module declaring
// github.com/ubgo/lath/kit MUST live at kit/ in the repository, or the
// proxy cannot resolve it.
var engineModules = []string{
	"github.com/ubgo/lath/kit",
	"github.com/ubgo/lath/pipeline",
	"github.com/ubgo/lath/steps",
}

// generatedFilePerm is the mode for the generated dispatcher. Not executable,
// owner-writable: it is source, rewritten on every run.
const generatedFilePerm = 0o600

// Exit codes. Distinct values so a wrapper script can tell a definition bug
// (fix the file) from an operational failure (a retry may help).
const (
	exitOK          = 0
	exitUsage       = 2
	exitCompile     = 3
	exitPipeline    = 4
	exitInternalErr = 1
)

// goWorkOffEnv disables workspace mode for the definition build.
//
// REQUIRED, not an optimisation: the definition module is intentionally not a
// go.work member, so with workspace mode active the toolchain refuses to
// resolve it at all.
const goWorkOffEnv = "GOWORK=off"

// goToolName is the toolchain binary this tool shells out to. Named because it
// appears in both the build invocation and the error message that explains a
// missing toolchain.
const goToolName = "go"
