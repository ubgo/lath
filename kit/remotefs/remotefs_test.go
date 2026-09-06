package remotefs_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/internal/fsprobe"
	"github.com/ubgo/lath/kit/remotefs"
	"github.com/ubgo/lath/kit/runner"
)

func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
}

// TestWriteFileRoundTrips runs against the LOCAL runner, which exercises the
// real shell pipeline. The base64 transport, the umask, the mkdir, rather
// than a fake that would only prove the command string.
func TestWriteFileRoundTrips(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	path := filepath.Join(t.TempDir(), "deep", "nested", "file.txt")

	// Content designed to break a naive transport: newlines, quotes, a dollar,
	// a backtick, and a leading dash.
	content := "-----BEGIN KEY-----\nline 'two' \"three\"\n$HOME `whoami`\n-----END-----\n"
	if err := remotefs.WriteFile(context.Background(), nil, path, content, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("round trip changed the content:\n got %q\nwant %q", got, content)
	}
}

// TestSensitiveFilesAreOwnerOnly pins the mode, and that it comes from a umask
// set BEFORE the redirect, so the file is never briefly readable.
func TestSensitiveFilesAreOwnerOnly(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	dir := t.TempDir()

	secretPath := filepath.Join(dir, "secret")
	if err := remotefs.WriteFile(context.Background(), nil, secretPath, "token", true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("sensitive file mode = %v; want 0600", perm)
	}

	publicPath := filepath.Join(dir, "public")
	if err := remotefs.WriteFile(context.Background(), nil, publicPath, "hello", false); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("public file mode = %v; want 0644", perm)
	}
}

// TestContentNeverAppearsInTheCommandLine is the property that keeps a
// credential out of the process list and shell history.
func TestContentNeverAppearsInTheCommandLine(t *testing.T) {
	t.Parallel()
	const credential = "ghp_this_must_not_appear_in_argv"
	f := &runner.Fake{}
	_ = remotefs.WriteFile(context.Background(), f, "/srv/.env", credential, true)

	joined := strings.Join(f.Commands(), " ")
	if strings.Contains(joined, credential) {
		t.Errorf("the credential reached argv: %s", joined)
	}
	// The umask precedes the REDIRECT, so the file is never briefly readable.
	if strings.Index(joined, "umask") > strings.Index(joined, "base64 -d >") {
		t.Errorf("umask is set after the redirect: %s", joined)
	}
	// But it follows the mkdir. A umask narrow enough for a 0600 file strips
	// the execute bit, so a directory created under it cannot be entered and
	// the redirect fails on a path that was just made.
	if strings.Index(joined, "mkdir") > strings.Index(joined, "umask") {
		t.Errorf("umask is set before the mkdir, which breaks the directory: %s", joined)
	}
	// 177 is the complement of 0600. The mode a sensitive file must land at.
	if !strings.Contains(joined, "umask 177") {
		t.Errorf("sensitive write did not derive an owner-only umask: %s", joined)
	}
}

func TestMkdirAllCreatesAndChowns(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "b", "c"),
	}
	if err := remotefs.MkdirAll(context.Background(), nil, "", paths...); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Errorf("%s was not created: %v", p, err)
		}
	}
	// Creating nothing is not an error. The natural result of an empty slice.
	if err := remotefs.MkdirAll(context.Background(), nil, ""); err != nil {
		t.Errorf("MkdirAll with no paths = %v", err)
	}
}

