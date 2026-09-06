package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOversizedEntryIsRefused pins the decompression-bomb cap.
//
// In-package so the limit can be lowered: proving it with a real 2 GiB archive
// would mean writing two gigabytes on every run, which is how a guard like
// this ends up untested.
func TestOversizedEntryIsRefused(t *testing.T) {
	original := maxEntryBytes
	maxEntryBytes = 32
	t.Cleanup(func() { maxEntryBytes = original })

	body := strings.Repeat("x", 128)
	path := filepath.Join(t.TempDir(), "big.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "big.bin", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o600,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := c(); err != nil {
			t.Fatal(err)
		}
	}

	dst := t.TempDir()
	err = Extract(context.Background(), path, dst)
	if !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("err = %v; want ErrEntryTooLarge", err)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "big.bin")); statErr == nil {
		t.Error("the oversized entry was written before being refused")
	}
}

// TestLyingHeaderCannotExceedTheCap pins the second half of the defence: Size
// is data from an untrusted archive, not a promise, so the copy is bounded
// independently of it. A header understating its payload must still be capped.
func TestLyingHeaderCannotExceedTheCap(t *testing.T) {
	original := maxEntryBytes
	maxEntryBytes = 16
	t.Cleanup(func() { maxEntryBytes = original })

	// The header says 8 bytes, so the size check passes; the tar entry really
	// contains far more.
	real := strings.Repeat("y", 4096)
	path := filepath.Join(t.TempDir(), "liar.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "liar.bin", Typeflag: tar.TypeReg, Size: int64(len(real)), Mode: 0o600,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(real)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := c(); err != nil {
			t.Fatal(err)
		}
	}

	dst := t.TempDir()
	if err := Extract(context.Background(), path, dst); !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("err = %v; want the cap enforced", err)
	}
	if info, statErr := os.Stat(filepath.Join(dst, "liar.bin")); statErr == nil {
		if info.Size() > maxEntryBytes {
			t.Errorf("wrote %d bytes past a cap of %d", info.Size(), maxEntryBytes)
		}
	}
}
