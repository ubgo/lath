package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// manifest describes one cached pipeline binary.
//
// Why a sidecar rather than encoding everything in the filename: a cache
// directory of anonymous hashes cannot be reasoned about, you cannot tell
// which project an entry came from, whether that project still exists, or
// whether an entry is safe to delete. The filename carries enough to recognise
// an entry at a glance; the manifest carries enough to act on it.
type manifest struct {
	// Project is the absolute path of the definition directory. Used by prune
	// to decide whether an entry is orphaned.
	Project string `json:"project"`
	// Name is the human-readable half of the entry's filename.
	Name string `json:"name"`
	// Hash is the content hash, the entry's real identity.
	Hash string `json:"hash"`
	// Targets are the command names the entry exposes, so `-cache` can show
	// what an entry does without executing it.
	Targets []string `json:"targets"`
	// Built is when the entry was compiled.
	Built time.Time `json:"built"`
	// LathVersion and GoVersion record the build environment. Both are part of
	// the hash, so a mismatch cannot happen, they are here for diagnosis.
	LathVersion string `json:"lath_version"`
	GoVersion   string `json:"go_version"`
}

// cacheName derives the readable half of a cache entry's filename.
//
// Preference order, repository name, then the directory containing the
// definition, then a constant. A repository name survives the checkout being
// moved or renamed, which a directory name does not.
//
// Cost note: this shells out to git, so it is called ONLY when writing a new
// entry. Lookups use cacheLookup, which matches on the hash suffix and never
// needs the name. That keeps the warm path free of a subprocess.
func cacheName(defDir string) string {
	if abs, err := filepath.Abs(defDir); err == nil {
		parent := filepath.Dir(abs)
		if repo := gitRepoName(parent); repo != "" {
			return sanitiseCacheName(repo)
		}
		if base := filepath.Base(parent); base != "." && base != string(filepath.Separator) {
			return sanitiseCacheName(base)
		}
	}
	return fallbackCacheName
}

// gitRepoName returns the basename of the git working tree containing dir, or
// "" when dir is not in a repository or git is unavailable.
func gitRepoName(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return ""
	}
	return filepath.Base(top)
}

// sanitiseCacheName reduces an arbitrary directory or repository name to
// something safe as a filename component.
//
// Anything outside [a-z0-9._] becomes a single separator, so a name like
// "My Repo (old)" cannot produce a filename that breaks globbing or shell
// completion. Truncated so one long name cannot dominate a listing.
func sanitiseCacheName(s string) string {
	var b strings.Builder
	lastWasSep := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			lastWasSep = false
		default:
			// Collapse runs, and never lead with a separator, a leading one
			// would make the name look like a flag in a listing.
			if !lastWasSep && b.Len() > 0 {
				b.WriteString(cacheNameSeparator)
				lastWasSep = true
			}
		}
	}
	out := strings.Trim(b.String(), cacheNameSeparator)
	if out == "" {
		return fallbackCacheName
	}
	if len(out) > maxCacheNameLen {
		out = strings.Trim(out[:maxCacheNameLen], cacheNameSeparator)
	}
	return out
}

// cacheLookup finds an existing entry for hash, or "" if none.
//
// It matches on the hash SUFFIX rather than reconstructing the full filename,
// which is what allows the readable prefix to be decorative: renaming a
// repository changes the name a future entry would get, but does not orphan
// the entry already cached under the old one.
//
// binarySuffix is part of the pattern, not decoration: on Windows an entry is
// written as <name>-<hash>.exe, and a glob ending at the hash would match
// nothing, silently recompiling the definition on every single run.
func cacheLookup(hash string) string {
	matches, err := filepath.Glob(filepath.Join(cacheDir(), "*"+cacheNameSeparator+hash+binarySuffix))
	if err != nil || len(matches) == 0 {
		return ""
	}
	// Deterministic when more than one name maps to the same content, two
	// checkouts of the same repository, for instance.
	sort.Strings(matches)
	return matches[0]
}

// cacheEntryPath returns where a NEW entry for d would be written.
func cacheEntryPath(d definition) string {
	return filepath.Join(cacheDir(), cacheName(d.Dir)+cacheNameSeparator+d.Hash+binarySuffix)
}

// writeManifest records what a cache entry is, beside the entry itself.
//
// A failure here is reported but never fatal: the manifest is for humans, and
// losing the ability to describe an entry must not stop a deploy.
func writeManifest(binPath string, d definition, targets []Target) error {
	abs, err := filepath.Abs(d.Dir)
	if err != nil {
		abs = d.Dir
	}
	m := manifest{
		Project:     abs,
		Name:        filepath.Base(binPath),
		Hash:        d.Hash,
		Targets:     commandNames(targets),
		Built:       time.Now().UTC(),
		LathVersion: version,
		GoVersion:   goVersionString(),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest for %s: %w", binPath, err)
	}
	return os.WriteFile(binPath+cacheManifestSuffix, data, generatedFilePerm)
}

// readManifest loads the sidecar for a cache entry, or reports why it cannot.
func readManifest(binPath string) (manifest, error) {
	data, err := os.ReadFile(binPath + cacheManifestSuffix)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("manifest for %s: %w", binPath, err)
	}
	return m, nil
}

// cacheEntry pairs an on-disk entry with what is known about it.
type cacheEntry struct {
	Path string
	Size int64
	// Manifest is absent for an entry written before manifests existed, or one
	// whose sidecar was removed by hand.
	Manifest    manifest
	HasManifest bool
	// ProjectExists reports whether the recorded definition directory is still
	// present. False means the entry is orphaned and prune will remove it.
	ProjectExists bool
}

