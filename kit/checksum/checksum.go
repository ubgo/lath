// Package checksum writes and verifies the manifest that ships beside release
// artifacts.
//
// The format is the one `sha256sum` and `shasum -a 256` read and write: one
// line per file, the hex digest, two spaces, the name. That choice is the
// whole point of this package, because it means the manifest is verifiable by
// a person on any machine with no tool from this project installed:
//
//	sha256sum -c checksums.txt
//
// A bespoke format would work identically inside a pipeline and be useless the
// moment someone downloads an artifact and wants to check it by hand.
//
// Names in a manifest are BASENAMES, never paths. A manifest is consumed in
// the directory its files were downloaded to, which is rarely the directory
// they were built in, and a path that leaked from the build machine turns
// verification into a puzzle about someone else's filesystem.
package checksum

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultName is what the manifest is conventionally called. Named because
// both the writer and every reader must agree, and a literal at either site is
// how they stop agreeing.
const DefaultName = "checksums.txt"

// separator divides a digest from its name. WIRE FORMAT: two spaces is what
// sha256sum emits and what its -c mode expects; a single space means
// "text mode" to some implementations and is silently accepted by others,
// which is exactly the kind of difference that shows up only on someone
// else's machine.
const separator = "  "

// ErrNotListed reports a file the manifest does not mention. Distinguished
// from a mismatch because the two mean opposite things: a missing entry is an
// incomplete manifest, a mismatch is a corrupted or substituted file.
var ErrNotListed = errors.New("checksum: file is not in the manifest")

// ErrMismatch reports a file whose digest is not the one recorded. The
// dangerous outcome, and the reason this package exists.
var ErrMismatch = errors.New("checksum: digest does not match")

// Entry is one file's line in a manifest.
type Entry struct {
	// Name is the file's BASENAME, as it will appear where the manifest is
	// consumed. See the package comment for why a path here is a bug.
	Name string
	// SHA256 is the lower-case hex digest.
	SHA256 string
}

// Of computes the digest of one file.
//
// Streamed rather than read whole: release artifacts are archives, and holding
// one in memory to hash it is a needless limit on how large an artifact may be.
func Of(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checksum: reading %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Manifest digests every path and returns the entries, sorted by name.
//
// Sorted because a manifest is committed, attached to releases, and diffed:
// filesystem order would make two runs over the same files produce different
// bytes, and a diff full of reordering hides the one line that changed.
func Manifest(paths []string) ([]Entry, error) {
	entries := make([]Entry, 0, len(paths))
	for _, path := range paths {
		sum, err := Of(path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Name: filepath.Base(path), SHA256: sum})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Render formats entries in the sha256sum format.
func Render(entries []Entry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s%s%s\n", e.SHA256, separator, e.Name)
	}
	return b.String()
}

// Parse reads a manifest, accepting what sha256sum writes.
//
// Tolerates the BSD/GNU binary marker: `shasum -b` prefixes the name with '*'
// to mean "binary mode", which is meaningless on every platform this runs on
// but appears in manifests written by other tools, and refusing them would
// make this package unable to read half the manifests in the wild.
//
// Blank lines are skipped. Anything else that is not "<digest><spaces><name>"
// is an error naming the line, because a manifest that parses to fewer entries
// than it has lines is a verification that silently checks less than it claims.
func Parse(r io.Reader) ([]Entry, error) {
	var entries []Entry
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		digest, name, ok := strings.Cut(text, " ")
		if !ok {
			return nil, fmt.Errorf("checksum: line %d is not <digest> <name>: %q", line, text)
		}
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "*")
		if name == "" {
			return nil, fmt.Errorf("checksum: line %d has a digest and no name", line)
		}
		if _, err := hex.DecodeString(digest); err != nil || len(digest) != sha256HexLen {
			return nil, fmt.Errorf("checksum: line %d has no sha256 digest: %q", line, digest)
		}
		entries = append(entries, Entry{Name: name, SHA256: strings.ToLower(digest)})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("checksum: %w", err)
	}
	return entries, nil
}

// sha256HexLen is the length of a sha256 digest in hex. A shorter string that
// still decodes as hex is a truncated digest, which would otherwise verify
// nothing while looking like a manifest.
const sha256HexLen = sha256.Size * 2

// Write renders a manifest for paths and writes it to dir under DefaultName,
// returning the manifest's own path.
//
// The manifest lands BESIDE the files it describes, which is what makes the
// basename rule work: a verifier runs in that directory and every name
// resolves.
func Write(dir string, paths []string) (string, error) {
	entries, err := Manifest(paths)
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, DefaultName)
	if err := os.WriteFile(out, []byte(Render(entries)), 0o644); err != nil {
		return "", fmt.Errorf("checksum: %w", err)
	}
	return out, nil
}

// Verify checks every file in dir that the manifest lists.
//
// It reports the FIRST failure with the file named, and treats a listed file
// that is absent as a mismatch rather than skipping it: a manifest describing
// artifacts that were never uploaded is the failure mode this exists to catch,
// and silence there would report a broken release as a good one.
func Verify(dir string, manifest []Entry) error {
	for _, e := range manifest {
		sum, err := Of(filepath.Join(dir, e.Name))
		if err != nil {
			return fmt.Errorf("checksum: %s: %w", e.Name, err)
		}
		if !strings.EqualFold(sum, e.SHA256) {
			return fmt.Errorf("checksum: %s: got %s, want %s: %w", e.Name, sum, e.SHA256, ErrMismatch)
		}
	}
	return nil
}

// VerifyFile checks one file against a manifest.
//
// Separate from Verify because a downloader has one artifact and a whole
// manifest: it needs "is THIS file the one listed", and running the full
// verification would fail on every artifact it did not download.
func VerifyFile(path string, manifest []Entry) error {
	name := filepath.Base(path)
	for _, e := range manifest {
		if e.Name != name {
			continue
		}
		sum, err := Of(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, e.SHA256) {
			return fmt.Errorf("checksum: %s: got %s, want %s: %w", name, sum, e.SHA256, ErrMismatch)
		}
		return nil
	}
	return fmt.Errorf("checksum: %s: %w", name, ErrNotListed)
}
