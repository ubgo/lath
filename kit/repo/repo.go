// Package repo locates the repository a task is running in.
//
// It exists because a shell script gets this free from $0 and a compiled
// binary does not. A lath definition is built into a cache directory far from
// its source, so the executable's own path says nothing about where the
// repository is. The answer has to come from the working directory instead.
package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Markers identify a repository root, in priority order.
//
// go.work first, then go.mod, then .git. The first two suit a Go repository
// and are precise; .git is the fallback for a definition living in a
// repository of another language, which lath explicitly supports.
//
// Exported so a caller with an unusual layout can search for its own marker
// rather than being told this list is the only possible answer.
var Markers = []string{"go.work", "go.mod", ".git"}

// ErrNoRepo means no marker was found above the starting directory.
var ErrNoRepo = errors.New("repo: no repository root found")

// Root returns the repository root containing the working directory.
// Option configures a search.
type Option func(*config)

type config struct{ markers []string }

// WithMarkers replaces the files that identify a root.
//
// A per-call option rather than mutation of the package's Markers slice:
// that slice is shared, so changing it races with every other caller and
// silently redefines "the repository" for code that never asked. This affects
// one lookup and nothing else.
//
// For a repository that marks its root differently, a .hg, a WORKSPACE, a
// lerna.json.
func WithMarkers(markers ...string) Option {
	return func(c *config) { c.markers = markers }
}

func build(opts []Option) config {
	c := config{markers: Markers}
	for _, o := range opts {
		o(&c)
	}
	if len(c.markers) == 0 {
		c.markers = Markers
	}
	return c
}

// Root reports the repository root containing the working directory.
//
// Found by walking upward for a marker, .git by default, see WithMarkers ,
// rather than by asking git, so it works in a checkout without the binary
// present and costs no process. That also means it answers for any project
// with a recognisable root, not only a git one.
//
// Invariant: returns an error rather than a best guess when no marker is
// found. A wrong root silently resolves every relative path in a deploy
// against the wrong tree, which is far worse than stopping.
func Root(opts ...Option) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("repo: %w", err)
	}
	return RootFrom(wd, opts...)
}

// RootFrom is Root, starting from a given directory.
//
// Exists so tests never need to chdir. A test that changes the process's
// working directory cannot run in parallel with any other.
func RootFrom(dir string, opts ...Option) (string, error) {
	c := build(opts)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("repo: %w", err)
	}
	for {
		for _, marker := range c.markers {
			if _, err := os.Stat(filepath.Join(abs, marker)); err == nil {
				return abs, nil
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs { // reached the filesystem root
			return "", fmt.Errorf("repo: looked for %v above %s: %w", c.markers, dir, ErrNoRepo)
		}
		abs = parent
	}
}

// Path joins parts onto the repository root.
func Path(parts ...string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

// MustPath is Path, panicking on failure.
//
// For a definition's package-level vars, where there is no error to return and
// no useful work to do without a repository root. Panicking at startup with a
// clear message beats every later call failing with an empty path.
func MustPath(parts ...string) string {
	p, err := Path(parts...)
	if err != nil {
		panic(err)
	}
	return p
}
