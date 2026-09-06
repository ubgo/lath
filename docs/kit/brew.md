# `kit/brew` — the Homebrew formula for a released binary

Renders the formula. Where it is committed, which tap it belongs to and what the description says are decisions about a project; the shape Homebrew demands is not, and getting it wrong is a failure a user meets at `brew install` rather than at release.

```go
formula := brew.Formula{
    Name: "volt", Description: "Build, release and ship Go CLIs",
    Homepage: "https://github.com/acme/tools", Version: "0.1.0",
    Platforms: []brew.Platform{
        {OS: brew.OSMacOS, Arch: brew.ArchARM64, URL: url, SHA256: sum},
    },
}
text, err := formula.Render()
```

Publishing is the composition, and it is tested end to end against a real repository:

```go
tap, _ := git.Clone(ctx, nil, tapURL, dir)
os.WriteFile(filepath.Join(dir, formula.Path()), []byte(text), 0o644)
tap = tap.WithIdentity("release bot", "bot@example.com")
tap.Commit(ctx, "volt 0.1.0", formula.Path())
tap.Push(ctx, "")
```

| Symbol | What |
|---|---|
| `Formula` | Name, Description, Homepage, Version, License, Binary, Platforms, TestCommand |
| `Platform` | One downloadable build: `OS`, `Arch`, `URL`, `SHA256` |
| `Render()` | The Ruby source, validated first |
| `Validate()` | Everything wrong, in one error |
| `Path()` | `Formula/<name>.rb`, where taps keep them |
| `ClassName(name)` | Homebrew's class rule: `my-cli` → `MyCli` |
| `OSMacOS` · `OSLinux` · `ArchARM64` · `ArchAMD64` | Go's words, translated to the DSL's |
| `OSValues` · `ArchValues` | The canonical lists |

## Gotchas

⚠️ **A `v` prefix on the version is refused, not trimmed.** Homebrew compares versions, and a `v` makes every comparison wrong — the symptom is `brew upgrade` quietly never seeing a new release. Trimming would leave the caller and the formula disagreeing about what shipped.

⚠️ **An empty `SHA256` is refused.** It renders a formula Homebrew accepts and then fails to verify, which is worse than one that does not render.

⚠️ **This is a binary formula only.** It downloads a prebuilt archive per platform and installs the executable. Formulas that build from source, carry dependencies or install services are a different shape, and pretending one template covers them would produce plausible files that do not work.

Platforms are emitted in a fixed order (macOS before Linux, arm before intel) whatever order they were given: the file is committed to a tap and diffed every release. A duplicate `os/arch` is refused because Ruby would take the second one silently.
