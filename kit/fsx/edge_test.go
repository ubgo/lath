package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ubgo/lath/kit/fsx"
)

// TestCopyFileOntoItself pins the check that stands between a path bug and
// data loss. Without it the truncating open empties the source, and the copy
// then faithfully writes those zero bytes back.
func TestCopyFileOntoItself(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "precious.txt")
	const content = "the only copy"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := fsx.CopyFile(path, path)
	if !errors.Is(err, fsx.ErrSameFile) {
		t.Errorf("CopyFile(x, x) = %v; want ErrSameFile", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != content {
		t.Errorf("the source now holds %q; want %q: the copy destroyed it", got, content)
	}
}

// TestCopyFileThroughASymlinkToItself pins that the check follows inodes, not
// path strings. The case a comparison of the two arguments would miss.
func TestCopyFileThroughASymlinkToItself(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privilege on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	const content = "still the only copy"
	if err := os.WriteFile(real, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := fsx.CopyFile(real, link); !errors.Is(err, fsx.ErrSameFile) {
		t.Errorf("CopyFile(file, symlink-to-file) = %v; want ErrSameFile", err)
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content is %q; want %q", got, content)
	}
}

func TestCopyFileOverwritesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Longer than the source: a copy that does not truncate leaves a tail.
	if err := os.WriteFile(dst, []byte("old and considerably longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsx.CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("dst = %q; want %q: stale bytes survived the overwrite", got, "new")
	}
}

func TestCopyFileMissingSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := fsx.CopyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "dst"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v; want it to wrap os.ErrNotExist", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "dst")); statErr == nil {
		t.Error("a failed copy left a destination file behind")
	}
}

func TestCopyFilePreservesMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "script.sh")
	dst := filepath.Join(dir, "copy.sh")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fsx.CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// An executable copied without its mode is a deploy that fails at the far
	// end, long after the copy reported success.
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v; want 0755", info.Mode().Perm())
	}
}

func TestCopyFileEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "empty"), filepath.Join(dir, "copy")
	if err := os.WriteFile(src, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsx.CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("copy of an empty file has %d bytes", len(got))
	}
}

// ── WriteAtomic ──────────────────────────────────────────────────────────

func TestWriteAtomicNilAndEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, data := range map[string][]byte{"nil": nil, "empty": {}} {
		path := filepath.Join(dir, name)
		if err := fsx.WriteAtomic(path, data, 0o644); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: wrote %d bytes; want an empty file", name, len(got))
		}
	}
}

// TestWriteAtomicLeavesNoTempOnFailure pins that a failed write does not
// litter. A run that fails and leaves .tmp-* files behind poisons any later
// directory listing, and the files are invisible to the caller that made them.
func TestWriteAtomicLeavesNoTempOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A path whose parent is a FILE: CreateTemp fails, so nothing is created.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteAtomic(filepath.Join(blocker, "child"), []byte("data"), 0o644); err == nil {
		t.Fatal("writing beneath a regular file succeeded")
	}
	assertNoTempFiles(t, dir)
}

// TestWriteAtomicRewriteKeepsOneFile pins that repeated writes do not
// accumulate temp files. The leak only shows up after many runs.
func TestWriteAtomicRewriteKeepsOneFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for i := range 5 {
		if err := fsx.WriteAtomic(path, []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after 5 writes; want 1", len(entries))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "e" {
		t.Errorf("content = %q; want the last write", got)
	}
}