// listCache enumerates cache entries, newest first.
func listCache() ([]cacheEntry, error) {
	dir := cacheDir()
	names, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// An absent cache directory is an EMPTY cache, not a failure: it
			// is the state of every machine before the first run, and
			// reporting it as an error would make `lath cache` fail on a
			// fresh checkout.
			return nil, nil
		}
		return nil, fmt.Errorf("read cache %s: %w", dir, err)
	}

	var out []cacheEntry
	for _, e := range names {
		if e.IsDir() || strings.HasSuffix(e.Name(), cacheManifestSuffix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		entry := cacheEntry{Path: path, Size: info.Size()}
		if m, err := readManifest(path); err == nil {
			entry.Manifest, entry.HasManifest = m, true
			if _, err := os.Stat(m.Project); err == nil {
				entry.ProjectExists = true
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Manifest.Built.After(out[j].Manifest.Built)
	})
	return out, nil
}

// removeEntry deletes a cache entry and its manifest.
func removeEntry(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	// A missing manifest is not an error: entries predating manifests, and
	// entries whose sidecar was removed by hand, are both normal.
	if err := os.Remove(path + cacheManifestSuffix); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// goVersionString is a variable so tests can pin it. Set from runtime.Version
// at init rather than called inline, so the manifest and the hash cannot report
// different toolchains.
var goVersionString = defaultGoVersion

// runCache handles `x cache` and its actions.
//
// Grouped in one function because they share the same listing and the same
// reporting shape; splitting them would duplicate both for no gain.
func runCache(action CacheAction) int {
	entries, err := listCache()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lath:", err)
		return exitInternalErr
	}
	if len(entries) == 0 {
		fmt.Printf("cache is empty (%s)\n", cacheDir())
		return exitOK
	}

	switch action {
	case CacheActionList:
		printCache(entries)
		return exitOK
	case CacheActionPrune:
		// Orphans only. An entry whose project still exists may be the very
		// binary that project runs, and deleting it would silently cost a
		// rebuild, prune must be safe enough to run without thinking.
		return removeMatching(entries, func(e cacheEntry) bool {
			return !e.ProjectExists
		}, "orphaned")
	case CacheActionClean:
		return removeMatching(entries, func(cacheEntry) bool { return true }, "")
	default:
		// Unreachable: Valid() gates every path here.
		fmt.Fprintf(os.Stderr, "lath: cache action %q is declared but not implemented\n", action)
		return exitInternalErr
	}
}

// printCache renders the listing.
func printCache(entries []cacheEntry) {
	fmt.Printf("%s\n\n", cacheDir())
	var total int64
	for _, e := range entries {
		total += e.Size
		name := filepath.Base(e.Path)
		if !e.HasManifest {
			// Predates manifests, or the sidecar was removed by hand. Shown
			// rather than hidden: an entry that cannot be explained is exactly
			// the one someone needs to know about.
			fmt.Printf("  %-40s %8s   (no manifest)\n", name, humanSize(e.Size))
			continue
		}
		status := ""
		if !e.ProjectExists {
			status = "  [project missing]"
		}
		fmt.Printf("  %-40s %8s   %s%s\n", name, humanSize(e.Size), e.Manifest.Project, status)
		fmt.Printf("  %-40s %8s   built %s · lath %s · %s · %s\n", "", "",
			e.Manifest.Built.Local().Format(time.RFC3339),
			e.Manifest.LathVersion, e.Manifest.GoVersion,
			strings.Join(e.Manifest.Targets, " "))
	}
	fmt.Printf("\n  %d entr%s, %s total\n", len(entries), plural(len(entries)), humanSize(total))
}

// removeMatching deletes every entry satisfying want, printing what it freed.
//
// Returns an EXIT CODE, not a count. It is called straight from the verb
// dispatch, and removing nothing is a successful run, not a failure. A caller
// wanting the number must count the predicate's matches itself; the doc used
// to read "reports what it freed", which is what it prints, and that wording
// cost a reader the difference.
//
// A single entry that cannot be removed is reported and skipped rather than
// aborting: a locked or already-deleted file must not leave the rest of a
// prune half-done.
func removeMatching(entries []cacheEntry, want func(cacheEntry) bool, label string) int {
	var removed int
	var freed int64
	for _, e := range entries {
		if !want(e) {
			continue
		}
		if err := removeEntry(e.Path); err != nil {
			fmt.Fprintf(os.Stderr, "lath: could not remove %s: %v\n", filepath.Base(e.Path), err)
			continue
		}
		removed++
		freed += e.Size
	}
	if removed == 0 {
		if label != "" {
			fmt.Printf("no %s entries to remove\n", label)
		} else {
			fmt.Println("nothing to remove")
		}
		return exitOK
	}
	fmt.Printf("removed %d entr%s, freed %s\n", removed, plural(removed), humanSize(freed))
	return exitOK
}

// byteUnit is the divisor between adjacent size units. 1024 rather than 1000:
// these are file sizes, and every tool a reader will compare against, ls -lh,
// du -h, uses binary units.
const byteUnit = 1024

// humanSize renders a byte count compactly.
func humanSize(n int64) string {
	if n < byteUnit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(byteUnit), 0
	for m := n / byteUnit; m >= byteUnit; m /= byteUnit {
		div *= byteUnit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// plural returns the suffix completing "entr(y|ies)".
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
