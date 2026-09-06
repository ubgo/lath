# kit/env

Required environment variables, reported together.

```go
import "github.com/ubgo/lath/kit/env"
```

## API

```go
func Require(name string) (string, error)
func RequireAll(names ...string) error
func Get(name, fallback string) string
```

```go
if err := env.RequireAll("DATABASE_URL", "GHCR_PAT", "VPS_HOST"); err != nil {
    return err     // names EVERY missing variable, not just the first
}
token, err := env.Require("GHCR_PAT")
port := env.Get("PORT", "8080")
```

## Two decisions

⚠️ **A variable set to whitespace counts as missing.** That is the shape a shell produces from an unset lookup, `VAR="$X"` with `X` unset, and treating it as present hands an empty token to whatever asked for it, failing much later and somewhere else.

The value itself is returned **verbatim**, padding included. Only the emptiness test trims: a caller who wanted padding gone can trim it, but one whose secret legitimately ends in a space cannot get it back.

**`RequireAll` reports every missing name.** Reporting only the first turns configuring a deploy into one round trip per missing variable.

`Get` agrees with `Require` about what "set" means, so a variable cannot pass one and fail the other.

## Errors

| | |
|---|---|
| `ErrMissing` | one or more variables are unset or blank; the message names them |
