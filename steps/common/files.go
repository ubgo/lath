package common

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ubgo/lath/kit/remotefs"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/secret"
	"github.com/ubgo/lath/pipeline"
)

// PutFile writes a file wherever its Runner points, local or remote.
//
// A credential written this way never touches the caller's disk, see
// remotefs.WriteFile.
type PutFile struct {
	// Path is the destination, on the runner.
	Path string
	// Content is literal text to write. Use Secret for credentials.
	Content string
	// Secret is a credential to write. Takes precedence over Content, and is
	// never logged.
	Secret secret.Value
	// Sensitive gives the file an owner-only mode. Implied by Secret, so a
	// credential cannot be written world-readable by forgetting a flag.
	Sensitive bool
	// Mode sets the file mode explicitly, overriding Sensitive. Zero means
	// Sensitive decides, 0600 or 0644.
	//
	// For the file that is neither: a script that must be executable, a
	// directory a group must reach.
	Mode os.FileMode
	// Owner is a "uid:gid" to chown the written file to. Empty leaves
	// ownership alone. See remotefs.WriteFileOwner for why a mode alone is not
	// enough when another user, a container's, must read the file.
	Owner string
	// Runner is where it lands. Nil means this machine.
	Runner runner.Runner
}

func (PutFile) Name() string { return "put-file" }

// Docs explains the step in a plan. See pipeline.Documented.
func (p PutFile) Docs() pipeline.Docs {
	return pipeline.Docs{Summary: "write a file on the target", Detail: p.Path}
}
func (PutFile) Requires() []pipeline.Key { return nil }
func (PutFile) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Writes the same bytes to the same path at the same mode. The
// file after two writes is the file after one.
func (PutFile) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (p PutFile) Validate() error {
	if p.Path == "" {
		return fmt.Errorf("Path is required")
	}
	return nil
}

func (p PutFile) Run(ctx context.Context, s *pipeline.State) error {
	body, sensitive := p.body()
	where := runner.OrLocal(p.Runner)

	if s.DryRun() {
		s.Detailf("would write %d bytes to %s %s", len(body), p.Path, where.Describe())
		return nil
	}
	// The mode is resolved here rather than branching on which remotefs call
	// to make, so Owner composes with every combination of Mode/Sensitive
	// instead of only some of them.
	mode := p.Mode
	if mode == 0 {
		mode = defaultMode(sensitive)
	}
	err := remotefs.WriteFileOwner(ctx, where, p.Path, body, mode, p.Owner)
	if err != nil {
		return err
	}
	// The byte count, never the content: this step routinely carries secrets.
	s.Detailf("wrote %d bytes to %s", len(body), p.Path)
	return nil
}

// defaultMode is the mode PutFile uses when the caller did not set one:
// owner-only for a credential, world-readable otherwise.
//
// Taken from remotefs rather than redeclared, so this package cannot drift
// from what remotefs.WriteFile would have chosen for the same content, which
// matters because PutFile no longer calls it, having moved to the owner-aware
// path.
func defaultMode(sensitive bool) os.FileMode {
	if sensitive {
		return remotefs.SensitiveMode
	}
	return remotefs.PublicMode
}

// body returns what to write and whether it must be owner-only.
func (p PutFile) body() (string, bool) {
	if !p.Secret.IsZero() {
		return p.Secret.Reveal(), true
	}
	return p.Content, p.Sensitive
}

// EnsureDir creates directories on the runner, optionally owned by a uid:gid.
type EnsureDir struct {
	// Paths are the directories to create.
	Paths []string
	// Owner is a "uid:gid" to chown to. Empty leaves ownership alone. See
	// remotefs.MkdirAll for why it matters.
	Owner string
	// Mode sets the directory mode. Zero uses mkdir's default.
	Mode os.FileMode
	// Runner is where they are created. Nil means this machine.
	Runner runner.Runner
}

func (EnsureDir) Name() string { return "ensure-dirs" }

// Docs explains the step in a plan. See pipeline.Documented.
func (e EnsureDir) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "create the directories the release needs, owned by the container's user",
		Detail:  strings.Join(e.Paths, ", "),
	}
}
func (EnsureDir) Requires() []pipeline.Key { return nil }
func (EnsureDir) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. mkdir -p and a best-effort chown: the second run finds the
// directories already as it wants them.
func (EnsureDir) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (e EnsureDir) Validate() error {
	if len(e.Paths) == 0 {
		return fmt.Errorf("at least one Path is required")
	}
	return nil
}

func (e EnsureDir) Run(ctx context.Context, s *pipeline.State) error {
	where := runner.OrLocal(e.Runner)
	if s.DryRun() {
		s.Detailf("would create %v %s", e.Paths, where.Describe())
		return nil
	}
	if err := remotefs.MkdirAllMode(ctx, where, e.Owner, e.Mode, e.Paths...); err != nil {
		return err
	}
	s.Detailf("%d director(ies) ready %s", len(e.Paths), where.Describe())
	return nil
}
