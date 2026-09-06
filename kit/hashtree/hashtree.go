// Package hashtree builds content digests for cache keys.
//
// The shape is a composable Hasher rather than a single function, because a
// real cache key is assembled from whatever the output actually depends on ,
// some files, a tool version, an environment variable, a lockfile, in no
// fixed order. A function that assumed "a tree, then some decorations" would
// be wrong for half its callers.
//
// The bug this exists to prevent: lath's own compile cache once covered only
// the definition's source, so upgrading lath left every previously compiled
// binary in place and the new behaviour silently did not apply. A cache key
// must cover everything the output depends on.
package hashtree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ubgo/lath/kit/scan"
)

// fieldSeparator delimits every contributed field.
//
// A separator is required, not cosmetic: without one, AddString("ab","c") and
// AddString("a","bc") produce the same digest, so two different dependency
// sets would share a cache entry. NUL is used because it cannot occur in a
// path and is vanishingly rare in a version string.
const fieldSeparator = "\x00"

// Hasher accumulates the inputs a cache key should depend on.
//
// Invariant: order-significant. Two callers composing the same inputs in the
// same order always agree; composing them in a different order deliberately
// does not, because a different assembly is a different question.
//
// Invariant: the first error encountered is retained and returned by Sum, so a
// chain of Add calls needs no error check at every step.
type Hasher struct {
	h   hash.Hash
	err error
}

// New returns an empty Hasher.
func New() *Hasher { return &Hasher{h: sha256.New()} }

// AddString folds literal values in. A tool version, a toolchain version, a
// build identifier.
func (t *Hasher) AddString(values ...string) *Hasher {
	for _, v := range values {
		t.write(v)
	}
	return t
}

// AddFile folds one file's path and contents in.
//
// A missing file is an error rather than an empty contribution: a key that
// silently ignores an absent dependency collides with one where the dependency
// exists, which is the failure this package is meant to prevent.
func (t *Hasher) AddFile(path string) *Hasher {
	if t.err != nil {
		return t
	}
	t.write("file:" + filepath.ToSlash(path))
	f, err := os.Open(path)
	if err != nil {
		t.err = fmt.Errorf("hashtree: %w", err)
		return t
	}
	defer f.Close()
	if _, err := io.Copy(t.h, f); err != nil {
		t.err = fmt.Errorf("hashtree: reading %s: %w", path, err)
	}
	return t
}

// AddTree folds in every file matching the filter, walked in a stable order.
//
// Relative paths are folded in as well as contents, so renaming a file changes
// the digest even when no byte of any file changed.
func (t *Hasher) AddTree(root string, f scan.Filter) *Hasher {
	if t.err != nil {
		return t
	}
	files, err := scan.Files(root, f)
	if err != nil {
		t.err = err
		return t
	}
	t.write("tree:" + filepath.ToSlash(root) + ":" + strconv.Itoa(len(files)))
	for _, path := range files {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		t.write("entry:" + filepath.ToSlash(rel))
		if t.AddFileContents(path); t.err != nil {
			return t
		}
	}
	return t
}

// AddFileContents folds a file's contents in WITHOUT its path. Used by AddTree,
// which contributes the relative path itself, and available to a caller that
// wants content identity independent of location.
func (t *Hasher) AddFileContents(path string) *Hasher {
	if t.err != nil {
		return t
	}
	f, err := os.Open(path)
	if err != nil {
		t.err = fmt.Errorf("hashtree: %w", err)
		return t
	}
	defer f.Close()
	if _, err := io.Copy(t.h, f); err != nil {
		t.err = fmt.Errorf("hashtree: reading %s: %w", path, err)
	}
	return t
}

// Sum returns the hex digest, or the first error encountered while building it.
func (t *Hasher) Sum() (string, error) {
	if t.err != nil {
		return "", t.err
	}
	return hex.EncodeToString(t.h.Sum(nil)), nil
}

// write contributes one length-delimited field.
func (t *Hasher) write(s string) {
	if t.err != nil {
		return
	}
	// The length prefix makes the separator unambiguous even if a value
	// somehow contains one.
	_, _ = io.WriteString(t.h, strconv.Itoa(len(s)))
	_, _ = io.WriteString(t.h, fieldSeparator)
	_, _ = io.WriteString(t.h, s)
	_, _ = io.WriteString(t.h, fieldSeparator)
}

// Of is the one-call form for the common case: hash one filtered tree.
func Of(root string, f scan.Filter) (string, error) {
	return New().AddTree(root, f).Sum()
}

// OfFiles hashes an explicit list, in the order given.
func OfFiles(paths []string) (string, error) {
	h := New()
	for _, p := range paths {
		h.AddFile(p)
	}
	return h.Sum()
}
