package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateCache points the cache at a temp directory.
//
// cacheDir reads os.UserCacheDir, which honours XDG_CACHE_HOME on unix and
// falls back to TMPDIR, setting both covers every platform this builds for
// without the test knowing which one it is on.
func isolateCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("TMPDIR", dir)
	// Windows: os.UserCacheDir reads %LocalAppData% and ignores every variable
	// above, so without this the isolation did nothing there and the tests ran
	// against the real cache — "got 1 entries from an empty cache".
	t.Setenv("LocalAppData", dir)
	// ASK cacheDir where it landed rather than predicting it: os.UserCacheDir
	// resolves differently per platform, ~/Library/Caches on macOS,
	// XDG_CACHE_HOME on unix, and a test that guessed would silently write
	// somewhere the code under test never looks.
	return cacheDir()
}

func TestCacheDirIsNamespaced(t *testing.T) {
	isolateCache(t)
	// The subdirectory is what stops lath's entries mingling with every other
	// tool's in a shared cache root.
	if got := filepath.Base(cacheDir()); got != cacheSubdir {
		t.Errorf("cacheDir() = %q, want it to end in %q", cacheDir(), cacheSubdir)
	}
}

// TestSanitiseCacheName covers the readable half of an entry's filename. It is
// built from a directory path, which may contain anything a filesystem allows
// and the result is itself used as a filename.
func TestSanitiseCacheName(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"acme_api", "acme_api"},
		{"My Project", "my-project"},
		{"a/b/c", "a-b-c"},
		{"UPPER", "upper"},
		{"weird:name*here", "weird-name-here"},
		{"", ""},
		{"...", "..."},
	}
	for _, tc := range cases {
		got := sanitiseCacheName(tc.in)
		if strings.ContainsAny(got, `/\:*?"<>|`) {
			t.Errorf("sanitiseCacheName(%q) = %q, still contains a path or shell metacharacter", tc.in, got)
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("sanitiseCacheName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCacheEntryPathIsHashKeyed pins the property the whole cache rests on:
// the entry's IDENTITY is the content hash, and the readable prefix is
// decoration. Two projects sharing a name must not share an entry.
func TestCacheEntryPathIsHashKeyed(t *testing.T) {
	isolateCache(t)
	a := cacheEntryPath(definition{Dir: ".lath", Hash: "aaaa1111"})
	b := cacheEntryPath(definition{Dir: ".lath", Hash: "bbbb2222"})
	if a == b {
		t.Fatal("two different hashes produced one path")
	}
	if !strings.HasSuffix(a, cacheNameSeparator+"aaaa1111"+binarySuffix) {
		t.Errorf("path %q does not end in its hash", a)
	}
}

func TestManifestRoundTrips(t *testing.T) {
	isolateCache(t)
	bin := filepath.Join(t.TempDir(), "entry")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	def := definition{Dir: ".lath", Hash: "c0ffee11"}
	targets := []Target{
		{Command: "deploy", Func: "Deploy"},
		{Command: "secrets push", Func: "Push", Namespace: "secrets"},
	}
	if err := writeManifest(bin, def, targets); err != nil {
		t.Fatal(err)
	}

	m, err := readManifest(bin)
	if err != nil {
		t.Fatal(err)
	}
	if m.Hash != def.Hash {
		t.Errorf("Hash = %q, want %q", m.Hash, def.Hash)
	}
	if !filepath.IsAbs(m.Project) {
		t.Errorf("Project = %q, want an absolute path: prune compares it against the filesystem", m.Project)
	}
	if len(m.Targets) != 2 {
		t.Errorf("Targets = %v, want both commands", m.Targets)
	}
	if m.GoVersion == "" || m.LathVersion == "" {
		t.Errorf("manifest omits the toolchain that produced it: %+v", m)
	}
	if m.Built.IsZero() {
		t.Error("Built is zero")
	}
}

// TestReadManifestMissing. An entry built before manifests existed, or one
// whose sidecar was removed by hand, must report that rather than crash.
func TestReadManifestMissing(t *testing.T) {
	t.Parallel()
	if _, err := readManifest(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("reading an absent manifest succeeded")
	}
}

// TestReadManifestCorrupt. A truncated write leaves invalid JSON; the error
// must name the entry rather than surfacing a bare syntax error.
func TestReadManifestCorrupt(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "entry")
	if err := os.WriteFile(bin+cacheManifestSuffix, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readManifest(bin)
	if err == nil {
		t.Fatal("corrupt manifest read as valid")
	}
	if !strings.Contains(err.Error(), "entry") {
		t.Errorf("error %q does not name the entry", err)
	}
}

func TestListCacheEmptyWhenAbsent(t *testing.T) {
	isolateCache(t)
	entries, err := listCache()
	if err != nil {
		t.Fatalf("an absent cache directory must read as empty, not fail: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries from an empty cache", len(entries))
	}
}

// TestListCacheReadsEntries covers the listing, including an entry with no
// manifest. Which is the shape of anything built by an older lath.
func TestListCacheReadsEntries(t *testing.T) {
	dir := isolateCache(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	withManifest := filepath.Join(dir, "proj-aaaa1111")
	if err := os.WriteFile(withManifest, []byte("binary-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(withManifest, definition{Dir: ".", Hash: "aaaa1111"}, nil); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "old-bbbb2222")
	if err := os.WriteFile(orphan, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := listCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (the manifest sidecar must not be listed as an entry)", len(entries))
	}
	var sawManifest, sawOrphan bool
	for _, e := range entries {
		if e.HasManifest {
			sawManifest = true
			if e.Size == 0 {
				t.Error("entry size not reported")
			}
		} else {
			sawOrphan = true
		}
	}
	if !sawManifest || !sawOrphan {
		t.Error("listing did not distinguish an entry with a manifest from one without")
	}
}

// TestRemoveEntryTakesTheManifestWithIt, leaving a sidecar behind would make
// the next listing report an entry whose binary is gone.
func TestRemoveEntryTakesTheManifestWithIt(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "entry")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(bin, definition{Dir: ".", Hash: "h"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := removeEntry(bin); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{bin, bin + cacheManifestSuffix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived removal", filepath.Base(p))
		}
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
	}
	for _, tc := range cases {
		if got := humanSize(tc.in); got != tc.want {
			t.Errorf("humanSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPlural covers the suffix completing "entr(y|ies)", the difference
// between a message that reads like English and one that reads like a program.
func TestPlural(t *testing.T) {
	t.Parallel()
	if got := plural(1); got != "y" {
		t.Errorf("plural(1) = %q, want %q", got, "y")
	}
	for _, n := range []int{0, 2, 17} {
		if got := plural(n); got != "ies" {
			t.Errorf("plural(%d) = %q, want %q", n, got, "ies")
		}
	}
}

// TestRemoveMatchingRemovesOnlyWhatMatches. A predicate that removed too much
// would silently delete other projects' entries.
func TestRemoveMatchingRemovesOnlyWhatMatches(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep")
	drop := filepath.Join(dir, "drop")
	for _, p := range []string{keep, drop} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries := []cacheEntry{{Path: keep}, {Path: drop}}
	// The return is an EXIT CODE, not a count: removing nothing is a
	// successful run. What is asserted here is the filesystem effect.
	if code := removeMatching(entries, func(e cacheEntry) bool {
		return filepath.Base(e.Path) == "drop"
	}, "test"); code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("a non-matching entry was removed")
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Error("the matching entry survived")
	}
}