// TestChownFailureDoesNotFailTheCall pins the best-effort contract: a
// directory that already has the right owner, on a host where this user cannot
// chown, must not stop a deploy.
func TestChownFailureDoesNotFailTheCall(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	fsprobe.NeedsEnforcedPermissions(t)
	dir := filepath.Join(t.TempDir(), "owned")
	// uid 1 is not ours, so the chown will be refused.
	if err := remotefs.MkdirAll(context.Background(), nil, "1:1", dir); err != nil {
		t.Errorf("a refused chown failed the call: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("the directory was not created despite the chown failing: %v", err)
	}
}

func TestExistsDistinguishesPresentFromAbsent(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	dir := t.TempDir()
	present := filepath.Join(dir, "here")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]bool{
		present:                       true,
		dir:                           true,
		filepath.Join(dir, "missing"): false,
	} {
		// An absent path is an ANSWER, not an error: `test -e` reports through
		// its exit status, so treating that as failure would make every
		// negative result an error.
		got, err := remotefs.Exists(context.Background(), nil, path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got != want {
			t.Errorf("Exists(%s) = %v; want %v", path, got, want)
		}
	}
}

func TestReadFileRoundTripsBinary(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	path := filepath.Join(t.TempDir(), "blob")
	content := []byte{0x00, 0x01, 0xff, 'a', '\n', 0x7f}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := remotefs.ReadFile(context.Background(), nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("ReadFile = %v; want %v", got, content)
	}
}

func TestQuoteDefusesShellMetacharacters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"/srv/app", "'/srv/app'"},
		{"/srv/my app", "'/srv/my app'"},
		{"it's", `'it'\''s'`},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
	} {
		if got := remotefs.Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestDirUsesTheRemotesSeparator pins that paths are treated as the TARGET's,
// not this machine's. A deploy from Windows still writes /srv/app/.env.
func TestDirUsesTheRemotesSeparator(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/srv/app/.env": "/srv/app",
		"/srv/.env":     "/srv",
		".env":          ".",
		"/.env":         ".",
	} {
		if got := remotefs.Dir(in); got != want {
			t.Errorf("Dir(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestWriteFileOwnerEmptyOwnerMatchesWriteFileMode pins the equivalence the
// doc comment claims: an empty owner is exactly WriteFileMode. Without this a
// future change to the chown branch could quietly alter the ordinary path.
func TestWriteFileOwnerEmptyOwnerMatchesWriteFileMode(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	const content = "value\n"
	dir := t.TempDir()

	owned := filepath.Join(dir, "owned.txt")
	if err := remotefs.WriteFileOwner(context.Background(), nil, owned, content, 0o640, ""); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain.txt")
	if err := remotefs.WriteFileMode(context.Background(), nil, plain, content, 0o640); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{owned, plain} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Errorf("%s: mode = %04o, want 0640", filepath.Base(path), got)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != content {
			t.Errorf("%s: content = %q, want %q", filepath.Base(path), body, content)
		}
	}
}

// TestWriteFileOwnerSurvivesUnchownableTarget covers the best-effort
// invariant. An ordinary user cannot chown to root, so this asks for exactly
// that: the write must still succeed, at the requested mode, with the content
// intact. A caller losing its file because a host refused a chown would be far
// worse than a file with the wrong owner.
func TestWriteFileOwnerSurvivesUnchownableTarget(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	fsprobe.NeedsEnforcedPermissions(t)
	path := filepath.Join(t.TempDir(), "creds", "key")
	const content = "-----BEGIN PRIVATE KEY-----\n"

	if err := remotefs.WriteFileOwner(context.Background(), nil, path, content, 0o600, "0:0"); err != nil {
		t.Fatalf("chown refusal must not fail the write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != content {
		t.Errorf("content = %q, want %q", body, content)
	}
}

// TestWriteFileOwnerQuotesOwner proves the owner is quoted before it reaches
// the shell. It comes from configuration, so a value containing shell syntax
// must not run. It must simply fail to be a valid owner and be swallowed by
// the best-effort branch, leaving the file untouched.
func TestWriteFileOwnerQuotesOwner(t *testing.T) {
	skipWithoutShell(t)
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	canary := filepath.Join(dir, "canary")

	// Command substitution, which a single-quoted word renders inert but an
	// unquoted one executes.
	owner := "0:0$(touch " + canary + ")"
	if err := remotefs.WriteFileOwner(context.Background(), nil, path, "x", 0o600, owner); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatal("injected command ran: the owner reached the shell unquoted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should still have been written: %v", err)
	}
}
