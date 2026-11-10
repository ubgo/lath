# kit/argv

Splitting a target's arguments into positionals and flags, and refusing a flag it does not know.

```go
import "github.com/ubgo/lath/kit/argv"
```

A definition's target receives plain strings (`func Deploy(ctx context.Context, args ...string) error`), and the natural way to read a flag is `slices.Contains(args, "--apply")`. That reads a misspelt flag as **absent rather than wrong**: `--aply` runs the dry run nobody asked for, and `--prebuit` quietly takes the path the flag existed to avoid. `Parse` makes the misspelling an error that names the flag it probably meant.

Deliberately small. No types, no defaults, no help text, no short-flag bundling: a target that wants those has the standard library's `flag`. This answers the one question every target has and gets wrong by default.

## API

```go
func Parse(args []string, known ...string) (Args, error)

func (a Args) Positional() []string
func (a Args) Has(flag string) bool
func (a Args) Value(flag string) (string, bool)
```

| Symbol | What |
|---|---|
| `Parse` | Separates `args`; any flag not in `known` is `ErrUnknownFlag`. A flag that takes a value is declared with a trailing `=` (`"--tag="`) |
| `Args.Positional` | The non-flag arguments, in order (a copy) |
| `Args.Has` | Whether a flag was given. Panics on a flag not declared to `Parse` |
| `Args.Value` | The value of `--name=value`, and whether it was given. Panics on an undeclared flag |
| `ErrUnknownFlag` | A flag the target does not accept |

```go
func Run(ctx context.Context, args ...string) error {
    a, err := argv.Parse(args, "--apply", "--allow-prod", "--prebuilt")
    if err != nil {
        return err   // unknown flag "--prebuit" (did you mean "--prebuilt"? accepted: --apply, --allow-prod, --prebuilt)
    }
    env := a.Positional()
    apply := a.Has("--apply")
    …
}
```

## Rules

- An argument is a flag when it starts with `-` and is not exactly `-` (stdin, by convention).
- A flag declared as `"--tag="` takes a value and must be given as `--tag=value`. The two-token form (`--tag abc prod`) is refused: read without a schema, `abc` becomes the first positional, which a target takes for the environment.
- Every other flag is a switch and must be given bare. `--apply=false` is refused, because read loosely it would mean `--apply` and apply.
- `Has` and `Value` take the flag's name without the `=`: declared as `"--tag="`, asked for as `Value("--tag")`.
- `Has` and `Value` **panic** on a name that was not declared. Answering false for `Has("--prebuit")` would move the silent misspelling from the user's command into the target's code.
- Everything after a bare `--` is positional, for a positional that starts with a dash.
- A repeated flag is allowed; the last value wins.
- The suggestion is offered only within two edits (a dropped, doubled or swapped letter). A farther one would point at a flag the user was not thinking of.

⚠️ **`known` is spelt the way a user types it, dashes included.** An entry that could never match (`"apply"`, `"--tag=x"`) is reported as the target's own mistake, not as `ErrUnknownFlag`, so it is not mistaken for the user's.
