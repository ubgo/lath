package hashtree_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/hashtree"
	"github.com/ubgo/lath/kit/internal/fsprobe"
	"github.com/ubgo/lath/kit/scan"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func sum(t *testing.T, h *hashtree.Hasher) string {
	t.Helper()
	s, err := h.Sum()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSeparatorPreventsFieldSmearing is the property a naive concatenation
// gets wrong: two different input sets must not collide because their
// concatenations happen to match.
func TestSeparatorPreventsFieldSmearing(t *testing.T) {
	t.Parallel()
	a := sum(t, hashtree.New().AddString("ab", "c"))
	b := sum(t, hashtree.New().AddString("a", "bc"))
	if a == b {
		t.Error(`AddString("ab","c") and AddString("a","bc") collided: fields are not delimited`)
	}
}

func TestOrderIsSignificant(t *testing.T) {
	t.Parallel()
	// A different assembly is a different question, so it must hash differently.
	if sum(t, hashtree.New().AddString("x", "y")) == sum(t, hashtree.New().AddString("y", "x")) {
		t.Error("input order did not affect the digest")
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{"a.go": "package a", "sub/b.go": "package b"})
	first, err := hashtree.Of(root, scan.Filter{Ext: []string{".go"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, err := hashtree.Of(root, scan.Filter{Ext: []string{".go"}})
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatal("the digest is not stable across runs")
		}
	}
	if len(first) != 64 {
		t.Errorf("digest is %d chars; want 64 hex", len(first))
	}
}

// TestContentChangeChangesDigest and its siblings pin what a cache key must
// notice. Each is a real way a stale binary gets reused.
func TestWhatChangesTheDigest(t *testing.T) {
	t.Parallel()
	f := scan.Filter{Ext: []string{".go"}}
	base := map[string]string{"a.go": "package a", "b.go": "package b"}

	root := tree(t, base)
	original, err := hashtree.Of(root, f)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(root string)
	}{
		{"a file's contents change", func(r string) {
			os.WriteFile(filepath.Join(r, "a.go"), []byte("package a // edited"), 0o600)
		}},
		{"a file is renamed, contents identical", func(r string) {
			os.Rename(filepath.Join(r, "b.go"), filepath.Join(r, "renamed.go"))
		}},
		{"a file is added", func(r string) {
			os.WriteFile(filepath.Join(r, "c.go"), []byte("package c"), 0o600)
		}},
		{"a file is removed", func(r string) {
			os.Remove(filepath.Join(r, "b.go"))
		}},
		{"a file moves to a subdirectory", func(r string) {
			os.MkdirAll(filepath.Join(r, "sub"), 0o755)
			os.Rename(filepath.Join(r, "b.go"), filepath.Join(r, "sub", "b.go"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tree(t, base)
			tc.mutate(r)
			got, err := hashtree.Of(r, f)
			if err != nil {
				t.Fatal(err)
			}
			if got == original {
				t.Errorf("the digest did not change when %s: a stale cache entry would be reused", tc.name)
			}
		})
	}
}

// TestUnmatchedFileDoesNotAffectTheDigest pins the other direction: touching
// something the filter excludes must not bust the cache.
func TestUnmatchedFileDoesNotAffectTheDigest(t *testing.T) {
	t.Parallel()
	f := scan.Filter{Ext: []string{".go"}}
	root := tree(t, map[string]string{"a.go": "package a"})
	before, err := hashtree.Of(root, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := hashtree.Of(root, f)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("a file the filter excludes changed the digest")
	}
}

// TestMixedComposition is the case Mix was replaced to serve: a key built from
// a tree AND a version AND a file, in whatever order the caller needs.
func TestMixedComposition(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{"a.go": "package a", "go.sum": "h1:abc"})
	f := scan.Filter{Ext: []string{".go"}}

	key := func(version string) string {
		return sum(t, hashtree.New().
			AddTree(root, f).
			AddString(version, "go1.26").
			AddFile(filepath.Join(root, "go.sum")))
	}

	// The exact bug this package exists for: the source is unchanged, only the
	// tool version moved, and the key MUST differ or the old binary is reused.
	if key("v1.0.0") == key("v1.1.0") {
		t.Error("a tool version change did not affect the key: the stale-binary bug")
	}
	if key("v1.0.0") != key("v1.0.0") {
		t.Error("the same inputs produced different keys")
	}
}

// TestErrorsAreDeferredToSum pins the chaining ergonomics: a broken step does
// not panic mid-chain, and the error survives to Sum.
func TestErrorsAreDeferredToSum(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope")

	_, err := hashtree.New().
		AddString("a").
		AddFile(missing).
		AddString("b").
		Sum()
	if err == nil {
		t.Fatal("a missing file produced no error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v; want it to name the file", err)
	}

	// A missing tree root likewise.
	if _, err := hashtree.Of(filepath.Join(t.TempDir(), "gone"), scan.Filter{}); err == nil {
		t.Error("a missing root produced no error")
	}
}

func TestOfFiles(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{"a": "one", "b": "two"})
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")

	ab, err := hashtree.OfFiles([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	ba, err := hashtree.OfFiles([]string{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if ab == ba {
		t.Error("OfFiles ignored the order it was given")
	}
	if _, err := hashtree.OfFiles(nil); err != nil {
		t.Errorf("hashing nothing is not an error: %v", err)
	}
}

// TestEmptyTreeStillHashes pins that an empty match set produces a usable
// digest rather than an error, "nothing matched" is a valid state to cache.
func TestEmptyTreeStillHashes(t *testing.T) {
	t.Parallel()
	got, err := hashtree.Of(t.TempDir(), scan.Filter{Ext: []string{".go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Errorf("digest = %q", got)
	}
}

// TestAnUnreadableFileFailsTheDigest. This package answers "has anything
// changed"; a file it cannot read is a question it cannot answer, and folding
// in nothing would produce a digest identical to one where the file was empty.
// That digest would then match a cached build and skip work that needed doing.
func TestAnUnreadableFileFailsTheDigest(t *testing.T) {
	t.Parallel()
	fsprobe.NeedsEnforcedPermissions(t)
	root := t.TempDir()
	sealed := filepath.Join(root, "sealed.txt")
	if err := os.WriteFile(sealed, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		sum  func() (string, error)
	}{
		{"AddFile", func() (string, error) { return hashtree.New().AddFile(sealed).Sum() }},
		{"AddFileContents", func() (string, error) {
			return hashtree.New().AddFileContents(sealed).Sum()
		}},
		{"AddTree", func() (string, error) { return hashtree.Of(root, scan.Filter{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.sum()
			if err == nil {
				t.Fatalf("%s returned the digest %s for a file it could not read", tc.name, got)
			}
			if got != "" {
				t.Error("a digest was returned alongside the error; a caller checking only the value would cache it")
			}
		})
	}
}

// TestAddTreeRefusesAMissingRoot. An empty tree and an absent tree hash
// differently only if the absent one is an error: otherwise deleting a source
// directory would leave the key unchanged and the stale build valid.
func TestAddTreeRefusesAMissingRoot(t *testing.T) {
	t.Parallel()
	_, err := hashtree.Of(filepath.Join(t.TempDir(), "nowhere"), scan.Filter{})
	if err == nil {
		t.Fatal("a root that does not exist produced a digest")
	}
}

// TestTheFirstErrorWins. Every Add is chainable and defers reporting to Sum,
// so a later successful Add must not clear an earlier failure, and a later
// failure must not replace the first, which is the one nearest the cause.
func TestTheFirstErrorWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("fine"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "first-missing.txt")
	alsoMissing := filepath.Join(dir, "second-missing.txt")

	_, err := hashtree.New().
		AddFile(missing).
		AddString("still chaining").
		AddFile(good).
		AddFile(alsoMissing).
		Sum()
	if err == nil {
		t.Fatal("a chain containing a missing file produced a digest")
	}
	if !strings.Contains(err.Error(), "first-missing.txt") {
		t.Errorf("err = %v; want the FIRST failure, which is nearest the cause", err)
	}
}
