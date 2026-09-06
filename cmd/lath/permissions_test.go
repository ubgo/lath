package main

import (
	"os"
	"path/filepath"
	"testing"
)

// needsEnforcedDirectoryPermissions skips the test unless this filesystem
// actually refuses a write into a directory with no write bit.
//
// A copy of kit/internal/fsprobe, deliberately: cmd/lath is a separate module
// and Go forbids importing another module's internal package. Exporting it
// from kit to save nine lines here would put a testing helper into a library's
// public API forever, which is the worse trade.
//
// Why probe rather than check the platform: the guard used to be
// `os.Geteuid() == 0`, a list of the cases somebody thought of. It missed
// Windows, where the write simply succeeds and the test reported a failure
// that was not one. Asking the filesystem covers root, Windows and any mount
// without permission support, and cannot go stale.
func needsEnforcedDirectoryPermissions(t *testing.T) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Skipf("cannot create a read-only directory to probe with: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := os.WriteFile(filepath.Join(dir, "probe"), []byte("x"), 0o600); err == nil {
		t.Skip("this filesystem does not enforce directory permissions " +
			"(running as root, on Windows, or on a mount without permission support)")
	}
}