// TestWriteAtomicNeverExposesAPartialFile is the property the package is named
// for: a reader either sees the old content or the new, never a prefix.
func TestWriteAtomicNeverExposesAPartialFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.txt")
	oldContent := strings.Repeat("o", 1<<16)
	newContent := strings.Repeat("n", 1<<16)
	if err := os.WriteFile(path, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue // the rename window is not a readable state on every OS
			}
			if s := string(b); s != oldContent && s != newContent {
				select {
				case bad <- s[:min(len(s), 40)]:
				default:
				}
				return
			}
		}
	}()

	for range 20 {
		if err := fsx.WriteAtomic(path, []byte(newContent), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := fsx.WriteAtomic(path, []byte(oldContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case s := <-bad:
		t.Errorf("a reader saw neither the old nor the new content: %q…", s)
	default:
	}
}

func TestWriteAtomicMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := fsx.WriteAtomic(path, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 0600 is the mode a caller picks deliberately. Landing at CreateTemp's
	// default, or at 0644, would publish a secret.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v; want 0600", info.Mode().Perm())
	}
}

// ── Exists ───────────────────────────────────────────────────────────────

func TestExistsAcrossPathShapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"a file", file, true},
		// A directory exists. A caller asking "is it there" is not asking
		// "is it a regular file".
		{"a directory", dir, true},
		{"missing", filepath.Join(dir, "nope"), false},
		{"missing under a missing parent", filepath.Join(dir, "a", "b", "c"), false},
		{"the empty path", "", false},
	} {
		got, err := fsx.Exists(tc.path)
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: Exists(%q) = %v; want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestExistsBrokenSymlink pins that a dangling link reports false rather than
// an error, os.Stat follows links, and the target is what a caller means.
func TestExistsBrokenSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privilege on Windows")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
		t.Fatal(err)
	}
	got, err := fsx.Exists(link)
	if err != nil {
		t.Errorf("err = %v; a dangling link is an answer, not a failure", err)
	}
	if got {
		t.Error("Exists reported true for a link whose target is missing")
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// ── failure paths ────────────────────────────────────────────────────────

// TestWriteAtomicOntoADirectory pins that a failing rename is reported AND
// cleans up. The temp file is already written at that point, so a bare return
// would leave it in the directory forever.
func TestWriteAtomicOntoADirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "iam-a-directory")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteAtomic(target, []byte("data"), 0o644); err == nil {
		t.Fatal("writing over a directory succeeded")
	}
	assertNoTempFiles(t, dir)
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Errorf("the directory was replaced or removed: %v %v", info, err)
	}
}

// TestCopyFileDirectoryOperands pins that a directory on either side fails
// cleanly rather than producing an empty or partial destination.
func TestCopyFileDirectoryOperands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(regular, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("a directory as the source", func(t *testing.T) {
		dst := filepath.Join(dir, "out-of-dir")
		if err := fsx.CopyFile(subdir, dst); err == nil {
			t.Error("copying a directory succeeded")
		}
		// If a destination was created, it must not be passed off as a copy.
		if b, err := os.ReadFile(dst); err == nil && len(b) > 0 {
			t.Errorf("a failed copy left %d bytes at the destination", len(b))
		}
	})

	t.Run("a directory as the destination", func(t *testing.T) {
		if err := fsx.CopyFile(regular, subdir); err == nil {
			t.Error("copying onto a directory succeeded")
		}
		if info, err := os.Stat(subdir); err != nil || !info.IsDir() {
			t.Errorf("the destination directory was damaged: %v %v", info, err)
		}
	})
}

// TestExistsDistinguishesMissingFromUnreadable pins the difference between
// "it is not there" and "I could not tell". Collapsing them to false makes a
// permission problem look like a clean slate, and the caller then creates or
// overwrites something it never actually checked.
func TestExistsDistinguishesMissingFromUnreadable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(locked, "file.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No execute bit: the file cannot be stat'd through its parent.
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got, err := fsx.Exists(inside)
	if err == nil {
		t.Fatal("an unreadable path reported no error: indistinguishable from missing")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v; want it to wrap os.ErrPermission", err)
	}
	if got {
		t.Error("Exists reported true alongside an error")
	}
	if !strings.Contains(err.Error(), inside) {
		t.Errorf("err = %v; want it to name the path", err)
	}
}

// TestCopyFileUnreadableSource pins the gap between "it is there" and "I can
// read it". Stat succeeds on a mode-000 file, so a copy that trusted Stat
// would create an empty destination and report success.
func TestCopyFileUnreadableSource(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "unreadable")
	dst := filepath.Join(dir, "copy")
	if err := os.WriteFile(src, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o644) })

	err := fsx.CopyFile(src, dst)
	if err == nil {
		t.Fatal("copying an unreadable file succeeded")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v; want it to wrap os.ErrPermission", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("an empty destination was created for a copy that could not read its source")
	}
}
