package scan_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ubgo/lath/kit/scan"
)

// tree writes a file layout under a temp dir and returns its root.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestFilesFilters(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"a.go":              "package a",
		"b.txt":             "text",
		"vendor/dep.go":     "package dep",
		"gen/generated.go":  "package gen",
		"pkg/c.go":          "package c",
		".hidden/secret.go": "package hidden",
	})

	for _, tc := range []struct {
		name   string
		filter scan.Filter
		want   []string
	}{
		{
			name:   "no constraints matches everything",
			filter: scan.Filter{},
			want:   []string{".hidden/secret.go", "a.go", "b.txt", "gen/generated.go", "pkg/c.go", "vendor/dep.go"},
		},
		{
			name:   "by extension",
			filter: scan.Filter{Ext: []string{".go"}},
			want:   []string{".hidden/secret.go", "a.go", "gen/generated.go", "pkg/c.go", "vendor/dep.go"},
		},
		{
			name:   "exclusions: the lint-script shape",
			filter: scan.Filter{Ext: []string{".go"}, Exclude: []string{"vendor/", "gen/", ".hidden/"}},
			want:   []string{"a.go", "pkg/c.go"},
		},
		{
			name:   "include narrows",
			filter: scan.Filter{Ext: []string{".go"}, Include: []string{"pkg/"}},
			want:   []string{"pkg/c.go"},
		},
		{
			name:   "exclusion beats inclusion",
			filter: scan.Filter{Ext: []string{".go"}, Include: []string{"vendor/"}, Exclude: []string{"vendor/"}},
			want:   nil,
		},
		{
			name:   "skip hidden",
			filter: scan.Filter{Ext: []string{".go"}, SkipHidden: true, Exclude: []string{"vendor/", "gen/"}},
			want:   []string{"a.go", "pkg/c.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scan.Files(root, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			var rel []string
			for _, p := range got {
				r, _ := filepath.Rel(root, p)
				rel = append(rel, r)
			}
			if len(rel) != len(tc.want) {
				t.Fatalf("got %v; want %v", rel, tc.want)
			}
			for i := range rel {
				// filepath.ToSlash on the RESULT, not on the package's output:
				// Files returns native paths on purpose, since a caller hands
				// them to os.Open. Only this comparison needs them in the
				// shape a human wrote the expectation in.
				if filepath.ToSlash(rel[i]) != tc.want[i] {
					t.Errorf("got %v; want %v", rel, tc.want)
					break
				}
			}
		})
	}
}

func TestGrep(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"good.go":       "package a\n\nfunc f() {}\n",
		"bad.go":        "package a\n\nfunc g() {\n\tos.Getenv(\"X\")\n}\n",
		"vendor/bad.go": "package v\n\nos.Getenv(\"IGNORED\")\n",
	})

	hits, err := scan.Grep(root,
		scan.Filter{Ext: []string{".go"}, Exclude: []string{"vendor/"}},
		regexp.MustCompile(`os\.Getenv`))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits; want 1 (vendor must be excluded): %v", len(hits), hits)
	}
	h := hits[0]
	if filepath.Base(h.File) != "bad.go" {
		t.Errorf("File = %q", h.File)
	}
	// 1-based, as an editor counts lines, off-by-one here makes every hit
	// point at the wrong line.
	if h.Line != 4 {
		t.Errorf("Line = %d; want 4", h.Line)
	}
	if h.Text != `os.Getenv("X")` {
		t.Errorf("Text = %q; want the trimmed line", h.Text)
	}
}

// TestLongLines pins that a minified or generated file does not abort a scan.
// bufio.Scanner's 64KB default fails with a message about a long line rather
// than about the file, which is a confusing way to learn a lint run was
// incomplete.
func TestLongLines(t *testing.T) {
	t.Parallel()
	long := make([]byte, 200*1024)
	for i := range long {
		long[i] = 'x'
	}
	root := tree(t, map[string]string{"big.go": string(long) + "\nos.Getenv(\"X\")\n"})

	hits, err := scan.Grep(root, scan.Filter{Ext: []string{".go"}},
		regexp.MustCompile(`os\.Getenv`))
	if err != nil {
		t.Fatalf("a 200KB line broke the scan: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("got %d hits; want 1", len(hits))
	}
}

func TestHitString(t *testing.T) {
	t.Parallel()
	h := scan.Hit{File: "a/b.go", Line: 12, Text: "boom"}
	// The compiler/grep format, so an editor can jump to it.
	if got := h.String(); got != "a/b.go:12: boom" {
		t.Errorf("String() = %q", got)
	}
}
