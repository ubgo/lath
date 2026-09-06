# The next primitives

**Status: built.** Everything below exists in `kit/`, is tested under `-race`, and cross-compiles for linux, macOS and Windows. The document was written as a proposal and reviewed before implementation, the same way [`PRIMITIVES-STDLIB.md`](PRIMITIVES-STDLIB.md) was; the design and the decisions are recorded here as they were settled, and §What was found while building records where reality pushed back.

`kit` was nineteen packages when this was written, and is larger now; see [the kit reference](kit/README.md) for the current list. The first eight (`proc`, `supervise`, `wait`, `scan`, `repo`, `out`, `fsx`, `env`) are covered by the sibling document. These are the seven that followed, plus `Retry`, which joined `supervise` rather than becoming a package of its own.

## The admission test

Every candidate is judged by one question: **does it encode a mechanism, or a decision?**

A mechanism describes *how* something is done, write atomically, poll until ready, don't leak a value through `%v`. A decision describes *what someone chose*. That `GITHUB_SECRET_` marks a CI variable, that key files live in `_keys/`, that an extension is dropped before a suffix is appended.

The test catches the case that fools everyone. A function deriving a secret's name from a filename is eight lines of pure string manipulation, so it *looks* like a mechanism. It is not: it is a compatibility contract with names already live in a remote system, and the collision inside it is a decision to stay bug-compatible. No amount of string-handling makes that universal.

Two supporting checks. **Can the package be named without naming a vendor?** `kit/github` fails on sight. **Would it look at home beside `os`, `io`, `net/http`?** Those are all mechanisms.

⚠️ And one trap to name early: anything can be made general by adding options, which is how a primitive becomes a twelve-parameter function nobody can call. If fitting a second project needs more than one or two options, that is not one primitive. It is two things sharing a name. Split rather than parameterize.

## The tiers

Three homes, not two. The middle one is where most mistakes land.

| Tier | Knows about | Home | Example |
|---|---|---|---|
| 1 | nothing but Go and the OS |  `kit/` | `proc`, `fsx`, `wait` |
| 2 | a vendor or ecosystem, but no single project | a separate module, possibly not lath's | a `gh secret set` wrapper, a Docker builder |
| 3 | one project's conventions | that project's `.lath/` | `GITHUB_SECRET_`, `_keys/`, `ENV_B64` |

Only tier 1 is proposed here. A definition is its own Go module, so it can import any tier-2 package directly from any source, which means tier 2 needs no blessing from lath, and lath never grows a dependency on Docker, GitHub, or anything else.


## Design principles

What "flexible, not coupled" means concretely. Every decision in this document was made against these, and any future one should be.

**Optional interface upgrades, never required configuration.** When some backends can do a thing and others cannot, define a small optional interface and type-assert for it. The way `secret.Comparer` works. Requiring every implementation to answer a question most of them cannot forces them to lie, and requiring the *caller* to configure which is which turns a capability into trivia the caller must look up.

**Accept stdlib interfaces; invent none where one exists.** `io.Writer`, `io.Reader`, `context.Context`, `fs.FileMode`, `slog.Value`. A primitive taking `io.Writer` composes with a file, a buffer, a terminal, a test, and something not written yet. A primitive taking a bespoke `Output` type composes with nothing.

**No hidden state, ever.** No cache file, no lockfile the caller did not name, no `$HOME` directory, no package-level variable. State that lives on one machine means the same command produces different results on a laptop and in CI, silently. Everything a primitive needs is an argument; everything it produces is a return value.

**One escape hatch per primitive, typed and documented.** `ssh.Host.Extra`, `proc`'s options. Real systems have configurations nobody anticipated, and a primitive with no escape hatch is abandoned wholesale the first time it does not quite fit, taking its safety guarantees with it.

**Options over new packages; splitting over parameters.** These pull in opposite directions and both are true. A variation on one job is an option. A second job wearing the same name is a second function. The test from the admission rules holds: if fitting a second caller needs more than one or two options, it was two things.

**The careless path must be the correct path.** Established while deciding how `Value` serialises, and applied since to archive extraction and lock acquisition. Where a safe default and a convenient default differ, take the safe one and make the other reachable by name, someone will hit the default without thinking, and that moment should not be the one that leaks a credential or hangs a build.

**Nothing in tier 1 may name a vendor.** Not in a package name, a type, a function, a constant, or a default. Vendors change; mechanisms do not.

---

## Worked example, decomposing a real pipeline

