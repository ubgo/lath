# `kit/settings` — a file the environment can override

The shape every deployment needs: values live in a file so a laptop deploy needs no ceremony, and any of them can be overridden by an environment variable so CI needs no file.

```go
cfg, err := settings.Load("/path/to/deploy.env")   // a missing file is not an error
if err := cfg.Require("VPS_HOST", "VPS_USER"); err != nil {
    return err                                     // names EVERY missing key
}
host := cfg.Get("VPS_HOST")
port := cfg.Int("VPS_PORT", 22)
```

| Symbol | What |
|---|---|
| `Load(path)` | Reads the file with the small dialect below. **A missing file is not an error** |
| `From(values, path)` | Wraps values another parser produced, keeping the override and `Require` behaviour. `path` is only for error messages |
| `Set.Get(key)` | The environment when the variable is present, otherwise the file |
| `Set.Int(key, fallback)` | Unset or unparseable falls back |
| `Set.Has(key)` | Set somewhere and not empty |
| `Set.Require(keys…)` | Every missing key at once, naming the file to edit |
| `Set.Path()` | The file it was loaded from |

## The dialect

`KEY=VALUE`, `#` comments, optional surrounding quotes, optional `export `. It is **not** a shell parser and does no interpolation, because a settings file that can execute is one that can surprise. A line with no `=` is skipped; a line with no name before the `=` is an error naming the line number.

## Keeping a secret out of the file

This package does not do `$(command)` substitution, and should not: a real .env parser already does. Parse with that and hand the result over:

```go
values, err := dotenv.Read(path, dotenv.WithCommandSubstitution(true))
cfg := settings.From(values, path)
```

```
CF_API_TOKEN=$(gopass show -o personal/cloudflare/api-dns-edit-all-zones)
```

The credential then lives in the password manager and the file holds the instruction to fetch it. `From` keeps what this package is actually good at, the environment override and reporting every missing key at once, and leaves parsing to something that specialises in it.

`Load` remains for a caller that wants no dependency and whose values are literals.

## Gotchas

⚠️ **Present wins, not non-empty.** `DOMAIN=` in the environment CLEARS what the file said. That is the only way to switch off something a shared file turns on, and it is what a test needs to reach a state the checkout's own file otherwise decides. A caller wanting "empty means absent" compares to `""` itself.

⚠️ **Values are plain strings, including secrets.** A registry token read through `Get` is an ordinary string, not a `secret.Value`, so it is a `%v` away from a log line. Wrap it in `secret.New` at the point you read it if it goes anywhere near output. This applies to a substituted value too: it stays out of the file, not out of memory.

⚠️ **The environment is read at `Get` time**, not at `Load` time, so a `Set` cannot go stale against the environment it is asked about. It also means `Get` is not free of side conditions: change the environment, change the answer.

`Require` reports all missing keys rather than the first because discovering three missing values one attempt at a time is three round trips through whatever slow thing follows, which for a deploy is an image build.
