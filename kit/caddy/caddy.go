// Package caddy installs configuration into a running Caddy and reloads it.
//
// Any Go program can use it: it knows nothing about pipelines or deploys. What
// it owns is the sequence that is the same everywhere, write the file, prove
// the configuration parses, then swap it in without dropping connections. What
// the file SAYS is the caller's, because a site block is policy and every
// project's differs.
//
// The arrangement it assumes is Caddy's own documented one: a main Caddyfile
// that imports a directory, so adding a site means adding a file rather than
// editing a shared one.
//
// Requires the docker CLI when the Caddy it drives runs in a container, which
// is the normal case and the only one Client supports today.
package caddy

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/remotefs"
	"github.com/ubgo/lath/kit/runner"
)

// Program is the CLI this package drives inside the container.
const Program = "caddy"

// DockerProgram is how a containerised Caddy is reached. Named rather than
// literal so a podman-based host is one edit.
const DockerProgram = "docker"

// DefaultConfigPath is where a container image conventionally keeps its
// Caddyfile.
const DefaultConfigPath = "/etc/caddy/Caddyfile"

// ConfigAdapter tells Caddy the file is Caddyfile syntax rather than JSON.
const ConfigAdapter = "caddyfile"

// SiteSuffix is the extension an imported site file must carry to be picked up
// by the conventional `import sites/*.caddy`.
const SiteSuffix = ".caddy"

// Client is one running Caddy.
type Client struct {
	// Container is the docker container Caddy runs in.
	Container string
	// SitesDir is the directory ON THE HOST whose contents Caddy imports,
	// normally mounted read-only into the container.
	//
	// The host path, not the container path: the file is written by the
	// runner, which is outside the container, and mounted in.
	SitesDir string
	// ConfigPath is the Caddyfile inside the container. Empty means
	// DefaultConfigPath.
	ConfigPath string
	// Where the commands run. Nil means this machine.
	Where runner.Runner
}

// SitePath is where a named site's file lands.
//
// Exported so a caller can report the path, or remove a site it no longer
// wants, without reconstructing the naming rule and getting it subtly wrong.
func (c Client) SitePath(name string) string {
	if !strings.HasSuffix(name, SiteSuffix) {
		name += SiteSuffix
	}
	return path.Join(c.SitesDir, name)
}

// EnsureSite writes a site file and applies it, leaving Caddy running the new
// configuration or the old one, never a broken one.
//
// The order is the point, and it is not the obvious one. Caddy refuses to
// START with an invalid configuration, so a bad file dropped into an imported
// directory does not merely fail to work: it takes down every OTHER site that
// Caddy serves the next time it restarts, which may be hours later and will
// look unrelated. Validating after writing and before reloading turns that
// into an error here.
//
// On a validation failure the file is removed again, so the directory is left
// exactly as it was found. Leaving it would arm precisely the delayed failure
// this sequence exists to prevent.
func (c Client) EnsureSite(ctx context.Context, name, content string) error {
	if c.Container == "" {
		return fmt.Errorf("caddy: Container is required")
	}
	if c.SitesDir == "" {
		return fmt.Errorf("caddy: SitesDir is required")
	}
	where := runner.OrLocal(c.Where)
	dst := c.SitePath(name)

	if err := remotefs.WriteFile(ctx, where, dst, content, false); err != nil {
		return fmt.Errorf("caddy: writing %s: %w", dst, err)
	}
	if err := c.Validate(ctx); err != nil {
		// Best effort: the validation error is what the caller needs, and a
		// failure to clean up must not replace it with a less useful one.
		_, _ = where.Run(ctx, "rm", []string{"-f", dst})
		return err
	}
	return c.Reload(ctx)
}

// Validate reports whether the configuration Caddy would load parses.
func (c Client) Validate(ctx context.Context) error {
	return c.exec(ctx, "validate")
}

// Reload swaps the configuration in place.
//
// Reload rather than restart: a restart drops every connection Caddy is
// serving, including those belonging to other sites on a shared instance,
// which is a poor way to deploy one service.
func (c Client) Reload(ctx context.Context) error {
	return c.exec(ctx, "reload")
}

// exec runs one caddy subcommand inside the container.
func (c Client) exec(ctx context.Context, subcommand string) error {
	configPath := c.ConfigPath
	if configPath == "" {
		configPath = DefaultConfigPath
	}
	where := runner.OrLocal(c.Where)
	args := []string{
		"exec", c.Container,
		Program, subcommand, "--adapter", ConfigAdapter, "--config", configPath,
	}
	r, err := where.Run(ctx, DockerProgram, args, proc.Capture())
	// Caddy prints what is wrong with a configuration on stderr, and that text
	// is the entire value of validating: a bare exit code says a file is bad
	// without saying which line.
	return runner.Check(Program+" "+subcommand, where, r, err)
}
