package caddy_test

import (
	"context"
	"encoding/base64"
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
	r := &runner.Fake{Reply: noSiteOnDisk()}
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
		case strings.Contains(cmd, "base64 -d"):
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
	r := &runner.Fake{Reply: append(noSiteOnDisk(), runner.Scripted{
		Match: "caddy validate", Stderr: "Caddyfile:3: unrecognized directive: revers_proxy", Exit: 1,
	})}
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

// noSiteOnDisk scripts the fake so the site file is absent: `test -e` fails,
// which remotefs reads as an answer rather than an error. A bare fake exits 0
// for everything and therefore claims every file exists.
func noSiteOnDisk() []runner.Scripted {
	return []runner.Scripted{{Match: "test -e", Exit: 1}}
}

// siteOnDisk scripts the fake so the site file appears to exist with the given
// content: `test -e` succeeds and `base64 <` returns it encoded, which is how
// remotefs reads a file back.
func siteOnDisk(content string) []runner.Scripted {
	return []runner.Scripted{
		{Match: "test -e"},
		{Match: "base64 <", Stdout: base64.StdEncoding.EncodeToString([]byte(content)) + "\n"},
	}
}

// TestEnsureSiteSkipsAnIdenticalFile. The write most likely to be refused is
// the one that changes nothing: a root-owned file in a git-tracked directory,
// byte-identical to what the deploy wants. It is not written. Validation and
// reload still run, because the route must be live when this returns.
func TestEnsureSiteSkipsAnIdenticalFile(t *testing.T) {
	t.Parallel()
	const site = "api.example.com {\n}\n"
	r := &runner.Fake{Reply: siteOnDisk(site)}
	c := caddy.Client{Container: "caddy", SitesDir: "/srv/sites", Where: r}

	if err := c.EnsureSite(context.Background(), "api.example.com", site); err != nil {
		t.Fatal(err)
	}
	if r.Ran("base64 -d") {
		t.Errorf("an identical file was rewritten: %v", r.Commands())
	}
	if !r.Ran("caddy validate") || !r.Ran("caddy reload") {
		t.Errorf("the configuration was not applied: %v", r.Commands())
	}
}

// TestEnsureSiteRewritesAChangedFile is the other half: a difference of one
// byte is a write. Without this the skip above could quietly become "never
// write when the file exists".
func TestEnsureSiteRewritesAChangedFile(t *testing.T) {
	t.Parallel()
	r := &runner.Fake{Reply: siteOnDisk("api.example.com {\n}\n")}
	c := caddy.Client{Container: "caddy", SitesDir: "/srv/sites", Where: r}

	if err := c.EnsureSite(context.Background(), "api.example.com", "api.example.com {\n\tlog\n}\n"); err != nil {
		t.Fatal(err)
	}
	if !r.Ran("base64 -d") {
		t.Errorf("a changed file was not written: %v", r.Commands())
	}
}

// TestEnsureSiteRestoresThePreviousFileItCouldNotValidate. Removing the file
// is right only when there was none: a route that worked before this deploy
// must still work after the deploy is rejected, or a broken replacement takes
// a live site down while reporting only that it was broken.
func TestEnsureSiteRestoresThePreviousFileItCouldNotValidate(t *testing.T) {
	t.Parallel()
	const previous = "api.example.com {\n}\n"
	reply := append(siteOnDisk(previous), runner.Scripted{
		Match: "caddy validate", Stderr: "Caddyfile:3: unrecognized directive: revers_proxy", Exit: 1,
	})
	r := &runner.Fake{Reply: reply}
	c := caddy.Client{Container: "caddy", SitesDir: "/srv/sites", Where: r}

	if err := c.EnsureSite(context.Background(), "api.example.com", "bad config"); err == nil {
		t.Fatal("an invalid configuration was accepted")
	}
	if r.Ran("rm -f /srv/sites/api.example.com.caddy") {
		t.Errorf("a previously good route was deleted: %v", r.Commands())
	}
	// Two writes: the new content, then the previous content put back. The
	// fake cannot see stdin, so the count is the evidence.
	var writes int
	for _, cmd := range r.Commands() {
		if strings.Contains(cmd, "base64 -d") {
			writes++
		}
	}
	if writes != 2 {
		t.Errorf("%d write(s), want the new file and then the restored one: %v", writes, r.Commands())
	}
	if r.Ran("caddy reload") {
		t.Error("an invalid configuration was reloaded anyway")
	}
}
