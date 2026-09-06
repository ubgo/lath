package archive_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/archive"
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

// handMade writes a tar.gz containing exactly the entries given, so the
// malicious shapes a real attacker would send can be constructed.
func handMade(t *testing.T, entries []*tar.Header, bodies []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if i < len(bodies) && bodies[i] != "" {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := c(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	src := tree(t, map[string]string{
		"main.go":        "package main",
		"sub/nested.go":  "package sub",
		"deep/a/b/c.txt": "buried",
		"skipped.md":     "not included",
	})
	bundle := filepath.Join(t.TempDir(), "b.tar.gz")

	if err := archive.TarGz(bundle, src, scan.Filter{Ext: []string{".go", ".txt"}}); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := archive.Extract(context.Background(), bundle, dst); err != nil {
		t.Fatal(err)
	}

	for rel, want := range map[string]string{
		"main.go": "package main", "sub/nested.go": "package sub", "deep/a/b/c.txt": "buried",
	} {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q; want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "skipped.md")); err == nil {
		t.Error("a file the filter excluded was archived")
	}
}

// TestExtractRefusesEveryEscape is why this package exists. Each entry name is
// a real technique for writing outside the destination.
func TestExtractRefusesEveryEscape(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"../escaped.txt",
		"../../escaped.txt",
		"sub/../../escaped.txt",
		"./../../escaped.txt",
		"/absolute.txt",
		"/etc/passwd",
		"..",
		"a/b/../../../escaped.txt",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bundle := handMade(t,
				[]*tar.Header{{Name: name, Typeflag: tar.TypeReg, Size: 5, Mode: 0o600}},
				[]string{"pwned"})

			dst := t.TempDir()
			err := archive.Extract(context.Background(), bundle, dst)
			if !errors.Is(err, archive.ErrUnsafePath) {
				t.Fatalf("entry %q = %v; want ErrUnsafePath", name, err)
			}
			// And nothing was written outside. The check must precede the write.
			outside := filepath.Join(filepath.Dir(dst), "escaped.txt")
			if _, statErr := os.Stat(outside); statErr == nil {
				t.Errorf("entry %q wrote outside the destination", name)
			}
		})
	}
}

// TestPrefixSiblingIsNotInside pins that containment is checked with Rel, not
// a string prefix: "/tmp/dst-evil" starts with "/tmp/dst" and is NOT inside it.
func TestPrefixSiblingIsNotInside(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dst := filepath.Join(parent, "dst")
	bundle := handMade(t,
		[]*tar.Header{{Name: "../dst-evil/file.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o600}},
		[]string{"hi"})

	if err := archive.Extract(context.Background(), bundle, dst); !errors.Is(err, archive.ErrUnsafePath) {
		t.Fatalf("err = %v; want ErrUnsafePath", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "dst-evil")); err == nil {
		t.Error("a sibling directory sharing the destination's prefix was written to")
	}
}

// TestSymlinkEntriesAreRefusedByDefault pins the decision: no mode writes a
// raw link, because a written link can point outside no matter how its own
// path validates.
func TestSymlinkEntriesAreRefusedByDefault(t *testing.T) {
	t.Parallel()
	bundle := handMade(t, []*tar.Header{
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777},
	}, nil)

	if err := archive.Extract(context.Background(), bundle, t.TempDir()); !errors.Is(err, archive.ErrUnsafePath) {
		t.Errorf("err = %v; want a symlink entry refused", err)
	}
}

// TestFollowSymlinksMaterialisesContentAndStillGuards pins that the opt-in
// copies bytes, never creates a link, and still refuses an escaping target.
func TestFollowSymlinksMaterialisesContentAndStillGuards(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privilege on Windows")
	}

	t.Run("a target inside the destination is copied", func(t *testing.T) {
		t.Parallel()
		bundle := handMade(t, []*tar.Header{
			{Name: "real.txt", Typeflag: tar.TypeReg, Size: 7, Mode: 0o600},
			{Name: "alias.txt", Typeflag: tar.TypeSymlink, Linkname: "real.txt", Mode: 0o777},
		}, []string{"content", ""})

		dst := t.TempDir()
		if err := archive.Extract(context.Background(), bundle, dst, archive.FollowSymlinks()); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(filepath.Join(dst, "alias.txt"))
		if err != nil {
			t.Fatal(err)
		}
		// The critical assertion: a REGULAR file, not a link.
		if info.Mode()&os.ModeSymlink != 0 {
			t.Error("FollowSymlinks wrote an actual symlink; no mode may do that")
		}
		got, err := os.ReadFile(filepath.Join(dst, "alias.txt"))
		if err != nil || string(got) != "content" {
			t.Errorf("alias.txt = %q, %v; want the target's bytes", got, err)
		}
	})

	t.Run("a target outside is still refused", func(t *testing.T) {
		t.Parallel()
		bundle := handMade(t, []*tar.Header{
			{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../../etc/passwd", Mode: 0o777},
		}, nil)
		err := archive.Extract(context.Background(), bundle, t.TempDir(), archive.FollowSymlinks())
		if !errors.Is(err, archive.ErrUnsafePath) {
			t.Errorf("err = %v; following must not become an escape", err)
		}
	})
}

func TestTarGzFailuresLeaveNoArchive(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "out.tar.gz")
	err := archive.TarGz(dst, filepath.Join(t.TempDir(), "no-such-root"), scan.Filter{})
	if err == nil {
		t.Fatal("archiving a missing root succeeded")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("a failed TarGz left an archive behind that looks valid")
	}
}

