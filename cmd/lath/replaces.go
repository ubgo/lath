package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// replaceDirective matches a go.mod replace whose target is a filesystem path.
//
// Only filesystem targets matter here. A replace pointing at another module
// version is resolved by the module proxy and pinned by go.sum, which the
// definition's own hash already covers.
const replaceKeyword = "replace"

// maxReplaceDepth bounds how far local replaces are followed.
//
// Cycles are possible in a workspace under development, and a bound is
// cheaper than tracking one. Two levels covers a definition replacing a
// module that itself replaces another, which is the deepest arrangement this
// tool has met.
const maxReplaceDepth = 3

// localReplaceHash summarises the source of every locally-replaced module the
// definition depends on, transitively.
//
// This exists because of a bug that cost a real debugging session. A definition
// under development replaces its engine modules with local paths. The cache key
// covered only files INSIDE the definition, so editing the engine changed
// nothing the hash could see: lath reused a stale binary, and a fix that had
// definitely been made appeared not to work. The symptom was worse than a plain
// failure, because the evidence said the new code was running.
//
// Returns an empty string when no local replaces exist, which is the normal
// case for a published definition and costs nothing.
// skippedDirs are directories the Go toolchain itself excludes from a build,
// so hashing them would make the cache key depend on files that can never
// affect the result, and vendor/ alone can be tens of thousands of them.
//
// A set rather than two comparisons so adding one is a single edit, and so the
// list reads as the closed set it is. Dot- and underscore-prefixed directories
// are handled separately: those are a RULE, not a list.
var skippedDirs = map[string]bool{
	"testdata": true,
	"vendor":   true,
}

func localReplaceHash(defDir string) (string, error) {
	dirs, err := localReplaceDirs(defDir)
	if err != nil {
		return "", err
	}
	if len(dirs) == 0 {
		return "", nil
	}

	h := sha256.New()
	for _, dir := range dirs {
		// The directory is framed by its path so that two modules swapping
		// contents changes the digest.
		fmt.Fprintf(h, "replace\x00%s\x00", filepath.ToSlash(dir))
		if err := hashDirInto(h, dir); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// localReplaceDirs resolves every filesystem replace target reachable from the
// definition, sorted and deduplicated so the result is stable.
func localReplaceDirs(defDir string) ([]string, error) {
	seen := map[string]bool{}
	var walk func(dir string, depth int) error

	walk = func(dir string, depth int) error {
		if depth > maxReplaceDepth {
			return nil
		}
		targets, err := parseLocalReplaces(filepath.Join(dir, goModFile), dir)
		if err != nil {
			return err
		}
		for _, t := range targets {
			if seen[t] {
				continue
			}
			seen[t] = true
			if err := walk(t, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(defDir, 0); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// parseLocalReplaces reads a go.mod and returns the absolute paths of replace
// targets that point at the filesystem.
//
// Hand-parsed rather than using golang.org/x/mod, because lath ships with no
// dependencies and the grammar needed here is two shapes.
func parseLocalReplaces(goModPath, relativeTo string) ([]string, error) {
	f, err := os.Open(goModPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // not every replace target is a module root
		}
		return nil, fmt.Errorf("reading %s: %w", goModPath, err)
	}
	defer f.Close()

	var out []string
	inBlock := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == replaceKeyword+" (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, replaceKeyword+" "):
			line = strings.TrimSpace(strings.TrimPrefix(line, replaceKeyword))
		case !inBlock:
			continue
		}
		if target, ok := replaceTarget(line); ok {
			abs := target
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(relativeTo, target)
			}
			if info, err := os.Stat(abs); err == nil && info.IsDir() {
				resolved, err := filepath.Abs(abs)
				if err != nil {
					return nil, err
				}
				out = append(out, resolved)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", goModPath, err)
	}
	return out, nil
}

// replaceTarget extracts the right-hand side of "old => new", reporting
// whether it is a filesystem path.
//
// A module path on the right (e.g. "=> example.com/fork v1.2.3") is not one:
// go.sum pins it, and the definition's own hash already covers go.sum.
func replaceTarget(line string) (string, bool) {
	i := strings.Index(line, "=>")
	if i < 0 {
		return "", false
	}
	fields := strings.Fields(line[i+2:])
	if len(fields) == 0 {
		return "", false
	}
	target := fields[0]
	if strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") ||
		target == "." || target == ".." || filepath.IsAbs(target) {
		return target, true
	}
	return "", false
}

// hashDirInto folds a module directory's Go sources into h.
//
// Mirrors hashableFiles: the same file types, the same skips, the same
// length-prefixed framing, so the two agree about what "the source changed"
// means.
func hashDirInto(h interface{ Write([]byte) (int, error) }, dir string) error {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != dir && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				skippedDirs[name]) {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, goFileSuffix) && name != goModFile && name != goSumFile {
			return nil
		}
		// Tests cannot affect the compiled dispatcher, and excluding them
		// keeps an unrelated test edit from triggering a rebuild.
		if strings.HasSuffix(name, "_test"+goFileSuffix) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.Strings(files)

	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("scan %s: %w", path, err)
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(content))
		if _, err := h.Write(content); err != nil {
			return err
		}
	}
	return nil
}