Before ranking candidates in the abstract, here is the test applied to something concrete: `acme_api/.lath/secrets` (a private repository), 375 lines that read a `.env` file and publish GitHub Environment secrets.

| Code | Verdict | Where it goes |
|---|---|---|
| `readEnv.Run` | not a primitive | `ubgo/dotenv` |
| `stripPrefixedLines` | **subsumed** | `ubgo/dotenv`, `Entries()` + `Render()` |
| `readKeyFiles` | half | the manifest format is policy; "read a file as a credential" becomes `secret.FromFile` |
| `keyFileSecretName` | policy | stays. A compatibility contract with live secret names |
| `buildSecrets.Run` | policy | stays. The `GITHUB_SECRET_` scheme is one project's invention |
| `countPrefixed` | trivial | deleted |
| `writeFile.Run` | half | `secret.Set.WriteFile`, which enforces the file mode |
| `push.Run` | **mostly primitive** | `secret.Sync` + `secret.Store` |
| `parse` guards | pattern | `confirm.Phrase` plus a protected-target guard |

⚠️ **The push is the case worth studying, because the first pass got it wrong.** It was classified tier 2 on sight. It invokes `gh`, therefore it is vendor-coupled, therefore it cannot be a primitive, but the `gh` invocation is six lines of forty-three. The other thirty-seven iterate a desired set, skip names the remote refuses, honour dry run, redact every line of output, and continue far enough to report partial failure. None of that mentions GitHub, and all of it would be rewritten by the next person publishing secrets anywhere else.

The lesson generalises: **a vendor name in a function is not proof the function is policy.** Ask what remains after the vendor call is replaced by an interface. If a mechanism is left standing, that mechanism belongs in tier 1 and the vendor call is a thin adapter beneath it.

The opposite result is just as informative. `buildSecrets.Run` has no vendor name anywhere in it and is *entirely* policy, partitioning variables by a prefix is three lines of Go, and the convention that makes those three lines meaningful belongs to acme_api. Nothing hides underneath. Had kit absorbed it, kit would have swallowed one project's naming scheme.

**Net: roughly 40% of those 375 lines are general.** The orchestration, the credential handling and the file writing move; the naming schemes and env-file semantics stay.

---

## Ranked by evidence

Ordered by whether existing code demands them, not by how appealing they sound. The first four have receipts; the last four are plausible, which is precisely the state in which primitives get built and then never used.

| Primitive | Owns | Evidence |
|---|---|---|
| `secret` | credentials: a value that cannot be printed, a set, and reconciliation against any store | The secrets pipeline handles plaintext private keys across four files, protected only by convention. Its push is 37 lines of reusable orchestration around 6 lines of vendor call |
| `lock` | single-instance guard, with stale detection | lath already lost a day to two supervisors fighting over the same port |
| `hashtree` | content hash of a file set | lath already built this internally for its compile cache |
| `ssh` | run a command on a remote host | acme_api's entire deploy runs over it |
| `archive` | tar/gz with extraction guards | deploys ship bundles; the guard is the value |
| `confirm` | typed confirmation for dangerous actions | hand-rolled twice already |
| `supervise.Retry` | one-shot retry, distinct from process supervision | network calls in every deploy |
| `download` | fetch with checksum verification | only if tasks fetch binaries |

---

## 1 · `kit/secret`, credentials, and what may be done to them

The strongest candidate, and larger than one value type. It covers three things a task runner does with credentials: hold one without leaking it, group them, and reconcile a group against somewhere they must be stored.

### 1a · `Value`, a value that cannot leak

Today the pipeline's `Redacted()` is a convention. Nothing stops `fmt.Printf("%v", s)`, `json.Marshal`, or `slog.Info("...", "cfg", cfg)` from writing a private key into a CI log that is retained for ninety days.

```go
// Value holds a credential. The plaintext is unreachable through every
// standard Go printing and serialisation path; Reveal is the only door.
type Value struct{ /* unexported */ }

func New(plaintext string) Value
func FromEnv(name string) (Value, error)    // wraps env.Require
func FromFile(path string) (Value, error)   // never lands in a printable variable

// Reveal returns the plaintext. Named so it is greppable: a review can find
// every place a secret escapes by searching for one word.
func (v Value) Reveal() string

// The containment surface. Each entry is a path by which a value would
// otherwise escape, and all of them must be closed for the type to mean
// anything — one gap and the guarantee is theatre.
func (v Value) String() string                    // "«secret 40 bytes»"
func (v Value) GoString() string                  // %#v
func (v Value) Format(f fmt.State, verb rune)     // %s %v %q %x — all redacted
func (v Value) MarshalJSON() ([]byte, error)   // always redacted — no plaintext path
func (v Value) MarshalText() ([]byte, error)   // always redacted
func (v Value) LogValue() slog.Value              // log/slog
func (v Value) UnmarshalJSON(b []byte) error      // reading config IS allowed

func (v Value) Len() int                          // safe to log
func (v Value) IsZero() bool
func (v Value) Equal(other Value) bool            // constant-time
```

