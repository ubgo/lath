package scan_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/internal/fsprobe"
	"github.com/ubgo/lath/kit/scan"
)

// rel converts absolute results back to root-relative slash paths, so the
// assertions read as the tree that was built.
func rel(t *testing.T, root string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		r, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(r))
	}
	return out
}

// ── Filter ───────────────────────────────────────────────────────────────

func TestFilterCombinations(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"main.go":                 "package main",
		"main_test.go":            "package main",
		"README.md":               "# hi",
		"vendor/dep/dep.go":       "package dep",
		"internal/gen/api_gen.go": "package gen",
		"internal/svc/svc.go":     "package svc",
		".hidden/secret.go":       "package secret",
		".dotfile.go":             "package dot",
		"nested/deep/deep.go":     "package deep",
	}
	root := tree(t, files)

	for _, tc := range []struct {
		name   string
		filter scan.Filter
		want   []string
	}{
		{
			"no filter takes everything, hidden included",
			scan.Filter{},
			[]string{".dotfile.go", ".hidden/secret.go", "README.md", "internal/gen/api_gen.go",
				"internal/svc/svc.go", "main.go", "main_test.go", "nested/deep/deep.go", "vendor/dep/dep.go"},
		},
		{
			"by extension",
			scan.Filter{Ext: []string{".md"}},
			[]string{"README.md"},
		},
		{
			"several extensions",
			scan.Filter{Ext: []string{".md", ".go"}, SkipHidden: true},
			[]string{"README.md", "internal/gen/api_gen.go", "internal/svc/svc.go",
				"main.go", "main_test.go", "nested/deep/deep.go", "vendor/dep/dep.go"},
		},
		{
			"exclude by path substring",
			scan.Filter{Ext: []string{".go"}, Exclude: []string{"vendor/", "/gen/"}, SkipHidden: true},
			[]string{"internal/svc/svc.go", "main.go", "main_test.go", "nested/deep/deep.go"},
		},
		{
			"exclude by filename substring",
			scan.Filter{Ext: []string{".go"}, Exclude: []string{"_test.go"}, SkipHidden: true},
			[]string{"internal/gen/api_gen.go", "internal/svc/svc.go", "main.go",
				"nested/deep/deep.go", "vendor/dep/dep.go"},
		},
		{
			"include narrows to a subtree",
			scan.Filter{Ext: []string{".go"}, Include: []string{"internal/"}},
			[]string{"internal/gen/api_gen.go", "internal/svc/svc.go"},
		},
		{
			// The precedence that matters: a file matching both is excluded.
			// The other order would make Exclude advisory.
			"exclude beats include",
			scan.Filter{Ext: []string{".go"}, Include: []string{"internal/"}, Exclude: []string{"/gen/"}},
			[]string{"internal/svc/svc.go"},
		},
		{
			"SkipHidden drops dot-directories and dot-files",
			scan.Filter{Ext: []string{".go"}, SkipHidden: true, Include: []string{""}},
			[]string{"internal/gen/api_gen.go", "internal/svc/svc.go", "main.go",
				"main_test.go", "nested/deep/deep.go", "vendor/dep/dep.go"},
		},
		{
			"an include matching nothing yields nothing",
			scan.Filter{Include: []string{"no-such-segment"}},
			nil,
		},
		{
			// A caller writing "go" instead of ".go" gets nothing rather than
			// everything. A silent empty result, which is why it is pinned.
			"an extension without its dot matches nothing",
			scan.Filter{Ext: []string{"go"}},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scan.Files(root, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			assertPaths(t, rel(t, root, got), tc.want)
		})
	}
}

// TestSkipHiddenKeepsAHiddenRoot pins that scanning a dot-directory directly
// works. Excluding the root because it is hidden would make it impossible to
// scan .lath or .github. The directories a task runner most wants.
func TestSkipHiddenKeepsAHiddenRoot(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{".lath/dev/guard.go": "package dev"})
	got, err := scan.Files(filepath.Join(root, ".lath"), scan.Filter{
		Ext: []string{".go"}, SkipHidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("found %v; want the one file under the hidden root", got)
	}
}

