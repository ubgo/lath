package fsx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ubgo/lath/kit/fsx"
)

func TestWriteAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := fsx.WriteAtomic(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "v1" {
		t.Fatalf("read %q, %v", content, err)
	}

	// Overwrite must replace, not append.
	if err := fsx.WriteAtomic(path, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != "v2" {
		t.Errorf("after overwrite = %q; want v2", content)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v; want 0600", info.Mode().Perm())
	}
}

// TestWriteAtomicLeavesNoTempFile pins that a completed write leaves the
// directory clean. A leftover .tmp-* on every call is the failure mode of a
// naive implementation.
func TestWriteAtomicLeavesNoTempFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := fsx.WriteAtomic(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v; want only the written file", names)
	}
}

// TestWriteAtomicFailureLeavesOriginal pins the property the whole function
// exists for: a failed write must not destroy what was already there.
func TestWriteAtomicFailureLeavesOriginal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "keep")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A directory that cannot be written to forces the temp-file creation to
	// fail, which is the earliest possible failure point.
	readonly := filepath.Join(dir, "ro")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteAtomic(filepath.Join(readonly, "f"), []byte("x"), 0o600); err == nil {
		t.Skip("running as root; the read-only directory was writable")
	}

	content, err := os.ReadFile(path)
	if err != nil || string(content) != "original" {
		t.Errorf("original file = %q, %v; want it untouched", content, err)
	}
}

func TestCopyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("contents"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := fsx.CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(dst)
	if err != nil || string(content) != "contents" {
		t.Fatalf("dst = %q, %v", content, err)
	}
	info, _ := os.Stat(dst)
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v; want the source's 0640", info.Mode().Perm())
	}
}

func TestExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	if ok, err := fsx.Exists(path); ok || err != nil {
		t.Errorf("missing file: %v, %v; want false, nil", ok, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := fsx.Exists(path); !ok || err != nil {
		t.Errorf("present file: %v, %v; want true, nil", ok, err)
	}
}