**Used by.** Every credential in the acme_api pipeline, thirteen for one environment, including a decoded SSH private key and two `ghp_` tokens. Also billing-api, which reads secrets from the environment through PKL's `read("env:VAR")` and currently has nothing stopping one reaching a log line.

**Decided, JSON always redacts, and offers no plaintext path at all.**

Four options were on the table. *Redact by default* silently writes wrong data: a task that generated a `.creds` file by marshalling a struct would emit the literal text `«secret 40 bytes»` where a credential belongs, and the failure would surface days later as an authentication error nowhere near the code that caused it. *Error on marshal* is loud and correct, and fails for a subtler reason. If redacting a config for a debug log is impossible, callers work around it by logging `Reveal()` instead, which defeats the type entirely. *Redact with a `Revealing` wrapper* works but adds machinery, and leaves a plaintext path that will eventually be used by accident.

So: encoding **can never** produce plaintext, which makes logging or dumping a config unconditionally safe and leaves nobody needing a workaround. Writing a real credentials file becomes something a caller does deliberately through `Reveal()`, which is the correct shape, because emitting live credentials to disk should never be a side effect of marshalling a struct that happened to be in scope.

The rule this follows, and the one to apply to the rest of the package: **the safest design is the one where the careless path is also the correct path.**

**Decided, `Equal` stays, and the reason is not the one you would expect.**

The usual justification is timing attacks: an ordinary `==` returns at the first differing byte, so an attacker who can run the comparison repeatedly and measure it recovers the value a byte at a time. That threat does not exist here. Timing attacks need an attacker in the loop, and this is a deploy tool run by its owner. On its own merits the crypto argument would not earn the function.

It earns its place because of what happens without it. A caller needing to compare two credentials writes `a.Reveal() == b.Reveal()`, which puts two plaintext strings into local variables one bad log line from disclosure, and adds two more hits to the `Reveal` audit that is supposed to be short enough to read. **Omitting `Equal` forces callers into exactly the pattern the type exists to prevent.**

Given the function is being written anyway, `subtle.ConstantTimeCompare` costs nothing and is correct if the package is ever used somewhere an attacker *is* in the loop.

### 1b · `Set`, a named group

```go
// Set is an ordered, named collection. Ordered so a rendered file and a
// push sequence are stable between runs; an unstable order makes every
// regeneration look like a change.
type Set struct{ /* unexported */ }

func NewSet() *Set
func (s *Set) Put(key string, v Value)
func (s *Set) Get(key string) (Value, bool)
func (s *Set) Keys() []string
func (s *Set) Len() int

// String reports "13 secrets" — never the names' values, and never a value.
func (s *Set) String() string

// Summary is the only detailed rendering, and it is redacted by
// construction: "GHCR_PAT (40 bytes)".
func (s *Set) Summary() []string

// WriteFile renders the set to a dotenv-shaped file with an enforced mode.
//
// Refuses a world- or group-readable perm outright rather than warning: the
// file holds every value in plaintext, and the existing tool in this
// repository wrote exactly such a file at 0644 for a year without anyone
// noticing.
func (s *Set) WriteFile(path string, perm os.FileMode) error

var ErrPermTooOpen = errors.New("secret: refusing to write credentials at a readable mode")
```

**Used by.** The `write .secrets file` step, which currently hand-rolls the rendering and the 0600. Also anywhere a task materialises credentials for a subprocess to read.

### 1c · `Store` and `Sync`, reconciliation

This is the part the first draft of this document missed entirely, having dismissed the whole push step as vendor-coupled.