func TestFilesEdgeRoots(t *testing.T) {
	t.Parallel()
	t.Run("an empty directory", func(t *testing.T) {
		t.Parallel()
		got, err := scan.Files(t.TempDir(), scan.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v; want nothing", got)
		}
	})

	t.Run("a root that does not exist", func(t *testing.T) {
		t.Parallel()
		_, err := scan.Files(filepath.Join(t.TempDir(), "nope"), scan.Filter{})
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v; want it to wrap os.ErrNotExist", err)
		}
	})

	t.Run("a file as the root", func(t *testing.T) {
		t.Parallel()
		root := tree(t, map[string]string{"only.go": "package main"})
		got, err := scan.Files(filepath.Join(root, "only.go"), scan.Filter{Ext: []string{".go"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("got %v; want the file itself", got)
		}
	})

	t.Run("directories are never returned", func(t *testing.T) {
		t.Parallel()
		root := tree(t, map[string]string{"pkg.go/inside.txt": "x"})
		got, err := scan.Files(root, scan.Filter{Ext: []string{".go"}})
		if err != nil {
			t.Fatal(err)
		}
		// The directory is named pkg.go, so an implementation matching on
		// extension without checking IsDir would return it.
		if len(got) != 0 {
			t.Errorf("got %v; a directory was returned as a file", got)
		}
	})
}

// ── Match / Grep ─────────────────────────────────────────────────────────

func TestMatchLineNumbersAndTrimming(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"a.go": "package main\n\n    indented TODO here\nlast\n",
	})
	hits, err := scan.Grep(root, scan.Filter{Ext: []string{".go"}}, regexp.MustCompile("TODO"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits; want 1", len(hits))
	}
	// Line numbers are 1-based: a 0-based hit sends a reader to the wrong line
	// in every editor.
	if hits[0].Line != 3 {
		t.Errorf("Line = %d; want 3", hits[0].Line)
	}
	if hits[0].Text != "indented TODO here" {
		t.Errorf("Text = %q; want it trimmed", hits[0].Text)
	}
	if !strings.HasSuffix(hits[0].File, "a.go") {
		t.Errorf("File = %q", hits[0].File)
	}
	// The String form is what gets printed, and editors parse file:line:.
	if want := fmt.Sprintf("%s:3: indented TODO here", hits[0].File); hits[0].String() != want {
		t.Errorf("String() = %q; want %q", hits[0].String(), want)
	}
}

func TestMatchFileShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		content   string
		wantLines []int
	}{
		{"an empty file", "", nil},
		{"one line, no trailing newline", "TODO", []int{1}},
		{"a trailing newline does not add a line", "TODO\n", []int{1}},
		{"several matches", "TODO\nx\nTODO\n", []int{1, 3}},
		{"every line matches", "TODO\nTODO\n", []int{1, 2}},
		// A blank final line must not be counted as content.
		{"a blank line at the end", "TODO\n\n", []int{1}},
		{"CRLF line endings", "x\r\nTODO\r\n", []int{2}},
		{"a match on the last line without a newline", "a\nb\nTODO", []int{3}},
		{"no match", "nothing here\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := tree(t, map[string]string{"f.txt": tc.content})
			hits, err := scan.Grep(root, scan.Filter{}, regexp.MustCompile("TODO"))
			if err != nil {
				t.Fatal(err)
			}
			var lines []int
			for _, h := range hits {
				lines = append(lines, h.Line)
			}
			if fmt.Sprint(lines) != fmt.Sprint(tc.wantLines) {
				t.Errorf("lines = %v; want %v", lines, tc.wantLines)
			}
		})
	}
}

// TestMatchPreservesFileOrder pins that hits arrive in the order the files
// were given, so output is stable between runs.
func TestMatchPreservesFileOrder(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"a.go": "TODO a\n", "b.go": "TODO b\n", "c.go": "TODO c\n",
	})
	files, err := scan.Files(root, scan.Filter{Ext: []string{".go"}})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := scan.Match(files, regexp.MustCompile("TODO"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits; want 3", len(hits))
	}
	for i, want := range []string{"a.go", "b.go", "c.go"} {
		if !strings.HasSuffix(hits[i].File, want) {
			t.Errorf("hit %d is %s; want %s: WalkDir's lexical order was not preserved", i, hits[i].File, want)
		}
	}
}

func TestMatchMissingFileReportsThePath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone.go")
	_, err := scan.Match([]string{missing}, regexp.MustCompile("x"))
	if err == nil {
		t.Fatal("matching a missing file succeeded")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v; want it to name the file", err)
	}
}

func TestMatchNoFiles(t *testing.T) {
	t.Parallel()
	for _, files := range [][]string{nil, {}} {
		hits, err := scan.Match(files, regexp.MustCompile("x"))
		if err != nil {
			t.Errorf("err = %v; scanning nothing is not a failure", err)
		}
		if len(hits) != 0 {
			t.Errorf("got %v", hits)
		}
	}
}

