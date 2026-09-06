# kit/scan

Walking, filtering and grepping a directory tree.

```go
import "github.com/ubgo/lath/kit/scan"
```

## API

```go
func Files(root string, f Filter) ([]string, error)
func Match(files []string, pattern *regexp.Regexp) ([]Hit, error)
func Grep(root string, f Filter, pattern *regexp.Regexp) ([]Hit, error)

type Filter struct {
    Ext        []string   // ".go" — WITH the dot; empty means any
    Exclude    []string   // path substrings; beats Include
    Include    []string   // path substrings; empty means all
    SkipHidden bool       // drop dot-directories and dot-files
}

type Hit struct{ File string; Line int; Text string }
func (h Hit) String() string      // "file:12: text" — editors parse this
```

```go
hits, err := scan.Grep(".", scan.Filter{
    Ext:        []string{".go"},
    Exclude:    []string{"vendor/", "/gen/"},
    SkipHidden: true,
}, regexp.MustCompile(`\bTODO\b`))

for _, h := range hits {
    fmt.Println(h)     // apps/api/main.go:42: // TODO: retry
}
```

`Grep` is exactly `Files` followed by `Match`, so a caller can drop to the parts without a change in behaviour.

## Details

**Exclude beats Include.** The other order would make `Exclude` advisory.

⚠️ **`Ext` needs the dot.** `"go"` instead of `".go"` matches nothing. A silent empty result, which is why it is worth stating.

**Results are in `WalkDir`'s lexical order**, so output is stable between runs.

**Line numbers are 1-based**, and `Text` is trimmed. The pattern is matched against the **untrimmed** line, so `^package ` anchors work as written.

**A missing root is an error**, never an empty result. "No matches" and "could not look" must not be the same answer. A lint that silently finds nothing passes CI.

**Lines up to 1MB are handled**, so a minified bundle does not abort a scan. Past that, the error names the file rather than silently skipping it.

**Binary files are data, not a crash.** A tree contains `.png` and `.so` files; NUL bytes are matched like anything else.

**A directory named `pkg.go` is never returned by an `Ext: [".go"]` filter**. The type is checked, not only the name.