```go
// Store is somewhere credentials live. Implementations are tier 2 — a
// GitHub Environment, a Vault path, a 1Password vault, an AWS parameter
// prefix — and none of them belong in kit.
type Store interface {
    Set(ctx context.Context, key string, v Value) error
    List(ctx context.Context) ([]string, error)
    Delete(ctx context.Context, key string) error
    // Reserved lets the store declare names it will refuse, so a caller
    // never has to hardcode another system's rules.
    Reserved(key string) bool
}

// Sync reconciles a desired Set against a Store: writes what is missing or
// changed, skips what the store reserves, and optionally removes what is no
// longer wanted.
//
// Invariant: continues past a single failure and reports every one, rather
// than aborting at the first. A half-published set with an unclear boundary
// is the worst outcome, so the Report must say exactly where it got to.
func Sync(ctx context.Context, s Store, desired *Set, opts ...SyncOption) (Report, error)

type Report struct {
    Set     []string
    Skipped []string           // reserved by the store
    Deleted []string           // only with Prune
    Failed  map[string]error
}

func DryRun() SyncOption               // report the plan, change nothing
func Prune() SyncOption                // delete live entries absent from desired
func Progress(fn func(Event)) SyncOption
```

A `Store` may additionally implement `Comparer` to let `Sync` skip unchanged entries, see the decision below.

**Used by.** The `push` step, which shrinks to constructing a `Set` and calling `Sync`. The GitHub adapter beneath it is roughly twenty lines: `Set` shells to `gh secret set` with the value on stdin, `Reserved` reports `strings.HasPrefix(key, "GITHUB_")`, `List` parses `gh secret list --json name`.

`Prune` also retires `CleanNonExistingSecrets` from `apps/scripts`, which is this exact reconciliation, written once for one vendor. That function existing already is the evidence that `Sync` is a mechanism rather than a guess.

**Decided, write every key, every time; let a capable store opt out.**

A local digest of what was last pushed would be quieter, and it introduces machine-local state: the digest lives wherever it was last written, so a push from CI and a push from a laptop disagree about what is current, and the disagreement is silent. State that lives on one machine is coupling to that machine.

So the default is stateless, write everything, always correct from anywhere, but some stores *can* read a value back (Vault can; a GitHub Environment cannot), and hardcoding the pessimistic assumption would waste that.

```go
// Comparer is an optional upgrade. A Store that can read values back
// implements it, and Sync skips writes for entries already correct.
//
// Optional rather than part of Store: requiring every implementation to
// answer a question most backends cannot would force them all to lie.
type Comparer interface {
    Same(ctx context.Context, key string, v Value) (bool, error)
}
```

Sync type-asserts for it. A store that has the capability declares it; one that does not is unaffected and needs no configuration. **No local cache file is written under any circumstance.**

## 2 · `kit/lock`, single instance, honestly

The mechanism that would have prevented lath's worst bug so far: a dev watchdog and a `portless --force` invocation each killing the other, forever, because nothing arbitrated who owned the port.

```go
// Lock is a held, exclusive claim on a resource, represented by a file.
type Lock struct{ /* unexported */ }

func Acquire(ctx context.Context, path string, opts ...Option) (*Lock, error)
func (l *Lock) Release() error
func (l *Lock) Path() string

// Read reports who holds a lock without attempting to take it — for a
// "already running as pid N since T" message rather than a bare failure.
func Read(path string) (Info, error)

type Info struct {
    PID   int
    Since time.Time
    Host  string   // set, so a lock on a shared filesystem is diagnosable
    Note  string   // caller-supplied, e.g. the command line
}

func Wait(d time.Duration) Option        // block for the lock instead of failing
func StaleAfter(d time.Duration) Option  // age past which a lock may be broken
func Note(s string) Option

var ErrHeld = errors.New("lock: held by another process")
```

**Used by.** The dev watchdog, first. It is the mechanism that would have prevented two supervisors killing each other over a port, which cost a day. Then any deploy: two people shipping to one host simultaneously is a corrupted release, and `Acquire` on a lock beside the deploy path makes the second one wait or fail with a message naming who holds it. Migrations want the same guarantee for a stronger reason. Two concurrent schema changes are not recoverable by retrying.

**The hard part is stale detection, and it is where most implementations are wrong.** A lock file whose owning process is dead must be reclaimable, or one crash wedges the tool until someone deletes a file they do not know about, but a PID alone is not enough, PIDs are reused, so a naive liveness check can conclude a lock is live when the original holder died and an unrelated process inherited the number. The proposal is to record PID **and** start time, and treat the lock as stale when the process is absent *or* its start time does not match. `proc.Find` already provides the liveness half.

**Decided, fail fast; `Wait` opts into blocking.**

Failing immediately means the careless call tells you the truth at once: something else holds this, here is who and since when. Defaulting to waiting means a forgotten lock turns a CI job into one that hangs until the runner's global timeout kills it, with no output explaining why. The failure mode that costs the most to diagnose.

