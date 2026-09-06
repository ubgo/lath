// Package fsprobe answers what the filesystem under a test will actually
// enforce, so a test can skip on a fact rather than on a guess about the
// platform.
//
// Why it exists: a dozen tests in this kit assert that an operation is REFUSED
// — reading a file with mode 0000, writing into a directory with no write bit.
// Those assertions are meaningful on a developer's machine and meaningless in
// three common situations: as root, on Windows, and on a filesystem mounted
// without permission support. Each of those makes the operation SUCCEED, and
// the test then reports a failure that is not one.
//
// The obvious guard is `runtime.GOOS == "windows" || os.Geteuid() == 0`, which
// is a list of the cases somebody thought of. This asks the filesystem
// directly instead, which is the same move as asking the kernel whether pid 1
// is signallable rather than assuming it belongs to root.
//
// Internal on purpose: it is a testing convenience, not something this kit
// promises to anyone.
package fsprobe

import (
	"os"
	"path/filepath"
	"testing"
)

// unreadable is the mode a file gets when the test wants reading it to fail.
const unreadable os.FileMode = 0o000

// unwritableDir is the mode a directory gets when the test wants writing into
// it to fail. Read and execute, so the directory can still be listed and
// traversed; only creation is meant to be refused.
const unwritableDir os.FileMode = 0o500

// NeedsEnforcedPermissions skips the test unless this filesystem actually
// refuses a read of an unreadable file.
//
// Call it at the top of any test whose subject is "the error path when
// permission is denied". The skip message names the fact, not the platform,
// because the reader's next question is always why.
func NeedsEnforcedPermissions(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, []byte("x"), unreadable); err != nil {
		t.Skipf("cannot create an unreadable file to probe with: %v", err)
	}
	if _, err := os.ReadFile(probe); err == nil {
		t.Skip("this filesystem does not enforce read permissions " +
			"(running as root, on Windows, or on a mount without permission support)")
	}
}

// NeedsEnforcedDirectoryPermissions skips the test unless this filesystem
// actually refuses a write into a directory with no write bit.
//
// Separate from NeedsEnforcedPermissions because the two are not the same
// question: a filesystem can enforce file modes and still let a privileged
// process write into a read-only directory.
func NeedsEnforcedDirectoryPermissions(t *testing.T) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, unwritableDir); err != nil {
		t.Skipf("cannot create a read-only directory to probe with: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := os.WriteFile(filepath.Join(dir, "probe"), []byte("x"), 0o600); err == nil {
		t.Skip("this filesystem does not enforce directory permissions " +
			"(running as root, on Windows, or on a mount without permission support)")
	}
}

// NeedsModePreservation skips the test unless this filesystem stores the mode
// bits it is given.
//
// Windows keeps a single read-only attribute rather than nine permission bits,
// so a file created 0600 reports 0666 back. A test asserting an exact mode is
// asking a question that platform cannot answer, and the skip says so instead
// of reporting a failure.
func NeedsModePreservation(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	const want os.FileMode = 0o600
	if err := os.WriteFile(probe, []byte("x"), want); err != nil {
		t.Skipf("cannot create a file to probe with: %v", err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Skipf("cannot stat the probe file: %v", err)
	}
	if info.Mode().Perm() != want {
		t.Skipf("this filesystem does not preserve mode bits (0%o came back as 0%o)",
			want, info.Mode().Perm())
	}
}