// TestMatchVeryLongLine pins that a minified or generated file does not abort
// the scan. bufio.Scanner's default limit is 64KB, which a bundle exceeds.
func TestMatchVeryLongLine(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 200*1024) + "TODO" + strings.Repeat("y", 200*1024)
	root := tree(t, map[string]string{"bundle.js": long + "\n"})
	hits, err := scan.Grep(root, scan.Filter{}, regexp.MustCompile("TODO"))
	if err != nil {
		t.Fatalf("err = %v; a 400KB line must be scannable", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits; want 1", len(hits))
	}
	if hits[0].Line != 1 {
		t.Errorf("Line = %d; want 1", hits[0].Line)
	}
}

// TestMatchLineOverTheLimitErrorsClearly pins that a line past the cap reports
// which file, rather than silently returning no hits for it.
func TestMatchLineOverTheLimitErrorsClearly(t *testing.T) {
	t.Parallel()
	const overLimit = 2 * 1024 * 1024 // maxLineBytes is 1MB
	root := tree(t, map[string]string{"huge.txt": strings.Repeat("z", overLimit)})
	_, err := scan.Grep(root, scan.Filter{}, regexp.MustCompile("TODO"))
	if err == nil {
		t.Fatal("a line past the limit produced no error")
	}
	if !strings.Contains(err.Error(), "huge.txt") {
		t.Errorf("err = %v; want it to name the file", err)
	}
}

// TestMatchBinaryFileDoesNotCrash pins that NUL bytes are data, not a panic.
// A scan of a source tree meets .png and .so files routinely.
func TestMatchBinaryFileDoesNotCrash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binary := append([]byte{0x7f, 'E', 'L', 'F', 0, 0, 0}, []byte("TODO\x00\x01\x02")...)
	if err := os.WriteFile(filepath.Join(root, "a.bin"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	hits, err := scan.Grep(root, scan.Filter{}, regexp.MustCompile("TODO"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("got %d hits; want the match inside the binary", len(hits))
	}
}

func TestMatchUnreadableFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	fsprobe.NeedsEnforcedPermissions(t)
	root := tree(t, map[string]string{"secret.go": "TODO\n"})
	path := filepath.Join(root, "secret.go")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, err := scan.Match([]string{path}, regexp.MustCompile("TODO"))
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v; want it to wrap os.ErrPermission", err)
	}
}

// TestGrepEqualsFilesThenMatch pins that the convenience wrapper is exactly
// its two parts, so a caller can drop to them without a change in behaviour.
func TestGrepEqualsFilesThenMatch(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"a.go": "TODO one\n", "b.md": "TODO two\n", "vendor/c.go": "TODO three\n",
	})
	f := scan.Filter{Ext: []string{".go"}, Exclude: []string{"vendor/"}}
	pattern := regexp.MustCompile("TODO")

	viaGrep, err := scan.Grep(root, f, pattern)
	if err != nil {
		t.Fatal(err)
	}
	files, err := scan.Files(root, f)
	if err != nil {
		t.Fatal(err)
	}
	viaParts, err := scan.Match(files, pattern)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(viaGrep) != fmt.Sprint(viaParts) {
		t.Errorf("Grep = %v; Files+Match = %v", viaGrep, viaParts)
	}
}

// TestGrepAnchoredPattern pins that the caller's regexp is applied as given ,
// anchors included, and matched against the untrimmed line.
func TestGrepAnchoredPattern(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"a.go": "package main\n  package indented\n",
	})
	hits, err := scan.Grep(root, scan.Filter{}, regexp.MustCompile(`^package `))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Line != 1 {
		t.Errorf("got %v; the anchor must apply to the raw line, matching only line 1", hits)
	}
}

func assertPaths(t *testing.T, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

// TestGrepPropagatesTheWalkFailure pins that Grep surfaces an unusable root
// rather than reporting no hits. "No matches" and "could not look" must not
// be the same answer. A lint that silently finds nothing passes CI.
func TestGrepPropagatesTheWalkFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such-root")
	hits, err := scan.Grep(missing, scan.Filter{}, regexp.MustCompile("TODO"))
	if err == nil {
		t.Fatal("grepping a missing root reported success")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v; want it to wrap os.ErrNotExist", err)
	}
	if hits != nil {
		t.Errorf("hits = %v; want nil alongside the error", hits)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v; want it to name the root", err)
	}
}