`Wait(d)` bounds the blocking, and the context bounds everything: a cancelled ctx aborts a wait immediately, so Ctrl-C is always honoured.

## 3 · `kit/hashtree`, content hashing for cache keys

lath already computes one of these to decide whether a definition needs recompiling. Extracting it means the tool eats its own primitive.

```go
// Of returns a hex SHA-256 over the contents AND relative paths of every
// file matching the filter, walked in a stable order. The one-call form,
// for the common case.
//
// Paths are folded in, not just contents: renaming a file changes the tree
// even when no byte of any file changed.
func Of(root string, f scan.Filter) (string, error)
func OfFiles(paths []string) (string, error)
```

For anything beyond one tree, compose (see the decision below):

```go
h := hashtree.New().
    AddTree(definitionDir, goSources).
    AddString(lathVersion, runtime.Version()).
    AddFile("go.sum")
key, err := h.Sum()
```

This shape exists because of a real bug: lath's cache key covered only the definition's source, so upgrading lath left every previously compiled binary in place and the new behaviour silently did not apply. A cache key must cover everything the output depends on, and what that is differs per caller.

**Used by.** lath itself, whose compile cache is exactly this. Beyond that, every expensive step worth skipping when nothing changed: rebuilding a Docker image when no source file moved, re-running `task dbent:g` when no Ent schema was touched, regenerating gqlgen resolvers when no `.graphqls` changed. billing-api runs those generators often enough that a correct skip is worth real time.

**Decided, replace `Mix` with a composable `Hasher`, and keep `Of` as the one-call form.**

`Mix` was the wrong shape: it assumed a digest is built from a tree first and decorated afterwards. Real cache keys are assembled from whatever a pipeline happens to depend on, some files, a tool version, an env var, the contents of a lockfile, in no fixed order.

```go
// Hasher accumulates whatever a cache key should depend on. Every Add is
// order-significant and separated internally, so two callers composing the
// same inputs in the same order always agree.
type Hasher struct{ /* unexported */ }

func New() *Hasher
func (h *Hasher) AddString(s ...string) *Hasher
func (h *Hasher) AddFile(path string) *Hasher
func (h *Hasher) AddTree(root string, f scan.Filter) *Hasher
func (h *Hasher) Sum() (string, error)   // first error encountered wins
```

lath's own key becomes `New().AddTree(defDir, goFiles).AddString(version, runtime.Version()).Sum()`. The same four inputs, with nothing hardcoding that a tree comes first. This is strictly more flexible than `Of` + `Mix` and removes a function whose whole existence was a question mark.

**Note the risk:** if lath adopts this for its own cache key, a bug here breaks compilation itself. That is the point of eating your own primitive, but it should be a deliberate choice rather than a side effect.

## 4 · `kit/ssh`, commands on another machine

The weakest of the four with evidence, because its option surface is genuinely large and it shells out to whatever `ssh` binary is present.

```go
type Host struct {
    Addr    string
    User    string
    Port    int             // 0 means the ssh default
    KeyFile string          // empty means the agent
    Extra   []string        // escape hatch for raw ssh flags
}

func Run(ctx context.Context, h Host, command string, opts ...proc.Option) (proc.Result, error)
func Output(ctx context.Context, h Host, command string) (string, error)
func Copy(ctx context.Context, h Host, localPath, remotePath string) error
func Reachable(ctx context.Context, h Host, timeout time.Duration) error

var ErrUnreachable = errors.New("ssh: host did not answer")
```

Deliberately reusing `proc.Option` rather than inventing a parallel option set, so `Dir`, `Timeout`, `Out`, and `Capture` mean the same thing locally and remotely.

**Used by.** The whole of acme_api's deploy, writing the env file, pulling the image, restarting the container, checking health, all of which currently lives inside a workflow YAML block where it cannot be tested or run locally. Also fetching logs from a host during an incident, which today means remembering the flags by hand.

**Decided, tier 1, and `Extra` stays.**

SSH is a protocol with one near-universal client, which puts it in the same position as `pgrep` in `proc`: kit already shells out to an external program where that program is the portable way to do the job, and documents where it is unavailable. Pushing `ssh` to tier 2 would mean every project that deploys to a machine rewrites remote-exec, which is the duplication this kit exists to end.

`Extra []string` is kept deliberately and is not an embarrassment. An exhaustive `Host` struct would need a field for every flag `ssh` accepts, and would still be missing one the day someone needs `ProxyJump` or a `-o` nobody anticipated. A typed escape hatch is what keeps the primitive usable with configurations it was never designed for. The alternative is a caller abandoning the package entirely the first time it does not fit.

