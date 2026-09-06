package caddy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/caddy"
	"github.com/ubgo/lath/kit/runner"
)

// TestSitePathAddsTheSuffixCaddyImportsOn. The conventional main Caddyfile
// does `import sites/*.caddy`, so a file written without the extension is a
// route that silently never exists.
func TestSitePathAddsTheSuffixCaddyImportsOn(t *testing.T) {
	t.Parallel()
	c := caddy.Client{SitesDir: "/srv/sites"}
	for _, name := range []string{"api.example.com", "api.example.com.caddy"} {
		if got, want := c.SitePath(name), "/srv/sites/api.example.com.caddy"; got != want {
			t.Errorf("SitePath(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestEnsureSiteValidatesBeforeReloading is the whole reason this package
// exists. Caddy refuses to START on an invalid configuration, so a bad file in
// an imported directory takes down every OTHER site the next time it restarts,
// hours later and looking unrelated.
func TestEnsureSiteValidatesBeforeReloading(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	c := caddy.Client{Container: "caddy", SitesDir: "/srv/sites", Where: r}

	if err := c.EnsureSite(context.Background(), "api.example.com", "example.com {\n}\n"); err != nil {
		t.Fatal(err)
	}
	cmds := r.Commands()
	var order []string
	for _, cmd := range cmds {
		switch {
		case strings.Contains(cmd, "caddy validate"):
			order = append(order, "validate")
		case strings.Contains(cmd, "caddy reload"):
			order = append(order, "reload")
		case strings.Contains(cmd, "/srv/sites/api.example.com.caddy"):
			order = append(order, "write")
		}
	}
	if len(order) < 3 || order[0] != "write" || order[1] != "validate" || order[2] != "reload" {
		t.Errorf("sequence was %v, want write, validate, reload: %v", order, cmds)
	}
}

// TestEnsureSiteRemovesAFileItCouldNotValidate. Leaving a broken file behind
// arms exactly the delayed, unrelated-looking failure the validation exists to
// prevent: Caddy keeps serving until something restarts it, and then does not
// come back.
func TestEnsureSiteRemovesAFileItCouldNotValidate(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: []runner.Scripted{
		{Match: "caddy validate", Stderr: "Caddyfile:3: unrecognized directive: revers_proxy", Exit: 1},
	}}
	c := caddy.Client{Container: "caddy", SitesDir: "/srv/sites", Where: r}

	err := c.EnsureSite(context.Background(), "api.example.com", "bad config")
	if err == nil {
		t.Fatal("an invalid configuration was accepted")
	}
	// Caddy's own message names the line; a bare exit code does not.
	if !strings.Contains(err.Error(), "unrecognized directive") {
		t.Errorf("err = %v, want caddy's diagnostic", err)
	}
	var removed, reloaded bool
	for _, cmd := range r.Commands() {
		if strings.Contains(cmd, "rm -f /srv/sites/api.example.com.caddy") {
			removed = true
		}
		if strings.Contains(cmd, "caddy reload") {
			reloaded = true
		}
	}
	if !removed {
		t.Errorf("the invalid file was left in the imported directory: %v", r.Commands())
	}
	if reloaded {
		t.Error("an invalid configuration was reloaded anyway")
	}
}

// TestEnsureSiteRefusesAnIncompleteClient, rather than running `docker exec ""`
// and reporting whatever docker says about an empty container name.
func TestEnsureSiteRefusesAnIncompleteClient(t *testing.T) {
	t.Parallel()
	for _, c := range []caddy.Client{
		{SitesDir: "/srv/sites"},
		{Container: "caddy"},
	} {
		if err := c.EnsureSite(context.Background(), "x", "y"); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

// TestReloadNotRestart. A restart drops every connection Caddy is serving,
// including other sites on a shared instance.
func TestReloadNotRestart(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{}
	c := caddy.Client{Container: "caddy", SitesDir: "/srv/sites", Where: r}
	if err := c.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.Commands(), " ")
	if !strings.Contains(got, "caddy reload") {
		t.Errorf("commands = %q", got)
	}
	if strings.Contains(got, "docker restart") || strings.Contains(got, "caddy stop") {
		t.Errorf("the configuration was applied by restarting: %q", got)
	}
}
