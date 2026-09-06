package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrintCacheShowsEveryEntryKind(t *testing.T) {
	isolateCache(t)
	entries := []cacheEntry{
		{
			Path: "/cache/proj-aaaa1111", Size: 9 * 1024 * 1024,
			HasManifest: true, ProjectExists: true,
			Manifest: manifest{
				Project: "/work/proj", Hash: "aaaa1111", Targets: []string{"deploy", "secrets push"},
				Built: time.Now(), LathVersion: "dev", GoVersion: "go1.24",
			},
		},
		{
			Path: "/cache/gone-bbbb2222", Size: 1024,
			HasManifest: true, ProjectExists: false,
			Manifest: manifest{Project: "/deleted", Built: time.Now()},
		},
		// Predates manifests, or its sidecar was removed by hand.
		{Path: "/cache/old-cccc3333", Size: 512},
	}
	out := captureOutput(t, func() { printCache(entries) })

	for _, want := range []string{
		"proj-aaaa1111", "9.0 MB", "/work/proj", "deploy", "secrets push",
		"[project missing]", // the orphan must be flagged, since prune will take it
		"no manifest",       // and an unexplainable entry must be shown, not hidden
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cache listing omits %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "3 entries") {
		t.Errorf("listing does not total the entries:\n%s", out)
	}
}

func TestPrintCacheEmpty(t *testing.T) {
	isolateCache(t)
	out := captureOutput(t, func() { printCache(nil) })
	if !strings.Contains(out, "0 entries") {
		t.Errorf("an empty cache should still report its directory and a zero total:\n%s", out)
	}
}

// TestListTargetsGroupsNamespaces. A grouped target is typed as two words, so
// the listing has to show the grouping or the reader cannot construct the
// command.
func TestListTargetsGroupsNamespaces(t *testing.T) {
	targets := []Target{
		{Command: "deploy", Func: "Deploy", Doc: "Deploy ships the app"},
		{Command: "secrets push", Func: "Push", Namespace: "secrets", Doc: "Push uploads them"},
		{Command: "secrets plan", Func: "Plan", Namespace: "secrets", Doc: "Plan shows the diff"},
	}
	out := captureOutput(t, func() { listTargets(targets) })

	for _, want := range []string{"deploy", "secrets", "push", "plan", "Deploy ships the app"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing omits %q:\n%s", want, out)
		}
	}
	// The namespace is a heading; its targets are indented under it.
	nsIdx, pushIdx := strings.Index(out, "secrets"), strings.Index(out, "push")
	if nsIdx < 0 || pushIdx < nsIdx {
		t.Errorf("namespaced targets are not grouped under their namespace:\n%s", out)
	}
}

// TestListTargetsWithoutDocs. A target with no doc comment must still be
// listed, with a note rather than a blank column.
func TestListTargetsWithoutDocs(t *testing.T) {
	out := captureOutput(t, func() {
		listTargets([]Target{{Command: "hello", Func: "Hello"}})
	})
	if !strings.Contains(out, "hello") {
		t.Errorf("undocumented target not listed:\n%s", out)
	}
}

// TestWarnRejectedNamesTheShapes. An exported function that is ALMOST a
// target is the most confusing case: it exists, it is exported, and nothing
// runs it. The warning has to say what shapes are accepted.
func TestWarnRejectedNamesTheShapes(t *testing.T) {
	out := captureOutput(t, func() {
		warnRejected([]string{"deploy.go: Helper(int) string"})
	})
	if !strings.Contains(out, "Helper") {
		t.Errorf("warning does not name the rejected function:\n%s", out)
	}
	if !strings.Contains(out, "supported shapes") {
		t.Errorf("warning does not say what IS accepted:\n%s", out)
	}
}

func TestWarnRejectedSilentWhenNothingRejected(t *testing.T) {
	if out := captureOutput(t, func() { warnRejected(nil) }); out != "" {
		t.Errorf("warned with nothing to warn about: %q", out)
	}
}

// TestCacheLookupFindsByHashSuffix pins the lookup rule: an entry is found by
// its HASH, not by rebuilding its filename. The readable prefix is decoration,
// so renaming a repository must not orphan an entry that is still valid.
func TestCacheLookupFindsByHashSuffix(t *testing.T) {
	dir := isolateCache(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "some-old-name"+cacheNameSeparator+"deadbeef")
	if err := os.WriteFile(want, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := cacheLookup("deadbeef"); got != want {
		t.Errorf("cacheLookup = %q, want %q", got, want)
	}
	if got := cacheLookup("nosuchhash"); got != "" {
		t.Errorf("cacheLookup for an absent hash = %q, want empty", got)
	}
}

func TestCacheLookupEmptyCache(t *testing.T) {
	isolateCache(t)
	if got := cacheLookup("anything"); got != "" {
		t.Errorf("lookup in an absent cache = %q, want empty", got)
	}
}