## 5 · `kit/archive`, bundles, safely

```go
// TarGz writes the filtered tree under root to dst.
func TarGz(dst, root string, f scan.Filter) error

// Extract unpacks src into dstDir, refusing any entry that would write
// outside it.
//
// The guard is the reason this exists rather than a call to archive/tar:
// a tar entry named "../../etc/thing" is written wherever it says unless
// something checks, and most hand-rolled extractors do not.
func Extract(ctx context.Context, src, dstDir string, opts ...ExtractOption) error

func FollowSymlinks() ExtractOption   // write the target's bytes, never a link

var ErrUnsafePath = errors.New("archive: entry escapes the destination")
```

**Used by.** Any deploy that ships a build rather than an image. The binary plus its assets, transferred and unpacked. Also snapshotting a directory before a destructive step, so there is something to restore from.

**Decided, never write a symlink; offer to materialise its contents instead.**

A symlink entry can escape the destination even when every path in the archive looks relative, and validating that properly means resolving each link against the extraction root at every step. The kind of check that is correct in review and wrong in practice.

```go
func FollowSymlinks() ExtractOption   // write the TARGET's bytes as a regular file
```

Default: a symlink entry is refused with `ErrUnsafePath`. With the option: the target's contents are written as a regular file, so nothing that could point outside the destination is ever created on disk. **There is no mode that writes a raw link**. Two safe behaviours, and no unsafe one to reach for under deadline.

## 6 · `kit/confirm`. A gate that cannot be scripted past by accident

Hand-rolled twice already: `--apply` on the secrets push, `--allow-prod` on the environment guard.

```go
// Yes asks a yes/no question.
func Yes(ctx context.Context, prompt string, opts ...Option) (bool, error)

// Phrase requires the operator to type an exact string — the pattern used
// for irreversible actions, where a reflexive "y" is too cheap.
func Phrase(ctx context.Context, prompt, required string, opts ...Option) error

func In(r io.Reader) Option
func Out(w io.Writer) Option
func Assume(yes bool) Option   // for --yes / non-interactive callers

var ErrDeclined = errors.New("confirm: declined")
var ErrNotInteractive = errors.New("confirm: no terminal to ask on")
```

**Used by.** `--apply` on the secrets push and `--allow-prod` on the environment guard, both of which are hand-rolled today. A production deploy is the obvious third. `task dbent:migrate` and any database reset are the fourth, and the one where a reflexive `y` does the most damage.

**The mechanism worth having is the terminal check.** A prompt that blocks forever in CI is worse than no prompt: the job hangs until it times out, with no output explaining why. `ErrNotInteractive` makes that a fast, legible failure, and `Assume` is how a caller says the decision was already made on the command line.

## 7 · `supervise.Retry`, one-shot operations

Proposed as functions in the existing package, **not a new one**, because retry and supervision share their backoff arithmetic entirely. Two packages both owning backoff is how they drift apart.

```go
// Retry runs fn until it succeeds, the context ends, or the policy's limit
// is reached.
//
// The difference from Loop is intent, and it shows up in one place: Loop
// supervises something that is expected to run forever, so any return is a
// failure to retry. Retry drives an operation expected to finish, so a
// permanent error must stop it rather than being attempted ten times.
func Retry(ctx context.Context, p Policy, fn func(context.Context) error) error
func RetryWithStats(ctx context.Context, p Policy, fn func(context.Context) error) (Stats, error)

// Permanent marks an error as not worth retrying — a 400, a malformed
// request, a missing file. Retry stops at the first one.
func Permanent(err error) error
func IsPermanent(err error) bool
```

**Used by.** Every network call in a deploy: `gh` API requests that hit a rate limit, a registry push that fails mid-upload, a health check against a service still starting. `Permanent` is what stops a malformed request being attempted ten times.

**Decided, yes, `Loop` honours `Permanent`, because the caller says so rather than the package guessing.**

The objection was that `Loop`'s contract is "an error means retry", and that rule is what makes it a supervisor rather than a wrapper. That still holds. Honouring `Permanent` does not weaken it, because nothing is inferred: an error is retried unless the caller *explicitly wrapped it* to say otherwise.

The alternative is worse for exactly the reason this document exists. A supervisor that can never be told "this one is fatal" forces callers to build their own escape. A counter, a sentinel check, a bespoke wrapper around `Loop`, and every one of them reimplements the same idea differently. One explicit marker, honoured by both functions, keeps that in the package where it can be tested.