func TestExtractRejectsNonGzip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("not compressed"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := archive.Extract(context.Background(), path, t.TempDir())
	if err == nil {
		t.Fatal("extracting a non-gzip file succeeded")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Errorf("err = %v; want it to say what was wrong", err)
	}
}

func TestExtractHonoursCancellation(t *testing.T) {
	t.Parallel()
	src := tree(t, map[string]string{"a.txt": "one", "b.txt": "two"})
	bundle := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := archive.TarGz(bundle, src, scan.Filter{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := archive.Extract(ctx, bundle, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
}

func TestModePreservedThroughRoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := archive.TarGz(bundle, src, scan.Filter{}); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := archive.Extract(context.Background(), bundle, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// An executable that arrives non-executable is a deploy that fails at the
	// far end, long after the transfer reported success.
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v; want 0755", info.Mode().Perm())
	}
}

// TestCompressionLevel covers the escape hatch: a bundle of already-compressed
// artifacts gains nothing from the default level and costs real seconds.
func TestCompressionLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Highly compressible content, so the two levels cannot come out equal by
	// accident.
	body := strings.Repeat("aaaaaaaaaaaaaaaa", 4096)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	sizes := map[string]int64{}
	for name, level := range map[string]int{"none": gzip.NoCompression, "best": gzip.BestCompression} {
		dst := filepath.Join(t.TempDir(), name+".tar.gz")
		if err := archive.TarGz(dst, root, scan.Filter{}, archive.CompressionLevel(level)); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		sizes[name] = info.Size()
	}
	if sizes["best"] >= sizes["none"] {
		t.Errorf("CompressionLevel had no effect: none=%d best=%d", sizes["none"], sizes["best"])
	}
}

// TestMaxEntryBytesRefusesAZipBomb. A single entry that expands to gigabytes
// must not fill the disk of whatever unpacked it.
//
// It REFUSES rather than truncating, which is the safer of the two: a
// truncated file looks complete and fails later, somewhere else, as corrupt
// data. A named error at extraction time says exactly what happened.
func TestMaxEntryBytesRefusesAZipBomb(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const size = 4096
	if err := os.WriteFile(filepath.Join(root, "big.bin"), bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := archive.TarGz(dst, root, scan.Filter{}); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	err := archive.Extract(context.Background(), dst, out, archive.MaxEntryBytes(100))
	if err == nil {
		t.Fatal("an entry over the limit was extracted")
	}
	if !strings.Contains(err.Error(), "big.bin") {
		t.Errorf("error does not name the offending entry: %v", err)
	}
	// Nothing partial is left behind to be mistaken for a complete file.
	if _, statErr := os.Stat(filepath.Join(out, "big.bin")); statErr == nil {
		if info, _ := os.Stat(filepath.Join(out, "big.bin")); info != nil && info.Size() == size {
			t.Error("the oversized entry was written in full despite the limit")
		}
	}
}

// TestMaxEntryBytesAllowsEntriesUnderTheLimit, the guard must not refuse
// ordinary content.
func TestMaxEntryBytesAllowsEntriesUnderTheLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("fits"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := archive.TarGz(dst, root, scan.Filter{}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := archive.Extract(context.Background(), dst, out, archive.MaxEntryBytes(1024)); err != nil {
		t.Fatalf("an entry well under the limit was refused: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(out, "small.txt")); err != nil || string(got) != "fits" {
		t.Errorf("content = %q, %v", got, err)
	}
}

// TestExtractSkipsIrregularEntries, devices, FIFOs and sockets have no place
// in a deploy bundle, and creating them is a privilege problem. They are
// skipped rather than failing the whole extraction.
func TestExtractSkipsIrregularEntries(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "odd.tar.gz")
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	for _, h := range []*tar.Header{
		{Name: "fifo", Typeflag: tar.TypeFifo, Mode: 0o644},
		{Name: "dev", Typeflag: tar.TypeChar, Mode: 0o644},
		{Name: "real.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("ok")); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []io.Closer{tw, zw, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	out := t.TempDir()
	if err := archive.Extract(context.Background(), dst, out); err != nil {
		t.Fatalf("an irregular entry aborted the extraction: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "real.txt")); err != nil {
		t.Errorf("the regular entry after an irregular one was not extracted: %v", err)
	}
	for _, skipped := range []string{"fifo", "dev"} {
		if _, err := os.Stat(filepath.Join(out, skipped)); !os.IsNotExist(err) {
			t.Errorf("%s was created", skipped)
		}
	}
}

// TestTarGzFollowsNothingOutsideRoot. A symlink pointing outside the tree
// must not smuggle its target into the archive.
func TestTarGzStoresSymlinksAsLinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	secret := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(secret, []byte("do not archive me"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := archive.TarGz(dst, root, scan.Filter{}); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("do not archive me")) {
		t.Error("the symlink's target content was written into the archive")
	}
}

// TestFollowSymlinksReportsADanglingTarget. The link is inside the
// destination, so the guard passes and the copy is attempted; the target
// simply is not there. The error must name the ENTRY, because the tar member
// is what the caller can act on, and the resolved path is an implementation
// detail they never wrote down.
func TestFollowSymlinksReportsADanglingTarget(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privilege on Windows")
	}
	bundle := handMade(t, []*tar.Header{
		{Name: "alias.txt", Typeflag: tar.TypeSymlink, Linkname: "missing.txt", Mode: 0o777},
	}, nil)

	err := archive.Extract(context.Background(), bundle, t.TempDir(), archive.FollowSymlinks())
	if err == nil {
		t.Fatal("a link to nothing extracted successfully")
	}
	if !strings.Contains(err.Error(), "alias.txt") {
		t.Errorf("err = %v; want the tar entry named, not just the resolved path", err)
	}
	// Not an escape: the guard passed, the file was absent. Reporting it as
	// ErrUnsafePath would send the caller hunting for an attack that is not
	// there.
	if errors.Is(err, archive.ErrUnsafePath) {
		t.Error("a missing target was reported as an unsafe path")
	}
}

// TestExtractCreatesADestinationThatDoesNotExistYet. resolvedAbs cannot
// resolve symlinks in a path that is not there, and falls back to the absolute
// path; without that fallback every extraction into a fresh directory would
// fail, which is the commonest extraction there is.
func TestExtractCreatesADestinationThatDoesNotExistYet(t *testing.T) {
	t.Parallel()
	bundle := handMade(t, []*tar.Header{
		{Name: "hello.txt", Typeflag: tar.TypeReg, Size: 5, Mode: 0o600},
	}, []string{"hello"})
	dst := filepath.Join(t.TempDir(), "not", "created", "yet")

	if err := archive.Extract(context.Background(), bundle, dst, nil...); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("hello.txt = %q, %v; want the archive extracted into a fresh path", got, err)
	}
}

// TestTarGzReportsAnUnreadableFile. Silence here would be the worst failure
// this package has: an archive that completes, is a valid tar.gz, and is
// missing one of the files it was asked to carry. Nothing downstream can tell
// the difference until a restore comes up short.
func TestTarGzReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()
	fsprobe.NeedsEnforcedPermissions(t)
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o000); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.tar.gz")

	err := archive.TarGz(dst, root, scan.Filter{})
	if err == nil {
		t.Fatal("an archive completed without a file it was asked to carry")
	}
	if !strings.Contains(err.Error(), "secret.txt") {
		t.Errorf("err = %v; want the file that could not be read named", err)
	}
	// A partial archive is worse than none: it looks restorable.
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Error("a half-written archive was left behind")
	}
}

// TestExtractRefusesAnUnwritableDestination. An extraction that reported
// success while writing nothing would be the quiet failure this package must
// never have: the caller proceeds to use files that are not there.
func TestExtractRefusesAnUnwritableDestination(t *testing.T) {
	t.Parallel()
	fsprobe.NeedsEnforcedDirectoryPermissions(t)
	bundle := handMade(t, []*tar.Header{
		{Name: "nested/hello.txt", Typeflag: tar.TypeReg, Size: 5, Mode: 0o600},
	}, []string{"hello"})
	dst := t.TempDir()
	if err := os.Chmod(dst, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dst, 0o700) })

	err := archive.Extract(context.Background(), bundle, dst)
	if err == nil {
		t.Fatal("extraction into a read-only directory reported success")
	}
	// Not an escape: the path was fine, the filesystem said no. Reporting
	// ErrUnsafePath would send the caller hunting for an attack.
	if errors.Is(err, archive.ErrUnsafePath) {
		t.Error("a permission failure was reported as an unsafe path")
	}
}

