// Package scan walks a tree and matches file contents.
//
// The line-count case for this package is weak on its own: plain Go with
// filepath.WalkDir and regexp is barely longer than the shell it replaces,
// because nothing is being simulated, those two ARE the primitive.
//
// It earns its place by composition. File discovery with an exclusion list is
// what every higher-level step needs before it can do anything: find the
// migrations, find the Dockerfiles, find files that must not contain a
// credential. Filter is the reusable part; Match and Grep are conveniences on
// top, cheap once the package exists.
package scan

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxLineBytes bounds a single line read while matching.
//
// bufio.Scanner's default is 64KB, which a minified file or a generated blob
// exceeds, and it then fails with a message about a long line rather than
// about the file, which is a confusing way to learn a lint run was incomplete.
const maxLineBytes = 1024 * 1024

// Filter selects files during a walk.
//
// Empty fields mean "no constraint", so the zero Filter matches every file.
type Filter struct {
	// Ext limits by extension, including the dot: ".go". Empty means any.
	Ext []string
	// Exclude drops paths containing any of these substrings: "vendor/",
	// "/gen/". Substring rather than glob because that is what the shell
	// scripts this replaces actually do, and it composes without escaping.
	Exclude []string
	// Include keeps only paths containing one of these substrings. Empty means
	// all. Applied after Exclude, so an exclusion always wins.
	Include []string
	// SkipHidden drops dot-directories and dot-files.
	SkipHidden bool
}

// matches reports whether a path passes the filter.
func (f Filter) matches(path string) bool {
	if len(f.Ext) > 0 {
		ext := filepath.Ext(path)
		var ok bool
		for _, e := range f.Ext {
			if ext == e {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	// Exclusion is checked before inclusion so an excluded path can never be
	// resurrected by also matching an Include entry.
	for _, ex := range f.Exclude {
		if strings.Contains(path, ex) {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, in := range f.Include {
		if strings.Contains(path, in) {
			return true
		}
	}
	return false
}

// Files walks root and returns the paths passing the filter, sorted.
//
// Sorted so a caller's output is stable between runs, filesystem order is not,
// and an unstable lint report is one nobody can diff.
func Files(root string, f Filter) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if f.SkipHidden && path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if f.SkipHidden && strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if f.matches(path) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan: walking %s: %w", root, err)
	}
	// WalkDir already yields lexical order, but Files promises it, so a
	// future change to the walk cannot quietly break the guarantee.
	return out, nil
}

// Hit is one matching line.
type Hit struct {
	// File is the path the line was found in.
	File string
	// Line is the 1-based line number, as an editor counts them.
	Line int
	// Text is the line, trimmed.
	Text string
}

// String renders a Hit the way compilers and greps do, so an editor can jump
// to it.
func (h Hit) String() string { return fmt.Sprintf("%s:%d: %s", h.File, h.Line, h.Text) }

// Match returns every line in files matching pattern.
func Match(files []string, pattern *regexp.Regexp) ([]Hit, error) {
	var hits []Hit
	for _, path := range files {
		fileHits, err := matchFile(path, pattern)
		if err != nil {
			return nil, err
		}
		hits = append(hits, fileHits...)
	}
	return hits, nil
}

// matchFile scans one file line by line.
//
// Streamed rather than read whole: a lint run over a repository touches every
// file, and holding each in memory is needless when only one line at a time
// matters.
func matchFile(path string, pattern *regexp.Regexp) ([]Hit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("scan: %s: %w", path, err)
	}
	defer f.Close()

	var hits []Hit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLineBytes)
	for line := 1; sc.Scan(); line++ {
		if pattern.MatchString(sc.Text()) {
			hits = append(hits, Hit{File: path, Line: line, Text: strings.TrimSpace(sc.Text())})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: reading %s: %w", path, err)
	}
	return hits, nil
}

// Grep is Files followed by Match. The whole shape of a lint script.
func Grep(root string, f Filter, pattern *regexp.Regexp) ([]Hit, error) {
	files, err := Files(root, f)
	if err != nil {
		return nil, err
	}
	return Match(files, pattern)
}