## 8 · `kit/download`, fetch with verification

Lowest evidence. Included for completeness; would only be built if tasks actually fetch binaries.

```go
func ToFile(ctx context.Context, url, dst string, opts ...Option) error

func SHA256(hex string) Option        // verify before the file is moved into place
func Header(key, value string) Option
func Timeout(d time.Duration) Option

var ErrChecksumMismatch = errors.New("download: checksum does not match")
```

**Used by.** Toolchain bootstrap, fetching `pkl`, `gitleaks`, `task`, or `air` on a fresh machine or a CI runner, which is currently a README instruction rather than a task. The checksum is the point: an unverified binary fetched over the network and then executed is the supply-chain problem in miniature.

Writes through `fsx.WriteAtomic`, so a failed or unverified download never leaves a partial file where a later step would find it and assume it is good.

---

## What was found while building

Everything above was written before implementation. This section records where reality disagreed.

### Three defects, none of them in the new code

Each is recorded with what it was, how it surfaced, and what now prevents it, because a defect written down without a guard is a defect waiting for its second occurrence.

#### 1 · `proc` never compiled on Windows

**What.** `proc/find.go` called `syscall.Kill`, which does not exist on Windows. The package therefore failed to build for that platform, and so did every package importing it, which, once `lock` landed, was most of the kit.

**Why it hid.** The package *documented* the limitation: a comment above `Find` said it was unsupported on Windows, and `ErrUnsupported` existed to report it at runtime. That reads like a handled case. It was not handled; it was a compile error nobody had ever triggered, because every build until then had been for the host platform.

**How it surfaced.** `lock` needed a liveness check, which meant thinking about Windows, which meant running `GOOS=windows go build ./...` for the first time.

**Fixed.** Split into `kill_unix.go` and `kill_windows.go`, with `errProcessGone` aliased per platform. The Windows implementation returns `ErrUnsupported`, which is what the comment always claimed happened.

**Prevented.** `task crosscheck` builds every module for linux, darwin and windows, and is wired into `task check`. Verified by deleting `kill_windows.go` and confirming the gate fails.

⚠️ **The general lesson**, and the reason this one is worth the space: **"unsupported" in a doc comment is not the same as "unsupported" to a compiler, and only one of them is verifiable.** Any claim about a platform, a version, or a configuration that is stated in prose rather than exercised by the build is a claim nobody has checked. The rule generalises past Go. A README that says "works on Python 3.9+" and a CI matrix that only runs 3.12 are the same bug.

#### 2 · `confirm` reported Ctrl-D as a broken terminal

**What.** An immediate EOF at a prompt. The operator pressing Ctrl-D, or piped input running out, returned `confirm: reading the answer: EOF` instead of a decline.

**Why it matters.** Both `Yes` and `Phrase` default to no, so returning the empty line produces exactly the right outcome. Returning an error instead reports a deliberate refusal as a malfunction, and a caller distinguishing "declined" from "the terminal broke" would branch the wrong way, most likely by retrying a prompt the operator has already answered.

**How it surfaced.** A table-driven test that included `""` alongside `"\n"` as an answer. The newline case passed; the bare-EOF case did not.

**Fixed.** `ask` treats `io.EOF` as end-of-input rather than failure. Ctrl-D means no.

**Prevented.** The answer table pins `""` for both `Yes` and `Phrase`, alongside every other shape, `y`, `Y`, `yes`, `YES`, whitespace-padded, `ye`, `1`, `true`, and a bare Enter.

#### 3 · A helper package was mistaken for a command namespace

**What.** The GitHub adapter written for the migration landed at `.lath/ghsecrets/`. lath treats every subdirectory of a definition as a command namespace, so it inspected the package, found an exported `Environment` function whose signature is not dispatchable, and had nothing to run.

**How it surfaced.** Instantly, in the output of the very next `lath list`:

```
lath: 1 exported function(s) found but not usable as commands:
  Environment (ghsecrets/ghsecrets.go) — unsupported signature
```

**Why that is the interesting part.** That warning was added the day before, to fix a case where three targets with a mistyped signature had been dropped in silence. It caught a real structural mistake within seconds of the mistake being made, in a situation nobody anticipated when writing it. The original bug was about *signatures*, and this was about *package placement*. A diagnostic that only fires on the case its author imagined is worth much less than one that reports the general condition.

**Fixed.** Moved to `.lath/internal/ghsecrets/`. lath already skips `internal/`, following Go's own rule. The convention was there and the mistake was not knowing to use it.