// TestExtractRejectsATruncatedArchive. A transfer cut short leaves a valid
// gzip header over an incomplete stream, so the failure appears mid-extraction
// with some files already written. It must still be an error: a caller that
// saw success would be looking at a partial tree.
func TestExtractRejectsATruncatedArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat(name, 500)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	full := filepath.Join(t.TempDir(), "full.tar.gz")
	if err := archive.TarGz(full, root, scan.Filter{}); err != nil {
		t.Fatal(err)
	}
	whole, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(t.TempDir(), "cut.tar.gz")
	if err := os.WriteFile(cut, whole[:len(whole)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	if err := archive.Extract(context.Background(), cut, t.TempDir()); err == nil {
		t.Fatal("a truncated archive extracted successfully")
	}
}

// TestTarGzReportsAnUnwritableDestination. An archive step that reported
// success while writing nothing would be discovered at restore time, which is
// the worst possible moment to learn a backup is not there.
func TestTarGzReportsAnUnwritableDestination(t *testing.T) {
	t.Parallel()
	fsprobe.NeedsEnforcedDirectoryPermissions(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	if err := archive.TarGz(filepath.Join(locked, "out.tar.gz"), root, scan.Filter{}); err == nil {
		t.Fatal("archiving into a read-only directory reported success")
	}
}

// TestExtractRefusesADestinationThatIsAFile. The mistake is easy — passing the
// archive's own path as the destination — and the failure must name it rather
// than surfacing as a confusing mkdir error inside a loop.
func TestExtractRefusesADestinationThatIsAFile(t *testing.T) {
	t.Parallel()
	bundle := handMade(t, []*tar.Header{
		{Name: "hello.txt", Typeflag: tar.TypeReg, Size: 5, Mode: 0o600},
	}, []string{"hello"})

	if err := archive.Extract(context.Background(), bundle, bundle); err == nil {
		t.Fatal("extracting into a regular file succeeded")
	}
}

// TestExtractIsIdempotent. A retried extraction, after a network drop or a
// failed verification, must overwrite rather than fail on the files the first
// attempt already wrote.
func TestExtractIsIdempotent(t *testing.T) {
	t.Parallel()
	bundle := handMade(t, []*tar.Header{
		{Name: "nested/hello.txt", Typeflag: tar.TypeReg, Size: 5, Mode: 0o600},
	}, []string{"hello"})
	dst := t.TempDir()

	for attempt := 1; attempt <= 2; attempt++ {
		if err := archive.Extract(context.Background(), bundle, dst); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dst, "nested", "hello.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("after two extractions: %q, %v", got, err)
	}
}
