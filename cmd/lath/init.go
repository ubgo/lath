package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// goDirectivePattern extracts the version from a `go 1.24` line in a go.mod.
// Matched textually rather than via the modfile package to keep this binary
// free of dependencies it would otherwise not need.
var goDirectivePattern = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)

// scaffold creates a definition directory in dir.
//
// Why this is a runner-owned command rather than something a user assembles by
// hand: a definition needs a go.mod whose module path, `go` directive and
// requirements all line up, and a package that is `main` WITHOUT a `func main`.
// That last part is unusual enough that hand-writing it invites a `func main`
// nobody can remove later without breaking the generated dispatcher.
//
// Invariant: refuses rather than overwriting. A definition holds real deploy
// logic, and clobbering it because someone re-ran a setup command would be
// unrecoverable from the tool's side.
func scaffold(dir, enginePath string) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("scaffold %s: %w", dir, ErrAlreadyInitialised)
	}
	if err := os.MkdirAll(dir, definitionDirPerm); err != nil {
		return fmt.Errorf("scaffold %s: %w", dir, err)
	}

	goVersion := parentGoDirective(".")
	files := map[string]string{
		goModFile:       renderGoMod(goVersion, enginePath),
		starterFileName: renderStarter(),
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), scaffoldFilePerm); err != nil {
			return fmt.Errorf("scaffold %s: %w", path, err)
		}
	}
	return nil
}

// parentGoDirective reads the `go` version from the repository's own go.mod so
// the definition compiles with the same toolchain as the application.
//
// A mismatch is not fatal. Go downloads a newer toolchain on demand, but that
// is an invisible network fetch mid-deploy, which is worth avoiding.
//
// Returns fallbackGoDirective when there is no go.mod: a definition may live in
// a repository of any language, and lath deploys more than Go applications.
func parentGoDirective(repoRoot string) string {
	content, err := os.ReadFile(filepath.Join(repoRoot, goModFile))
	if err != nil {
		return fallbackGoDirective
	}
	if m := goDirectivePattern.FindSubmatch(content); m != nil {
		return string(m[1])
	}
	return fallbackGoDirective
}

// renderGoMod builds the definition's module file.
//
// enginePath, when non-empty, adds replace directives pointing the engine
// requirements at a local checkout. That is a development affordance: until the
// engine modules are published, a definition cannot resolve them any other way,
// and emitting a go.mod that provably cannot tidy would be a worse default than
// asking for the path.
func renderGoMod(goVersion, enginePath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\ngo %s\n", definitionModuleName, goVersion)

	if enginePath == "" {
		// PUBLISHED ENGINE. No require and no replace: `go mod tidy` reads the
		// imports in the starter file and resolves real versions from the
		// module proxy. Emitting `v0.0.0` here would pin a version that does
		// not exist and tidy would fail.
		return b.String()
	}

	// LOCAL ENGINE CHECKOUT. The engine is not published, so the definition can
	// only resolve it through a replace. The v0.0.0 requirement is a
	// placeholder. A replace overrides the version entirely, so the value is
	// never fetched or compared.
	b.WriteString("\nrequire (\n")
	for _, m := range engineModules {
		fmt.Fprintf(&b, "\t%s v0.0.0\n", m)
	}
	b.WriteString(")\n")
	b.WriteString("\n// Local engine checkout, supplied when this definition was scaffolded.\n")
	b.WriteString("// Delete this block once the engine is published, then run\n")
	b.WriteString("// `go mod tidy` to pin real versions.\n")
	b.WriteString("replace (\n")
	for _, m := range engineModules {
		// The replace points at the module's OWN directory, which is the last
		// element of its import path. A replace aimed at the repository root
		// would resolve nothing: the root is a workspace, not a module.
		fmt.Fprintf(&b, "\t%s => %s\n", m, filepath.Join(enginePath, path.Base(m)))
	}
	b.WriteString(")\n")
	return b.String()
}

// renderStarter builds the example definition.
//
// It ships two commands rather than one because the pair demonstrates the
// distinction that matters most: Plan validates and describes without touching
// anything, Deploy executes. A single-command starter tends to become a deploy
// that nobody ever previews.
func renderStarter() string {
	return `// Package main is this repository's deploy definition.
//
// Every EXPORTED function here is a command. Writing one declares it, there is
// no registration list, no switch, and no func main. Supported shapes:
//
//	func Name()
//	func Name() error
//	func Name(ctx context.Context) error
//	func Name(args ...string) error
//	func Name(ctx context.Context, args ...string) error
//
// The first sentence of each doc comment becomes its help text in ` + "`lath -l`" + `.
//
// ` + "`go build ./...`" + ` FAILS here, and that is expected: this is package main
// with no func main, because lath generates the entry point at build time.
// ` + "`go vet`" + `, ` + "`go test`" + `, ` + "`gofmt`" + ` and the editor all work normally.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ubgo/lath/pipeline"
	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/steps/common"
	"github.com/ubgo/lath/steps/docker"
	"github.com/ubgo/lath/steps/git"
)

// Plan lists the steps without executing any of them.
//
// Safe to run against production: validation is side-effect free and no step is
// invoked. Use it as the review step before a real deploy.
func Plan() error {
	p := deployPipeline()
	if err := p.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	fmt.Printf("pipeline %q: %d steps (nothing executed)\n", p.Name, len(p.Steps))
	for _, e := range p.Plan() {
		fmt.Printf("%2d/%d  %-18s needs=%v gives=%v\n",
			e.Position, e.Total, e.Name, e.Requires, e.Provides)
	}
	return nil
}

// Deploy builds and ships the application.
func Deploy(ctx context.Context) error {
	st := pipeline.NewState(pipeline.ModeExecute, pipeline.NewTextReporter(os.Stdout))
	if err := deployPipeline().Run(ctx, st); err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	fmt.Println("done")
	return nil
}

// deployPipeline is unexported, so it is NOT a command, that is how helpers
// stay out of the command list.
//
// Replace these steps with your own. Ordering invariants that validation cannot
// infer for you: migrate before processes start, health-check before traffic
// moves, retire the previous version last.
func deployPipeline() pipeline.Pipeline {
	return pipeline.Pipeline{
		Name: "deploy",
		Steps: []pipeline.Step{
			git.ResolveCommit{},
			common.ResolveEnv{Environment: "prod"},
			docker.Build{
				Repository: "app",
				// Options carries everything docker build accepts.
				Options: dockerkit.BuildOptions{Dockerfile: "Dockerfile", Context: "."},
			},
		},
	}
}
`
}

// tidy runs `go mod tidy` in dir so go.sum exists.
//
// go.sum is not optional: Go defaults to -mod=readonly, so without it neither
// the compiler nor the language server will fetch anything, they refuse
// instead. Failure here is reported but not fatal, because the usual cause is
// an engine module that is not published yet, which the caller can explain
// better than this function can.
func tidy(dir string) error {
	cmd := exec.Command(goToolName, "mod", "tidy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), goWorkOffEnv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go mod tidy in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