**Prevented.** The warning is the prevention, and it is pinned by three tests including the hardest case: a namespace whose functions are *all* unusable, which is dropped entirely and would otherwise leave no trace in any output.

### Where the design changed

| Designed | Built | Why |
|---|---|---|
| `Retry` and `Loop` sharing backoff | one `runLoop` engine behind both, with a `oneShot` flag selecting vocabulary | The two report differently on purpose. A supervisor is "restarting", a one-shot operation is "retrying". A shared engine that said the same thing for both would make a failed deploy step read like a crash-looping service. |
| `maxEntryBytes` as a `const` | an unexported `var` | Proving a 2 GiB cap with a real 2 GiB archive means writing two gigabytes per test run, which is how a guard like this ends up untested. Lowering it in-package is the only way the check is exercised at all. |
| `isTerminal` fixed | an unexported `withTTYCheck` option | Without a seam the entire answer-parsing path, which answers count as yes, whether a bare Enter authorises anything, is unreachable in a test. Coverage went from 48% to 87% and the table of accepted answers is now pinned. |
| `Set.WriteFile` warning on a loose mode | refusing outright | A warning is a line in a log nobody reads. The tool this replaced wrote every credential at 0644 for a year without anyone noticing. |

### What the migration proved, including where it disappointed

The stated goal was that a "push to GitHub" is mostly reusable orchestration. Measured after the rewrite, counting code lines with comments and blanks stripped:

```
                                    before    after
.lath/secrets                          375      294
.lath/internal/ghsecrets (new)           —       75
                                    ──────   ──────
total in this project                  375      369
```

⚠️ **Six lines. The project-local line count did not meaningfully move, and that is the honest headline.**

Two reasons, both worth stating rather than explaining away. The adapter is *new code*, before, roughly twenty lines of `gh` invocation sat inline inside the push step; now it is a full `Store` with `Set`, `List`, `Delete` and `Reserved`, because reconciliation needs to read and delete, not only write, and the pipeline gained capability during the migration: `--prune` and per-key failure enumeration did not exist before.

So the fair reading is: **the same line count now buys strictly more**, not that the code got smaller.

Where the numbers *do* move:

| | Lines | Note |
|---|---|---|
| the tool being replaced (`apps/scripts/…/secrets_build.go`) | 550 | same job, one vendor, untested |
| the pipeline that replaces it | 369 | **−33%**, and split by blast radius |
| the push step alone | 43 → 27 | the loop, reserved-name skip, dry-run branch and failure accounting all left |
| `kit/secret` | 324 code, 601 test | written once, shared, and the next project writes none of it |

And the property that has no line count at all: the **`Reveal` audit is two lines**. One in `Set.WriteFile`, one in the adapter's stdin, across the entire kit *and* the migrated pipeline. Before, every value was a bare `string` and the discipline was a naming convention. That is the change worth having, and it would not show up in any diff statistic.

⚠️ The generalisable point: **extracting a primitive does not shrink the first caller.** It shrinks the second one. The first caller pays to define the interface and usually gains features while doing so, which is exactly what happened here. Anyone justifying an extraction on the first call site's line count is measuring the wrong thing, and would have concluded, from these numbers, that this was not worth doing.

---

## Rejected, with reasons

| Candidate | Why not |
|---|---|
| `dotenv` | Subsumed by [`ubgo/dotenv`](https://github.com/ubgo/dotenv), which already models entries by kind and can `Render()` a file back preserving comments and order. The exact thing a hand-rolled version would exist to do |
| `tmpl` | `text/template` **is** the primitive. A wrapper adds a name and nothing else |
| `table` | Output formatting with no mechanism underneath. If it happens, it belongs in `out` |
| `semver` | A decision-heavy domain with mature modules. Not a mechanism |
| `set` / `diff` | Comparing two collections is three lines of Go and a `map` |
| anything vendor-named | `github`, `docker`, `vault`, fails the naming check on sight. Tier 2 |

---

## What was built, and what was not

All eight, in the order the evidence suggested: `secret` first, then `lock`, `hashtree` and `ssh`, then `archive`, `confirm`, `Retry` and `download`.

`ssh` stayed tier 1 as decided, and building it did not change that view, `Extra` earned its place immediately, since the deploy it exists to serve needs flags the struct does not model.

The one thing deliberately not built: a second `secret.Store`. The interface has exactly one implementation, which is the minimum at which an interface can be wrong. It should be re-examined when a second backend appears, because two real cases will shape it better than one real case and one imagined one.
