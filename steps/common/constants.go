package common

import "github.com/ubgo/lath/pipeline"

// Rule: NO BARE STRINGS for any value with a closed set of choices. Every
// switch, map key, and comparison in this package picks from the constants
// below. A typo becomes a compile error and a rename is one edit.
//
// Each group ships a canonical *Values slice so the read path (validation,
// flag parsing, UI listing) and the write path (a step choosing a value) can
// never drift apart, they iterate the same source.

// State keys. The values steps hand to each other.
//
// The TYPE lives in the pipeline package; the concrete keys live here because
// the engine has no opinion about what flows through it. Adding a key means
// adding it to KeyValues too, so tooling can enumerate the data model.
const (
	// KeyCommit is the resolved short commit SHA of what is being deployed.
	// Type: string. Provided by ResolveCommit.
	KeyCommit pipeline.Key = "commit"

	// KeyEnv is the target environment name (e.g. "prod"). Type: string.
	// Provided by ResolveEnv.
	KeyEnv pipeline.Key = "env"

	// KeyBranch is the branch the deployed commit was on, or
	// gitkit.DetachedHead when HEAD is detached. Type: string. Provided by
	// ResolveCommit.
	KeyBranch pipeline.Key = "branch"

	// KeyImage is the fully-qualified image reference including tag.
	// Type: string. Provided by BuildImage.
	KeyImage pipeline.Key = "image"

	// KeyContainers holds the names of the containers just started.
	// Type: []string. Provided by StartProcesses; consumed by every step that
	// health-checks or retires them.
	KeyContainers pipeline.Key = "containers"

	// KeyRouted holds the subset of KeyContainers that receive inbound
	// traffic, in the same order. Type: []string. Provided by StartProcesses
	// from each Process's Route flag; consumed by whatever points a proxy at
	// the release.
	//
	// Separate from KeyContainers because they are genuinely different sets: a
	// queue consumer dials out and must be health-checked and retired like
	// everything else, while sending a proxy its name would route public
	// requests to something with no listener. Without this key the Route flag
	// declared an intent that nothing acted on, and the step that fed a proxy
	// handed it every container a deploy started.
	KeyRouted pipeline.Key = "routed"
)

// KeyValues is the canonical iteration order for every key this package
// defines. Drives documentation generation and the key-coverage test.
var KeyValues = []pipeline.Key{KeyCommit, KeyBranch, KeyEnv, KeyImage, KeyContainers, KeyRouted}
