# kit/secret

Credentials that cannot be printed by accident, grouped into sets, and reconciled against wherever they must be stored.

```go
import "github.com/ubgo/lath/kit/secret"
```

## Sync events

`Progress(fn)` receives an `Event` per key, whose `Kind` is one of:

| Kind | Meaning |
|---|---|
| `EventSet` | The value was written |
| `EventUnchanged` | Already correct; nothing sent |
| `EventDeleted` | Removed by `Prune`, present in the store, absent from the desired set |
| `EventSkipped` | Not attempted; the store declined it, or a dry run |
| `EventProvisioned` | The store created something it needed first, via `Provisioner` |
| `EventFailed` | This key failed. The sync continues and reports every failure at the end |

⚠️ **`EventFailed` is not the end of the run.** `Sync` deliberately continues past a failure so one bad key does not hide the other nineteen; the `Report` carries them all.

## Why it exists

A credential in a `string` is one careless `%v` from a log that is retained for ninety days. Every guard built on discipline, "remember to redact this", eventually meets someone who did not remember. `secret.Value` closes every standard path out, so forgetting produces a redaction rather than a disclosure.

## Value. A credential that will not print

```go
func New(plaintext string) Value
func FromEnv(name string) (Value, error)      // wraps env.Require: blank counts as missing
func FromFile(path string) (Value, error)     // bytes never pass through a printable variable

func (v Value) Reveal() string                // the ONLY way out
func (v Value) Len() int                      // safe to log
func (v Value) IsZero() bool
func (v Value) Equal(other Value) bool         // constant-time
```

Every one of these is closed and returns a redaction, never the value:

```go
String()  GoString()  Format()  MarshalJSON()  MarshalText()  LogValue()
```

So all of this is safe:

```go
v := secret.New("ghp_realtoken")

fmt.Printf("%v %s %q %#v\n", v, v, v, v)      // «secret 14 bytes» ×4
json.Marshal(struct{ Token secret.Value }{v}) // {"Token":"«secret 14 bytes»"}
slog.Info("starting", "token", v)             // token=«secret 14 bytes»
fmt.Errorf("failed with %v", v)               // …with «secret 14 bytes»
```

`UnmarshalJSON` and `UnmarshalText` stay **open**, because reading a credential out of a config file is how one normally arrives:

```go
type Config struct {
    Host  string
    Token secret.Value `json:"token"`
}
var cfg Config
json.Unmarshal(data, &cfg)     // works
cfg.Token.Reveal()             // "ghp_realtoken"
json.Marshal(cfg)              // redacted again — the round trip cannot leak
```

⚠️ **JSON has no plaintext path at all.** Not a default that can be overridden. There is no wrapper, no option. Writing a real credentials file is done deliberately through `Reveal()`, because emitting live credentials to disk should never be a side effect of marshalling a struct that happened to be in scope.

**`Reveal` is named to be greppable.** The audit for a whole codebase is one command:

```sh
grep -rn '\.Reveal()' --include='*.go' | grep -v _test
```

Keep the result short enough to read on one screen. Growth is the signal to look.

**Why `Equal` exists**, given a task runner rarely compares credentials: without it, a caller writes `a.Reveal() == b.Reveal()`, which puts two plaintext strings into locals and adds two entries to that audit. Constant-time comparison is then free.

## Set, a named group

```go
func NewSet() *Set
func (s *Set) Put(key string, v Value)
func (s *Set) Get(key string) (Value, bool)
func (s *Set) Has(key string) bool
func (s *Set) Keys() []string                 // sorted
func (s *Set) Len() int
func (s *Set) String() string                 // "13 secrets" — no names, no values
func (s *Set) Summary() []string              // ["GHCR_PAT (40 bytes)", …]
func (s *Set) WriteFile(path string, perm os.FileMode, header ...string) error
```

Iteration is **sorted by key**, and that matters more than it looks: a rendered file and a push order both derive from it, and an unstable order makes every regeneration look like a change.

`String()` reports a count and nothing else. Not even names, because a secret's name often discloses what system it opens.

