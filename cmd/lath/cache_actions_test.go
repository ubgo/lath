package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedCache writes n entries, marking the first `orphans` of them as belonging
// to projects that no longer exist.
func seedCache(t *testing.T, n, orphans int) string {
	t.Helper()
	dir := isolateCache(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir()
	for i := 0; i < n; i++ {
		bin := filepath.Join(dir, fmt.Sprintf("proj%d-hash%04d", i, i))
		if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		project := live
		if i < orphans {
			project = filepath.Join(live, "deleted-project")
		}
		if err := writeManifest(bin, definition{Dir: project, Hash: fmt.Sprintf("hash%04d", i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRunCacheEmpty(t *testing.T) {
	isolateCache(t)
	var code int
	out := captureOutput(t, func() { code = runCache(CacheActionList) })
	if code != exitOK {
		t.Errorf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "empty") {
		t.Errorf("an empty cache should say so:\n%s", out)
	}
}

func TestRunCacheList(t *testing.T) {
	seedCache(t, 3, 0)
	var code int
	out := captureOutput(t, func() { code = runCache(CacheActionList) })
	if code != exitOK {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(out, "3 entries") {
		t.Errorf("listing did not total:\n%s", out)
	}
}

// TestRunCachePruneTakesOnlyOrphans is the safety property: an entry whose
// project still exists may be the very binary that project runs, so prune must
// be safe enough to run without thinking.
func TestRunCachePruneTakesOnlyOrphans(t *testing.T) {
	dir := seedCache(t, 4, 2)
	var code int
	captureOutput(t, func() { code = runCache(CacheActionPrune) })
	if code != exitOK {
		t.Errorf("exit = %d", code)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var bins int
	for _, e := range left {
		if !strings.HasSuffix(e.Name(), cacheManifestSuffix) {
			bins++
		}
	}
	if bins != 2 {
		t.Errorf("%d entries left, want the 2 whose projects still exist", bins)
	}
}

func TestRunCachePruneWithNoOrphans(t *testing.T) {
	seedCache(t, 2, 0)
	var code int
	out := captureOutput(t, func() { code = runCache(CacheActionPrune) })
	if code != exitOK {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(out, "orphaned") {
		t.Errorf("pruning nothing should say what it looked for:\n%s", out)
	}
}

func TestRunCacheCleanTakesEverything(t *testing.T) {
	dir := seedCache(t, 3, 1)
	var code int
	captureOutput(t, func() { code = runCache(CacheActionClean) })
	if code != exitOK {
		t.Errorf("exit = %d", code)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("clean left %d file(s)", len(left))
	}
}

// TestCacheActionValues is the drift guard between what `cache` accepts and
// what runCache handles.
func TestCacheActionValues(t *testing.T) {
	seedCache(t, 1, 0)
	for _, a := range CacheActionValues {
		if !a.Valid() {
			t.Errorf("%q is in CacheActionValues but not Valid", a)
		}
		var code int
		captureOutput(t, func() { code = runCache(a) })
		if code != exitOK {
			t.Errorf("cache action %q exited %d: every declared action must be handled", a, code)
		}
	}
}

// TestCacheNameUsesTheRepositoryName. The readable half of an entry's
// filename comes from the repository, not the definition directory, so every
// project's entries do not read as ".lath".
func TestCacheNameUsesTheRepositoryName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	repo := filepath.Join(t.TempDir(), "My Repo")
	if err := os.MkdirAll(filepath.Join(repo, definitionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", ".")
	cmd.Dir = repo
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	got := cacheName(filepath.Join(repo, definitionDir))
	if got != "my-repo" {
		t.Errorf("cacheName = %q, want the sanitised repository name", got)
	}
}

// TestCacheNameOutsideARepository falls back to the parent directory, and
// finally to a fixed name. A cache entry must always be nameable.
func TestCacheNameOutsideARepository(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "plainproject")
	def := filepath.Join(parent, definitionDir)
	if err := os.MkdirAll(def, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := cacheName(def); got == "" {
		t.Error("cacheName returned empty; an entry must always be nameable")
	}
}

func TestReportDiscoveryError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		says string
	}{
		{"no definition", ErrNoDefinition, exitUsage, definitionDir},
		{"no targets", ErrNoTargets, exitUsage, "lath:"},
		{"duplicate target", ErrDuplicateTarget, exitUsage, "lath:"},
		{"anything else", errors.New("disk on fire"), exitInternalErr, "disk on fire"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			out := captureOutput(t, func() { code = reportDiscoveryError(tc.err) })
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
			if !strings.Contains(out, tc.says) {
				t.Errorf("message omits %q:\n%s", tc.says, out)
			}
		})
	}
}

// TestASweepReportsEveryEntryItCannotRemove.
//
// `cache clean` reclaims disk. An entry that will not delete — a permissions
// oddity, a file another process holds — must be REPORTED and the sweep must
// keep going: aborting on the first one silently leaves every later entry on
// disk, and the user reads the result as "there was nothing to remove".
//
// A read-only cache directory makes every removal fail, which is the portable
// way to provoke this: on unix, deleting a file depends on the directory's
// permissions rather than the file's, and per-file immutability needs flags
// that do not exist everywhere.
func TestASweepReportsEveryEntryItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root deletes anything")
	}
	const entries = 3
	dir := seedCache(t, entries, entries)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var code int
	out := captureOutput(t, func() { code = runCache(CacheActionClean) })
	if code != exitOK {
		t.Errorf("exit = %d; entries that will not delete must not fail the command", code)
	}
	// Every entry was attempted, not just the first: the count is the proof
	// the loop continued.
	if got := strings.Count(out, "could not remove"); got != entries {
		t.Errorf("reported %d failures, want %d — the sweep stopped early:\n%s", got, entries, out)
	}
	if !strings.Contains(out, "nothing to remove") {
		t.Errorf("the outcome was not stated:\n%s", out)
	}
}

// TestAnEntryWithNoManifestIsStillRemoved. Entries predating manifests, and
// ones whose sidecar was deleted by hand, are both normal; treating the
// missing sidecar as a failure would make them unreclaimable forever.
func TestAnEntryWithNoManifestIsStillRemoved(t *testing.T) {
	dir := seedCache(t, 1, 1)
	orphan := filepath.Join(dir, "manifestless-hash0001")
	if err := os.WriteFile(orphan, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := removeEntry(orphan); err != nil {
		t.Fatalf("an entry with no manifest could not be removed: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("the entry is still there")
	}
}

// TestWriteManifestReportsAnUnwritableCache. A build that cannot record what
// it built produces a cache entry nothing can identify later, which `cache
// list` then shows as an unattributable binary.
func TestWriteManifestReportsAnUnwritableCache(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := writeManifest(filepath.Join(dir, "proj-hash"), definition{Dir: dir, Hash: "hash"}, nil)
	if err == nil {
		t.Fatal("writing a manifest into a read-only directory succeeded")
	}
}