```go
set := secret.NewSet()
set.Put("GHCR_PAT", token)
set.Put("VPS_HOST", host)

for _, line := range set.Summary() {
    fmt.Println(" ", line)                    // GHCR_PAT (40 bytes)
}
err := set.WriteFile("/srv/.secrets", 0o600, "generated — do not commit")
```

⚠️ `WriteFile` **refuses** any mode readable beyond the owner, returning `ErrPermTooOpen`. Refused rather than warned about: the tool this replaced wrote every secret at 0644 for a year and nobody noticed.

## Store and Sync, reconciliation

`Store` is an interface, so this package names no vendor. GitHub Environments, Vault, 1Password and AWS Parameter Store are all adapters written elsewhere.

```go
type Store interface {
    Set(ctx context.Context, key string, v Value) error
    List(ctx context.Context) ([]string, error)
    Delete(ctx context.Context, key string) error
    Reserved(key string) bool          // the store declares names it refuses
}

func Sync(ctx context.Context, store Store, desired *Set, opts ...SyncOption) (Report, error)

func DryRun() SyncOption        // report the plan, change nothing
func Prune() SyncOption         // delete live entries absent from desired
func Provision() SyncOption     // create the destination first — see below
func Progress(fn func(Event)) SyncOption
```

```go
report, err := secret.Sync(ctx, store, set,
    secret.Progress(func(e secret.Event) { fmt.Println(" ", e) }),
    secret.Prune(),
)
fmt.Println(report)              // "11 set, 2 skipped, 1 deleted"
for _, key := range report.FailedKeys() {
    fmt.Printf("  FAILED %s: %v\n", key, report.Failed[key])
}
```

**A failure on one credential does not stop the run.** Every entry is attempted and every failure recorded, because aborting midway leaves a half-published set whose boundary the operator cannot determine. The error wraps `ErrSyncIncomplete`; the `Report` says exactly what happened.

`Report` is complete by construction: every key in the desired set appears in exactly one of `Set`, `Unchanged`, `Skipped` or `Failed`. `Accounted()` checks that.

### Two optional upgrades

A `Store` may implement either, and `Sync` type-asserts for them. Optional rather than part of `Store`, because most backends cannot do them and requiring an implementation would force them to lie.

```go
type Comparer interface {                    // for a store that can read values back
    Same(ctx context.Context, key string, v Value) (bool, error)
}
type Provisioner interface {                 // for a destination that must exist first
    Ensure(ctx context.Context) error
}
```

Without a `Comparer`, **every credential is written every time**. That is deliberate: a local digest of what was last pushed would be quieter and would live on one machine, so a push from CI and a push from a laptop would silently disagree about what is current. No local cache is written under any circumstance.

`Provision()` is **opt-in and never automatic**. The destination is usually named by a positional argument, so auto-creating would turn a typo into provisioned infrastructure instead of the error that tells the operator they mistyped. Under `DryRun` it stays entirely read-only and reports the intent.

## Writing a Store

Roughly twenty lines. This is the whole GitHub adapter:

```go
type ghEnv struct{ env string }

func (e ghEnv) Reserved(key string) bool { return strings.HasPrefix(key, "GITHUB_") }

func (e ghEnv) Set(ctx context.Context, key string, v secret.Value) error {
    r, err := proc.Run(ctx, "gh", []string{"secret", "set", key, "--env", e.env},
        proc.Stdin(strings.NewReader(v.Reveal())), proc.Capture())
    switch {
    case err != nil:   return err
    case !r.OK():      return fmt.Errorf("%s: %s", r, strings.TrimSpace(string(r.Stderr)))
    }
    return nil
}
```

⚠️ Note `proc.Stdin`, not an argument. **A credential passed as an argument is visible in the process list to every user on the host.**

## Errors

| | |
|---|---|
| `ErrPermTooOpen` | `WriteFile` was given a mode others could read |
| `ErrSyncIncomplete` | at least one credential was not written; see `Report.Failed` |
| `ErrCannotProvision` | `Provision()` was requested of a store that is not a `Provisioner` |
